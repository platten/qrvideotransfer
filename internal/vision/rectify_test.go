package vision

import (
	"math"
	"testing"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/video"
)

// geomMeshError returns the largest distance, in cells, between the mesh's cell centres and the
// rendered ones of a plain frame.
func geomMeshError(c *geomCode, m *Mesh) float64 {
	worst := 0.0
	p := float64(c.pitch)
	for gy := 0; gy < c.layout.GridY(); gy += 3 {
		for gx := 0; gx < c.layout.GridX(); gx += 3 {
			got := m.At(float64(gx)+0.5, float64(gy)+0.5)
			e := geomDist(got, Point{(float64(gx) + 0.5) * p, (float64(gy) + 0.5) * p}) / p
			if !(e <= worst) {
				worst = e
			}
		}
	}
	return worst
}

// TestFitPlain: on rendered frames the mesh is exact to a twentieth of a cell, every anchor is found
// at its saddle with the anchor's dark and light levels, and the lit phase quadrant is frame mod 4.
func TestFitPlain(t *testing.T) {
	for _, g := range []struct{ gx, gy, pitch int }{{212, 120, 4}, {432, 270, 4}, {320, 180, 8}} {
		c := geomNewCode(t, g.gx, g.gy, g.pitch, 2, 4, 20)
		for k := range 4 {
			f := geomPlain(c, k)
			m, obs, err := MeshFitter{}.Fit(f, geomPlainQuad(c), c.layout)
			if err != nil {
				t.Fatalf("%dx%d: %v", g.gx, g.gy, err)
			}
			if e := geomMeshError(c, m); e > 0.05 {
				t.Errorf("%dx%d frame %d: mesh error %.3f cells", g.gx, g.gy, k, e)
			}
			anchors := c.layout.Anchors()
			if len(obs) != len(anchors) {
				t.Fatalf("%dx%d: %d observations for %d anchors", g.gx, g.gy, len(obs), len(anchors))
			}
			for i, a := range obs {
				want := Point{float64(anchors[i].X+2) * float64(g.pitch), float64(anchors[i].Y+2) * float64(g.pitch)}
				switch {
				case a.Anchor != anchors[i] || !a.Found:
					t.Errorf("%dx%d anchor %d: %+v", g.gx, g.gy, i, a)
				case geomDist(a.Corner, want) > 0.05*float64(g.pitch):
					t.Errorf("%dx%d anchor %d: corner %v, want %v", g.gx, g.gy, i, a.Corner, want)
				case math.Abs(float64(a.Black.Y)-16) > 1 || math.Abs(float64(a.White.Y)-235) > 1:
					t.Errorf("%dx%d anchor %d: black %v white %v", g.gx, g.gy, i, a.Black, a.White)
				}
				for p := range 4 {
					lit := a.Phase[p] > 125
					if lit != (p == k%4) {
						t.Errorf("%dx%d frame %d anchor %d: phase %v", g.gx, g.gy, k, i, a.Phase)
						break
					}
				}
			}
		}
	}
}

// geomPaintCells paints cells [x0, x1) x [y0, y1) of a rendered frame with luma v.
func geomPaintCells(c *geomCode, f *video.Frame, x0, y0, x1, y1 int, v func(cx, cy int) uint16) {
	p := c.pitch
	for y := y0 * p; y < y1*p; y++ {
		for x := x0 * p; x < x1*p; x++ {
			f.Y[y*f.W+x] = v(x/p, y/p)
		}
	}
}

