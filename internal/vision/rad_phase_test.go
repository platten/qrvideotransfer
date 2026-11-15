package vision

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/channel"
)

// phaseStats summarises a phase field against the truth. Cells are split by whether all four
// phase markers their weights are interpolated from were observed (inner) or some were predicted
// from neighbours (pocket): the format places no anchor beside the finders, so the cells in the
// four corners depend on predicted markers.
type phaseStats struct {
	cells, mixed, pocket int     // cells inside the capture; those whose largest true weight is below 1; pocket cells
	maxErr, maxPocket    float64 // largest weight error over inner and over pocket cells
	sumSq                float64 // summed squared weight error over all cells and phases
	credited, wrong      int     // cells credited to some phase; those whose true frame weight is below 0.5
	minTrue              float64 // smallest true weight among credited cells
}

// pocketCells reports, per data cell, whether its weights depend on a phase marker that was not
// observed (or not usable) in the capture.
func pocketCells(s *radSession, rc *radCapture) []bool {
	lat := newLattice(s.layout, TileW, TileH)
	for i := range rc.anchors {
		if k, in := lat.index(rc.anchors[i].Anchor); in {
			_, lat.ok[k] = anchorWeights(&rc.anchors[i], 2.4)
		}
	}
	out := make([]bool, len(s.layout.DataCells()))
	for i, c := range s.layout.DataCells() {
		k, fx, fy := lat.locate(float64(c.X)+0.5-18, float64(c.Y)+0.5-15, false)
		out[i] = fx < 0 || fx > 1 || fy < 0 || fy > 1
		for _, n := range lat.corners(k, fx, fy) {
			out[i] = out[i] || n.w != 0 && !lat.ok[n.n]
		}
	}
	return out
}

// measure compares the estimated field of a capture with the truth at every data cell.
func (st *phaseStats) measure(s *radSession, rc *radCapture, pf *PhaseField) {
	mix := rc.trueMix(s.layout)
	pocket := pocketCells(s, rc)
	for i := range s.layout.DataCells() {
		if math.IsNaN(float64(rc.obs[i].Y)) {
			continue
		}
		var w [4]float64
		top := 0.0
		for _, m := range mix[i] {
			w[m.Frame%4] += m.W
			top = math.Max(top, m.W)
		}
		st.cells++
		if top < 1 {
			st.mixed++
		}
		if pocket[i] {
			st.pocket++
		}
		for p := range 4 {
			d := math.Abs(float64(pf.Weights[i][p]) - w[p])
			if pocket[i] {
				st.maxPocket = math.Max(st.maxPocket, d)
			} else {
				st.maxErr = math.Max(st.maxErr, d)
			}
			st.sumSq += d * d
		}
		if p, ok := pf.Phase(i); ok {
			_, tw := credited(mix[i], p)
			st.credited++
			if tw < 0.5 {
				st.wrong++
			}
			if st.credited == 1 || tw < st.minTrue {
				st.minTrue = tw
			}
		}
	}
}

// boundaryAngle estimates the direction of the boundaries from the truth: the principal axis of
// the mixed cells' positions in one capture.
func boundaryAngle(s *radSession, rc *radCapture) (float64, bool) {
	var sx, sy, sxx, syy, sxy, n float64
	for _, c := range s.layout.DataCells() {
		if c.X%4 != 0 || c.Y%4 != 0 {
			continue
		}
		mix := rc.tr.CellMix(int(c.X), int(c.Y))
		if len(mix) != 2 || math.Abs(mix[0].W-0.5) > 0.2 {
			continue
		}
		x, y := float64(c.X), float64(c.Y)
		sx, sy, sxx, syy, sxy, n = sx+x, sy+y, sxx+x*x, syy+y*y, sxy+x*y, n+1
	}
	if n < 20 {
		return 0, false
	}
	cxx, cyy, cxy := sxx/n-(sx/n)*(sx/n), syy/n-(sy/n)*(sy/n), sxy/n-(sx/n)*(sy/n)
	a := 0.5 * math.Atan2(2*cxy, cxx-cyy) * 180 / math.Pi
	if a < 0 {
		a += 180
	}
	return a, true
}

