package vision

import (
	"math"
	"testing"

	"github.com/platten/qrvideotransfer/internal/channel"
	"github.com/platten/qrvideotransfer/internal/frame"
)

// The moustache lens of the simulator's variant: k1 = -0.05 with k2 = +0.05 (geomLens units, the
// radius in half capture widths). It is a plausible lens, not a fit to a recording: a phone's
// main camera corrects its distortion in the camera and leaves a residual of the order of 1-2% at
// the frame's edge whose sign changes along the radius ("moustache"); here the displacement is
// -1.2% at r = 0.7 and +2.1% in a 16:9 frame's corner. The S2 recordings showed such a residual
// (k1 near -0.07 and k2 near +0.06 on both phones) and motivated the k2 term; the simulator stays
// uncalibrated.
const (
	geomMoustacheK1 = -0.05
	geomMoustacheK2 = 0.05
)

// geomMoustache returns preset p at w x h (0 for the preset's size) with the moustache lens and the
// code filling fill of the frame width.
func geomMoustache(t testing.TB, p string, w, h int, fill float64) channel.Config {
	t.Helper()
	cfg := geomConfig(t, p, w, h)
	cfg.Name += ", moustache lens"
	cfg.Geometry.K1, cfg.Geometry.K2, cfg.Geometry.Fill = geomMoustacheK1, geomMoustacheK2, fill
	return cfg
}

// geomEdgeStats are the outcomes of a set of poses, with the cell-centre error measured apart in
// the edge columns (outside the first and the last anchor column) and in the others.
type geomEdgeStats struct {
	poses, found, wrong, header int
	edgeSq, innerSq             float64
	edgeCells, innerCells       int
}

// geomEdgeSweep locates, reads and fits n poses of code c filmed with cfg, judging each quad.
func geomEdgeSweep(t *testing.T, c *geomCode, cfg channel.Config, n int, seed uint64) geomEdgeStats {
	t.Helper()
	gx := c.layout.GridX()
	var s geomEdgeStats
	for _, p := range geomPoses(t, c, cfg, n, seed) {
		s.poses++
		var d FinderDetector
		q, ok := d.Detect(p.f, nil)
		if !ok {
			continue
		}
		hs, hok := SlotReader{}.Read(p.f, q)
		if hok && hs[0] == c.headers[hs[0].FrameIndex%uint32(len(c.headers))] {
			s.header++
		}
		mesh, _, err := MeshFitter{}.Fit(p.f, q, c.layout)
		rms := math.Inf(1)
		if err == nil {
			for _, edge := range []bool{true, false} {
				r, m := channel.CentreError(p.tr, func(x, y int) (float64, float64, bool) {
					if (x < 14 || x >= gx-14) != edge || !geomMeasured(x, y) {
						return 0, 0, false
					}
					pt := mesh.At(float64(x)+0.5, float64(y)+0.5)
					return pt.X, pt.Y, true
				})
				if edge {
					s.edgeSq, s.edgeCells = s.edgeSq+r*r*float64(m), s.edgeCells+m
				} else {
					s.innerSq, s.innerCells = s.innerSq+r*r*float64(m), s.innerCells+m
					rms = r
				}
			}
		}
		if geomJudge(c, p.tr, q, d.hidden, hok, err == nil, rms) == geomWrong {
			s.wrong++
			continue
		}
		s.found++
	}
	t.Logf("%s, %d poses: located %d, wrong %d, header %d; centre error %.4f cell RMS in the edge columns over %d cells, %.4f elsewhere over %d",
		cfg.Name, s.poses, s.found, s.wrong, s.header, math.Sqrt(s.edgeSq/float64(max(1, s.edgeCells))), s.edgeCells,
		math.Sqrt(s.innerSq/float64(max(1, s.innerCells))), s.innerCells)
	return s
}

// TestGeometryMoustache: handheld 4K poses of a 480 x 270 grid filling 85% of the frame width,
// through the moustache lens (a phone filming a laptop's screen). At least 90% are located (all 40
// when written; at 90% of the width the lens pushes some corner finders out of the frame), none
// wrongly, every header read, and the cell-centre error is at most 0.15 cell RMS both in the edge
// columns, outside the anchors, and elsewhere. 12 poses always (seeds 50000 to 50011, so at least
// 11 located); the 40 with QRVT_LONG_TESTS=1.
func TestGeometryMoustache(t *testing.T) {
	n := raceSize(geomLongSize(40, 12), 1)
	c := geomNewCode(t, 480, 270, 4, 2, 4, 18)
	s := geomEdgeSweep(t, c, geomMoustache(t, "handheld", 0, 0, 0.85), n, 50000)
	if s.wrong != 0 || s.header != s.found+s.wrong || 10*s.found < 9*n {
		t.Errorf("located %d of %d, wrong %d, header %d", s.found, n, s.wrong, s.header)
	}
	edge, inner := math.Sqrt(s.edgeSq/float64(max(1, s.edgeCells))), math.Sqrt(s.innerSq/float64(max(1, s.innerCells)))
	if !(edge <= 0.15) || !(inner <= 0.15) {
		t.Errorf("centre error %.4f cell RMS in the edge columns, %.4f elsewhere, want at most 0.15", edge, inner)
	}
}

