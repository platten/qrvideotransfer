package payload

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"flag"
	"hash/crc32"
	"hash/crc64"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ulikunitz/xz"
)

var xzSamples = flag.String("xz-samples", "", "write the inputs of testdata/xz to this folder")

// xzOf compresses b with the encoder's settings, or with the given block size when it is not zero.
func xzOf(t testing.TB, b []byte, blockSize int64) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := xz.WriterConfig{DictCap: XZDictCap, BlockSize: blockSize}.NewWriter(&out)
	must(t, err)
	_, err = w.Write(b)
	must(t, err)
	must(t, w.Close())
	return out.Bytes()
}

// pseudoRandom returns n bytes that do not compress.
func pseudoRandom(n int) []byte {
	b := make([]byte, n)
	var x uint64 = 99
	for i := range b {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		b[i] = byte(x)
	}
	return b
}

// decodeXZ decodes data as Unpack does.
func decodeXZ(data []byte) ([]byte, error) {
	r, release, err := decompressor(bytes.NewReader(data), MethodXZ)
	if err != nil {
		return nil, err
	}
	defer release()
	return io.ReadAll(r)
}

// withDictHook records every dictionary capacity handed to the LZMA2 decoder while f runs.
func withDictHook(tb testing.TB) *[]int {
	var caps []int
	old := newLZMA2
	tb.Cleanup(func() { newLZMA2 = old })
	newLZMA2 = func(r io.Reader, dictCap int) (io.Reader, error) {
		caps = append(caps, dictCap)
		return old(r, dictCap)
	}
	return &caps
}

func TestXZDecodesEncoderOutput(t *testing.T) {
	inputs := map[string][]byte{
		"empty":          nil,
		"small":          []byte("hello, qrvt\n"),
		"incompressible": pseudoRandom(300 << 10),
		"compressible":   bytes.Repeat([]byte("qrvt stream "), 300<<10),
	}
	for name, in := range inputs {
		if out, err := decodeXZ(xzOf(t, in, 0)); err != nil || !bytes.Equal(out, in) { // one block, as Pack writes
			t.Fatalf("%s: decode: %v", name, err)
		}
		if len(in) > 64<<10 { // many blocks: refused (FORMAT.md section 12)
			if _, err := decodeXZ(xzOf(t, in, 64<<10)); !isFormat(err) {
				t.Fatalf("%s, many blocks: %v", name, err)
			}
		}
	}
}

