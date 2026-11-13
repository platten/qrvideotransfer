package frame

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"testing"
)

type descriptorGolden struct {
	CRC32C123456789 string `json:"crc32c_123456789"`
	Descriptors     []struct {
		Fields struct {
			Session         uint64
			StreamLen       int64
			ShardSize, K, M int
			Blocks, Frames  int
			C               int
			OrderSeed       uint32
			FPS             uint8
			Hash            string
		}
		Bytes string
	}
}

// A descriptor for 10 MiB at 480 x 270, Gray4, normal, parity 20 (FORMAT.md section 13).
func exampleDescriptor() (Descriptor, Header, int) {
	d := Descriptor{
		Session: 0x0123456789abcdef, StreamLen: 10 << 20, ShardSize: 128, K: 27307, M: 5462, Blocks: 3,
		Frames: 656, CodewordsPerFrame: 151, OrderSeed: 1, DisplayFPS: 12,
	}
	h := exampleHeader
	h.Session = 0xcdef
	return d, h, 151
}

func TestDescriptorGolden(t *testing.T) {
	var g descriptorGolden
	readGolden(t, "descriptor.json", &g)
	if g.CRC32C123456789 != "e3069283" {
		t.Errorf("golden CRC-32C = %s", g.CRC32C123456789)
	}
	for _, c := range g.Descriptors {
		f := c.Fields
		d := Descriptor{
			Session: f.Session, StreamLen: f.StreamLen, ShardSize: f.ShardSize, K: f.K, M: f.M,
			Blocks: f.Blocks, Frames: f.Frames, CodewordsPerFrame: f.C, OrderSeed: f.OrderSeed, DisplayFPS: f.FPS,
		}
		if _, err := hex.Decode(d.StreamHash[:], []byte(f.Hash)); err != nil {
			t.Fatal(err)
		}
		b := d.Marshal()
		if got := hex.EncodeToString(b[:]); got != c.Bytes {
			t.Errorf("Marshal = %s, want %s", got, c.Bytes)
		}
		h := Header{Version: 1, Session: uint16(f.Session), TotalFrames: uint32(f.Frames), GridX: 480, GridY: 270, Modulation: 2, InnerCode: 2}
		got, err := ParseDescriptor(append(b[:], make([]byte, 128-DescriptorSize)...), h, f.C)
		if err != nil || got != d {
			t.Errorf("ParseDescriptor = %+v, %v; want %+v", got, err, d)
		}
	}
}

// fixCRC rewrites the CRC-32C of a 59-byte descriptor after a test has changed a byte.
func fixCRC(b []byte) []byte {
	binary.BigEndian.PutUint32(b[55:], crc32.Checksum(b[:55], crc32.MakeTable(crc32.Castagnoli)))
	return b
}

func TestParseDescriptorRejects(t *testing.T) {
	d, h, c := exampleDescriptor()
	good := d.Marshal()
	if _, err := ParseDescriptor(good[:], h, c); err != nil {
		t.Fatalf("example descriptor rejected: %v", err)
	}
	bytesWith := func(f func(b []byte)) []byte {
		b := good
		f(b[:])
		return fixCRC(b[:])
	}
	fieldsWith := func(f func(d *Descriptor)) []byte {
		x := d
		f(&x)
		b := x.Marshal()
		return b[:]
	}
	h2 := h
	h2.InnerCode = 9
	cases := []struct {
		name string
		b    []byte
		h    Header
		c    int
	}{
		{"short", good[:58], h, c},
		{"crc", func() []byte { b := good; b[20] ^= 1; return b[:] }(), h, c},
		{"magic", bytesWith(func(b []byte) { b[0] = 'X' }), h, c},
		{"version", bytesWith(func(b []byte) { b[4] = 2 }), h, c},
		{"reserved", bytesWith(func(b []byte) { b[38] = 1 }), h, c},
		{"session", fieldsWith(func(d *Descriptor) { d.Session++ }), h, c},
		{"frames vs header", good[:], func() Header { x := h; x.TotalFrames = 660; return x }(), c},
		{"shard size", fieldsWith(func(d *Descriptor) { d.ShardSize = 192 }), h, c},
		{"unknown inner code", good[:], h2, c},
		{"codewords per frame", good[:], h, 150},
		{"descriptor C", fieldsWith(func(d *Descriptor) { d.CodewordsPerFrame = 150 }), h, c},
		{"blocks", fieldsWith(func(d *Descriptor) { d.Blocks = 4 }), h, c},
		{"K", fieldsWith(func(d *Descriptor) { d.K = 27306 }), h, c},
		{"M zero", fieldsWith(func(d *Descriptor) { d.M = 0 }), h, c},
		{"M too large", fieldsWith(func(d *Descriptor) { d.M = 32769 }), h, c},
		{"F vs plan", fieldsWith(func(d *Descriptor) { d.M = 9000 }), h, c},
		{"invalid header", good[:], func() Header { x := h; x.Reserved = 1; return x }(), c},
		{"C below 2", fieldsWith(func(d *Descriptor) { d.CodewordsPerFrame = 1 }), h, 1},
		{"empty stream", fieldsWith(func(d *Descriptor) { d.StreamLen = 0 }), h, c},
	}
	for _, tc := range cases {
		if _, err := ParseDescriptor(tc.b, tc.h, tc.c); !errors.Is(err, ErrDescriptor) {
			t.Errorf("%s: err = %v, want ErrDescriptor", tc.name, err)
		}
	}
}

func TestDescriptorFieldWidths(t *testing.T) {
	d := Descriptor{StreamLen: 1<<48 - 1, ShardSize: 0xffff, K: 0xffff, M: 0xffff, Blocks: 1<<24 - 1, Frames: 1<<24 - 1, CodewordsPerFrame: 0xffff, OrderSeed: ^uint32(0), DisplayFPS: 255}
	b := d.Marshal()
	if got := hex.EncodeToString(b[13:39]); got != "ffffffffffffffffffffffffffffffffffffffffffffffffff00" {
		t.Errorf("all-ones fields = %s", got)
	}
}

func FuzzParseDescriptor(f *testing.F) {
	d, h, c := exampleDescriptor()
	good := d.Marshal()
	f.Add(good[:])
	f.Add(make([]byte, DescriptorSize))
	f.Fuzz(func(t *testing.T, b []byte) {
		got, err := ParseDescriptor(b, h, c)
		if err != nil {
			return
		}
		again := got.Marshal()
		if string(again[:]) != string(b[:DescriptorSize]) {
			t.Fatal("accepted descriptor does not re-marshal to its input")
		}
	})
}
