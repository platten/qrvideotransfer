package frame

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/platten/qrvideotransfer/internal/fec/conv"
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

// encodeBytes encodes the 14 header bytes b as Encode does, whatever their CRC.
func encodeBytes(b [14]byte) [HeaderSlots]bool {
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

// headerWith returns exampleHeader changed by f.
func headerWith(f func(*Header)) Header {
	h := exampleHeader
	f(&h)
	return h
}

// TestHeaderErrorText: each reason's text is exactly the one Validate gave before HeaderError
// existed, and those of the registries and the grid are the ones their own checks give.
func TestHeaderErrorText(t *testing.T) {
	_, modErr := LookupModulation(5)
	_, innerErr := LookupInner(7)
	for _, c := range []struct {
		r    HeaderReason
		h    Header
		want string
	}{
		{HeaderLength, Header{}, "frame: header is not 236 soft bits"},
		{HeaderCRC, Header{}, "frame: header CRC mismatch"},
		{HeaderReserved, headerWith(func(h *Header) { h.Reserved = 3 }), "frame: header reserved bits are not zero"},
		{HeaderVersion, headerWith(func(h *Header) { h.Version = 2 }), "frame: unsupported format version 2"},
		{HeaderFrameCount, headerWith(func(h *Header) { h.TotalFrames = 658 }), "frame: invalid frame count 658"},
		{HeaderFrameIndex, headerWith(func(h *Header) { h.FrameIndex = 656 }), "frame: frame index 656 not below frame count 656"},
		{HeaderModulation, headerWith(func(h *Header) { h.Modulation = 5 }), modErr.Error()},
		{HeaderInner, headerWith(func(h *Header) { h.InnerCode = 7 }), innerErr.Error()},
		{HeaderGrid, headerWith(func(h *Header) { h.GridX = 1 }), CheckGrid(1, 270).Error()},
	} {
		e := &HeaderError{Reason: c.r, Header: c.h}
		if e.Error() != c.want || !errors.Is(e, ErrHeader) {
			t.Errorf("reason %d: %q (wraps ErrHeader: %v), want %q", c.r, e.Error(), errors.Is(e, ErrHeader), c.want)
		}
		if c.r <= HeaderCRC {
			continue
		}
		if err := c.h.Validate(); err == nil || err.Error() != c.want {
			t.Errorf("reason %d: Validate gave %v, want %q", c.r, err, c.want)
		}
		if got, r := DecodeHeaderReason(soft(c.h.Encode())); r != c.r || got != c.h {
			t.Errorf("reason %d: DecodeHeaderReason = %+v, %d", c.r, got, r)
		}
	}
	if !strings.HasPrefix(modErr.Error(), "frame: unknown modulation 5") || !strings.HasPrefix(innerErr.Error(), "frame: unknown inner code 7") {
		t.Errorf("registry texts changed: %v, %v", modErr, innerErr)
	}
	if err := exampleHeader.Validate(); err != nil {
		t.Errorf("Validate of a valid header: %v", err)
	}
}

// TestDecodeHeaderReasonCRCAndLength: a header whose CRC does not match is refused for HeaderCRC
// and soft bits of any other length than HeaderSlots for HeaderLength, both with a zero header.
func TestDecodeHeaderReasonCRCAndLength(t *testing.T) {
	good := exampleHeader.Bytes()
	if encodeBytes(good) != exampleHeader.Encode() {
		t.Fatal("encodeBytes differs from Encode")
	}
	for i := range 112 {
		b := good
		b[i/8] ^= 0x80 >> (i % 8) // one field or CRC bit flipped, the CRC not recomputed
		if h, r := DecodeHeaderReason(soft(encodeBytes(b))); r != HeaderCRC || h != (Header{}) {
			t.Errorf("bit %d flipped: %+v, %d, want HeaderCRC", i, h, r)
		}
	}
	s := soft(exampleHeader.Encode())
	for _, n := range []int{0, 1, HeaderSlots - 1, HeaderSlots + 1, 2 * HeaderSlots} {
		in := make([]int8, n)
		copy(in, s)
		if h, r := DecodeHeaderReason(in); r != HeaderLength || h != (Header{}) {
			t.Errorf("%d soft bits: %+v, %d, want HeaderLength", n, h, r)
		}
	}
	if h, r := DecodeHeaderReason(s); r != HeaderOK || h != exampleHeader {
		t.Errorf("valid header: %+v, %d", h, r)
	}
}

// checkReasonAgrees asserts DecodeHeader(s) == (h, r == HeaderOK), with a zero header on a
// refusal, and that the reason agrees with Validate for the header returned.
func checkReasonAgrees(t *testing.T, s []int8) {
	t.Helper()
	h, r := DecodeHeaderReason(s)
	dh, ok := DecodeHeader(s)
	want := Header{}
	if r == HeaderOK {
		want = h
	}
	if ok != (r == HeaderOK) || dh != want {
		t.Fatalf("DecodeHeader = %+v, %v; DecodeHeaderReason = %+v, %d", dh, ok, h, r)
	}
	err := h.Validate()
	var he *HeaderError
	switch {
	case r == HeaderOK && err != nil:
		t.Fatalf("accepted %+v, which Validate refuses: %v", h, err)
	case r > HeaderCRC && (!errors.As(err, &he) || he.Reason != r || he.Header != h):
		t.Fatalf("refused %+v for %d, Validate gave %v", h, r, err)
	case (r == HeaderLength || r == HeaderCRC) && h != (Header{}):
		t.Fatalf("reason %d with header %+v", r, h)
	case r > HeaderGrid:
		t.Fatalf("reason %d out of range", r)
	}
}

// TestDecodeHeaderReasonProperty: on arbitrary soft bits, DecodeHeader(s) == (h, r == HeaderOK)
// for (h, r) = DecodeHeaderReason(s). The inputs are random headers of any field values, encoded
// and partly corrupted, so every reason is reached, and pure noise.
func TestDecodeHeaderReasonProperty(t *testing.T) {
	rng := rand.New(rand.NewPCG(9, 10))
	seen := map[HeaderReason]int{}
	for i := range 20000 {
		var s []int8
		switch i % 4 {
		case 0: // noise of any length near HeaderSlots
			s = make([]int8, HeaderSlots-2+rng.IntN(5))
			for j := range s {
				s[j] = int8(rng.IntN(256) - 128)
			}
		default: // a random header, a third of them with a CRC error, and up to 12 flipped slots
			total := 4 * (1 + rng.Uint32N(MaxFrames/4))
			h := Header{
				Version: Version, Session: uint16(rng.Uint32()), FrameIndex: rng.Uint32N(total), TotalFrames: total,
				GridX: 480, GridY: 270, Modulation: 1 + uint8(rng.IntN(4)), InnerCode: 1 + uint8(rng.IntN(4)),
			}
			for range rng.IntN(3) { // any value of up to two fields, which may be valid or not
				switch rng.IntN(8) {
				case 0:
					h.Reserved = uint8(rng.IntN(16))
				case 1:
					h.Version = uint8(rng.IntN(16))
				case 2:
					h.TotalFrames = rng.Uint32N(1 << 22)
				case 3:
					h.FrameIndex = rng.Uint32N(1 << 22)
				case 4:
					h.Modulation = uint8(rng.IntN(8))
				case 5:
					h.InnerCode = uint8(rng.IntN(8))
				case 6:
					h.GridX = uint16(rng.IntN(2048))
				default:
					h.GridY = uint16(rng.IntN(2048))
				}
			}
			b := h.Bytes()
			if i%4 == 3 {
				b[12+rng.IntN(2)] ^= 1 << rng.IntN(8)
			}
			s = soft(encodeBytes(b), rng.Perm(HeaderSlots)[:rng.IntN(13)]...)
		}
		checkReasonAgrees(t, s)
		_, r := DecodeHeaderReason(s)
		seen[r]++
	}
	for r := HeaderOK; r <= HeaderGrid; r++ {
		if seen[r] == 0 {
			t.Errorf("reason %d never reached", r)
		}
	}
	t.Logf("reasons seen: %v", seen)
}

// TestDecodeHeaderReasonAllocs: DecodeHeaderReason allocates no more than DecodeHeader does for
// a valid header, whatever the reason, since it builds no error value; vision calls it in its lens
// loop.
func TestDecodeHeaderReasonAllocs(t *testing.T) {
	good := soft(exampleHeader.Encode())
	base := testing.AllocsPerRun(100, func() { DecodeHeader(good) })
	bad := good[:1:1]
	crc := exampleHeader.Bytes()
	crc[13] ^= 1
	inputs := map[string][]int8{
		"ok": good, "length": bad, "crc": soft(encodeBytes(crc)),
		"version":    soft(headerWith(func(h *Header) { h.Version = 2 }).Encode()),
		"frame":      soft(headerWith(func(h *Header) { h.FrameIndex = 656 }).Encode()),
		"modulation": soft(headerWith(func(h *Header) { h.Modulation = 5 }).Encode()),
		"inner":      soft(headerWith(func(h *Header) { h.InnerCode = 7 }).Encode()),
		"grid":       soft(headerWith(func(h *Header) { h.GridX = 1 }).Encode()),
	}
	for name, s := range inputs {
		n := testing.AllocsPerRun(100, func() { DecodeHeaderReason(s) })
		bytes := allocBytes(func() { DecodeHeaderReason(s) })
		t.Logf("%s: %v allocations, %d bytes (DecodeHeader of a valid header: %v)", name, n, bytes, base)
		if n > base {
			t.Errorf("%s: %v allocations, more than DecodeHeader's %v", name, n, base)
		}
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
		checkReasonAgrees(t, s)
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