// xzSample is the input of the fixtures in testdata/xz: a little incompressible data and 2.5 MiB of
// text (several LZMA chunks of at most 2 MiB of output each). uncompressed.xz is 96 KiB of
// incompressible data only, which xz stores as uncompressed LZMA2 chunks.
func xzSample(name string) []byte {
	if name == "uncompressed.xz" {
		return pseudoRandom(96 << 10)
	}
	var b bytes.Buffer
	b.Write(pseudoRandom(20 << 10))
	for i := 0; b.Len() < 5<<19; i++ {
		b.WriteString("qrvt payload line ")
		b.WriteString(strings.Repeat("x", i%37))
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// xzFixtures are the arguments of XZ Utils 5.x for each file in testdata/xz. To regenerate them, run
// the test with -xz-samples DIR, which writes the two inputs to DIR, and then, for each file,
// xz -c --format=xz ARGS < DIR/INPUT > testdata/xz/FILE (the input is uncompressed.bin for
// uncompressed.xz and sample.bin otherwise). This package may not run programs itself (SECURITY.md
// section 9.6).
var xzFixtures = map[string][]string{
	"check-none.xz":   {"-6", "--check=none"},
	"check-crc32.xz":  {"-6", "--check=crc32"},
	"check-crc64.xz":  {"-6", "--check=crc64"},
	"check-sha256.xz": {"-6", "--check=sha256"},
	"blocks.xz":       {"-6", "--block-size=262144"},
	"threads.xz":      {"-6", "-T3", "--block-size=1MiB"}, // block headers carry both sizes
	"block-list.xz":   {"-6", "--block-list=100,200,1MiB,0"},
	"uncompressed.xz": {"-6"},
}

// xzMultiBlock are the fixtures with more than one block (11, 3 and 4), which a decoder refuses.
var xzMultiBlock = map[string]bool{"blocks.xz": true, "threads.xz": true, "block-list.xz": true}

// TestXZDecodesXZUtilsOutput decodes the one-block streams written by XZ Utils 5.8.4 (every check
// type, sizes in the block header, uncompressed chunks) and refuses the ones with several blocks.
func TestXZDecodesXZUtilsOutput(t *testing.T) {
	if *xzSamples != "" {
		must(t, os.WriteFile(filepath.Join(*xzSamples, "sample.bin"), xzSample(""), 0o600))
		must(t, os.WriteFile(filepath.Join(*xzSamples, "uncompressed.bin"), xzSample("uncompressed.xz"), 0o600))
	}
	for name := range xzFixtures {
		in := xzSample(name)
		data, err := os.ReadFile(filepath.Join("testdata", "xz", name))
		must(t, err)
		caps := withDictHook(t)
		out, err := decodeXZ(data)
		switch {
		case xzMultiBlock[name]:
			if !isFormat(err) {
				t.Errorf("%s has several blocks: %v", name, err)
			}
		case err != nil || !bytes.Equal(out, in):
			t.Errorf("%s: %v", name, err)
		}
		for _, c := range *caps {
			if c > XZDictCap {
				t.Errorf("%s: dictionary %d", name, c)
			}
		}
	}
}

// replaceBlockHeader returns data with its first block header replaced by h.
func replaceBlockHeader(data, h []byte) []byte {
	old := (int(data[12]) + 1) * 4
	return append(append(bytes.Clone(data[:12]), h...), data[12+old:]...)
}

// blockHeader builds a block header from its flags and fields, with zero padding and its CRC-32.
func blockHeader(flags byte, fields ...byte) []byte {
	n := (2 + len(fields) + 4 + 3) / 4 * 4
	h := make([]byte, n)
	h[0], h[1] = byte(n/4-1), flags
	copy(h[2:], fields)
	binary.LittleEndian.PutUint32(h[n-4:], crc32.ChecksumIEEE(h[:n-4]))
	return h
}

// xzParts splits a one-block stream with a CRC-64 check into its block (header to check) and index.
func xzParts(t *testing.T, data []byte) (block []byte, records [][2]uint64) {
	t.Helper()
	back := (int(binary.LittleEndian.Uint32(data[len(data)-8:])) + 1) * 4
	i := len(data) - 12 - back
	r := bytes.NewReader(data[i+1:])
	n, _ := binary.ReadUvarint(r)
	for range n {
		u, _ := binary.ReadUvarint(r)
		c, _ := binary.ReadUvarint(r)
		records = append(records, [2]uint64{u, c})
	}
	return data[12:i], records
}

// xzStream assembles a stream from the stream header of good, blocks and index records, with a
// correct index and footer.
func xzStream(good, blocks []byte, records [][2]uint64) []byte {
	index := binary.AppendUvarint([]byte{0}, uint64(len(records)))
	for _, rec := range records {
		index = binary.AppendUvarint(binary.AppendUvarint(index, rec[0]), rec[1])
	}
	for len(index)%4 != 0 {
		index = append(index, 0)
	}
	index = binary.LittleEndian.AppendUint32(index, crc32.ChecksumIEEE(index))
	footer := binary.LittleEndian.AppendUint32(nil, uint32(len(index)/4-1))
	footer = append(footer, good[6], good[7])
	s := append(bytes.Clone(good[:12]), blocks...)
	s = append(s, index...)
	s = binary.LittleEndian.AppendUint32(s, crc32.ChecksumIEEE(footer))
	return append(append(s, footer...), 'Y', 'Z')
}

// paddedSample returns a one-block stream of the encoder whose block has padding before its check.
func paddedSample(t *testing.T) ([]byte, []byte) {
	t.Helper()
	for n := 1; n < 64; n++ {
		in := bytes.Repeat([]byte("pad"), n)
		good := xzOf(t, in, 0)
		if _, recs := xzParts(t, good); recs[0][0]%4 != 0 {
			return in, good
		}
	}
	t.Fatal("no sample with block padding")
	return nil, nil
}

func TestXZRefusals(t *testing.T) {
	in := bytes.Repeat([]byte("content "), 1000)
	good := xzOf(t, in, 0)
	huge := replaceBlockHeader(good, blockHeader(0, 0x21, 0x01, 40)) // 4 GiB dictionary
	withPadding := func(h []byte) []byte {
		h = bytes.Clone(h)
		h[len(h)-5] = 1 // the last padding byte
		binary.LittleEndian.PutUint32(h[len(h)-4:], crc32.ChecksumIEEE(h[:len(h)-4]))
		return h
	}
	badCRC := blockHeader(0, 0x21, 0x01, 22)
	badCRC[len(badCRC)-1] ^= 1
	block, recs := xzParts(t, good)
	edit := func(f func(b []byte, r [][2]uint64) ([]byte, [][2]uint64)) []byte {
		b, r := f(bytes.Clone(block), append([][2]uint64{}, recs...))
		return xzStream(good, b, r)
	}
	_, padded := paddedSample(t)
	pblock, precs := xzParts(t, padded)
	pblock = bytes.Clone(pblock)
	pblock[precs[0][0]-8] = 1 // the first padding byte after the data
	footerFlags := bytes.Clone(good)
	footerFlags[len(footerFlags)-3] = 0x01
	binary.LittleEndian.PutUint32(footerFlags[len(footerFlags)-12:], crc32.ChecksumIEEE(footerFlags[len(footerFlags)-8:len(footerFlags)-2]))
	cases := map[string]struct {
		data []byte
		want func(error) bool
	}{
		"dictionary 4 GiB":         {huge, isLimit("xz dictionary")},
		"second stream":            {append(bytes.Clone(good), xzOf(t, in, 0)...), isFormat},
		"second stream, 4 GiB":     {append(bytes.Clone(good), huge...), isFormat},
		"stream padding":           {append(bytes.Clone(good), 0, 0, 0, 0), isFormat},
		"trailing byte":            {append(bytes.Clone(good), 'x'), isFormat},
		"delta then LZMA2":         {replaceBlockHeader(good, blockHeader(0x01, 0x03, 0x01, 0x00, 0x21, 0x01, 22)), isFormat},
		"reserved flag bit":        {replaceBlockHeader(good, blockHeader(0x04, 0x21, 0x01, 22)), isFormat},
		"reserved bit, 4 GiB":      {replaceBlockHeader(good, blockHeader(0x20, 0x21, 0x01, 40)), isFormat},
		"compressed size wrong":    {replaceBlockHeader(good, blockHeader(0x40, 0x05, 0x21, 0x01, 22)), isFormat},
		"uncompressed size wrong":  {replaceBlockHeader(good, blockHeader(0x80, 0x05, 0x21, 0x01, 22)), isFormat},
		"header checksum":          {replaceBlockHeader(good, badCRC), isFormat},
		"header padding not zero":  {replaceBlockHeader(good, withPadding(blockHeader(0, 0x21, 0x01, 22))), isFormat},
		"stream header checksum":   {append(append(bytes.Clone(good[:8]), good[8]^1), good[9:]...), isFormat},
		"truncated after the data": {good[:len(good)-20], isFormat},
		"check value": {edit(func(b []byte, r [][2]uint64) ([]byte, [][2]uint64) {
			b[len(b)-1] ^= 1
			return b, r
		}), isFormat},
		"index with two records": {edit(func(b []byte, r [][2]uint64) ([]byte, [][2]uint64) {
			return b, append(r, r[0])
		}), isFormat},
		"index unpadded size": {edit(func(b []byte, r [][2]uint64) ([]byte, [][2]uint64) {
			r[0][0]++
			return b, r
		}), isFormat},
		"index uncompressed size": {edit(func(b []byte, r [][2]uint64) ([]byte, [][2]uint64) {
			r[0][1]--
			return b, r
		}), isFormat},
		"index checksum":       {append(append(bytes.Clone(good[:len(good)-13]), good[len(good)-13]^1), good[len(good)-12:]...), isFormat},
		"footer flags":         {footerFlags, isFormat},
		"footer magic":         {append(bytes.Clone(good[:len(good)-1]), 'X'), isFormat},
		"block padding":        {xzStream(padded, pblock, precs), isFormat},
		"index padding":        {indexPadding(t, good), isFormat},
		"chunk declares more":  {xzDesync(t), isFormat},
		"chunk declares fewer": {chunkShort(t, good), isFormat},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var err error
			caps := withDictHook(t)
			alloc, _ := allocated(func() { _, err = decodeXZ(c.data) })
			if !c.want(err) {
				t.Fatalf("decode: %v", err)
			}
			for _, d := range *caps {
				if d > XZDictCap {
					t.Fatalf("dictionary %d handed to the decoder", d)
				}
			}
			// Each block's dictionary, at most XZDictCap, is allocated when its data starts; a
			// refusal before any block data comes before any dictionary.
			bound := uint64(XZDictCap + 1<<20)
			if len(*caps) == 0 {
				bound = 1 << 20
			}
			if alloc > bound {
				t.Fatalf("allocated %d MiB", alloc>>20)
			}
		})
	}
}