// TestFitDamaged: anchors covered by grey are not found, an anchor whose saddle is moved by a cell
// is rejected as an outlier, and the mesh stays exact.
func TestFitDamaged(t *testing.T) {
	c := geomNewCode(t, 320, 180, 8, 2, 1, 21)
	f := geomPlain(c, 0)
	anchors := c.layout.Anchors()
	grey := map[int]bool{3: true, 10: true, 11: true, len(anchors) / 2: true}
	moved := len(anchors)/2 + 3
	for i := range grey {
		a := anchors[i]
		geomPaintCells(c, f, a.X, a.Y, a.X+8, a.Y+4, func(int, int) uint16 { return 126 })
	}
	a := anchors[moved]
	// Saddle quadrant boundary moved one cell right: the corner is at x+3.
	geomPaintCells(c, f, a.X, a.Y, a.X+4, a.Y+4, func(cx, cy int) uint16 {
		if (cx-a.X >= 3) != (cy-a.Y >= 2) {
			return 235
		}
		return 16
	})
	m, obs, err := MeshFitter{}.Fit(f, geomPlainQuad(c), c.layout)
	if err != nil {
		t.Fatal(err)
	}
	for i, o := range obs {
		if want := !grey[i] && i != moved; o.Found != want {
			t.Errorf("anchor %d: found %v, want %v", i, o.Found, want)
		}
	}
	if e := geomMeshError(c, m); e > 0.05 {
		t.Errorf("mesh error %.3f cells", e)
	}
}

// TestFitCropped: when part of the code is outside the capture, the anchors inside are found and the
// fit succeeds.
func TestFitCropped(t *testing.T) {
	c := geomNewCode(t, 212, 120, 4, 2, 1, 22)
	src := geomPlain(c, 0)
	w := src.W * 3 / 4
	f := &video.Frame{W: w, H: src.H, Bits: 8, Y: make([]uint16, w*src.H)}
	for y := range src.H {
		copy(f.Y[y*w:(y+1)*w], src.Y[y*src.W:])
	}
	_, obs, err := MeshFitter{}.Fit(f, geomPlainQuad(c), c.layout)
	if err != nil {
		t.Fatal(err)
	}
	for i, o := range obs {
		inside := float64(o.Anchor.X+6)*float64(c.pitch) < float64(w)
		if inside && !o.Found {
			t.Errorf("anchor %d inside the capture not found", i)
		}
		if float64(o.Anchor.X)*float64(c.pitch) > float64(w) && o.Found {
			t.Errorf("anchor %d outside the capture found", i)
		}
	}
}

// TestFitErrors: invalid inputs give an error, not a mesh.
func TestFitErrors(t *testing.T) {
	c := geomNewCode(t, 212, 120, 4, 2, 1, 23)
	q := geomPlainQuad(c)
	nan := q
	nan.Centres[1].Y = math.Inf(1)
	for name, tc := range map[string]struct {
		f *video.Frame
		q Quad
		l *frame.Layout
	}{
		"nil layout": {geomPlain(c, 0), q, nil},
		"nil frame":  {nil, q, c.layout},
		"short":      {&video.Frame{W: 848, H: 480, Bits: 8, Y: make([]uint16, 99)}, q, c.layout},
		"inf":        {geomPlain(c, 0), nan, c.layout},
		"degenerate": {geomPlain(c, 0), Quad{Module: 2}, c.layout},
	} {
		if m, _, err := (MeshFitter{}).Fit(tc.f, tc.q, tc.l); err == nil || m != nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// TestFitTooFewAnchors: when fewer than a quarter of the anchors inside the capture are located
// (here none, then a fifth), Fit reports an error and no mesh; with a third located it fits.
func TestFitTooFewAnchors(t *testing.T) {
	c := geomNewCode(t, 320, 180, 8, 2, 1, 24)
	anchors := c.layout.Anchors()
	for _, tc := range []struct {
		keep  int // keep every keep-th anchor, grey out the rest; 0 keeps none
		fails bool
	}{{0, true}, {5, true}, {3, false}} {
		f := geomPlain(c, 0)
		for i, a := range anchors {
			if tc.keep == 0 || i%tc.keep != 0 {
				geomPaintCells(c, f, a.X, a.Y, a.X+8, a.Y+4, func(int, int) uint16 { return 126 })
			}
		}
		m, _, err := MeshFitter{}.Fit(f, geomPlainQuad(c), c.layout)
		if fails := err != nil; fails != tc.fails || (m == nil) != tc.fails {
			t.Errorf("keeping every %d: error %v, mesh %v", tc.keep, err, m != nil)
		}
	}
}
