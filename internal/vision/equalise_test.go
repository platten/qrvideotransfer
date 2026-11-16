package vision

import (
	"math"
	"testing"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
	"github.com/platten/qrvideotransfer/internal/prng"
)

// synth is a synthetic capture of a grid without a simulator: every data cell shows a random level
// whose appearance varies smoothly over the grid, plus Gaussian noise.
type synth struct {
	l       *frame.Layout
	c       *modem.Constellation
	levels  []int
	obs     []modem.Obs
	palette []modem.Obs
	anchors []AnchorObs
	centre  func(l, x, y int) [3]float32 // true centre of level l at cell (x, y)
}

// gauss returns a standard normal deviate (Box-Muller).
func gauss(g *prng.SplitMix64) float64 {
	u := (float64(g.Next()>>11) + 0.5) / (1 << 53)
	v := float64(g.Next()>>11) / (1 << 53)
	return math.Sqrt(-2*math.Log(u)) * math.Cos(2*math.Pi*v)
}

// newSynth builds a synthetic capture: black and white drift across the grid (vignetting and a
// tilt in exposure), the middle levels sit where a camera gamma puts them, chroma shifts by the
// given white-balance offset, and the noise has standard deviation sigma.
func newSynth(t testing.TB, gridX, gridY int, mod uint8, sigma float64, wb float32, seed uint64) *synth {
	t.Helper()
	l, err := frame.NewLayout(gridX, gridY)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := modem.ByID(mod)
	def := c.DefaultStats(1)
	s := &synth{l: l, c: c}
	s.centre = func(lv, x, y int) [3]float32 {
		b := 40 + 0.04*float32(x)
		w := 225 - 0.15*float32(y) - 0.03*float32(x)
		r := (def.Centre[lv][0] - 16) / 219
		r = float32(math.Pow(float64(r), 0.8))
		return [3]float32{b + (w-b)*r, def.Centre[lv][1] + wb, def.Centre[lv][2] - wb}
	}
	g := prng.SplitMix64{State: seed}
	noisy := func(ce [3]float32) modem.Obs {
		return modem.Obs{Y: ce[0] + float32(sigma*gauss(&g)), Cb: ce[1] + float32(sigma*gauss(&g)), Cr: ce[2] + float32(sigma*gauss(&g))}
	}
	for _, cell := range l.DataCells() {
		lv := int(g.Next() % uint64(c.Levels()))
		s.levels = append(s.levels, lv)
		s.obs = append(s.obs, noisy(s.centre(lv, int(cell.X), int(cell.Y))))
	}
	for cp := range 4 {
		for k := range 8 {
			x, y := 30+(gridX-60)*(cp&1), 4+(gridY-8)*(cp>>1)
			s.palette = append(s.palette, noisy(s.centre(k%c.Levels(), x, y)))
		}
	}
	for _, a := range l.Anchors() {
		o := AnchorObs{Anchor: a, Found: true}
		o.Black = noisy(s.centre(0, a.X+4, a.Y+2))
		o.White = noisy(s.centre(c.Levels()-1, a.X+4, a.Y+2))
		s.anchors = append(s.anchors, o)
	}
	return s
}

// checkField compares the field with the true centres at every tile centre inside the grid.
func checkField(t *testing.T, s *synth, f *Field, tolC, sigma float64) {
	t.Helper()
	if f == nil {
		t.Fatal("no field")
	}
	worst, worstV := 0.0, 0.0
	for j := range f.TilesY {
		for i := range f.TilesX {
			x, y := i*TileW+TileW/2, j*TileH+TileH/2
			if x >= s.l.GridX() || y >= s.l.GridY() {
				continue
			}
			st := f.At(x, y)
			if st.Levels != s.c.Levels() {
				t.Fatalf("tile %d,%d: %d levels", i, j, st.Levels)
			}
			for lv := range st.Levels {
				want := s.centre(lv, x, y)
				for p := range 3 {
					if p > 0 && s.c.Planes() == modem.PlaneY {
						continue
					}
					worst = math.Max(worst, math.Abs(float64(st.Centre[lv][p]-want[p])))
				}
				if sigma > 0 {
					worstV = math.Max(worstV, math.Abs(math.Sqrt(float64(st.Variance[lv]))/sigma-1))
				}
			}
		}
	}
	t.Logf("largest centre error %.3f, largest relative deviation error %.3f", worst, worstV)
	if worst > tolC {
		t.Errorf("centre error %.3f above %.3f", worst, tolC)
	}
	if worstV > 0.35 {
		t.Errorf("relative deviation error %.3f above 0.35", worstV)
	}
}