// indexPadding returns good with a non-zero index padding byte, its CRC-32 fixed.
func indexPadding(t *testing.T, good []byte) []byte {
	t.Helper()
	back := (int(binary.LittleEndian.Uint32(good[len(good)-8:])) + 1) * 4
	i := len(good) - 12 - back
	index := bytes.Clone(good[i : i+back-4])
	if index[len(index)-1] != 0 {
		t.Skip("index has no padding")
	}
	index[len(index)-1] = 1
	s := append(bytes.Clone(good[:i]), index...)
	s = binary.LittleEndian.AppendUint32(s, crc32.ChecksumIEEE(index))
	return append(s, good[len(good)-12:]...)
}

// chunkShort returns good with its first LZMA chunk declaring one compressed byte fewer than it has.
func chunkShort(t *testing.T, good []byte) []byte {
	t.Helper()
	c := 12 + (int(good[12])+1)*4
	if good[c] < 0x80 {
		t.Fatalf("first chunk control %#x", good[c])
	}
	s := bytes.Clone(good)
	n := binary.BigEndian.Uint16(s[c+3:]) - 1
	binary.BigEndian.PutUint16(s[c+3:], n)
	return s
}

// xzDesync is the round-2 review's exploit (TestR2XZDesync): an LZMA chunk that declares more
// compressed bytes than its range coder uses. A decoder that starts the next chunk where the range
// coder stopped finds an end marker, padding, a valid check and a second block header asking for a
// 4 GiB dictionary, all inside the declared chunk.
func xzDesync(t *testing.T) []byte {
	t.Helper()
	es := []tarEntry{fileHdr("a", "content")}
	a := archive(t, MarshalManifest(manifestFor(es...)), es...)
	good := xzOf(t, a, 0)
	hs := (int(good[12]) + 1) * 4
	h1 := good[12 : 12+hs]
	c := 12 + hs
	ctl := good[c]
	if ctl < 0xe0 {
		t.Fatalf("first chunk control %#x", ctl)
	}
	packed := int(binary.BigEndian.Uint16(good[c+3:c+5])) + 1
	data := good[c+6 : c+6+packed]
	if good[c+6+packed] != 0 {
		t.Fatalf("expected one chunk then the end marker, got %#x", good[c+6+packed])
	}
	count := 6 + packed + 1
	pad := (4 - count%4) % 4
	sum := binary.LittleEndian.AppendUint64(nil, crc64.Checksum(a, crc64.MakeTable(crc64.ECMA)))
	h2 := bytes.Clone(h1)
	h2[bytes.Index(h2, []byte{0x21, 0x01})+2] = 40
	binary.LittleEndian.PutUint32(h2[len(h2)-4:], crc32.ChecksumIEEE(h2[:len(h2)-4]))
	var tail bytes.Buffer
	tail.WriteByte(0)
	tail.Write(make([]byte, pad))
	tail.Write(sum)
	tail.Write(h2)
	tail.Write([]byte{0xe0, 0, 0, 0, 0, 0x5d, 0, 0, 0, 0, 0})
	var s bytes.Buffer
	s.Write(good[:c])
	np := packed + tail.Len()
	s.Write([]byte{ctl, good[c+1], good[c+2], byte((np - 1) >> 8), byte(np - 1), good[c+5]})
	s.Write(data)
	s.Write(tail.Bytes())
	s.Write(good[c+6+packed:])
	return s.Bytes()
}

