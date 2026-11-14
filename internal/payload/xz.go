package payload

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"hash/crc64"
	"io"
	"slices"

	"github.com/ulikunitz/xz/lzma"
)

// newLZMA2 creates the LZMA2 decoder for one block's data; tests record the dictionary it is given.
var newLZMA2 = func(r io.Reader, dictCap int) (io.Reader, error) {
	return lzma.Reader2Config{DictCap: dictCap}.NewReader2(r)
}

// xzReader decodes exactly one xz stream (.xz file format 1.2.0) within the limits of this package.
//
// It parses the whole container itself: the stream header, each block header, every LZMA2 chunk
// header, the block padding and check, the index and the footer. The LZMA2 decoder of
// github.com/ulikunitz/xz/lzma sees only the chunks of one block, one chunk at a time, and is
// created with the dictionary of the block header, which must be at most XZDictCap; the library
// never parses an xz block header. (Its xz reader treats ReaderConfig.DictCap as a minimum and
// allocates whatever dictionary a block header asks for.)
//
// Each chunk must be consumed exactly: the decoder may read no byte beyond the chunk's declared
// compressed size, and when the chunk's declared output is complete it must have read all of it.
// Otherwise a chunk could declare spare bytes that a decoder starting the next chunk where the range
// coder stopped would parse differently. Nothing may follow the stream's footer.
type xzReader struct {
	r     *bufio.Reader
	err   error
	state int

	flags     [2]byte   // stream flags, repeated in the footer
	sum       hash.Hash // the block check; nil for none
	checkSize int

	// The current block.
	lz                 io.Reader // its LZMA2 decoder, created at its first chunk
	dict               int
	hdrSize            int64
	csize, usize       int64 // declared in its header; -1 when absent
	data, out, pending int64 // LZMA2 bytes and output so far; output still to come from the chunk
	feed               bytes.Reader
	props              bool  // an LZMA chunk has set the properties
	afterU             bool  // the last chunk was uncompressed
	unc, resets        int64 // uncompressed-chunk bytes and LZMA state resets so far

	blocks  uint64    // blocks decoded
	records hash.Hash // SHA-256 of each block's unpadded and uncompressed size
}

const (
	xzStreamHeader = iota
	xzBlock
	xzChunk
	xzEnd
)

// xzResetEvery is the uncompressed data an LZMA2 block needs per LZMA state reset.
const xzResetEvery = 32 << 10

var xzMagic = []byte{0xfd, '7', 'z', 'X', 'Z', 0}

// newXZReader returns a reader of the decompressed data of the one xz stream in r.
func newXZReader(r io.Reader) *xzReader {
	return &xzReader{r: bufio.NewReader(r), records: sha256.New()}
}

func xzErr(format string, a ...any) error {
	return fmt.Errorf("%w: xz: %s", ErrFormat, fmt.Sprintf(format, a...))
}

func (x *xzReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, x.err
	}
	for x.err == nil && x.pending == 0 && x.state != xzEnd {
		x.err = x.next()
	}
	switch {
	case x.err != nil:
		return 0, x.err
	case x.pending == 0:
		return 0, io.EOF
	}
	if int64(len(p)) > x.pending {
		p = p[:x.pending]
	}
	n, err := x.lz.Read(p)
	x.pending -= int64(n)
	x.out += int64(n)
	if x.sum != nil {
		_, _ = x.sum.Write(p[:n])
	}
	switch {
	case err != nil && !errors.Is(err, io.EOF):
		x.err = xzErr("LZMA2: %v", err)
	case n < len(p):
		x.err = xzErr("LZMA2 chunk ends early")
	case x.pending == 0 && x.feed.Len() > 0:
		x.err = xzErr("an LZMA2 chunk declares %d compressed bytes more than it uses", x.feed.Len())
	}
	if x.err != nil {
		return 0, x.err
	}
	return n, nil
}

