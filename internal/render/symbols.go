package render

import (
	"errors"

	"github.com/platten/qrvideotransfer/internal/frame"
)

// Mapper turns the codewords of a frame into data-cell symbols (FORMAT.md section 9). It reuses one
// bit buffer, so it is not safe for concurrent use; give each worker its own.
type Mapper struct {
	m     *frame.Mapping
	words []uint64
}

// NewMapper returns a Mapper for a mapping.
func NewMapper(m *frame.Mapping) *Mapper {
	return &Mapper{m: m}
}

// Symbols writes the symbol of every data cell of frame f to dst, which must hold exactly
// Bits()/BitsPerCell() entries. codewords holds the C codewords of the frame, codeword 0 being the
// descriptor; each holds Sent() bits packed eight per byte, most significant bit first, as
// fec.InnerCode.Encode writes them. Positions that receive no codeword bit hold 0; every position is
// then scrambled.
func (p *Mapper) Symbols(dst []uint8, f uint32, codewords [][]byte) error {
	return errors.New("render: not implemented")
}
