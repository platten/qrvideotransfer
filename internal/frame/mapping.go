package frame

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
	return &Mapping{}, nil
}

// InterleaveSeed returns the interleaver seed of FORMAT.md section 9:
// Seed("qrvt/inner-interleave/v1", session16 | GridX16 | GridY16 | modulation8 | inner8).
func InterleaveSeed(h Header) uint64 {
	return 0
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
	return 0
}

// Position returns the bit position q of sent bit bit of codeword codeword in frame frame.
func (m *Mapping) Position(frame uint32, codeword, bit int) int {
	return 0
}

// ScrambleBit returns the scramble bit of position q in frame frame. It hashes once per call; use
// Scrambler for many positions of one frame.
func (m *Mapping) ScrambleBit(frame uint32, position int) uint8 {
	return 0
}

// Scrambler returns the scrambling sequence of frame frame.
func (m *Mapping) Scrambler(frame uint32) Scrambler {
	return 0
}

// Scrambler is the scrambling sequence of one frame: the SplitMix64 state it starts from.
type Scrambler uint64

// Bit returns the scramble bit of position q.
func (s Scrambler) Bit(q int) uint8 {
	return 0
}
