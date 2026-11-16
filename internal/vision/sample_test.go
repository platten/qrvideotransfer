package vision

import (
	"math"
	"testing"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
	"github.com/platten/qrvideotransfer/internal/video"
)

// geomObsTol is the tolerance of observation comparisons, in 8-bit code values: far below the
// smallest difference that matters (one 10-bit code is 0.25) and far above float32 rounding.
const geomObsTol = 0.01

// geomObsNear reports whether two observations agree within geomObsTol per plane and in saturation.
// Vision tests never assert bit-exact floating-point values.
func geomObsNear(a, b modem.Obs) bool {
	near := func(x, y float32) bool { return math.Abs(float64(x-y)) <= geomObsTol }
	return near(a.Y, b.Y) && near(a.Cb, b.Cb) && near(a.Cr, b.Cr) && a.Saturated == b.Saturated
}

// geomPlainMesh returns the exact mesh of a rendered frame of the code.
func geomPlainMesh(c *geomCode) *Mesh {
	p := float64(c.pitch)
	return geomAffineMesh(func(x, y float64) Point { return Point{x * p, y * p} })
}

// TestSamplePlain: on rendered frames every cell's values are its symbol's appearance, for
// brightness and colour modulations and at 10 bits. A rendered frame's white is exactly the
// limited-range white, where a clipped highlight lands, so a white cell inside a white
// neighbourhood is flagged as glare: at most 3.5% of the cells with black and white (designer
// ruling, 2026-11-15: 1.46-2.80% measured over 30 codes), 0.1% otherwise.
func TestSamplePlain(t *testing.T) {
	for _, mod := range []uint8{1, 2, 3, 4} {
		c := geomNewCode(t, 212, 120, 8, mod, 1, 30)
		cm, err := modem.ByID(mod)
		if err != nil {
			t.Fatal(err)
		}
		cells := c.layout.DataCells()
		obs := make([]modem.Obs, len(cells))
		FootprintSampler{}.Sample(obs, geomPlain(c, 0), geomPlainMesh(c), cells)
		flagged := 0
		for i, o := range obs {
			s := cm.Map(c.symbols[0][i])
			k := float32(int(1) << (s.Bits - 8))
			want := modem.Obs{Y: float32(s.Y) / k, Cb: float32(s.Cb) / k, Cr: float32(s.Cr) / k, Saturated: o.Saturated}
			if !geomObsNear(o, want) {
				t.Fatalf("modulation %d cell %d: got %+v, want %+v", mod, i, o, want)
			}
			if o.Saturated {
				flagged++
				if s.Y != uint16(235)<<(s.Bits-8) {
					t.Errorf("modulation %d cell %d: flagged at Y %d", mod, i, s.Y)
				}
			}
		}
		most := 0.001
		if mod == 1 {
			most = 0.035
		}
		if share := float64(flagged) / float64(len(cells)); share > most {
			t.Errorf("modulation %d: %.2f%% of cells flagged, want at most %.1f%%", mod, 100*share, 100*most)
		}
	}
}

// TestSamplePartialClip: display white at the clip over only part of the code (non-uniform screen,
// vignetting, an exposure gradient). A rendered 212 x 120 frame keeps its white at 235 over the left
// share of the width and has it lowered to 226 elsewhere, for shares of 5% to 95%: the glare flag
// stays within the same ceilings as for white at the clip everywhere, 3.5% of the cells with black
// and white and 0.1% otherwise, at every share.
func TestSamplePartialClip(t *testing.T) {
	for _, mod := range []uint8{1, 2, 3} {
		c := geomNewCode(t, 212, 120, 8, mod, 1, 30)
		cells := c.layout.DataCells()
		obs := make([]modem.Obs, len(cells))
		most := 0.001
		if mod == 1 {
			most = 0.035
		}
		worst, at := 0.0, 0
		for share := 5; share <= 95; share += raceSize(5, 30) {
			f := geomPlain(c, 0)
			edge := f.W * share / 100
			for y := range f.H {
				for x := edge; x < f.W; x++ {
					if f.Y[y*f.W+x] == 235 {
						f.Y[y*f.W+x] = 226
					}
				}
			}
			FootprintSampler{}.Sample(obs, f, geomPlainMesh(c), cells)
			flagged := 0
			for _, o := range obs {
				if o.Saturated {
					flagged++
				}
			}
			if s := float64(flagged) / float64(len(cells)); s > worst {
				worst, at = s, share
			}
		}
		t.Logf("modulation %d: at most %.2f%% of cells flagged (white clipped over %d%% of the width)", mod, 100*worst, at)
		if worst > most {
			t.Errorf("modulation %d: %.2f%% of cells flagged with white clipped over %d%% of the width, want at most %.1f%%", mod, 100*worst, at, 100*most)
		}
	}
}

