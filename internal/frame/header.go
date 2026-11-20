package frame

import (
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/platten/qrvideotransfer/internal/fec"
	"github.com/platten/qrvideotransfer/internal/fec/conv"
)

// Header sizes (FORMAT.md section 6).
const (
	HeaderBits  = 112 // fields and CRC
	HeaderSlots = 236 // coded bits, one per slot
	MaxFrames   = fec.MaxFrames
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

// HeaderReason says why a header was refused, or HeaderOK; Validate's checks in their order, after
// the length and the CRC.
type HeaderReason uint8

// The reasons a header is refused.
const (
	HeaderOK         HeaderReason = iota
	HeaderLength                  // not HeaderSlots soft bits
	HeaderCRC                     // the CRC-16 does not match
	HeaderReserved                // reserved bits not zero
	HeaderVersion                 // unsupported format version
	HeaderFrameCount              // frame count not a multiple of 4 in 4..MaxFrames
	HeaderFrameIndex              // frame index not below the frame count
	HeaderModulation              // unknown modulation id
	HeaderInner                   // unknown inner-code id
	HeaderGrid                    // grid outside the limits
)

// ErrHeader is wrapped by every HeaderError.
var ErrHeader = errors.New("frame: invalid header")

// HeaderError is the error of Validate: the reason and the header refused.
type HeaderError struct {
	Reason HeaderReason
	Header Header
}

// Error names the refused value, in the words of LookupModulation, LookupInner and CheckGrid for
// their reasons (FORMAT.md section 14).
func (e *HeaderError) Error() string {
	h := e.Header
	switch e.Reason {
	case HeaderLength:
		return fmt.Sprintf("frame: header is not %d soft bits", HeaderSlots)
	case HeaderCRC:
		return "frame: header CRC mismatch"
	case HeaderReserved:
		return "frame: header reserved bits are not zero"
	case HeaderVersion:
		return fmt.Sprintf("frame: unsupported format version %d", h.Version)
	case HeaderFrameCount:
		return fmt.Sprintf("frame: invalid frame count %d", h.TotalFrames)
	case HeaderFrameIndex:
		return fmt.Sprintf("frame: frame index %d not below frame count %d", h.FrameIndex, h.TotalFrames)
	case HeaderModulation:
		return fmt.Sprintf("frame: unknown modulation %d", h.Modulation)
	case HeaderInner:
		return fmt.Sprintf("frame: unknown inner code %d", h.InnerCode)
	case HeaderGrid:
		return fmt.Sprintf("frame: grid %d x %d outside the limits", h.GridX, h.GridY)
	}
	return fmt.Sprintf("frame: header refused for reason %d", e.Reason)
}

// Unwrap returns ErrHeader.
func (e *HeaderError) Unwrap() error { return ErrHeader }

// reason applies Validate's checks; it allocates nothing.
func (h Header) reason() HeaderReason {
	switch {
	case h.Reserved != 0:
		return HeaderReserved
	case h.Version != Version:
		return HeaderVersion
	case h.TotalFrames < 4 || h.TotalFrames%4 != 0 || h.TotalFrames > MaxFrames:
		return HeaderFrameCount
	case h.FrameIndex >= h.TotalFrames:
		return HeaderFrameIndex
	case !knownModulation(h.Modulation):
		return HeaderModulation
	case !knownInner(h.InnerCode):
		return HeaderInner
	case !gridOK(int(h.GridX), int(h.GridY)):
		return HeaderGrid
	}
	return HeaderOK
}

// DecodeHeaderReason is DecodeHeader with the reason for a refusal. It returns the decoded header
// with a reason of Validate, so that the refused value can be named, and a zero header with
// HeaderLength or HeaderCRC. It builds no error value and allocates no more than DecodeHeader.
func DecodeHeaderReason(soft []int8) (Header, HeaderReason) {
	if len(soft) != HeaderSlots {
		return Header{}, HeaderLength
	}
	coded := make([]int8, HeaderSlots)
	for n := range coded {
		v := soft[slotOf(n)]
		if maskBit(n) == 1 {
			v = -max(v, -127) // unmask without overflowing -128
		}
		coded[n] = v
	}
	msg := conv.Decode(coded)
	var b [14]byte
	for i := range HeaderBits {
		b[i/8] |= msg[i] << (7 - i%8)
	}
	r := bitReader{b: b[:]}
	h := Header{
		Version:     get[uint8](&r, 4),
		Session:     get[uint16](&r, 16),
		FrameIndex:  get[uint32](&r, 22),
		TotalFrames: get[uint32](&r, 22),
		GridX:       get[uint16](&r, 11),
		GridY:       get[uint16](&r, 11),
		Modulation:  get[uint8](&r, 3),
		InnerCode:   get[uint8](&r, 3),
		Reserved:    get[uint8](&r, 4),
	}
	if get[uint16](&r, 16) != CRC16(b[:12]) {
		return Header{}, HeaderCRC
	}
	return h, h.reason()
}

// headerMask is SHA-256("qrvt/header-mask/v1"), XORed onto the coded bits.
var headerMask = sha256.Sum256([]byte("qrvt/header-mask/v1"))

func maskBit(n int) uint8 { return headerMask[n/8] >> (7 - n%8) & 1 }

// slotOf returns the slot of coded bit n.
func slotOf(n int) int { return 67 * n % HeaderSlots }

// bitWriter appends big-endian bit fields to a byte slice.
type bitWriter struct {
	b []byte
	n int
}

func (w *bitWriter) put(v uint64, width int) {
	for i := width - 1; i >= 0; i-- {
		if v>>i&1 == 1 {
			w.b[w.n/8] |= 0x80 >> (w.n % 8)
		}
		w.n++
	}
}

// bitReader reads big-endian bit fields from a byte slice.
type bitReader struct {
	b []byte
	n int
}

// get reads a field of width bits; width must not exceed the size of T.
func get[T ~uint8 | ~uint16 | ~uint32](r *bitReader, width int) T {
	var v T
	for range width {
		v = v<<1 | T(r.b[r.n/8]>>(7-r.n%8)&1)
		r.n++
	}
	return v
}

// Bytes returns the 96 field bits followed by the CRC-16, as 14 bytes. Field values wider than
// their field are truncated.
func (h Header) Bytes() [14]byte {
	var b [14]byte
	w := bitWriter{b: b[:]}
	w.put(uint64(h.Version), 4)
	w.put(uint64(h.Session), 16)
	w.put(uint64(h.FrameIndex), 22)
	w.put(uint64(h.TotalFrames), 22)
	w.put(uint64(h.GridX), 11)
	w.put(uint64(h.GridY), 11)
	w.put(uint64(h.Modulation), 3)
	w.put(uint64(h.InnerCode), 3)
	w.put(uint64(h.Reserved), 4)
	w.put(uint64(CRC16(b[:12])), 16)
	return b
}

// Validate checks the rules a decoder applies to a header (FORMAT.md sections 3 and 6). Its error
// is a *HeaderError.
func (h Header) Validate() error {
	if r := h.reason(); r != HeaderOK {
		return &HeaderError{Reason: r, Header: h}
	}
	return nil
}

// Encode returns the bit shown in each of the 236 slots, indexed by slot number s = 4i + r; true is
// bit 1, drawn light.
func (h Header) Encode() [HeaderSlots]bool {
	b := h.Bytes()
	msg := make([]uint8, HeaderBits+conv.Memory)
	for i := range HeaderBits {
		msg[i] = b[i/8] >> (7 - i%8) & 1
	}
	var slots [HeaderSlots]bool
	for n, c := range conv.Encode(msg) {
		slots[slotOf(n)] = c^maskBit(n) == 1
	}
	return slots
}

// DecodeHeader decodes the soft bits of the 236 slots, indexed by slot number. A soft bit is a
// log-likelihood ratio: positive means bit 0 (dark), negative bit 1 (light), zero unknown. It reports
// false unless the CRC matches and the header passes Validate.
// DecodeHeaderReason gives the reason for a refusal.
func DecodeHeader(soft []int8) (Header, bool) {
	h, r := DecodeHeaderReason(soft)
	if r != HeaderOK {
		return Header{}, false
	}
	return h, true
}

// CRC16 returns CRC-16/CCITT-FALSE of b: polynomial 0x1021, initial value 0xFFFF, no reflection,
// no final XOR.
func CRC16(b []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, v := range b {
		crc ^= uint16(v) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}
