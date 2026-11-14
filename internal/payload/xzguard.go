package payload

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"

	"github.com/ulikunitz/xz/lzma"
)

// newLZMA2 creates the LZMA2 decoder for one block's data; tests record the dictionary it is given.
var newLZMA2 = func(r io.Reader, dictCap int) (io.Reader, error) {
	return lzma.Reader2Config{DictCap: dictCap}.NewReader2(r)
}

// xzGuard passes one xz stream through unchanged while parsing its structure, and refuses a block
// whose LZMA2 dictionary is larger than XZDictCap before the decoder sees the block header.
// github.com/ulikunitz/xz v0.5.17 treats ReaderConfig.DictCap as a minimum and allocates the
// dictionary a block header asks for (lzmafilter.go), so the limit must be enforced here.
//
// The walk follows the .xz file format 1.1.0 and the library's own checks: a 12-byte stream header
// (magic, flags, CRC-32); blocks, each a header of (first byte + 1) * 4 bytes (flags with reserved
// bits clear, optional sizes, exactly one filter, LZMA2 with a 1-byte property, zero padding, CRC-32),
// LZMA2 chunks up to the end-of-data chunk (their headers carry their sizes, lzma/header2.go),
// padding and a check; an index of one record per block; and a 12-byte footer. A dictionary is
// refused only in a header that is otherwise valid, so that every [LimitError] stands for a header
// the library would have accepted. Nothing may follow the footer: qrvt writes exactly one stream.
type xzGuard struct {
	r     io.Reader
	buf   []byte // checked bytes not yet returned
	raw   int64  // bytes to pass through without parsing
	state int
	check int64 // size of each block's check

	// The current block: its declared sizes (-1 when absent) and the LZMA2 bytes and output so far.
	csize, usize, data, out int64

	blocks, records uint64 // blocks seen; index records still to read
	index           int64  // index bytes so far, for its padding
	err             error
}

const (
	xzStreamHeader = iota
	xzBlock
	xzChunk
	xzIndex
	xzEnd
)

var xzMagic = []byte{0xfd, '7', 'z', 'X', 'Z', 0}

// xzCheckSizes are the check types the library supports: none, CRC-32, CRC-64, SHA-256.
var xzCheckSizes = map[byte]int64{0x00: 0, 0x01: 4, 0x04: 8, 0x0a: 32}

func (g *xzGuard) Read(p []byte) (int, error) {
	for g.err == nil && len(g.buf) == 0 && g.raw == 0 && g.state != xzEnd {
		g.err = g.next()
	}
	switch { // the error first: the unit that failed is still in the buffer and must not be served
	case g.err != nil:
		return 0, g.err
	case len(g.buf) > 0:
		n := copy(p, g.buf)
		g.buf = g.buf[n:]
		return n, nil
	case g.raw == 0:
		return 0, io.EOF // the footer has been passed on: the stream is complete
	}
	if int64(len(p)) > g.raw {
		p = p[:g.raw]
	}
	n, err := g.r.Read(p)
	g.raw -= int64(n)
	if errors.Is(err, io.EOF) && g.raw > 0 {
		err = xzErr("truncated")
	}
	return n, err
}

// finish refuses anything after the stream, once the decoder has read all of it.
func (g *xzGuard) finish() error {
	if g.err != nil {
		return g.err
	}
	if g.state != xzEnd || g.raw > 0 || len(g.buf) > 0 {
		return xzErr("the decoder stopped before the end of the stream")
	}
	var b [1]byte
	if n, _ := io.ReadFull(g.r, b[:]); n > 0 {
		return xzErr("data after the stream")
	}
	return nil
}

// take reads exactly n bytes into the checked buffer and returns them.
func (g *xzGuard) take(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(g.r, b); err != nil {
		return nil, xzErr("truncated: %v", err)
	}
	g.buf = append(g.buf, b...)
	return b, nil
}

// uvarint reads a multibyte integer of at most 10 bytes into the checked buffer.
func (g *xzGuard) uvarint() (uint64, error) {
	var x uint64
	for i := range 10 {
		b, err := g.take(1)
		if err != nil {
			return 0, err
		}
		g.index++
		x |= uint64(b[0]&0x7f) << (7 * i)
		if b[0] < 0x80 {
			if i == 9 && b[0] > 1 {
				break
			}
			return x, nil
		}
	}
	return 0, xzErr("integer overflow")
}

func xzErr(format string, a ...any) error {
	return fmt.Errorf("%w: xz: %s", ErrFormat, fmt.Sprintf(format, a...))
}

