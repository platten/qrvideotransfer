package frame

import (
	"crypto/sha256"
	"encoding/hex"
	"math/rand/v2"
	"testing"
)

type headerGolden struct {
	CRC16123456789 string `json:"crc16_123456789"`
	Headers        []struct {
		Fields struct {
			Version, Session, FrameIndex, TotalFrames, GridX, GridY, Modulation, InnerCode, Reserved uint32
		}
		Bytes string
		CRC   string
		Slots string
	}
}

func (g headerGolden) header(i int) Header {
	f := g.Headers[i].Fields
	return Header{
		Version: uint8(f.Version), Session: uint16(f.Session), FrameIndex: f.FrameIndex,
		TotalFrames: f.TotalFrames, GridX: uint16(f.GridX), GridY: uint16(f.GridY),
		Modulation: uint8(f.Modulation), InnerCode: uint8(f.InnerCode), Reserved: uint8(f.Reserved),
	}
}

// exampleHeader is the header of the worked table in FORMAT.md section 13.
var exampleHeader = Header{Version: 1, Session: 0x1234, FrameIndex: 5, TotalFrames: 656, GridX: 480, GridY: 270, Modulation: 2, InnerCode: 2}

func TestCRC16(t *testing.T) {
	if got := CRC16([]byte("123456789")); got != 0x29b1 {
		t.Errorf("CRC16(123456789) = %04x, want 29b1", got)
	}
}

// FORMAT.md section 13: header fields to bytes and CRC-16.
func TestHeaderBytes(t *testing.T) {
	b := exampleHeader.Bytes()
	if got, want := hex.EncodeToString(b[:]), "11234000014002903c04392003c7"; got != want {
		t.Errorf("Bytes = %s, want %s", got, want)
	}
}

func TestHeaderMask(t *testing.T) {
	sum := sha256.Sum256([]byte("qrvt/header-mask/v1"))
	if got := hex.EncodeToString(sum[:]); got != "7f02f40ba124b46bb5de9b5e7af0c1d012226027f03b1165cda2dece1ee7bcfd" {
		t.Errorf("mask = %s, differs from FORMAT.md section 6.2", got)
	}
}

func TestHeaderGolden(t *testing.T) {
	var g headerGolden
	readGolden(t, "header.json", &g)
	if g.CRC16123456789 != "29b1" {
		t.Errorf("golden CRC16 = %s", g.CRC16123456789)
	}
	for i, c := range g.Headers {
		h := g.header(i)
		b := h.Bytes()
		if got := hex.EncodeToString(b[:]); got != c.Bytes {
			t.Errorf("header %d bytes = %s, want %s", i, got, c.Bytes)
		}
		slots := h.Encode()
		for s, bit := range slots {
			if want := c.Slots[s] == '1'; bit != want {
				t.Fatalf("header %d slot %d = %v, want %v", i, s, bit, want)
			}
		}
		if got, ok := DecodeHeader(soft(slots)); !ok || got != h {
			t.Errorf("header %d: decode = %+v, %v", i, got, ok)
		}
	}
}

// soft turns slot bits into soft bits of magnitude 20, flipping the listed slots.
func soft(slots [HeaderSlots]bool, flips ...int) []int8 {
	s := make([]int8, HeaderSlots)
	for i, b := range slots {
		s[i] = 20
		if b {
			s[i] = -20
		}
	}
	for _, f := range flips {
		s[f] = -s[f]
	}
	return s
}

func TestDecodeHeaderCorrectsErrors(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	slots := exampleHeader.Encode()
	for range 2000 {
		flips := r.Perm(HeaderSlots)[:4]
		if got, ok := DecodeHeader(soft(slots, flips...)); !ok || got != exampleHeader {
			t.Fatalf("errors in slots %v not corrected", flips)
		}
	}
	// A whole slot row of one column erased, and extreme values.
	s := soft(slots)
	for i := range s {
		if i%4 == 0 {
			s[i] = 0
		} else if s[i] < 0 {
			s[i] = -128
		} else {
			s[i] = 127
		}
	}
	if got, ok := DecodeHeader(s); !ok || got != exampleHeader {
		t.Error("decode with erased row and extreme values failed")
	}
}

