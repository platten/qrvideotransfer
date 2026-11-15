package vision

import (
	"math"
	"testing"

	"github.com/platten/qrvideotransfer/internal/modem"
)

// TestFitSkipsBlends: with anchors that see a boundary, the cells that blend two frames are marked
// to be left out of the fit; blend widens the variances by the share of another frame.
func TestFitSkipsBlends(t *testing.T) {
	s := newSynth(t, 480, 270, 2, 3, 0, 32)
	edge := func(_, y float64) [4]float64 {
		u := math.Min(1, math.Max(0, (y-135)/60))
		return [4]float64{1 - u, u, 0, 0}
	}
	pf := AnchorPhase{}.Estimate(synthAnchors(s.l, 2.4, edge), s.l)
	skip := mixedCells(pf)
	n := 0
	for _, k := range skip {
		if k {
			n++
		}
	}
	if n == 0 || n == len(skip) {
		t.Fatalf("%d of %d cells skipped", n, len(skip))
	}
	if mixedCells(PhaseField{Weights: make([][4]float32, 3)}) != nil {
		t.Fatal("a field with no clean cell should skip none")
	}
	st := modem.LocalStats{Levels: 2}
	st.Centre[1] = [3]float32{200, 128, 128}
	st.Centre[0] = [3]float32{0, 128, 128}
	st.Variance[0], st.Variance[1] = 4, 4
	blend(&st, 0, 1)
	if st.Variance[0] != 4 {
		t.Fatal("no blend changed the variance")
	}
	// Levels 0 and 200 in Y: the variance of the level centres is 100^2 (DESIGN.md section 10, step
	// 7), so a share of 0.1 of another frame adds 0.01 x 10,000.
	blend(&st, 0.1, 1)
	if want := float32(4 + 100); math.Abs(float64(st.Variance[0]-want)) > 1e-3 || st.Variance[1] != st.Variance[0] {
		t.Fatalf("blend variance %v, want %v", st.Variance[:2], want)
	}
	// With three planes it is the variance per plane, averaged: chroma is constant here.
	st.Variance[0], st.Variance[1] = 4, 4
	blend(&st, 0.1, 3)
	if want := float32(4 + 100.0/3); math.Abs(float64(st.Variance[0]-want)) > 1e-3 {
		t.Fatalf("three-plane blend variance %v, want %v", st.Variance[0], want)
	}
}

// TestSoftBitsMixtureCap: a credited cell the phase field reads at weight w below capWeight states
// no more than ln(w / (1 - w)) + capNats nats per bit, whatever its fit; cells at or above
// capWeight are not capped (at weight 1 nothing changes at all), and no soft bit changes sign.
func TestSoftBitsMixtureCap(t *testing.T) {
	s := newSynth(t, 212, 120, 2, 0.5, 0, 40)
	f := (&TileEqualiser{Mod: s.c}).Fit(s.obs, s.palette, s.l, s.anchors)
	n := len(s.obs)
	free := make([]int8, 2*n)
	SoftBits(free, s.obs, s.l, f, s.c, nil, 0)
	for _, w := range []float32{0.92, 0.95, 0.969, 0.97, 1} {
		pf := PhaseField{Weights: make([][4]float32, n)}
		for i := range pf.Weights {
			pf.Weights[i][0], pf.Weights[i][1] = w, 1-w
		}
		got := make([]int8, 2*n)
		SoftBits(got, s.obs, s.l, f, s.c, &pf, 0)
		limit := math.Inf(1)
		if w < capWeight {
			limit = modem.LLRScale * (math.Log(float64(w)/float64(1-w)) + capNats)
		}
		capped := 0
		for i := range got {
			if a := math.Abs(float64(got[i])); a > limit+0.5 {
				t.Fatalf("w %.3f: soft bit %d is %d, above the cap %.1f", w, i, got[i], limit)
			}
			if got[i] != 0 && (got[i] > 0) != (free[i] > 0) {
				t.Fatalf("w %.3f: soft bit %d changed sign", w, i)
			}
			if got[i] != free[i] {
				capped++
			}
		}
		if w == 1 && capped > 0 {
			t.Errorf("w %.3f: %d soft bits changed", w, capped)
		}
		if w < capWeight && capped == 0 {
			t.Errorf("w %.3f: no soft bit capped", w)
		}
	}
}
