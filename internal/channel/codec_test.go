package channel

import (
	"math"
	"slices"
	"testing"

	"github.com/platten/qrvideotransfer/internal/prng"
	"github.com/platten/qrvideotransfer/internal/video"
)

func noisyFrame(w, h int, bits uint8, seed uint64) *video.Frame {
	g := prng.SplitMix64{State: seed}
	f := &video.Frame{W: w, H: h, Bits: bits, Y: make([]uint16, w*h), Cb: make([]uint16, (w+1)/2*((h+1)/2)), Cr: make([]uint16, (w+1)/2*((h+1)/2))}
	base := uint64(64) << (bits - 8)
	for _, p := range [][]uint16{f.Y, f.Cb, f.Cr} {
		for i := range p {
			p[i] = uint16(base + g.Next()%(base*2))
		}
	}
	return f
}

func cloneFrame(f *video.Frame) *video.Frame {
	g := *f
	g.Y, g.Cb, g.Cr = slices.Clone(f.Y), slices.Clone(f.Cb), slices.Clone(f.Cr)
	return &g
}

// The DCT basis is orthonormal.
func TestDCTBasis(t *testing.T) {
	for u := range 8 {
		for v := range 8 {
			dot := 0.0
			for i := range 8 {
				dot += dctBasis[u][i] * dctBasis[v][i]
			}
			want := 0.0
			if u == v {
				want = 1
			}
			if math.Abs(dot-want) > 1e-12 {
				t.Errorf("basis %d . %d = %v", u, v, dot)
			}
		}
	}
}

// Quantiser idempotence: quantising an already quantised frame changes nothing. A tiny step is the
// identity, and a flat block survives any step.
func TestQuantiser(t *testing.T) {
	for _, bits := range []uint8{8, 10} {
		f := noisyFrame(64, 32, bits, uint64(bits))
		orig := cloneFrame(f)
		q := Quantiser(20)
		if err := q(f); err != nil {
			t.Fatal(err)
		}
		if slices.Equal(f.Y, orig.Y) {
			t.Fatal("quantiser changed nothing")
		}
		once := cloneFrame(f)
		if err := q(f); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(f.Y, once.Y) || !slices.Equal(f.Cb, once.Cb) || !slices.Equal(f.Cr, once.Cr) {
			t.Errorf("%d bits: quantiser is not idempotent", bits)
		}
		g := cloneFrame(orig)
		_ = Quantiser(0.01)(g)
		if !slices.Equal(g.Y, orig.Y) {
			t.Errorf("%d bits: a fine step changed the frame", bits)
		}
		g = cloneFrame(orig)
		_ = Quantiser(0)(g)
		if !slices.Equal(g.Y, orig.Y) {
			t.Error("step 0 changed the frame")
		}
	}
	f := flat(16, 16, 100)
	f.Cb = nil
	f.Cr = nil
	_ = Quantiser(50)(f)
	for _, v := range f.Y {
		if v != 100 {
			t.Fatalf("flat block became %d", v)
		}
	}
}

func TestQuantiserStep(t *testing.T) {
	c, _ := Preset("budget-android")
	if s := QuantiserStep(c); !near(s, 3/(17e6/(1920*1080*30)), 1e-12) {
		t.Errorf("step %v", s)
	}
	c, _ = Preset("ideal")
	if QuantiserStep(c) != 0 {
		t.Error("lossless has a step")
	}
}
