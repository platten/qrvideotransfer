package payload

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// xzGuard passes an xz stream through unchanged while parsing its structure, and refuses a block
// whose LZMA2 dictionary is larger than XZDictCap before the decoder sees the block header.
// github.com/ulikunitz/xz treats ReaderConfig.DictCap as a minimum and allocates the dictionary a
// block header asks for (lzmafilter.go, reader), so the limit must be enforced here.
//
// The walk follows the .xz file format 1.1.0: a 12-byte stream header; blocks, each a header of
// (first byte + 1) * 4 bytes, LZMA2 chunks up to the end-of-data chunk, padding to a multiple of four
// and a check; then an index, which begins with a zero byte, and the footer, passed through as they
// are. LZMA2 chunk headers carry their sizes (lzma/header2.go), so no decoding is needed.
type xzGuard struct {
	r     io.Reader
	buf   []byte // checked bytes not yet returned
	raw   int64  // bytes to pass through without parsing
	state int
	check int64 // size of each block's check
	data  int64 // LZMA2 bytes in the current block, for its padding
	err   error
}

const (
	xzStreamHeader = iota
	xzBlock
	xzChunk
	xzTail
)

var xzMagic = []byte{0xfd, '7', 'z', 'X', 'Z', 0}

func (g *xzGuard) Read(p []byte) (int, error) {
	for g.err == nil && len(g.buf) == 0 && g.raw == 0 && g.state != xzTail {
		g.err = g.next()
	}
	switch { // the error first: the unit that failed is still in the buffer and must not be served
	case g.err != nil:
		return 0, g.err
	case len(g.buf) > 0:
		n := copy(p, g.buf)
		g.buf = g.buf[n:]
		return n, nil
	case g.state == xzTail:
		return g.r.Read(p)
	}
	if int64(len(p)) > g.raw {
		p = p[:g.raw]
	}
	n, err := g.r.Read(p)
	g.raw -= int64(n)
	if errors.Is(err, io.EOF) && g.raw > 0 {
		err = io.ErrUnexpectedEOF
	}
	return n, err
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
		if !bytes.Equal(h[:6], xzMagic) || h[6] != 0 {
			return xzErr("bad stream header")
		}
		// Check sizes of the check types the library supports: none, CRC-32, CRC-64, SHA-256.
		sizes := map[byte]int64{0x00: 0, 0x01: 4, 0x04: 8, 0x0a: 32}
		s, ok := sizes[h[7]]
		if !ok {
			return xzErr("unsupported check type %#x", h[7])
		}
		g.check, g.state = s, xzBlock
	case xzBlock:
		b, err := g.take(1)
		if err != nil {
			return err
		}
		if b[0] == 0 { // the index: the rest of the stream carries no block
			g.state = xzTail
			return nil
		}
		h, err := g.take((int(b[0])+1)*4 - 1)
		if err != nil {
			return err
		}
		if err := checkBlockHeader(h); err != nil {
			return err
		}
		g.data, g.state = 0, xzChunk
	case xzChunk:
		c, err := g.take(1)
		if err != nil {
			return err
		}
		switch {
		case c[0] == 0: // end of data: padding and check follow
			g.data++
			g.raw = (4-g.data%4)%4 + g.check
			g.state = xzBlock
		case c[0] == 1 || c[0] == 2: // uncompressed chunk
			s, err := g.take(2)
			if err != nil {
				return err
			}
			g.raw = int64(binary.BigEndian.Uint16(s)) + 1
			g.data += 3 + g.raw
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
		default:
			return xzErr("bad LZMA2 chunk header %#x", c[0])
		}
	}
	return nil
}

// checkBlockHeader checks a block header after its size byte: exactly one filter, LZMA2, with a
// dictionary of at most XZDictCap. The library checks the CRC and everything else.
func checkBlockHeader(h []byte) error {
	flags, r := h[0], bytes.NewReader(h[1:len(h)-4])
	if flags&0x03 != 0 {
		return xzErr("more than one filter")
	}
	for _, bit := range []byte{0x40, 0x80} { // compressed size, uncompressed size: skipped when present
		if flags&bit == 0 {
			continue
		}
		if _, err := binary.ReadUvarint(r); err != nil {
			return xzErr("bad size in block header")
		}
	}
	id, err1 := binary.ReadUvarint(r)
	size, err2 := binary.ReadUvarint(r)
	code, err3 := r.ReadByte()
	if err := errors.Join(err1, err2, err3); err != nil || id != 0x21 || size != 1 {
		return xzErr("block filter is not LZMA2")
	}
	if code > 40 {
		return xzErr("bad LZMA2 dictionary code %d", code)
	}
	dict := int64(1)<<32 - 1
	if code < 40 {
		dict = (2 | int64(code)&1) << (11 + code/2)
	}
	if dict > XZDictCap {
		return &LimitError{"xz dictionary", dict, XZDictCap}
	}
	return nil
}
