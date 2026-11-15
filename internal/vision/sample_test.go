package vision

import (
	"math"
	"testing"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
	"github.com/platten/qrvideotransfer/internal/video"
)

// geomPlainMesh returns the exact mesh of a rendered frame of the code.
func geomPlainMesh(c *geomCode) *Mesh {
	p := float64(c.pitch)
	return geomAffineMesh(func(x, y float64) Point { return Point{x * p, y * p} })
}

// TestSamplePlain: on rendered frames every cell's observation is its symbol's appearance, for
// brightness and colour modulations and at 10 bits.
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
		for i, o := range obs {
			s := cm.Map(c.symbols[0][i])
			k := float32(int(1) << (s.Bits - 8))
			want := modem.Obs{Y: float32(s.Y) / k, Cb: float32(s.Cb) / k, Cr: float32(s.Cr) / k}
			if o != want {
				t.Fatalf("modulation %d cell %d: got %+v, want %+v", mod, i, o, want)
			}
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
	if o[0].Y != 100 {
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
	if obs[3] != (modem.Obs{Saturated: true}) {
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
	if o[0].Y != 60 || o[0].Cb != 0 {
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
			if o != (modem.Obs{Saturated: true}) {
				t.Errorf("%s: cell %d: %+v", name, i, o)
				break
			}
		}
	}
	obs := make([]modem.Obs, 3)
	FootprintSampler{}.Sample(obs, geomPlain(c, 0), geomPlainMesh(c), cells)
	if obs[2].Y == 0 {
		t.Error("short dst not filled")
	}
}

// TestSampleSimulated: on the tripod preset at 4K, black-and-white cells sampled through the fitted
// mesh fall on the right side of the midpoint between the anchors' dark and light levels for at
// least 99.9% of cells, over 2 poses from seed 4000.
func TestSampleSimulated(t *testing.T) {
	c := geomNewCode(t, 480, 270, 4, 1, 1, 34)
	cfg := geomConfig(t, "tripod", 0, 0)
	cells := c.layout.DataCells()
	obs := make([]modem.Obs, len(cells))
	n := 2
	if geomRace {
		n = 1
	}
	for _, p := range geomPoses(t, c, cfg, n, 4000) {
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