// next parses the next structural unit.
func (g *xzGuard) next() error {
	switch g.state {
	case xzStreamHeader:
		h, err := g.take(12)
		if err != nil {
			return err
		}
		s, ok := xzCheckSizes[h[7]]
		if !bytes.Equal(h[:6], xzMagic) || h[6] != 0 || !ok || crc32.ChecksumIEEE(h[6:8]) != binary.LittleEndian.Uint32(h[8:]) {
			return xzErr("bad stream header")
		}
		g.check, g.state = s, xzBlock
	case xzBlock:
		b, err := g.take(1)
		if err != nil {
			return err
		}
		if b[0] == 0 { // the index indicator
			g.index = 1
			if g.records, err = g.uvarint(); err != nil {
				return err
			}
			if g.records != g.blocks {
				return xzErr("index has %d records for %d blocks", g.records, g.blocks)
			}
			g.state = xzIndex
			return nil
		}
		h, err := g.take((int(b[0])+1)*4 - 1)
		if err != nil {
			return err
		}
		if g.csize, g.usize, err = checkBlockHeader(append([]byte{b[0]}, h...)); err != nil {
			return err
		}
		g.blocks++
		g.data, g.out, g.state = 0, 0, xzChunk
	case xzChunk:
		return g.chunk()
	case xzIndex:
		if g.records > 0 {
			g.records--
			for range 2 { // unpadded size, uncompressed size; the decoder compares them with the blocks
				if _, err := g.uvarint(); err != nil {
					return err
				}
			}
			return nil
		}
		g.raw = (4-g.index%4)%4 + 4 + 12 // padding, CRC-32, footer
		g.state = xzEnd
	}
	return nil
}

// chunk parses one LZMA2 chunk header and arranges for its data to pass through.
func (g *xzGuard) chunk() error {
	c, err := g.take(1)
	if err != nil {
		return err
	}
	switch {
	case c[0] == 0: // end of data: padding and check follow
		g.data++
		if (g.csize >= 0 && g.data != g.csize) || (g.usize >= 0 && g.out != g.usize) {
			return xzErr("block sizes differ from its header")
		}
		g.raw = (4-g.data%4)%4 + g.check
		g.state = xzBlock
	case c[0] == 1 || c[0] == 2: // uncompressed chunk
		s, err := g.take(2)
		if err != nil {
			return err
		}
		g.raw = int64(binary.BigEndian.Uint16(s)) + 1
		g.data += 3 + g.raw
		g.out += g.raw
	case c[0] >= 0x80: // LZMA chunk; with properties when bit 6 is set
		n := 4
		if c[0]&0x40 != 0 {
			n = 5
		}
		s, err := g.take(n)
		if err != nil {
			return err
		}
		g.raw = int64(binary.BigEndian.Uint16(s[2:4])) + 1
		g.data += int64(1+n) + g.raw
		g.out += int64(c[0]&0x1f)<<16 | int64(binary.BigEndian.Uint16(s[0:2])) + 1
	default:
		return xzErr("bad LZMA2 chunk header %#x", c[0])
	}
	return nil
}

// checkBlockHeader checks a whole block header (size byte first) as the library does, and then the
// dictionary: [ErrFormat] for anything the library would refuse, a [LimitError] for a valid header
// whose dictionary exceeds XZDictCap. It returns the declared compressed and uncompressed sizes, -1
// when absent.
func checkBlockHeader(h []byte) (csize, usize int64, err error) {
	n := len(h) - 4
	if crc32.ChecksumIEEE(h[:n]) != binary.LittleEndian.Uint32(h[n:]) {
		return 0, 0, xzErr("block header checksum")
	}
	flags, r := h[1], bytes.NewReader(h[2:n])
	if flags&0x3c != 0 {
		return 0, 0, xzErr("reserved block flags set")
	}
	sizes := [2]int64{-1, -1}
	for k, bit := range []byte{0x40, 0x80} { // compressed size, uncompressed size
		if flags&bit != 0 {
			v, err := binary.ReadUvarint(r)
			if err != nil || v >= 1<<63 {
				return 0, 0, xzErr("bad size in block header")
			}
			sizes[k] = int64(v) //nolint:gosec // below 2^63, checked above
		}
	}
	id, err1 := binary.ReadUvarint(r)
	size, err2 := binary.ReadUvarint(r)
	code, err3 := r.ReadByte()
	switch {
	case flags&0x03 != 0:
		return 0, 0, xzErr("more than one filter")
	case errors.Join(err1, err2, err3) != nil || id != 0x21 || size != 1:
		return 0, 0, xzErr("block filter is not LZMA2")
	case code > 40:
		return 0, 0, xzErr("bad LZMA2 dictionary code %d", code)
	}
	for r.Len() > 0 {
		if b, _ := r.ReadByte(); b != 0 {
			return 0, 0, xzErr("block header padding is not zero")
		}
	}
	dict := int64(1)<<32 - 1
	if code < 40 {
		dict = (2 | int64(code)&1) << (11 + code/2)
	}
	if dict > XZDictCap {
		return 0, 0, &LimitError{"xz dictionary", dict, XZDictCap}
	}
	return sizes[0], sizes[1], nil
}