func TestFitRecoversLevels(t *testing.T) {
	t.Parallel()
	for _, mod := range []uint8{1, 2, 3} {
		s := newSynth(t, 480, 270, mod, 4, 6, uint64(mod))
		f := (&TileEqualiser{Mod: s.c}).Fit(s.obs, s.palette, s.l, s.anchors)
		if f.TilesX != 15 || f.TilesY != 9 || f.TileW != TileW || f.TileH != TileH || len(f.Tiles) != 135 {
			t.Fatalf("field shape %d x %d of %d x %d, %d tiles", f.TilesX, f.TilesY, f.TileW, f.TileH, len(f.Tiles))
		}
		checkField(t, s, f, 2.5, 4)
	}
}

// TestFitWithoutReferences: with no palette and no anchors the fit starts from the default levels
// and still finds levels that are offset from them.
func TestFitWithoutReferences(t *testing.T) {
	s := newSynth(t, 212, 120, 2, 3, 0, 9)
	f := (&TileEqualiser{Mod: s.c, Rounds: 6}).Fit(s.obs, nil, s.l, nil)
	checkField(t, s, f, 3, 3)
}

// TestFitIgnoresBadObservations: saturated, non-finite and missing observations, and anchors that
// were not found or are unusable, do not disturb the fit.
func TestFitIgnoresBadObservations(t *testing.T) {
	s := newSynth(t, 212, 120, 2, 3, 0, 10)
	nan := float32(math.NaN())
	for i := range s.obs {
		switch i % 17 {
		case 3:
			s.obs[i] = modem.Obs{Y: 255, Saturated: true}
		case 5:
			s.obs[i].Y = nan
		case 7:
			s.obs[i].Y = float32(math.Inf(1))
		}
	}
	for i := range s.anchors {
		switch i % 4 {
		case 1:
			s.anchors[i] = AnchorObs{Anchor: s.anchors[i].Anchor}
		case 2:
			s.anchors[i].White.Saturated = true
		case 3:
			s.anchors[i].White = s.anchors[i].Black // no contrast
		}
	}
	s.palette[3] = modem.Obs{Y: nan}
	s.palette[9].Saturated = true
	obs := s.obs[:len(s.obs)-100] // the last cells are missing
	f := (&TileEqualiser{Mod: s.c}).Fit(obs, s.palette[:20], s.l, append(s.anchors, AnchorObs{Anchor: frame.Anchor{X: 5000, Y: 5000}, Found: true}))
	checkField(t, s, f, 2, 3)
}

// TestFitNoData: with no usable observation at all, the field holds the starting levels. Where a
// tile has its own anchor they are within the anchor's noise (sigma 3) of the truth; the tiles
// without one (the column beside the right border and the corners) take their neighbours' mean and
// so lag the synthetic gradient by up to one tile, about 7 code values. The variances are not
// checked: with no cell they stay the starting sixth of a level spacing, which says nothing about
// the noise.
func TestFitNoData(t *testing.T) {
	s := newSynth(t, 212, 120, 2, 3, 0, 11)
	f := (&TileEqualiser{Mod: s.c}).Fit(nil, s.palette, s.l, s.anchors)
	checkField(t, s, f, 8, 0)
	for k := range f.Tiles {
		st := &f.Tiles[k]
		sp := (st.Centre[3][0] - st.Centre[0][0]) / 3 / 6
		if math.Abs(float64(st.Variance[0]-sp*sp)) > 1e-3*float64(sp*sp) {
			t.Fatalf("tile %d: variance %v, want the starting %v", k, st.Variance[0], sp*sp)
		}
	}
	if f := (&TileEqualiser{Mod: s.c}).Fit(nil, nil, s.l, nil); f == nil || f.At(0, 0).Levels != 4 {
		t.Fatal("no default field")
	}
}

