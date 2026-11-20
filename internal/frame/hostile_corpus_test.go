package frame

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The descriptors and frame headers of the hostile corpus (SECURITY.md section 9.2,
// testdata/hostile/ at the module's root); internal/payload runs the rest.

func hostileFixture(t testing.TB, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "hostile", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// hostileDescriptor is one case of descriptors.json, applied to exampleDescriptor.
type hostileDescriptor struct {
	Name   string `json:"name"`
	Byte   int    `json:"byte"`
	Xor    byte   `json:"xor"`
	CRC    bool   `json:"crc"`
	Reason string `json:"reason"`
	bytes  []byte
}

// hostileDescriptors returns the cases of descriptors.json with their bytes: exampleDescriptor
// changed as each case says, its CRC recomputed unless the case says not.
func hostileDescriptors(t testing.TB) []hostileDescriptor {
	t.Helper()
	var cases []hostileDescriptor
	hostileFixture(t, "descriptors.json", &cases)
	d, _, _ := exampleDescriptor()
	good := d.Marshal()
	for i, k := range cases {
		b := good
		b[k.Byte] ^= k.Xor
		if k.CRC {
			fixCRC(b[:])
		}
		cases[i].bytes = b[:]
	}
	return cases
}

// TestHostileDescriptors: every case of descriptors.json is refused with ErrDescriptor and the
// reason given.
func TestHostileDescriptors(t *testing.T) {
	_, h, c := exampleDescriptor()
	for _, k := range hostileDescriptors(t) {
		_, err := ParseDescriptor(k.bytes, h, c)
		if !errors.Is(err, ErrDescriptor) || !strings.Contains(err.Error(), k.Reason) {
			t.Errorf("%s: got %v, want ErrDescriptor with %q", k.Name, err, k.Reason)
		}
	}
}

// hostileHeader is one case of frame-headers.json, applied to goodHeader.
type hostileHeader struct {
	Name   string `json:"name"`
	Field  string `json:"field"`
	Value  uint32 `json:"value"`
	Reason string `json:"reason"`
	h      Header
}

// goodHeader is the valid header the cases of frame-headers.json change.
var goodHeader = Header{Version: Version, Session: 0x1234, FrameIndex: 5, TotalFrames: 8, GridX: 480, GridY: 270, Modulation: 2, InnerCode: 2}

// hostileFieldReasons maps a field of frame-headers.json to the reason its cases are refused for.
var hostileFieldReasons = map[string]HeaderReason{
	"Reserved": HeaderReserved, "Version": HeaderVersion, "Modulation": HeaderModulation,
	"InnerCode": HeaderInner, "TotalFrames": HeaderFrameCount, "FrameIndex": HeaderFrameIndex,
	"GridX": HeaderGrid, "GridY": HeaderGrid,
}

// hostileFrameHeaders returns the cases of frame-headers.json with their headers.
func hostileFrameHeaders(t testing.TB) []hostileHeader {
	t.Helper()
	var cases []hostileHeader
	hostileFixture(t, "frame-headers.json", &cases)
	for i, k := range cases {
		h := goodHeader
		switch k.Field {
		case "Reserved":
			h.Reserved = uint8(k.Value)
		case "Version":
			h.Version = uint8(k.Value)
		case "Modulation":
			h.Modulation = uint8(k.Value)
		case "InnerCode":
			h.InnerCode = uint8(k.Value)
		case "TotalFrames":
			h.TotalFrames = k.Value
		case "FrameIndex":
			h.FrameIndex = k.Value
		case "GridX":
			h.GridX = uint16(k.Value)
		case "GridY":
			h.GridY = uint16(k.Value)
		default:
			t.Fatalf("%s: unknown field %q", k.Name, k.Field)
		}
		cases[i].h = h
	}
	return cases
}

// TestHostileFrameHeaders: every change of frame-headers.json to a valid header is refused by
// Validate with the reason given, and the header, encoded, does not decode.
func TestHostileFrameHeaders(t *testing.T) {
	if err := goodHeader.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, k := range hostileFrameHeaders(t) {
		if err := k.h.Validate(); err == nil || !strings.Contains(err.Error(), k.Reason) {
			t.Errorf("%s: Validate gave %v, want %q", k.Name, err, k.Reason)
		}
		if got, ok := DecodeHeader(soft(k.h.Encode())); ok {
			t.Errorf("%s: decoded %+v", k.Name, got)
		}
	}
}

// TestHostileFrameHeaderReasons: every case of frame-headers.json, encoded with its CRC
// recomputed, is refused by DecodeHeaderReason for its field's reason, returning the header it
// carried; Validate's error is a HeaderError of that reason and header, wraps ErrHeader, and has
// the HeaderError's text. ParseDescriptor, given the header, still names Validate's text under
// ErrDescriptor.
func TestHostileFrameHeaderReasons(t *testing.T) {
	d, _, c := exampleDescriptor()
	desc := d.Marshal()
	for _, k := range hostileFrameHeaders(t) {
		want := hostileFieldReasons[k.Field]
		got, r := DecodeHeaderReason(soft(k.h.Encode()))
		if r != want || got != k.h {
			t.Errorf("%s: DecodeHeaderReason = %+v, %d; want %+v, %d", k.Name, got, r, k.h, want)
		}
		err := k.h.Validate()
		var he *HeaderError
		if !errors.Is(err, ErrHeader) || !errors.As(err, &he) || he.Reason != want || he.Header != k.h {
			t.Errorf("%s: Validate gave %#v, want a HeaderError of reason %d wrapping ErrHeader", k.Name, err, want)
			continue
		}
		text := (&HeaderError{Reason: r, Header: got}).Error()
		if text != err.Error() || !strings.Contains(text, k.Reason) {
			t.Errorf("%s: HeaderError text %q, Validate's %q, want both with %q", k.Name, text, err, k.Reason)
		}
		_, perr := ParseDescriptor(desc[:], k.h, c)
		if !errors.Is(perr, ErrDescriptor) || perr.Error() != ErrDescriptor.Error()+": "+text {
			t.Errorf("%s: ParseDescriptor gave %v", k.Name, perr)
		}
	}
}