// TestXZDesync is the review's exploit through Unpack: a typed error, and well under the 4.1 GiB the
// library allocated when it parsed the smuggled block header.
func TestXZDesync(t *testing.T) {
	es := []tarEntry{fileHdr("a", "content")}
	a := archive(t, MarshalManifest(manifestFor(es...)), es...)
	h := Header{Method: MethodXZ, ArchiveLen: int64(len(a))}
	var err error
	stream := rawWithHeader(h, xzDesync(t))
	alloc, _ := allocated(func() { _, err = unpack(stream, UnpackOptions{OutputDir: t.TempDir()}) })
	if !isFormat(err) || alloc > 16<<20 {
		t.Fatalf("Unpack: %v, allocated %d MiB", err, alloc>>20)
	}
}

// wrapHeader builds a complete one-block xz stream around a block header without size fields: the
// stream header flags of sh, an empty block, its check, an index and a footer.
func wrapHeader(sh, h []byte) []byte {
	check := map[byte][]byte{0x00: nil, 0x01: make([]byte, 4), 0x04: make([]byte, 8)}[sh[7]]
	if sh[7] == 0x0a {
		s := sha256.Sum256(nil)
		check = s[:]
	}
	blk := append(append(bytes.Clone(h), 0, 0, 0, 0), check...) // end of data, then padding to four bytes
	return xzStream(sh, blk, [][2]uint64{{uint64(len(h) + 1 + len(check)), 0}})
}

