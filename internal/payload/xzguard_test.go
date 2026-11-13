package payload

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"strings"
	"testing"

	"github.com/ulikunitz/xz"
)

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

// guardAll reads data through a guard alone.
func guardAll(data []byte) ([]byte, error) {
	return io.ReadAll(&xzGuard{r: bytes.NewReader(data)})
}

// decodeXZ decodes data through the guard and the library, as Unpack does.
func decodeXZ(data []byte) ([]byte, error) {
	r, release, err := decompressor(bytes.NewReader(data), MethodXZ)
	if err != nil {
		return nil, err
	}
	defer release()
	return io.ReadAll(r)
}

func TestXZGuardPassesEncoderOutput(t *testing.T) {
	inputs := map[string][]byte{
		"empty":          nil,
		"small":          []byte("hello, qrvt\n"),
		"incompressible": pseudoRandom(300 << 10),
		"compressible":   bytes.Repeat([]byte("qrvt stream "), 300<<10),
	}
	for name, in := range inputs {
		for _, block := range []int64{0, 64 << 10} { // 0: one block, as Pack writes; else many blocks
			data := xzOf(t, in, block)
			if out, err := guardAll(data); err != nil || !bytes.Equal(out, data) {
				t.Fatalf("%s, block size %d: guard changed or refused the stream: %v", name, block, err)
			}
			if out, err := decodeXZ(data); err != nil || !bytes.Equal(out, in) {
				t.Fatalf("%s, block size %d: decode: %v", name, block, err)
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

func TestXZGuardRefusals(t *testing.T) {
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
		"index with two records":   {indexRecords(t, good, 2), isFormat},
		"truncated after the data": {good[:len(good)-20], isFormat},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var err error
			alloc, _ := allocated(func() { _, err = decodeXZ(c.data) })
			if !c.want(err) {
				t.Fatalf("decode: %v", err)
			}
			if alloc > XZDictCap {
				t.Fatalf("allocated %d MiB", alloc>>20)
			}
		})
	}
}

// indexRecords returns data with the record count of its index replaced by n (the index CRC is not
// fixed: the guard must refuse before the library checks it).
func indexRecords(t *testing.T, data []byte, n byte) []byte {
	t.Helper()
	back := (int(binary.LittleEndian.Uint32(data[len(data)-8:])) + 1) * 4
	i := len(data) - 12 - back
	if data[i] != 0 || data[i+1] != 1 {
		t.Fatalf("index not found: % x", data[i:i+2])
	}
	data = bytes.Clone(data)
	data[i+1] = n
	return data
}

// wrapHeader builds a complete one-block xz stream around a block header without size fields: the
// stream header flags of sh, an empty block, its check, an index and a footer. The library decodes it
// only if it accepts the header.
func wrapHeader(sh, h []byte) []byte {
	check := map[byte][]byte{0x00: nil, 0x01: make([]byte, 4), 0x04: make([]byte, 8)}[sh[7]]
	if sh[7] == 0x0a {
		s := sha256.Sum256(nil)
		check = s[:]
	}
	var b bytes.Buffer
	b.Write(sh[:12])
	b.Write(h)
	b.Write([]byte{0, 0, 0, 0}) // end of data, then padding to four bytes
	b.Write(check)
	index := binary.AppendUvarint([]byte{0, 1}, uint64(len(h)+1+len(check)))
	index = append(index, 0) // uncompressed size
	for len(index)%4 != 0 {
		index = append(index, 0)
	}
	index = binary.LittleEndian.AppendUint32(index, crc32.ChecksumIEEE(index))
	b.Write(index)
	footer := binary.LittleEndian.AppendUint32(nil, uint32(len(index)/4-1))
	footer = append(footer, sh[6], sh[7])
	b.Write(binary.LittleEndian.AppendUint32(nil, crc32.ChecksumIEEE(footer)))
	b.Write(footer)
	b.WriteString("YZ")
	return b.Bytes()
}

func TestWrapHeader(t *testing.T) {
	good := xzOf(t, nil, 0)
	h := blockHeader(0, 0x21, 0x01, 0)
	data := wrapHeader(good, h)
	if out, err := decodeXZ(data); err != nil || len(out) != 0 {
		t.Fatalf("reference stream: %v", err)
	}
}

// FuzzXZGuard checks the guard on arbitrary bytes: it never panics; it passes the bytes on unchanged
// or stops with a typed error after an unchanged prefix; a dictionary refusal concerns a header the
// library accepts once the dictionary is reduced; and when the guard accepts a whole stream, the
// library decodes it the same way with and without the guard.
func FuzzXZGuard(f *testing.F) {
	in := []byte(strings.Repeat("qrvt ", 200))
	good := xzOf(f, in, 0)
	f.Add(good)
	f.Add(xzOf(f, pseudoRandom(5000), 1024))
	f.Add(replaceBlockHeader(good, blockHeader(0, 0x21, 0x01, 40)))
	f.Add(replaceBlockHeader(good, blockHeader(0x01, 0x03, 0x01, 0x00, 0x21, 0x01, 22)))
	f.Add(append(bytes.Clone(good), good...))
	f.Fuzz(func(t *testing.T, data []byte) {
		out, err := guardAll(data)
		if !bytes.HasPrefix(data, out) {
			t.Fatal("the guard changed the bytes")
		}
		var l *LimitError
		switch {
		case err == nil:
		case errors.As(err, &l):
			if l.Limit != "xz dictionary" || l.Value <= XZDictCap {
				t.Fatalf("limit error %v", err)
			}
			if len(out) == 12 && data[13]&0xc0 == 0 { // the first block, without size fields
				h := bytes.Clone(data[12 : 12+(int(data[12])+1)*4])
				h[4] = 0 // the dictionary code: 4 KiB
				binary.LittleEndian.PutUint32(h[len(h)-4:], crc32.ChecksumIEEE(h[:len(h)-4]))
				if _, err := decodeXZ(wrapHeader(data, h)); err != nil {
					t.Fatalf("refused a header the library rejects anyway: %v", err)
				}
			}
			return
		case isFormat(err):
			return
		default:
			t.Fatalf("untyped error %v", err)
		}
		// The guard accepted a stream: every dictionary is within the cap, so the library may run
		// unguarded. With nothing after the stream both must agree; with something, the guarded
		// decode must refuse it.
		guarded, gerr := decodeXZ(data)
		if len(out) < len(data) {
			if !isFormat(gerr) {
				t.Fatalf("data after the stream: %v", gerr)
			}
			return
		}
		r, rerr := xz.ReaderConfig{DictCap: XZDictCap, SingleStream: true}.NewReader(bytes.NewReader(data))
		var plain []byte
		if rerr == nil {
			plain, rerr = io.ReadAll(r)
		}
		if (rerr == nil) != (gerr == nil) || !bytes.Equal(plain, guarded) {
			t.Fatalf("library %v, guarded %v", rerr, gerr)
		}
		if gerr != nil && !isFormat(gerr) {
			t.Fatalf("untyped decode error %v", gerr)
		}
	})
}
