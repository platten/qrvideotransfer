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

func hostileFixture(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "hostile", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// TestHostileDescriptors: every change of descriptors.json to a valid descriptor (its CRC
// recomputed unless the case says not) is refused with ErrDescriptor and the reason given.
func TestHostileDescriptors(t *testing.T) {
	var cases []struct {
		Name   string `json:"name"`
		Byte   int    `json:"byte"`
		Xor    byte   `json:"xor"`
		CRC    bool   `json:"crc"`
		Reason string `json:"reason"`
	}
	hostileFixture(t, "descriptors.json", &cases)
	d, h, c := exampleDescriptor()
	good := d.Marshal()
	for _, k := range cases {
		b := good
		b[k.Byte] ^= k.Xor
		if k.CRC {
			fixCRC(b[:])
		}
		_, err := ParseDescriptor(b[:], h, c)
		if !errors.Is(err, ErrDescriptor) || !strings.Contains(err.Error(), k.Reason) {
			t.Errorf("%s: got %v, want ErrDescriptor with %q", k.Name, err, k.Reason)
		}
	}
}

// TestHostileFrameHeaders: every change of frame-headers.json to a valid header is refused by
// Validate with the reason given, and the header, encoded, does not decode.
func TestHostileFrameHeaders(t *testing.T) {
	var cases []struct {
		Name   string `json:"name"`
		Field  string `json:"field"`
		Value  uint32 `json:"value"`
		Reason string `json:"reason"`
	}
	hostileFixture(t, "frame-headers.json", &cases)
	good := Header{Version: Version, Session: 0x1234, FrameIndex: 5, TotalFrames: 8, GridX: 480, GridY: 270, Modulation: 2, InnerCode: 2}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, k := range cases {
		h := good
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
		if err := h.Validate(); err == nil || !strings.Contains(err.Error(), k.Reason) {
			t.Errorf("%s: Validate gave %v, want %q", k.Name, err, k.Reason)
		}
		bits := h.Encode()
		soft := make([]int8, HeaderSlots)
		for i, b := range bits {
			soft[i] = 100
			if b {
				soft[i] = -100
			}
		}
		if got, ok := DecodeHeader(soft); ok {
			t.Errorf("%s: decoded %+v", k.Name, got)
		}
	}
}