func TestWrapHeader(t *testing.T) {
	good := xzOf(t, nil, 0)
	if out, err := decodeXZ(wrapHeader(good, blockHeader(0, 0x21, 0x01, 0))); err != nil || len(out) != 0 {
		t.Fatalf("reference stream: %v", err)
	}
}

// FuzzXZ checks the decoder on arbitrary bytes: it never panics, fails only with a typed error, never
// hands the LZMA2 decoder a dictionary above XZDictCap (the oracle records every one), and a stream
// it accepts decodes the same with the library alone.
func FuzzXZ(f *testing.F) {
	in := []byte(strings.Repeat("qrvt ", 200))
	good := xzOf(f, in, 0)
	f.Add(good)
	f.Add(xzOf(f, pseudoRandom(5000), 1024))
	f.Add(replaceBlockHeader(good, blockHeader(0, 0x21, 0x01, 40)))
	f.Add(replaceBlockHeader(good, blockHeader(0x01, 0x03, 0x01, 0x00, 0x21, 0x01, 22)))
	f.Add(append(bytes.Clone(good), good...))
	var caps []int
	old := newLZMA2
	f.Cleanup(func() { newLZMA2 = old })
	newLZMA2 = func(r io.Reader, dictCap int) (io.Reader, error) {
		caps = append(caps, dictCap)
		return old(r, dictCap)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		// Work and allocation: one pass that discards the output may allocate at most one dictionary
		// and one LZMA2 decoder, plus a fixed multiple of the input (chunk buffers and headers).
		caps = caps[:0]
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		r, release, _ := decompressor(bytes.NewReader(data), MethodXZ)
		_, _ = io.Copy(io.Discard, r)
		release()
		runtime.ReadMemStats(&after)
		if alloc := after.TotalAlloc - before.TotalAlloc; alloc > XZDictCap+2<<20+64*uint64(len(data)) {
			t.Fatalf("allocated %d bytes for %d input bytes", alloc, len(data))
		}
		if len(caps) > 1 {
			t.Fatalf("%d LZMA2 decoders created for one stream", len(caps))
		}
		caps = caps[:0]
		out, err := decodeXZ(data)
		for _, c := range caps {
			if c > XZDictCap {
				t.Fatalf("dictionary %d handed to the LZMA2 decoder", c)
			}
		}
		var l *LimitError
		switch {
		case err == nil:
		case errors.As(err, &l):
			if l.Limit != "xz dictionary" || l.Value <= XZDictCap {
				t.Fatalf("limit error %v", err)
			}
			return
		case isFormat(err):
			return
		default:
			t.Fatalf("untyped error %v", err)
		}
		// Accepted: every dictionary is within the cap and every chunk was consumed exactly, so the
		// library parses the stream as we did and may run on it alone.
		r, rerr := xz.ReaderConfig{DictCap: XZDictCap, SingleStream: true}.NewReader(bytes.NewReader(data))
		var plain []byte
		if rerr == nil {
			plain, rerr = io.ReadAll(r)
		}
		if rerr != nil || !bytes.Equal(plain, out) {
			t.Fatalf("accepted a stream the library decodes differently: %v", rerr)
		}
	})
}