// next parses the next structural unit, up to the next chunk with output or the end of the stream.
func (x *xzReader) next() error {
	switch x.state {
	case xzStreamHeader:
		return x.streamHeader()
	case xzBlock:
		b, err := x.r.ReadByte()
		if err != nil {
			return xzErr("truncated")
		}
		if b == 0 {
			return x.index()
		}
		if x.blocks > 0 {
			return xzErr("more than one block (FORMAT.md section 12 allows exactly one)")
		}
		h := make([]byte, (int(b)+1)*4)
		h[0] = b
		if err := x.full(h[1:]); err != nil {
			return err
		}
		if x.csize, x.usize, x.dict, err = checkBlockHeader(h); err != nil {
			return err
		}
		x.hdrSize, x.data, x.out, x.lz, x.props, x.state = int64(len(h)), 0, 0, nil, false, xzChunk
		if x.sum != nil {
			x.sum.Reset()
		}
	case xzChunk:
		return x.chunk()
	}
	return nil
}

// full reads exactly len(b) bytes.
func (x *xzReader) full(b []byte) error {
	if _, err := io.ReadFull(x.r, b); err != nil {
		return xzErr("truncated")
	}
	return nil
}

func (x *xzReader) streamHeader() error {
	var h [12]byte
	if err := x.full(h[:]); err != nil {
		return err
	}
	if !bytes.Equal(h[:6], xzMagic) || h[6] != 0 || crc32.ChecksumIEEE(h[6:8]) != binary.LittleEndian.Uint32(h[8:]) {
		return xzErr("bad stream header")
	}
	switch h[7] {
	case 0x00:
	case 0x01:
		x.sum, x.checkSize = crc32.NewIEEE(), 4
	case 0x04:
		x.sum, x.checkSize = crc64.New(crc64.MakeTable(crc64.ECMA)), 8
	case 0x0a:
		x.sum, x.checkSize = sha256.New(), 32
	default:
		return xzErr("unsupported check type %#x", h[7])
	}
	x.flags = [2]byte{h[6], h[7]}
	x.state = xzBlock
	return nil
}

// chunk parses one LZMA2 chunk. A chunk with output is handed to the block's decoder; the end
// marker ends the block, whose sizes, padding and check are verified.
//
// The library allocates new literal tables (up to 24 KiB) for every chunk that resets the LZMA
// state, so state resets are limited as encoders write them: the first chunk resets the
// dictionary and no later chunk does; only the first LZMA chunk sets the properties; and an LZMA
// chunk resets the state without new properties (control 0xA0-0xBF) only directly after
// uncompressed data and at most once per xzResetEvery bytes of uncompressed data so far, which is
// what XZ Utils does (each of its resets follows an uncompressed run of about 60 KiB) and more than
// qrvt's encoder does (it never writes one). Consecutive uncompressed chunks are handed to the
// decoder as one chunk of up to 64 KiB, because the library allocates a 32 KiB copy buffer for each
// uncompressed chunk it reads.
func (x *xzReader) chunk() error {
	c, err := x.r.ReadByte()
	if err != nil {
		return xzErr("truncated")
	}
	reset := c == 1 || c >= 0xe0
	switch {
	case c == 0:
		return x.endBlock()
	case x.data == 0 && !reset:
		return xzErr("the block's first LZMA2 chunk does not reset the dictionary")
	case x.data > 0 && reset:
		return xzErr("an LZMA2 chunk resets the dictionary after the block's first chunk")
	case c&0xe0 == 0xa0 && (!x.afterU || (x.resets+1)*xzResetEvery > x.unc):
		return xzErr("an LZMA2 chunk resets the state other than directly after uncompressed data, once per 32 KiB of it")
	case c >= 0xc0 && x.props:
		return xzErr("an LZMA2 chunk changes the properties after the block's first LZMA chunk")
	case c == 1 || c == 2:
		return x.uncompressed(c)
	case c < 0x80:
		return xzErr("bad LZMA2 chunk header %#x", c)
	}
	hdr := []byte{c, 0, 0, 0, 0}
	if c >= 0xc0 {
		hdr, x.props = append(hdr, 0), true
	}
	if err := x.full(hdr[1:]); err != nil {
		return err
	}
	// LZMA2 properties: lc + lp at most 4 and pb at most 4. The library allocates literal tables for
	// lc + lp up to 12 (3 MB) without checking.
	if p := int(hdr[len(hdr)-1]); len(hdr) == 6 && (p/45 > 4 || p%9+p/9%5 > 4) {
		return xzErr("bad LZMA2 properties %#x", p)
	}
	out := int64(c&0x1f)<<16 | int64(binary.BigEndian.Uint16(hdr[1:3])) + 1
	chunk := make([]byte, len(hdr)+int(binary.BigEndian.Uint16(hdr[3:5]))+1)
	copy(chunk, hdr)
	if err := x.full(chunk[len(hdr):]); err != nil {
		return err
	}
	x.data += int64(len(chunk))
	x.afterU = false
	if c&0xe0 == 0xa0 {
		x.resets++
	}
	return x.hand(chunk, out)
}

