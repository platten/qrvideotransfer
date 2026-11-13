package frame

import (
	"encoding/binary"
	"fmt"

	"github.com/platten/qrvideotransfer/internal/prng"
)

// Mapping places codeword bits on bit positions and scrambles them (FORMAT.md section 9). It is
// immutable and safe for concurrent use.
type Mapping struct {
	h                Header
	cells, b         int // data cells, bits per cell
	sent, nbits, cpf int
	pi               []uint32
}

// NewMapping computes the mapping for the session, grid, modulation and inner code of h. It fails
// unless h is valid and a frame holds at least two codewords.
func NewMapping(h Header) (*Mapping, error) {
	if err := h.Validate(); err != nil {
		return nil, err
	}
	mod, _ := LookupModulation(h.Modulation) // known after Validate
	inner, _ := LookupInner(h.InnerCode)
	l, _ := NewLayout(int(h.GridX), int(h.GridY)) // the grid was checked by Validate
	m := &Mapping{h: h, cells: len(l.DataCells()), b: mod.BitsPerCell, sent: inner.Sent}
	m.nbits = m.cells * m.b
	m.cpf = m.nbits / m.sent
	if m.cpf < 2 {
		return nil, fmt.Errorf("frame: grid %d x %d holds %d codewords per frame, fewer than 2", h.GridX, h.GridY, m.cpf)
	}
	m.pi = prng.Shuffle(m.nbits, InterleaveSeed(h))
	return m, nil
}

// InterleaveSeed returns the interleaver seed of FORMAT.md section 9:
// Seed("qrvt/inner-interleave/v1", session16 | GridX16 | GridY16 | modulation8 | inner8).
func InterleaveSeed(h Header) uint64 {
	var b [8]byte
	binary.BigEndian.PutUint16(b[0:], h.Session)
	binary.BigEndian.PutUint16(b[2:], h.GridX)
	binary.BigEndian.PutUint16(b[4:], h.GridY)
	b[6], b[7] = h.Modulation, h.InnerCode
	return prng.Seed("qrvt/inner-interleave/v1", b[:])
}

// CodewordsPerFrame returns C.
func (m *Mapping) CodewordsPerFrame() int { return m.cpf }

// Bits returns Nbits, the number of bit positions in a frame.
func (m *Mapping) Bits() int { return m.nbits }

// Sent returns the number of sent bits per codeword.
func (m *Mapping) Sent() int { return m.sent }

// BitsPerCell returns b.
func (m *Mapping) BitsPerCell() int { return m.b }

// Rotation returns rho for frame f: (f * 2654435761) mod Nbits.
func (m *Mapping) Rotation(frame uint32) int {
	return int(uint64(frame) * 2654435761 % uint64(m.nbits)) //nolint:gosec // below Nbits
}

// Position returns the bit position q of sent bit bit of codeword codeword in frame frame.
// codeword must be in 0..C-1 and bit in 0..Sent()-1.
func (m *Mapping) Position(frame uint32, codeword, bit int) int {
	return int(m.pi[(codeword*m.sent+bit+m.Rotation(frame))%m.nbits])
}

// ScrambleBit returns the scramble bit of position q in frame frame. It hashes once per call; use
// Scrambler for many positions of one frame.
func (m *Mapping) ScrambleBit(frame uint32, position int) uint8 {
	return m.Scrambler(frame).Bit(position)
}

// Scrambler returns the scrambling sequence of frame frame:
// SplitMix64 from Seed("qrvt/scramble/v1", session16 | f32).
func (m *Mapping) Scrambler(frame uint32) Scrambler {
	var b [6]byte
	binary.BigEndian.PutUint16(b[0:], m.h.Session)
	binary.BigEndian.PutUint32(b[2:], frame)
	return Scrambler(prng.Seed("qrvt/scramble/v1", b[:]))
}

// Scrambler is the scrambling sequence of one frame: the SplitMix64 state it starts from.
type Scrambler uint64

// Bit returns the scramble bit of position q: bit 63 - q mod 64 of output q div 64.
func (s Scrambler) Bit(q int) uint8 {
	return uint8(prng.At(uint64(s), uint64(q/64)) >> (63 - q%64) & 1) //nolint:gosec // q >= 0
}
