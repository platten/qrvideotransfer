package payload

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
)

func TestHeaderRoundTrip(t *testing.T) {
	h := Header{Method: MethodXZ, Encrypted: true, Signed: true, ArchiveLen: 0x0102030405, BodyLen: 0x0a0b0c}
	b := h.Marshal()
	want := "5152565301" + "02" + "03" + "00" + "0000000102030405" + "00000000000a0b0c"
	if got := hex.EncodeToString(b[:]); got != want {
		t.Fatalf("Marshal = %s, want %s", got, want)
	}
	g, err := ParseHeader(b[:])
	if err != nil || g != h {
		t.Fatalf("ParseHeader = %+v, %v; want %+v", g, err, h)
	}
	if got := h.StreamLen(); got != 24+0x0a0b0c+96 {
		t.Fatalf("StreamLen = %d", got)
	}
	h.Signed = false
	if got := h.StreamLen(); got != 24+0x0a0b0c {
		t.Fatalf("StreamLen unsigned = %d", got)
	}
}

func TestParseHeaderRejects(t *testing.T) {
	good := Header{Method: MethodZstd, ArchiveLen: 100, BodyLen: 50}.Marshal()
	cases := []struct {
		name string
		edit func(b []byte) []byte
		want func(error) bool
	}{
		{"short", func(b []byte) []byte { return b[:23] }, isFormat},
		{"magic", func(b []byte) []byte { b[0] = 'X'; return b }, isFormat},
		{"version 0", func(b []byte) []byte { b[4] = 0; return b }, isUnsupported("stream version", 0)},
		{"version 2", func(b []byte) []byte { b[4] = 2; return b }, isUnsupported("stream version", 2)},
		{"compression 3", func(b []byte) []byte { b[5] = 3; return b }, isUnsupported("compression", 3)},
		{"flag 0x04", func(b []byte) []byte { b[6] = 0x04; return b }, isFormat},
		{"flag 0x80", func(b []byte) []byte { b[6] = 0x81; return b }, isFormat},
		{"reserved", func(b []byte) []byte { b[7] = 1; return b }, isFormat},
		{"archive length", func(b []byte) []byte { b[8] = 0x80; return b }, isLimit("archive length")},
		{"body length", func(b []byte) []byte { b[16] = 0x01; return b }, isLimit("stream length")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := good
			_, err := ParseHeader(c.edit(b[:]))
			if !c.want(err) {
				t.Fatalf("ParseHeader: %v", err)
			}
		})
	}
	if _, err := ParseHeader(append(good[:], 0)); err != nil {
		t.Fatalf("ParseHeader with trailing bytes: %v", err) // only the first 24 bytes are the header
	}
}

func isFormat(err error) bool { return errors.Is(err, ErrFormat) }

func isUnsupported(what string, v int64) func(error) bool {
	return func(err error) bool {
		var u *UnsupportedVersionError
		return errors.As(err, &u) && u.What == what && u.Value == v
	}
}

func isLimit(name string) func(error) bool {
	return func(err error) bool {
		var l *LimitError
		return errors.As(err, &l) && l.Limit == name
	}
}

func isUnsafe(err error) bool {
	var u *UnsafeArchiveError
	return errors.As(err, &u)
}

func FuzzParseHeader(f *testing.F) {
	for _, h := range []Header{{}, {Method: MethodXZ, Encrypted: true, Signed: true, ArchiveLen: 7, BodyLen: 9}} {
		b := h.Marshal()
		f.Add(b[:])
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		h, err := ParseHeader(b)
		if err != nil {
			return
		}
		if len(b) < HeaderSize || h.Method > MethodXZ || h.ArchiveLen < 0 || h.BodyLen < 0 ||
			h.StreamLen() > MaxStreamLen+SignatureSize {
			t.Fatalf("accepted %x as %+v", b, h)
		}
		m := h.Marshal()
		if !bytes.Equal(m[:], b[:HeaderSize]) {
			t.Fatalf("round trip %x -> %x", b[:HeaderSize], m)
		}
	})
}