// uncompressed reads an uncompressed chunk of control byte c and the uncompressed chunks (control 2)
// that directly follow it, up to 64 KiB of data, and hands them to the decoder as one chunk.
func (x *xzReader) uncompressed(c byte) error {
	chunk := []byte{c, 0, 0}
	for {
		var s [2]byte
		if err := x.full(s[:]); err != nil {
			return err
		}
		n := int(binary.BigEndian.Uint16(s[:])) + 1
		k := len(chunk)
		chunk = append(chunk, make([]byte, n)...)
		if err := x.full(chunk[k:]); err != nil {
			return err
		}
		x.data += int64(3 + n)
		x.unc += int64(n)
		if next, err := x.r.Peek(3); err != nil || next[0] != 2 ||
			len(chunk)-3+int(binary.BigEndian.Uint16(next[1:]))+1 > 1<<16 {
			break
		}
		_, _ = x.r.Discard(1)
	}
	x.afterU = true
	out := len(chunk) - 3
	binary.BigEndian.PutUint16(chunk[1:], uint16(out-1)) //nolint:gosec // at most 64 KiB, checked above
	return x.hand(chunk, int64(out))
}

// hand gives one chunk, header included, to the block's decoder, creating it at the first chunk.
func (x *xzReader) hand(chunk []byte, out int64) error {
	x.feed.Reset(chunk)
	x.pending = out
	var err error
	if x.lz == nil {
		if x.lz, err = newLZMA2(&x.feed, max(x.dict, lzma.MinDictCap)); err != nil {
			return xzErr("LZMA2: %v", err)
		}
	}
	return nil
}

// endBlock checks the end of a block after its end marker: the declared sizes, the padding and the
// check, and records the block for the index.
func (x *xzReader) endBlock() error {
	x.data++
	if (x.csize >= 0 && x.data != x.csize) || (x.usize >= 0 && x.out != x.usize) {
		return xzErr("block sizes differ from its header")
	}
	tail := make([]byte, (4-x.data%4)%4+int64(x.checkSize))
	if err := x.full(tail); err != nil {
		return err
	}
	pad := len(tail) - x.checkSize
	if bytes.Count(tail[:pad], []byte{0}) != pad {
		return xzErr("block padding is not zero")
	}
	want := tail[pad:]
	if x.checkSize != 32 { // CRC-32 and CRC-64 are stored little-endian; hash.Hash gives big-endian
		slices.Reverse(want)
	}
	if x.sum != nil && !bytes.Equal(x.sum.Sum(nil), want) {
		return xzErr("block check differs")
	}
	x.blocks++
	x.record(uint64(x.hdrSize+x.data)+uint64(x.checkSize), uint64(x.out)) //nolint:gosec // sizes are not negative
	x.lz, x.state = nil, xzBlock
	return nil
}

// record adds a block's unpadded and uncompressed sizes to the hash compared with the index.
func (x *xzReader) record(unpadded, uncompressed uint64) {
	_, _ = x.records.Write(binary.LittleEndian.AppendUint64(binary.LittleEndian.AppendUint64(nil, unpadded), uncompressed))
}

