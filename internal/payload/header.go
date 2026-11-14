package payload

import (
	"crypto/ed25519"
	"encoding/binary"
	"fmt"
	"math"
)

// Compression selects how [Pack] compresses the archive. The values equal the root package's
// qrvt.Compression.
type Compression uint8

// Compression choices.
const (
	CompressAuto Compression = iota // zstd unless a sample does not compress (see [Pack])
	CompressZstd
	CompressXZ
	CompressNone
)

// Method is the compression byte of the stream header (FORMAT.md section 12).
type Method uint8

// Compression methods.
const (
	MethodNone Method = 0
	MethodZstd Method = 1
	MethodXZ   Method = 2
)

// Stream layout (FORMAT.md section 12).
const (
	HeaderSize    = 24                                            // stream header
	SignatureSize = ed25519.PublicKeySize + ed25519.SignatureSize // signature block of a signed stream
	FlagEncrypted = 0x01                                          // header flag: the body is age-encrypted
	FlagSigned    = 0x02                                          // header flag: a signature block follows
	streamMagic   = "QRVS"                                        // header bytes 0..3
	streamVersion = 1                                             // header byte 4
	signLabel     = "qrvt/sign/v1"                                // prefix of the signed message
	maxArchiveLen = math.MaxInt64                                 // archive lengths beyond int64
	headerFlags   = FlagEncrypted | FlagSigned                    // the only flag bits allowed
)

// Header is the stream header.
type Header struct {
	Method     Method
	Encrypted  bool
	Signed     bool
	ArchiveLen int64 // archive bytes before compression
	BodyLen    int64 // Lb
}

// Marshal returns the 24-byte encoding of h.
func (h Header) Marshal() [HeaderSize]byte {
	var b [HeaderSize]byte
	copy(b[:], streamMagic)
	b[4] = streamVersion
	b[5] = byte(h.Method)
	if h.Encrypted {
		b[6] |= FlagEncrypted
	}
	if h.Signed {
		b[6] |= FlagSigned
	}
	binary.BigEndian.PutUint64(b[8:], uint64(h.ArchiveLen)) //nolint:gosec // never negative
	binary.BigEndian.PutUint64(b[16:], uint64(h.BodyLen))   //nolint:gosec // never negative
	return b
}

// StreamLen returns the stream length L that h implies: 24 + Lb, plus 96 when signed.
func (h Header) StreamLen() int64 {
	n := HeaderSize + h.BodyLen
	if h.Signed {
		n += SignatureSize
	}
	return n
}

// ParseHeader decodes and checks the first 24 bytes of b. It rejects everything FORMAT.md section 12
// forbids in a header, and a body longer than [MaxStreamLen].
func ParseHeader(b []byte) (Header, error) {
	if len(b) < HeaderSize {
		return Header{}, fmt.Errorf("%w: stream shorter than its header", ErrFormat)
	}
	if string(b[:4]) != streamMagic {
		return Header{}, fmt.Errorf("%w: bad stream magic %q", ErrFormat, b[:4])
	}
	if b[4] != streamVersion {
		return Header{}, &UnsupportedVersionError{"stream version", int64(b[4])}
	}
	if b[5] > byte(MethodXZ) {
		return Header{}, &UnsupportedVersionError{"compression", int64(b[5])}
	}
	if b[6]&^headerFlags != 0 {
		return Header{}, fmt.Errorf("%w: unknown header flags %#02x", ErrFormat, b[6])
	}
	if b[7] != 0 {
		return Header{}, fmt.Errorf("%w: reserved header byte is %d", ErrFormat, b[7])
	}
	a, l := binary.BigEndian.Uint64(b[8:]), binary.BigEndian.Uint64(b[16:])
	if a > maxArchiveLen {
		return Header{}, &LimitError{"archive length", maxArchiveLen, maxArchiveLen, ""}
	}
	if l > MaxStreamLen {
		return Header{}, &LimitError{"stream length", int64(min(l, maxArchiveLen)), MaxStreamLen, ""}
	}
	h := Header{
		Method:     Method(b[5]),
		Encrypted:  b[6]&FlagEncrypted != 0,
		Signed:     b[6]&FlagSigned != 0,
		ArchiveLen: int64(a),
		BodyLen:    int64(l),
	}
	if h.StreamLen() > MaxStreamLen {
		return Header{}, &LimitError{"stream length", h.StreamLen(), MaxStreamLen, ""}
	}
	return h, nil
}