func TestDecodeHeaderRejects(t *testing.T) {
	mod := func(f func(*Header)) Header {
		h := exampleHeader
		f(&h)
		return h
	}
	bad := []Header{
		mod(func(h *Header) { h.Reserved = 1 }),
		mod(func(h *Header) { h.Version = 2 }),
		mod(func(h *Header) { h.TotalFrames = 658 }),
		mod(func(h *Header) { h.TotalFrames, h.FrameIndex = 0, 0 }),
		mod(func(h *Header) { h.FrameIndex = 656 }),
		mod(func(h *Header) { h.GridY = 29 }),
		mod(func(h *Header) { h.GridX = 1 }),
		mod(func(h *Header) { h.Modulation = 0 }),
		mod(func(h *Header) { h.Modulation = 5 }),
		mod(func(h *Header) { h.InnerCode = 0 }),
		mod(func(h *Header) { h.InnerCode = 7 }),
	}
	for _, h := range bad {
		if h.Validate() == nil {
			t.Errorf("%+v validates", h)
		}
		if _, ok := DecodeHeader(soft(h.Encode())); ok {
			t.Errorf("%+v decodes", h)
		}
	}
	if err := exampleHeader.Validate(); err != nil {
		t.Errorf("example header: %v", err)
	}
	// A CRC error: flip a field bit of the encoded message by re-encoding with a corrupted CRC is not
	// possible through the API, so use noise that decodes to some other path.
	r := rand.New(rand.NewPCG(7, 8))
	accepted := 0
	for range 1000 {
		s := make([]int8, HeaderSlots)
		for i := range s {
			s[i] = int8(r.IntN(256) - 128)
		}
		if _, ok := DecodeHeader(s); ok {
			accepted++
		}
	}
	if accepted > 0 {
		t.Errorf("%d of 1000 random inputs accepted", accepted)
	}
	if _, ok := DecodeHeader(make([]int8, HeaderSlots-1)); ok {
		t.Error("short input accepted")
	}
}

// Fields wider than their width are truncated by Encode, so they cannot alias other fields.
func TestHeaderFieldWidths(t *testing.T) {
	h := Header{Version: 0x1f, Session: 0xffff, FrameIndex: 1<<23 - 1, TotalFrames: 1<<23 - 1, GridX: 0xffff, GridY: 0xffff, Modulation: 0xff, InnerCode: 0xff, Reserved: 0xff}
	b := h.Bytes()
	if got := hex.EncodeToString(b[:12]); got != "ffffffffffffffffffffffff" {
		t.Errorf("all-ones header = %s", got)
	}
	h.Version = 0x10 // only bit 4, outside the 4-bit field
	b = h.Bytes()
	if b[0]>>4 != 0 {
		t.Errorf("version overflowed: %02x", b[0])
	}
}

func FuzzDecodeHeader(f *testing.F) {
	f.Add(make([]byte, HeaderSlots))
	sl := soft(exampleHeader.Encode())
	seed := make([]byte, len(sl))
	for i, v := range sl {
		seed[i] = byte(v)
	}
	f.Add(seed)
	f.Fuzz(func(t *testing.T, b []byte) {
		s := make([]int8, len(b))
		for i, v := range b {
			s[i] = int8(v)
		}
		h, ok := DecodeHeader(s)
		if !ok {
			return
		}
		if err := h.Validate(); err != nil {
			t.Fatalf("accepted an invalid header: %v", err)
		}
		if again, ok := DecodeHeader(soft(h.Encode())); !ok || again != h {
			t.Fatalf("re-encoding %+v does not round-trip", h)
		}
	})
}

func BenchmarkDecodeHeader(b *testing.B) {
	s := soft(exampleHeader.Encode(), 1, 100, 200)
	for b.Loop() {
		DecodeHeader(s)
	}
}
