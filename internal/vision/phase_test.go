package vision

import (
	"math"
	"testing"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
)

// camera returns the code value a camera with exponent gamma gives for linear light v in [0, 1],
// with white at 16 + span.
func camera(v, gamma, span float64) float32 {
	return float32(16 + span*math.Pow(v, 1/gamma))
}

// synthAnchors returns the anchors of a layout whose phase markers see the weights w(x, y) at the
// marker centre, filmed by a camera with the given exponent, panel black 2% of white, and an
// exposure that varies over the grid.
func synthAnchors(l *frame.Layout, gamma float64, w func(x, y float64) [4]float64) []AnchorObs {
	var out []AnchorObs
	for _, a := range l.Anchors() {
		span := 200 - 0.1*float64(a.X)
		x, y := float64(a.X+6), float64(a.Y+2)
		o := AnchorObs{Anchor: a, Found: true}
		o.Black = modem.Obs{Y: camera(0.02, gamma, span), Cb: 128, Cr: 128}
		o.White = modem.Obs{Y: camera(1, gamma, span), Cb: 128, Cr: 128}
		for q, wq := range w(x, y) {
			o.Phase[q] = camera(0.02+0.98*wq, gamma, span)
		}
		out = append(out, o)
	}
	return out
}

// ramp is a diagonal boundary from phase 1 to phase 2 across the middle of a 480 x 270 grid.
func ramp(x, y float64) [4]float64 {
	u := math.Min(1, math.Max(0, (x+2*y-500)/300))
	return [4]float64{0, 1 - u, u, 0}
}

func TestEstimateRamp(t *testing.T) {
	l, _ := frame.NewLayout(480, 270)
	for _, gamma := range []float64{2.0, 2.4, 2.8} {
		pf := AnchorPhase{Gamma: gamma}.Estimate(synthAnchors(l, gamma, ramp), l)
		if len(pf.Weights) != len(l.DataCells()) {
			t.Fatalf("%d weights for %d cells", len(pf.Weights), len(l.DataCells()))
		}
		worst := 0.0
		inner := innerCells(l)
		for i, c := range l.DataCells() {
			want := ramp(float64(c.X)+0.5, float64(c.Y)+0.5)
			sum := 0.0
			for p := range 4 {
				sum += float64(pf.Weights[i][p])
				if pf.Weights[i][p] < 0 {
					t.Fatalf("cell %d: negative weight %v", i, pf.Weights[i])
				}
				// Bilinear interpolation of a clipped ramp is exact away from its two kinks; at a kink
				// it cuts the corner by up to a quarter of the ramp's change over a lattice step.
				// Extrapolation across a kink is not bounded, so only inner cells are judged.
				if !inner[i] {
					continue
				}
				worst = math.Max(worst, math.Abs(float64(pf.Weights[i][p])-want[p]))
			}
			if math.Abs(sum-1) > 1e-4 {
				t.Fatalf("cell %d: weights %v sum to %v", i, pf.Weights[i], sum)
			}
		}
		t.Logf("gamma %.1f: largest weight error %.4f", gamma, worst)
		if worst > 0.1 {
			t.Errorf("gamma %.1f: largest weight error %.4f", gamma, worst)
		}
	}
}

// innerCells reports the data cells whose weights are interpolated between four anchors of the
// layout, rather than extrapolated beyond the outermost or from a predicted node.
func innerCells(l *frame.Layout) []bool {
	lat := newLattice(l, TileW, TileH)
	for _, a := range l.Anchors() {
		k, _ := lat.index(a)
		lat.ok[k] = true
	}
	out := make([]bool, len(l.DataCells()))
	for i, c := range l.DataCells() {
		k, fx, fy := lat.locate(float64(c.X)+0.5-18, float64(c.Y)+0.5-15, false)
		out[i] = fx >= 0 && fx <= 1 && fy >= 0 && fy <= 1
		for _, n := range lat.corners(k, fx, fy) {
			out[i] = out[i] && (n.w == 0 || lat.ok[n.n])
		}
	}
	return out
}

// TestEstimateWrongGamma: assuming 2.4 for a camera of exponent 2.0 biases an uneven blend.
func TestEstimateWrongGamma(t *testing.T) {
	l, _ := frame.NewLayout(212, 120)
	blend := func(float64, float64) [4]float64 { return [4]float64{0, 0, 0.3, 0.7} }
	pf := AnchorPhase{}.Estimate(synthAnchors(l, 2.0, blend), l)
	if w := pf.Weights[len(pf.Weights)/2]; math.Abs(float64(w[2]-0.3)) < 0.01 {
		t.Errorf("default gamma on a 2.0 camera gave %v", w)
	}
	pf = AnchorPhase{Gamma: 2}.Estimate(synthAnchors(l, 2.0, blend), l)
	if w := pf.Weights[len(pf.Weights)/2]; math.Abs(float64(w[2]-0.3)) > 1e-3 {
		t.Errorf("gamma 2.0 gave %v", w)
	}
}

