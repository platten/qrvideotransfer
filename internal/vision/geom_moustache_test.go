package vision

import (
	"math"
	"testing"

	"github.com/platten/qrvideotransfer/internal/channel"
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
// columns, outside the anchors, and elsewhere.
func TestGeometryMoustache(t *testing.T) {
	n := raceSize(40, 1)
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
// read, and the cell-centre error at most 0.15 cell RMS.
func TestGeometryMoustacheWindow(t *testing.T) {
	n := raceSize(24, 1)
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

// TestGeometryMoustacheCorner is the open issue of docs/DESIGN.md section 19 on the S2 laptop
// recordings: with the code filling the frame to its corners, the corner finders lie beyond every
// anchor, where the phones' distortion departs from the k1 + k2 model by up to a finder module,
// and the leave-one-out check rejects them. The simulator cannot show it: its lens is the
// detector's own k1 + k2 model, so the model extrapolates correctly to the corners (the case
// below is located). It stays skipped until the candidate fix of the open issue is built, with a
// simulator lens whose corners leave the model.
func TestGeometryMoustacheCorner(t *testing.T) {
	t.Skip("open issue (DESIGN section 19): corner finders beyond the anchors on real lenses; the simulator's lens is within the detector's model")
}
