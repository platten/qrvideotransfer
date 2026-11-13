package render

import (
	"testing"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/prng"
)

func mustMapping(t *testing.T, h frame.Header) *frame.Mapping {
	t.Helper()
	m, err := frame.NewMapping(h)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// codewordBytes returns C zeroed codewords of Sent() bits, packed.
func codewordBytes(m *frame.Mapping) [][]byte {
	cw := make([][]byte, m.CodewordsPerFrame())
	for i := range cw {
		cw[i] = make([]byte, (m.Sent()+7)/8)
	}
	return cw
}

// TestSymbolsWorkedExample reproduces the worked positions of FORMAT.md section 13: session 0,
// grid 480 x 270, Gray4, inner code 2; sent bit 0 of codeword 1 goes to data cell 942, second bit,
// in frame 0 (scramble bit 1) and to data cell 69,312, second bit, in frame 1 (scramble bit 0).
func TestSymbolsWorkedExample(t *testing.T) {
	m := mustMapping(t, frame.Header{Version: 1, TotalFrames: 4, GridX: 480, GridY: 270, Modulation: 2, InnerCode: 2})
	p := NewMapper(m)
	n := m.Bits() / m.BitsPerCell()
	for _, tc := range []struct {
		f        uint32
		cell     int
		scramble uint8
	}{{0, 942, 1}, {1, 69312, 0}} {
		cw := codewordBytes(m)
		zero := make([]uint8, n)
		if err := p.Symbols(zero, tc.f, cw); err != nil {
			t.Fatal(err)
		}
		cw[1][0] = 0x80
		one := make([]uint8, n)
		if err := p.Symbols(one, tc.f, cw); err != nil {
			t.Fatal(err)
		}
		if zero[tc.cell]&1 != tc.scramble {
			t.Errorf("frame %d: scramble bit %d, want %d", tc.f, zero[tc.cell]&1, tc.scramble)
		}
		for e := range n {
			want := zero[e]
			if e == tc.cell {
				want ^= 1
			}
			if one[e] != want {
				t.Errorf("frame %d cell %d: %d, want %d", tc.f, e, one[e], want)
			}
		}
	}
}

// TestSymbolsRandom compares random codewords with a direct evaluation of FORMAT.md section 9.
func TestSymbolsRandom(t *testing.T) {
	for mod := uint8(1); mod <= 4; mod++ {
		m := mustMapping(t, frame.Header{Version: 1, Session: 0xBEEF, TotalFrames: 8, GridX: 212, GridY: 120, Modulation: mod, InnerCode: 4})
		p := NewMapper(m)
		g := prng.SplitMix64{State: uint64(mod)}
		cw := codewordBytes(m)
		for _, c := range cw {
			for i := range c {
				c[i] = byte(g.Next())
			}
		}
		for _, f := range []uint32{0, 7} {
			bits := make([]uint8, m.Bits())
			for c := range m.CodewordsPerFrame() {
				for b := range m.Sent() {
					bits[m.Position(f, c, b)] = cw[c][b/8] >> (7 - b%8) & 1
				}
			}
			s := m.Scrambler(f)
			for q := range bits {
				bits[q] ^= s.Bit(q)
			}
			b := m.BitsPerCell()
			got := make([]uint8, m.Bits()/b)
			if err := p.Symbols(got, f, cw); err != nil {
				t.Fatal(err)
			}
			for e := range got {
				var want uint8
				for k := range b {
					want = want<<1 | bits[e*b+k]
				}
				if got[e] != want {
					t.Fatalf("mod %d frame %d cell %d: %d, want %d", mod, f, e, got[e], want)
				}
			}
		}
		dst := make([]uint8, m.Bits()/m.BitsPerCell())
		if a := testing.AllocsPerRun(3, func() { _ = p.Symbols(dst, 1, cw) }); a != 0 {
			t.Errorf("mod %d: %v allocations", mod, a)
		}
	}
}

func TestSymbolsErrors(t *testing.T) {
	m := mustMapping(t, frame.Header{Version: 1, TotalFrames: 4, GridX: 212, GridY: 120, Modulation: 2, InnerCode: 2})
	p := NewMapper(m)
	n := m.Bits() / m.BitsPerCell()
	cw := codewordBytes(m)
	if err := p.Symbols(make([]uint8, n-1), 0, cw); err == nil {
		t.Error("short dst: no error")
	}
	if err := p.Symbols(make([]uint8, n), 0, cw[1:]); err == nil {
		t.Error("too few codewords: no error")
	}
	cw[2] = cw[2][:len(cw[2])-1]
	if err := p.Symbols(make([]uint8, n), 0, cw); err == nil {
		t.Error("short codeword: no error")
	}
}