// TestEstimateMissingAnchors: anchors not found, saturated or without contrast are filled in from
// their neighbours; with none usable every weight is zero and the capture is not clean.
func TestEstimateMissingAnchors(t *testing.T) {
	l, _ := frame.NewLayout(480, 270)
	clean := func(float64, float64) [4]float64 { return [4]float64{0, 0, 0, 1} }
	a := synthAnchors(l, 2.4, clean)
	for i := range a {
		switch i % 5 {
		case 1:
			a[i].Found = false
		case 2:
			a[i].White.Saturated = true
		case 3:
			a[i].White.Y = a[i].Black.Y
		case 4:
			a[i].Phase[0] = float32(math.NaN())
		}
	}
	a = append(a, AnchorObs{Anchor: frame.Anchor{X: 7, Y: 3}, Found: true}) // not on the lattice
	pf := AnchorPhase{Gamma: 2.4}.Estimate(a, l)
	for i, w := range pf.Weights {
		if p, ok := pf.Phase(i); !ok || p != 3 || math.Abs(float64(w[3]-1)) > 1e-3 {
			t.Fatalf("cell %d: weights %v", i, w)
		}
	}
	if !pf.Clean() {
		t.Fatal("not clean")
	}
	for i := range a {
		a[i].Found = false
	}
	pf = AnchorPhase{}.Estimate(a, l)
	if pf.Clean() || len(pf.Weights) != len(l.DataCells()) {
		t.Fatal("clean without anchors")
	}
	if _, ok := pf.Phase(0); ok {
		t.Fatal("phase without anchors")
	}
	if _, ok := pf.Phase(-1); ok {
		t.Fatal("phase of cell -1")
	}
	var empty PhaseField
	if empty.Clean() {
		t.Fatal("empty field clean")
	}
}

// TestPhaseThreshold: a cell counts at CleanWeight and not below.
func TestPhaseThreshold(t *testing.T) {
	pf := PhaseField{Weights: [][4]float32{{0.92, 0.08}, {0.08, 0.9199, 0.0001}, {0, 0, 0.5, 0.5}}}
	if p, ok := pf.Phase(0); !ok || p != 0 {
		t.Errorf("cell 0: %d %v", p, ok)
	}
	for i := 1; i < 3; i++ {
		if _, ok := pf.Phase(i); ok {
			t.Errorf("cell %d counts", i)
		}
	}
	if _, ok := pf.Phase(3); ok {
		t.Error("cell 3 counts")
	}
}

func TestPaletteGamma(t *testing.T) {
	for _, mod := range []uint8{2, 4} {
		c, _ := modem.ByID(mod)
		def := c.DefaultStats(1)
		for _, gamma := range []float64{1.8, 2.2, 2.4, 3.0} {
			var pal []modem.Obs
			for cp := range 4 {
				for k := range 8 {
					v := float64((def.Centre[k%c.Levels()][0] - 16) / 219)
					pal = append(pal, modem.Obs{Y: camera(0.01+0.99*math.Pow(v, 2.4), gamma, 190+float64(cp)), Cb: 128, Cr: 128})
				}
			}
			pal[5] = modem.Obs{Y: float32(math.NaN())}
			pal[6].Saturated = true
			g, ok := PaletteGamma(pal, c)
			if !ok || math.Abs(g-gamma) > 0.02*gamma {
				t.Errorf("%s, camera gamma %.1f: measured %.3f, %v", c.Name(), gamma, g, ok)
			}
		}
	}
	for _, mod := range []uint8{1, 3} {
		c, _ := modem.ByID(mod)
		if _, ok := PaletteGamma(make([]modem.Obs, 32), c); ok {
			t.Errorf("%s gave a gamma", c.Name())
		}
	}
	c, _ := modem.ByID(2)
	if _, ok := PaletteGamma(nil, c); ok {
		t.Error("no palette gave a gamma")
	}
	flat := make([]modem.Obs, 32)
	for i := range flat {
		flat[i].Y = 100
	}
	if _, ok := PaletteGamma(flat, c); ok {
		t.Error("a flat palette gave a gamma")
	}
}