// TestSampleCentral: only the central 60% of a cell counts; its outer fifth on each side does not.
func TestSampleCentral(t *testing.T) {
	c := geomNewCode(t, 212, 120, 16, 2, 1, 31)
	f := geomPlain(c, 0)
	cell := c.layout.DataCells()[500]
	x0, y0 := int(cell.X)*16, int(cell.Y)*16
	for y := range 16 {
		for x := range 16 {
			v := uint16(100)
			if x < 3 || x > 12 || y < 3 || y > 12 {
				v = 200
			}
			f.Y[(y0+y)*f.W+x0+x] = v
		}
	}
	var o [1]modem.Obs
	FootprintSampler{}.Sample(o[:], f, geomPlainMesh(c), []frame.Cell{cell})
	if !geomObsNear(o[0], modem.Obs{Y: 100, Cb: o[0].Cb, Cr: o[0].Cr}) {
		t.Errorf("got %v, want 100", o[0].Y)
	}
}

// TestSampleSaturated: a cell with a sample at the top or bottom of the code range is saturated,
// and a cell outside the capture is saturated with zero values.
func TestSampleSaturated(t *testing.T) {
	c := geomNewCode(t, 212, 120, 8, 2, 1, 32)
	f := geomPlain(c, 0)
	cells := []frame.Cell{c.layout.DataCells()[100], c.layout.DataCells()[200], c.layout.DataCells()[300], {X: 2000, Y: 5}}
	f.Y[(int(cells[0].Y)*8+4)*f.W+int(cells[0].X)*8+4] = 255
	f.Y[(int(cells[1].Y)*8+4)*f.W+int(cells[1].X)*8+3] = 0
	obs := make([]modem.Obs, len(cells))
	FootprintSampler{}.Sample(obs, f, geomPlainMesh(c), cells)
	want := []bool{true, true, false, true}
	for i, o := range obs {
		if o.Saturated != want[i] {
			t.Errorf("cell %d: saturated %v, want %v", i, o.Saturated, want[i])
		}
	}
	if !geomObsNear(obs[3], modem.Obs{Saturated: true}) {
		t.Errorf("outside: %+v", obs[3])
	}
}

// TestSampleTiny: when no pixel centre falls in a cell's central part (cells under two pixels), the
// cell takes the pixel under its centre.
func TestSampleTiny(t *testing.T) {
	f := &video.Frame{W: 4, H: 4, Bits: 8, Y: []uint16{10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 110, 120, 130, 140, 150, 160}}
	m := geomAffineMesh(func(x, y float64) Point { return Point{x * 0.7, y * 0.7} })
	var o [1]modem.Obs
	FootprintSampler{}.Sample(o[:], f, m, []frame.Cell{{X: 2, Y: 2}})
	// The central part [1.54, 1.96] holds no pixel centre; the centre (1.75, 1.75) is in pixel (1, 1).
	if !geomObsNear(o[0], modem.Obs{Y: 60}) {
		t.Errorf("got %+v", o[0])
	}
}

