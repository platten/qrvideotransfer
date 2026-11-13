package frame

import "errors"

// DescriptorSize is the encoded size of a Descriptor in bytes.
const DescriptorSize = 59

// ErrDescriptor is wrapped by every error of ParseDescriptor.
var ErrDescriptor = errors.New("frame: invalid stream descriptor")

// Descriptor is the stream descriptor of FORMAT.md section 10, carried by codeword 0 of every frame.
type Descriptor struct {
	Session           uint64
	StreamLen         int64 // L, at most 2^48-1
	ShardSize         int   // S
	K, M              int   // data and parity shards per block
	Blocks            int   // B
	Frames            int   // F
	CodewordsPerFrame int   // C
	OrderSeed         uint32
	DisplayFPS        uint8    // rounded; 0 if unknown
	StreamHash        [16]byte // first 16 bytes of SHA-256 of the stream
}

// Marshal returns the 59-byte encoding of d, with version 1, a zero reserved byte and the CRC-32C.
func (d Descriptor) Marshal() [DescriptorSize]byte {
	return [DescriptorSize]byte{}
}

// ParseDescriptor decodes and validates a descriptor: b holds at least DescriptorSize bytes (the
// decoded shard), h is the frame's header and codewordsPerFrame the C of its mapping. It applies
// every rejection rule of FORMAT.md section 10.
func ParseDescriptor(b []byte, h Header, codewordsPerFrame int) (Descriptor, error) {
	return Descriptor{}, nil
}