// maxPhaseErr is the largest phase weight error allowed at an inner cell (DESIGN.md section 15).
// It is not met: the long version measured up to 0.082, above 0.05 in 9 of its 24 cases, chiefly
// where the panel and the camera scan in opposite directions or a blend lies beyond the anchors;
// DESIGN.md records it in section 15 and its change log. The assertion is left failing, as an exit
// test that could not be met.
const maxPhaseErr = 0.05

// TestPhaseWeights asserts DESIGN.md section 15 for frame mixing on `tripod` and `handheld` with
// their 4K cameras filming a 480 x 270 grid (8 frames shown at 30 fps, camera 60 fps, fixed seeds),
// with the panel scanning in each of its four directions and the screen rolled in the capture: the
// phase weight error of every inner cell is at most maxPhaseErr, and no cell is credited to a code
// frame other than the one it mostly shows. Pocket cells (see phaseStats) are reported, not bounded.
// The CI version runs the four directions with rolls 0 and 30 degrees, 2 captures each (about
// 234,000 cells per case); the long version also roll 60, 8 captures each; the race version one
// combination.
func TestPhaseWeights(t *testing.T) {
	t.Parallel()
	ncap := 2
	rots, rolls := []int{0, 90, 180, 270}, []float64{0, 30}
	switch {
	case longTests():
		rolls, ncap = []float64{0, 30, 60}, 8
	case radRace:
		rots, rolls, ncap = []int{90}, []float64{30}, 2
	}
	s := newRadSession(t, 480, 270, 4, 2, 8, 0x77, false)
	for pi, name := range []string{"tripod", "handheld"} {
		for _, rot := range rots {
			for _, roll := range rolls {
				t.Run(fmt.Sprintf("%s/scan%d/roll%.0f", name, rot, roll), func(t *testing.T) {
					c := preset(t, name, 3840, 2160)
					c.Display.ScanRotation = rot
					c.Geometry.Roll = roll
					// The rolled 16:9 screen must fit the 16:9 capture, with a margin for the tilt.
					sn, cs := math.Sincos(roll * math.Pi / 180)
					c.Geometry.Fill = math.Min(c.Geometry.Fill, 0.9*math.Min(1/(cs+sn*9/16), 9.0/16/(sn+cs*9/16)))
					caps := s.shoot(t, c, time.Second/30, ncap, uint64(100*pi+rot)+uint64(roll))
					var st phaseStats
					var angles []float64
					for _, rc := range caps {
						pf := s.phaseOf(rc)
						st.measure(s, rc, &pf)
						if a, ok := boundaryAngle(s, rc); ok {
							angles = append(angles, math.Round(a))
						}
					}
					t.Logf("boundary directions %v degrees; cells %d (mixed %d, pocket %d); largest weight error %.4f inner, %.4f pocket; RMS %.4f; credited %d, wrong %d, smallest true weight credited %.4f",
						angles, st.cells, st.mixed, st.pocket, st.maxErr, st.maxPocket, math.Sqrt(st.sumSq/float64(4*st.cells)), st.credited, st.wrong, st.minTrue)
					if st.mixed == 0 || st.credited == 0 {
						t.Fatalf("%d mixed and %d credited cells: the case does not exercise the estimator", st.mixed, st.credited)
					}
					if st.maxErr > maxPhaseErr {
						t.Errorf("largest phase weight error %.4f above %.2f", st.maxErr, maxPhaseErr)
					}
					if st.wrong != 0 {
						t.Errorf("%d cells credited to the wrong code frame", st.wrong)
					}
				})
			}
		}
	}
}

// TestExposureTooLong: with an exposure of three display frames no cell is clean, so the capture
// is unusable (DESIGN.md section 7, rule 10); with the preset's exposure it is usable.
func TestExposureTooLong(t *testing.T) {
	s := newRadSession(t, 212, 120, 4, 2, 8, 0x78, false)
	for _, k := range []struct {
		exposure time.Duration
		clean    bool
	}{{100 * time.Millisecond, false}, {4 * time.Millisecond, true}} {
		c := preset(t, "tripod", 1600, 900)
		c.Timing.Exposure = k.exposure
		for _, rc := range s.shoot(t, c, time.Second/30, 3, 5) {
			pf := s.phaseOf(rc)
			if got := pf.Clean(); got != k.clean && (k.clean || channel.CleanFraction(rc.tr, CleanWeight) == 0) {
				t.Errorf("exposure %v, capture %d: clean %v, want %v", k.exposure, rc.tr.Capture, got, k.clean)
			}
		}
	}
}
