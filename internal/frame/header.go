package frame

// Header sizes (FORMAT.md section 6).
const (
	HeaderBits  = 112 // fields and CRC
	HeaderSlots = 236 // coded bits, one per slot
	MaxFrames   = 1<<22 - 4
	Version     = 1
)

// Header is the per-frame header of FORMAT.md section 6.1.
type Header struct {
	Version      uint8  // 4 bits; 1
	Session      uint16 // low 16 bits of the session id
	FrameIndex   uint32 // 22 bits; 0..TotalFrames-1
	TotalFrames  uint32 // 22 bits; F, a multiple of 4, at least 4
	GridX, GridY uint16 // 11 bits each
	Modulation   uint8  // 3 bits; FORMAT.md section 7
	InnerCode    uint8  // 3 bits; FORMAT.md section 8
	Reserved     uint8  // 4 bits; zero
}

// Bytes returns the 96 field bits followed by the CRC-16, as 14 bytes.
func (h Header) Bytes() [14]byte {
	return [14]byte{}
}

// Validate checks the rules a decoder applies to a header (FORMAT.md sections 3 and 6).
func (h Header) Validate() error {
	return nil
}

// Encode returns the bit shown in each of the 236 slots, indexed by slot number s = 4i + r; true is
// bit 1, drawn light.
func (h Header) Encode() [HeaderSlots]bool {
	return [HeaderSlots]bool{}
}

// DecodeHeader decodes the soft bits of the 236 slots, indexed by slot number. A soft bit is a
// log-likelihood ratio: positive means bit 0 (dark), negative bit 1 (light), zero unknown. It reports
// false unless the CRC matches and the header passes Validate.
func DecodeHeader(soft []int8) (Header, bool) {
	return Header{}, false
}

// CRC16 returns CRC-16/CCITT-FALSE of b: polynomial 0x1021, initial value 0xFFFF, no reflection,
// no final XOR.
func CRC16(b []byte) uint16 {
	return 0
}