// TestFieldAt: interpolation is exact at tile centres and linear between them, constant outside.
func TestFieldAt(t *testing.T) {
	f := &Field{TilesX: 2, TilesY: 2, TileW: TileW, TileH: TileH, Tiles: make([]modem.LocalStats, 4)}
	for i := range f.Tiles {
		f.Tiles[i] = modem.LocalStats{Levels: 2}
		f.Tiles[i].Centre[1] = [3]float32{float32(10 * i), 128, 128}
		f.Tiles[i].Variance[1] = float32(i + 1)
	}
	// Tile centres lie between cells: (15.5, 14.5) and so on, in cell indices.
	at := func(x, y int) float32 { return f.At(x, y).Centre[1][0] }
	for _, k := range []struct {
		x, y int
		want float32
	}{{0, 0, 0}, {15, 14, 0}, {48, 14, 10}, {15, 45, 20}, {63, 59, 30}, {500, 14, 10}} {
		st := f.At(k.x, k.y)
		if got := st.Centre[1][0]; got != k.want || st.Levels != 2 {
			t.Errorf("At(%d, %d) = %v, want %v", k.x, k.y, got, k.want)
		}
		if v := st.Variance[1]; math.Abs(float64(v-(k.want/10+1))) > 1e-4 {
			t.Errorf("At(%d, %d) variance %v", k.x, k.y, v)
		}
	}
	if m := (at(31, 14) + at(32, 14)) / 2; math.Abs(float64(m-5)) > 1e-4 {
		t.Errorf("midway between two tiles: %v, want 5", m)
	}
	if m := (at(31, 29) + at(32, 29) + at(31, 30) + at(32, 30)) / 4; math.Abs(float64(m-15)) > 1e-4 {
		t.Errorf("midway between four tiles: %v, want 15", m)
	}
	one := &Field{TilesX: 1, TilesY: 1, TileW: TileW, TileH: TileH, Tiles: f.Tiles[3:]}
	if one.At(100, 100).Centre[1][0] != 30 {
		t.Error("single tile")
	}
}

// TestSoftBits: cells count only when clean for the phase; others and missing observations give
// zeros.
func TestSoftBits(t *testing.T) {
	s := newSynth(t, 212, 120, 2, 3, 0, 12)
	f := (&TileEqualiser{Mod: s.c}).Fit(s.obs, s.palette, s.l, s.anchors)
	n := len(s.obs)
	dst := make([]int8, 2*n)
	if got := SoftBits(dst, s.obs, s.l, f, s.c, nil, 0); got != n {
		t.Fatalf("nil phase field: %d cells, want %d", got, n)
	}
	errs := 0
	for i, lv := range s.levels {
		sym := s.c.SymbolOf(lv)
		for k := range 2 {
			if (dst[2*i+k] > 0) != (symbolBit(sym, 2, k) == 0) {
				errs++
			}
		}
	}
	if errs > n/1000 {
		t.Errorf("%d bit errors in %d bits", errs, 2*n)
	}
	pf := PhaseField{Weights: make([][4]float32, n)}
	for i := range pf.Weights {
		pf.Weights[i][i%4] = 0.95
		pf.Weights[i][(i+1)%4] = 0.05
	}
	s.obs[0].Y = float32(math.NaN())
	got := SoftBits(dst, s.obs, s.l, f, s.c, &pf, 1)
	if want := (n + 2) / 4; got != want {
		t.Fatalf("phase 1: %d cells, want %d", got, want)
	}
	for i := range n {
		if zero := dst[2*i] == 0 && dst[2*i+1] == 0; zero != (i%4 != 1) {
			t.Fatalf("cell %d: soft bits %v", i, dst[2*i:2*i+2])
		}
	}
	if SoftBits(dst, s.obs[:10], s.l, f, s.c, &pf, 0) != 2 {
		t.Fatal("short observations")
	}
}