// index reads the index (its indicator byte is read) and the footer, and checks that nothing
// follows.
func (x *xzReader) index() error {
	crc := crc32.NewIEEE()
	_, _ = crc.Write([]byte{0})
	size := int64(1)
	uvarint := func() (uint64, error) {
		var v uint64
		for i := range 9 {
			b, err := x.r.ReadByte()
			if err != nil {
				return 0, xzErr("truncated")
			}
			_, _ = crc.Write([]byte{b})
			size++
			v |= uint64(b&0x7f) << (7 * i)
			if b < 0x80 {
				if b == 0 && i > 0 {
					break // not the shortest encoding
				}
				return v, nil
			}
		}
		return 0, xzErr("bad integer in the index")
	}
	n, err := uvarint()
	if err != nil {
		return err
	}
	if n != x.blocks {
		return xzErr("index has %d records for %d blocks", n, x.blocks)
	}
	blocks := x.records
	x.records = sha256.New()
	for range n {
		u, err := uvarint()
		if err != nil {
			return err
		}
		c, err := uvarint()
		if err != nil {
			return err
		}
		x.record(u, c)
	}
	if !bytes.Equal(blocks.Sum(nil), x.records.Sum(nil)) {
		return xzErr("index records differ from the blocks")
	}
	tail := make([]byte, (4-size%4)%4+4+12) // padding, CRC-32, footer
	if err := x.full(tail); err != nil {
		return err
	}
	pad := len(tail) - 16
	_, _ = crc.Write(tail[:pad])
	f := tail[pad+4:]
	switch {
	case bytes.Count(tail[:pad], []byte{0}) != pad:
		return xzErr("index padding is not zero")
	case crc.Sum32() != binary.LittleEndian.Uint32(tail[pad:]):
		return xzErr("index checksum")
	case crc32.ChecksumIEEE(f[4:10]) != binary.LittleEndian.Uint32(f[:4]):
		return xzErr("footer checksum")
	case int64(binary.LittleEndian.Uint32(f[4:8])) != (size+int64(pad))/4,
		f[8] != x.flags[0] || f[9] != x.flags[1],
		f[10] != 'Y' || f[11] != 'Z':
		return xzErr("bad stream footer")
	}
	switch _, err := x.r.ReadByte(); {
	case err == nil:
		return xzErr("data after the stream")
	case !errors.Is(err, io.EOF):
		return err
	}
	x.state = xzEnd
	return nil
}

// checkBlockHeader checks a whole block header (size byte first): [ErrFormat] for anything malformed
// or other than a single LZMA2 filter, a [LimitError] for a valid header whose dictionary exceeds
// XZDictCap. It returns the declared compressed and uncompressed sizes, -1 when absent, and the
// dictionary size.
func checkBlockHeader(h []byte) (csize, usize int64, dict int, err error) {
	n := len(h) - 4
	if crc32.ChecksumIEEE(h[:n]) != binary.LittleEndian.Uint32(h[n:]) {
		return 0, 0, 0, xzErr("block header checksum")
	}
	flags, r := h[1], bytes.NewReader(h[2:n])
	if flags&0x3c != 0 {
		return 0, 0, 0, xzErr("reserved block flags set")
	}
	sizes := [2]int64{-1, -1}
	for k, bit := range []byte{0x40, 0x80} { // compressed size, uncompressed size
		if flags&bit != 0 {
			v, err := binary.ReadUvarint(r)
			if err != nil || v >= 1<<63 || (k == 0 && v == 0) {
				return 0, 0, 0, xzErr("bad size in block header")
			}
			sizes[k] = int64(v) //nolint:gosec // below 2^63, checked above
		}
	}
	id, err1 := binary.ReadUvarint(r)
	size, err2 := binary.ReadUvarint(r)
	code, err3 := r.ReadByte()
	switch {
	case flags&0x03 != 0:
		return 0, 0, 0, xzErr("more than one filter")
	case errors.Join(err1, err2, err3) != nil || id != 0x21 || size != 1:
		return 0, 0, 0, xzErr("block filter is not LZMA2")
	case code > 40:
		return 0, 0, 0, xzErr("bad LZMA2 dictionary code %d", code)
	}
	for r.Len() > 0 {
		if b, _ := r.ReadByte(); b != 0 {
			return 0, 0, 0, xzErr("block header padding is not zero")
		}
	}
	d := int64(1)<<32 - 1
	if code < 40 {
		d = (2 | int64(code)&1) << (11 + code/2)
	}
	if d > XZDictCap {
		return 0, 0, 0, &LimitError{"xz dictionary", d, XZDictCap}
	}
	return sizes[0], sizes[1], int(d), nil
}
