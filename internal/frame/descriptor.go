package frame

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"

	"github.com/platten/qrvideotransfer/internal/fec"
)

// DescriptorSize is the encoded size of a Descriptor in bytes.
const DescriptorSize = 59

// ErrDescriptor is wrapped by every error of ParseDescriptor.
var ErrDescriptor = errors.New("frame: invalid stream descriptor")

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

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
// Field values wider than their field are truncated.
func (d Descriptor) Marshal() [DescriptorSize]byte {
	var b [DescriptorSize]byte
	copy(b[0:], "QRVT")
	b[4] = 1
	binary.BigEndian.PutUint64(b[5:], d.Session)
	put48(b[13:], uint64(d.StreamLen))                              //nolint:gosec // truncated to 48 bits
	binary.BigEndian.PutUint16(b[19:], uint16(d.ShardSize))         //nolint:gosec // 16-bit field
	binary.BigEndian.PutUint16(b[21:], uint16(d.K))                 //nolint:gosec // 16-bit field
	binary.BigEndian.PutUint16(b[23:], uint16(d.M))                 //nolint:gosec // 16-bit field
	put24(b[25:], uint32(d.Blocks))                                 //nolint:gosec // 24-bit field
	put24(b[28:], uint32(d.Frames))                                 //nolint:gosec // 24-bit field
	binary.BigEndian.PutUint16(b[31:], uint16(d.CodewordsPerFrame)) //nolint:gosec // 16-bit field
	binary.BigEndian.PutUint32(b[33:], d.OrderSeed)
	b[37] = d.DisplayFPS
	copy(b[39:], d.StreamHash[:])
	binary.BigEndian.PutUint32(b[55:], crc32.Checksum(b[:55], castagnoli))
	return b
}

// put48 writes the low 48 bits of v, big-endian.
func put48(b []byte, v uint64) {
	var t [8]byte
	binary.BigEndian.PutUint64(t[:], v)
	copy(b, t[2:])
}

// put24 writes the low 24 bits of v, big-endian.
func put24(b []byte, v uint32) {
	var t [4]byte
	binary.BigEndian.PutUint32(t[:], v)
	copy(b, t[1:])
}

func get24(b []byte) int { return int(b[0])<<16 | int(b[1])<<8 | int(b[2]) }

// ParseDescriptor decodes and validates a descriptor: b holds at least DescriptorSize bytes (the
// decoded shard), h is the frame's header and codewordsPerFrame the C of its mapping. It applies
// every rejection rule of FORMAT.md section 10.
func ParseDescriptor(b []byte, h Header, codewordsPerFrame int) (Descriptor, error) {
	bad := func(format string, a ...any) (Descriptor, error) {
		return Descriptor{}, fmt.Errorf("%w: "+format, append([]any{ErrDescriptor}, a...)...)
	}
	if err := h.Validate(); err != nil {
		return bad("%v", err)
	}
	if codewordsPerFrame < 2 {
		return bad("%d codewords per frame", codewordsPerFrame)
	}
	if len(b) < DescriptorSize {
		return bad("%d bytes", len(b))
	}
	if crc32.Checksum(b[:55], castagnoli) != binary.BigEndian.Uint32(b[55:]) {
		return bad("CRC mismatch")
	}
	if string(b[:4]) != "QRVT" || b[4] != 1 || b[38] != 0 {
		return bad("wrong magic, version or reserved byte")
	}
	d := Descriptor{
		Session:           binary.BigEndian.Uint64(b[5:]),
		StreamLen:         int64(binary.BigEndian.Uint16(b[13:]))<<32 | int64(binary.BigEndian.Uint32(b[15:])),
		ShardSize:         int(binary.BigEndian.Uint16(b[19:])),
		K:                 int(binary.BigEndian.Uint16(b[21:])),
		M:                 int(binary.BigEndian.Uint16(b[23:])),
		Blocks:            get24(b[25:]),
		Frames:            get24(b[28:]),
		CodewordsPerFrame: int(binary.BigEndian.Uint16(b[31:])),
		OrderSeed:         binary.BigEndian.Uint32(b[33:]),
		DisplayFPS:        b[37],
	}
	copy(d.StreamHash[:], b[39:55])
	inner, _ := LookupInner(h.InnerCode) // known after Validate
	switch {
	case uint16(d.Session) != h.Session: //nolint:gosec // low 16 bits on purpose
		return bad("session %016x does not match header session %04x", d.Session, h.Session)
	case d.Frames != int(h.TotalFrames):
		return bad("frame count %d does not match header %d", d.Frames, h.TotalFrames)
	case d.ShardSize != inner.ShardSize:
		return bad("shard size %d does not match inner code %d", d.ShardSize, h.InnerCode)
	case d.CodewordsPerFrame != codewordsPerFrame:
		return bad("codewords per frame %d, expected %d", d.CodewordsPerFrame, codewordsPerFrame)
	case d.M < 1 || d.M > fec.MaxShards:
		return bad("parity shards %d out of range", d.M)
	}
	blocks, k, err := fec.Shape(d.StreamLen, d.ShardSize)
	if err != nil || blocks != d.Blocks || k != d.K {
		return bad("blocks %d and K %d do not match stream length %d", d.Blocks, d.K, d.StreamLen)
	}
	p := fec.Plan{Blocks: blocks, K: k, M: d.M, ShardSize: d.ShardSize, Packets: int64(blocks) * int64(k+d.M)}
	if f := p.Frames(codewordsPerFrame); f != d.Frames {
		return bad("frame count %d, the plan needs %d", d.Frames, f)
	}
	return d, nil
}
