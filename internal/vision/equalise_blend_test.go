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
	blend(&st, 0)
	if st.Variance[0] != 4 {
		t.Fatal("no blend changed the variance")
	}
	blend(&st, 0.1)
	// Level centres 0 and 200 in Y: spread 2 x 100^2 over 3 planes and 2 levels.
	if want := float32(4 + 0.01*20000/6); math.Abs(float64(st.Variance[0]-want)) > 1e-3 || st.Variance[1] != st.Variance[0] {
		t.Fatalf("blend variance %v, want %v", st.Variance[:2], want)
	}
}