// TestGeometryMoustacheWindow: the pattern played rescaled in a window, as in the S2 recording of
// rescaled playback: a 1920 x 1080 video on a 2560 x 1600 panel (scaled to fit, with bars), filmed
// by a 1080p camera through the moustache lens. No wrong quad, at least 90% located, every header
// read, and the cell-centre error at most 0.15 cell RMS. 12 poses always, 24 with
// QRVT_LONG_TESTS=1.
func TestGeometryMoustacheWindow(t *testing.T) {
	n := raceSize(geomLongSize(24, 12), 1)
	c := geomNewCode(t, 240, 135, 8, 2, 4, 19)
	cfg := geomMoustache(t, "tripod", 1920, 1080, 0.85)
	cfg.Display.PanelW, cfg.Display.PanelH = 2560, 1600
	s := geomEdgeSweep(t, c, cfg, n, 51000)
	if s.wrong != 0 || s.header != s.found+s.wrong || 10*s.found < 9*n {
		t.Errorf("located %d of %d, wrong %d, header %d", s.found, n, s.wrong, s.header)
	}
	edge, inner := math.Sqrt(s.edgeSq/float64(max(1, s.edgeCells))), math.Sqrt(s.innerSq/float64(max(1, s.innerCells)))
	if !(edge <= 0.15) || !(inner <= 0.15) {
		t.Errorf("centre error %.4f cell RMS in the edge columns, %.4f elsewhere, want at most 0.15", edge, inner)
	}
}

// TestGeometryMoustacheCorner characterises the open issue of docs/DESIGN.md section 19 on the S2
// laptop recordings: there a corner finder lies beyond every anchor, where the phones' distortion
// departs from the k1 + k2 model by up to a finder module, so the anchors put it 0.6 to 1 module
// from where it is. The simulator's lens is the detector's own model, so the case is built
// directly: on a rendered 480 x 270 code at 4 pixels per cell, the top-left or the bottom-left
// finder (the left ones, as on the recordings), with its separator, is moved outwards by 0.6 or 1
// module, the place it leaves painted in the separator's light. Today the moved finder is never
// confirmed and the capture is never a wrong quad: the quad is located from the other three
// finders (the three-finder path), with that corner predicted within a module of the code's own
// finder. On the recordings the three-finder path did not rescue such frames, whose prediction
// rests on the same lens model; the open issue's candidate fix must locate them, and must keep
// this case's outcome.
func TestGeometryMoustacheCorner(t *testing.T) {
	c := geomNewCode(t, 480, 270, 4, 2, 1, 46)
	want := geomPlainQuad(c)
	plain := geomPlain(c, 0)
	u := 2 * c.pitch // one finder module, in pixels: Uy is two cells on a 270-row grid
	for _, corner := range []frame.Corner{frame.TopLeft, frame.BottomLeft} {
		k := int(corner)
		sep := c.layout.Separator(corner).Pixels(c.pitch)
		for _, mod := range []float64{0.6, 1} {
			d := int(math.Round(mod * float64(u)))
			dx, dy := -d, -d // outwards: up and to the left
			if corner == frame.BottomLeft {
				dy = d
			}
			f := geomPlain(c, 0)
			for y := sep.Min.Y; y < sep.Max.Y; y++ {
				for x := sep.Min.X; x < sep.Max.X; x++ {
					f.Y[y*f.W+x] = 235
				}
			}
			for y := sep.Min.Y; y < sep.Max.Y; y++ {
				for x := sep.Min.X; x < sep.Max.X; x++ {
					if tx, ty := x+dx, y+dy; tx >= 0 && ty >= 0 && tx < f.W && ty < f.H {
						f.Y[ty*f.W+tx] = plain.Y[y*plain.W+x]
					}
				}
			}
			moved := Point{want.Centres[k].X + float64(dx), want.Centres[k].Y + float64(dy)}
			var det FinderDetector
			q, ok := det.Detect(f, nil)
			t.Logf("finder %d moved %.1f module: located %v %v", k, mod, ok, q.Centres)
			if !ok {
				t.Errorf("finder %d moved %.1f module: not located", k, mod)
				continue
			}
			if geomDist(q.Centres[k], moved) <= want.Module/2 {
				t.Errorf("finder %d moved %.1f module: the moved finder was confirmed", k, mod)
			}
			for j := range 4 {
				tol := want.Module / 2
				if j == k {
					tol = want.Module
				}
				if e := geomDist(q.Centres[j], want.Centres[j]); e > tol {
					t.Errorf("finder %d moved %.1f module: wrong quad, centre %d at %v, want %v", k, mod, j, q.Centres[j], want.Centres[j])
				}
			}
		}
	}
}