// TestSampleInvalid: a malformed frame or mesh gives saturated zero observations, and a short dst
// is filled as far as it goes.
func TestSampleInvalid(t *testing.T) {
	c := geomNewCode(t, 212, 120, 4, 2, 1, 33)
	cells := c.layout.DataCells()[:10]
	nanMesh := geomPlainMesh(c)
	nanMesh.Nodes[3].X = math.NaN()
	for name, tc := range map[string]struct {
		f *video.Frame
		m *Mesh
	}{
		"nil frame": {nil, geomPlainMesh(c)},
		"short":     {&video.Frame{W: 848, H: 480, Bits: 8, Y: make([]uint16, 5)}, geomPlainMesh(c)},
		"bad mesh":  {geomPlain(c, 0), &Mesh{}},
		"nan mesh":  {geomPlain(c, 0), nanMesh},
	} {
		obs := make([]modem.Obs, len(cells))
		FootprintSampler{}.Sample(obs, tc.f, tc.m, cells)
		for i, o := range obs {
			if !geomObsNear(o, modem.Obs{Saturated: true}) {
				t.Errorf("%s: cell %d: %+v", name, i, o)
				break
			}
		}
	}
	obs := make([]modem.Obs, 3)
	FootprintSampler{}.Sample(obs, geomPlain(c, 0), geomPlainMesh(c), cells)
	if obs[2].Saturated || obs[2].Y < 1 {
		t.Error("short dst not filled")
	}
}

// TestSampleSimulated: on the tripod preset at 4K, black-and-white cells sampled through the fitted
// mesh fall on the right side of the midpoint between the anchors' dark and light levels for at
// least 99.9% of cells, over 2 poses from seed 4000.
func TestSampleSimulated(t *testing.T) {
	raceSkipStatistics(t)
	c := geomNewCode(t, 480, 270, 4, 1, 1, 34)
	cfg := geomConfig(t, "tripod", 0, 0)
	cells := c.layout.DataCells()
	obs := make([]modem.Obs, len(cells))
	for _, p := range geomPoses(t, c, cfg, 2, 4000) {
		var d FinderDetector
		q, ok := d.Detect(p.f, nil)
		if !ok {
			t.Fatal("not found")
		}
		m, anchors, err := MeshFitter{}.Fit(p.f, q, c.layout)
		if err != nil {
			t.Fatal(err)
		}
		var lo, hi float64
		n := 0
		for _, a := range anchors {
			if a.Found {
				lo, hi, n = lo+float64(a.Black.Y), hi+float64(a.White.Y), n+1
			}
		}
		mid := float32((lo + hi) / 2 / float64(n))
		FootprintSampler{}.Sample(obs, p.f, m, cells)
		wrong := 0
		for i, o := range obs {
			if (o.Y > mid) != (c.symbols[0][i]&1 != 0) {
				wrong++
			}
		}
		if r := float64(wrong) / float64(len(cells)); r > 0.001 {
			t.Errorf("seed %d: %.4f%% of cells on the wrong side", p.tr.Capture, 100*r)
		}
	}
}

// TestSampleClipCostSpread: the glare rule's cost on perfect rendered frames, whose white is the
// clip, over ten codes (seeds 30 to 39) on each of three grids (212 x 120 and 240 x 135 at 8 pixels
// per cell, 480 x 270 at 4): every one of the 30 black-and-white codes loses at most 3.5% of its
// cells (designer ruling, 2026-11-15) and every four-grey code at most 0.1%. The largest is logged.
func TestSampleClipCostSpread(t *testing.T) {
	seeds := raceSize(10, 1)
	for _, mod := range []uint8{1, 2} {
		most := 0.001
		if mod == 1 {
			most = 0.035
		}
		worst, sum, n := 0.0, 0.0, 0
		for _, g := range [][3]int{{212, 120, 8}, {240, 135, 8}, {480, 270, 4}} {
			for s := range seeds {
				c := geomNewCode(t, g[0], g[1], g[2], mod, 1, uint64(30+s))
				cells := c.layout.DataCells()
				obs := make([]modem.Obs, len(cells))
				FootprintSampler{}.Sample(obs, geomPlain(c, 0), geomPlainMesh(c), cells)
				flagged := 0
				for _, o := range obs {
					if o.Saturated {
						flagged++
					}
				}
				share := float64(flagged) / float64(len(cells))
				worst, sum, n = max(worst, share), sum+share, n+1
				if share > most {
					t.Errorf("modulation %d, %dx%d seed %d: %.2f%% of cells flagged, want at most %.1f%%", mod, g[0], g[1], 30+s, 100*share, 100*most)
				}
			}
		}
		t.Logf("modulation %d: %d codes, mean %.2f%%, largest %.2f%% of cells flagged", mod, n, 100*sum/float64(n), 100*worst)
	}
}
