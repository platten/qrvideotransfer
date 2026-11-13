package render

import (
	"fmt"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/prng"
)

// Mapper turns the codewords of a frame into data-cell symbols (FORMAT.md section 9). It reuses one
// bit buffer, so it is not safe for concurrent use; give each worker its own.
type Mapper struct {
	m     *frame.Mapping
	words []uint64 // bit position q is bit 63 - q mod 64 of word q div 64
}

// NewMapper returns a Mapper for a mapping.
func NewMapper(m *frame.Mapping) *Mapper {
	return &Mapper{m: m, words: make([]uint64, (m.Bits()+63)/64)}
}

// Symbols writes the symbol of every data cell of frame f to dst, which must hold exactly
// Bits()/BitsPerCell() entries. codewords holds the C codewords of the frame, codeword 0 being the
// descriptor; each holds Sent() bits packed eight per byte, most significant bit first, as
// fec.InnerCode.Encode writes them. Positions that receive no codeword bit hold 0; every position is
// then scrambled.
func (p *Mapper) Symbols(dst []uint8, f uint32, codewords [][]byte) error {
	m := p.m
	b, sent := m.BitsPerCell(), m.Sent()
	if len(dst) != m.Bits()/b {
		return fmt.Errorf("render: %d symbols for %d data cells", len(dst), m.Bits()/b)
	}
	if len(codewords) != m.CodewordsPerFrame() {
		return fmt.Errorf("render: %d codewords for %d per frame", len(codewords), m.CodewordsPerFrame())
	}
	for c, cw := range codewords {
		if len(cw) < (sent+7)/8 {
			return fmt.Errorf("render: codeword %d holds %d bytes, fewer than %d bits", c, len(cw), sent)
		}
	}
	w := p.words
	clear(w)
	for c, cw := range codewords {
		for t := range sent {
			if cw[t/8]>>(7-t%8)&1 != 0 {
				q := m.Position(f, c, t)
				w[q/64] |= 1 << (63 - q%64)
			}
		}
	}
	s := uint64(m.Scrambler(f))
	for i := range w {
		w[i] ^= prng.At(s, uint64(i))
	}
	q := 0
	for e := range dst {
		var v uint8
		for range b {
			v = v<<1 | uint8(w[q/64]>>(63-q%64)&1)
			q++
		}
		dst[e] = v
	}
	return nil
}
