package vision

import (
	"math"
	"testing"

	"github.com/platten/qrvideotransfer/internal/modem"
	"github.com/platten/qrvideotransfer/internal/prng"
)

// TestFFT compares the mixed-radix transform with the definition and checks the round trip.
func TestFFT(t *testing.T) {
	g := prng.SplitMix64{State: 3}
	for _, k := range [][2]int{{1, 1}, {8, 4}, {12, 30}, {25, 18}} {
		w, h := k[0], k[1]
		re, im := make([]float64, w*h), make([]float64, w*h)
		for i := range re {
			re[i], im[i] = gauss(&g), gauss(&g)
		}
		r0, i0 := append([]float64(nil), re...), append([]float64(nil), im...)
		ft := newFFT2(w, h)
		ft.transform(re, im, false)
		for v := range h {
			for u := range w {
				var sr, si float64
				for y := range h {
					for x := range w {
						a := -2 * math.Pi * (float64(u*x)/float64(w) + float64(v*y)/float64(h))
						sr += r0[y*w+x]*math.Cos(a) - i0[y*w+x]*math.Sin(a)
						si += r0[y*w+x]*math.Sin(a) + i0[y*w+x]*math.Cos(a)
					}
				}
				if math.Abs(sr-re[v*w+u]) > 1e-9 || math.Abs(si-im[v*w+u]) > 1e-9 {
					t.Fatalf("%d x %d bin %d,%d: %v%+vi, want %v%+vi", w, h, u, v, re[v*w+u], im[v*w+u], sr, si)
				}
			}
		}
		ft.transform(re, im, true)
		for i := range re {
			if math.Abs(re[i]-r0[i]) > 1e-12 || math.Abs(im[i]-i0[i]) > 1e-12 {
				t.Fatalf("%d x %d round trip differs at %d", w, h, i)
			}
		}
	}
	for n, want := range map[int]int{0: 1, 1: 1, 7: 8, 121: 125, 212: 216, 270: 270, 271: 288} {
		if got := smooth(n); got != want {
			t.Errorf("smooth(%d) = %d, want %d", n, got, want)
		}
	}
}

// residualRMS returns the RMS difference between the observations and the true centres.
func residualRMS(s *synth, obs []modem.Obs) float64 {
	sum := 0.0
	for i, c := range s.l.DataCells() {
		d := float64(obs[i].Y - s.centre(s.levels[i], int(c.X), int(c.Y))[0])
		sum += d * d
	}
	return math.Sqrt(sum / float64(len(obs)))
}

// addWave adds a plane wave of the given amplitude and frequency, in cycles per cell, to the Y
// plane of every data cell.
func addWave(s *synth, amp, fx, fy float64) {
	for i, c := range s.l.DataCells() {
		s.obs[i].Y += float32(amp * math.Sin(2*math.Pi*(fx*float64(c.X)+fy*float64(c.Y))+1))
	}
}

func TestCancelStripes(t *testing.T) {
	for _, k := range []struct {
		gx, gy int
		mod    uint8
		fx, fy float64
	}{{212, 120, 2, 0.13, 0.31}, {480, 270, 2, 0.41, -0.07}, {320, 180, 3, -0.22, 0.45}} {
		s := newSynth(t, k.gx, k.gy, k.mod, 2, 0, 20)
		addWave(s, 20, k.fx, k.fy)
		before := residualRMS(s, s.obs)
		f := (&TileEqualiser{Mod: s.c}).Fit(s.obs, s.palette, s.l, s.anchors)
		n := CancelStripes(s.obs, s.l, f, s.c)
		after := residualRMS(s, s.obs)
		t.Logf("%d x %d %s: residual RMS %.2f before, %.2f after removing %d bins", k.gx, k.gy, s.c.Name(), before, after, n)
		if n == 0 || after > 3.5 {
			t.Errorf("residual RMS %.2f after removing %d bins", after, n)
		}
	}
}

// TestCancelStripesNone: without stripes, cancellation changes almost nothing.
func TestCancelStripesNone(t *testing.T) {
	s := newSynth(t, 212, 120, 2, 3, 0, 21)
	before := residualRMS(s, s.obs)
	f := (&TileEqualiser{Mod: s.c}).Fit(s.obs, s.palette, s.l, s.anchors)
	CancelStripes(s.obs, s.l, f, s.c)
	if after := residualRMS(s, s.obs); after > before*1.02 {
		t.Errorf("residual RMS %.3f became %.3f", before, after)
	}
}

// TestEqualiserStripes: the equaliser with Stripes set cancels the stripes in place and fits the
// levels as if there were none.
func TestEqualiserStripes(t *testing.T) {
	s := newSynth(t, 480, 270, 2, 3, 0, 22)
	// 15 stays below half the narrowest level spacing of the synthetic grid (37 in its corner), so
	// the levels are decidable everywhere.
	addWave(s, 15, 0.29, 0.17)
	f := (&TileEqualiser{Mod: s.c, Stripes: true}).Fit(s.obs, s.palette, s.l, s.anchors)
	checkField(t, s, f, 3, 3)
	if r := residualRMS(s, s.obs); r > 4 {
		t.Errorf("residual RMS %.2f", r)
	}
}

// TestCancelStripesDegenerate: missing observations are left alone and a nil field removes nothing.
func TestCancelStripesDegenerate(t *testing.T) {
	s := newSynth(t, 212, 120, 2, 3, 0, 23)
	addWave(s, 20, 0.2, 0.2)
	s.obs[10].Y = float32(math.NaN())
	s.obs[11].Saturated = true
	y11 := s.obs[11].Y
	f := (&TileEqualiser{Mod: s.c}).Fit(s.obs, s.palette, s.l, s.anchors)
	if CancelStripes(s.obs[:50], s.l, f, s.c) < 0 || CancelStripes(s.obs, s.l, nil, s.c) != 0 {
		t.Fatal("degenerate input")
	}
	CancelStripes(s.obs, s.l, f, s.c)
	if !math.IsNaN(float64(s.obs[10].Y)) || s.obs[11].Y != y11 {
		t.Error("missing observations changed")
	}
}
