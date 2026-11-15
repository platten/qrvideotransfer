package vision

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/channel"
)

// phaseStats summarises a phase field against the truth.
type phaseStats struct {
	cells, mixed    int     // cells inside the capture; those whose largest true weight is below 1
	maxErr, sumSq   float64 // largest and summed squared weight error over cells and phases
	credited, wrong int     // cells credited to some phase; those whose true frame weight is below 0.5
	minTrue         float64 // smallest true weight among credited cells
}

// measure compares the estimated field of a capture with the truth at every data cell.
func (st *phaseStats) measure(s *radSession, rc *radCapture, pf *PhaseField) {
	mix := rc.trueMix(s.layout)
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
		for p := range 4 {
			d := math.Abs(float64(pf.Weights[i][p]) - w[p])
			st.maxErr = math.Max(st.maxErr, d)
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

// TestPhaseWeights asserts DESIGN.md section 15 for frame mixing: on `tripod` and `handheld`, with
// the panel scanning in each of its four directions and the screen rolled by 0, 30 and 60 degrees in
// the capture, the phase weight error of every data cell is at most 0.05, and no cell is credited to
// a code frame other than the one it mostly shows. The CI version films a 212 x 120 grid with a 1600
// x 900 camera, 8 captures per case of 8 frames shown at 30 fps (fixed seeds); the long version a 480
// x 270 grid with the 4K cameras, 16 captures per case.
func TestPhaseWeights(t *testing.T) {
	grid, cw, ch, ncap := [2]int{212, 120}, 1600, 900, 8
	if longTests() {
		grid, cw, ch, ncap = [2]int{480, 270}, 3840, 2160, 16
	}
	s := newRadSession(t, grid[0], grid[1], 4, 2, 8, 0x77, false)
	for pi, name := range []string{"tripod", "handheld"} {
		for _, rot := range []int{0, 90, 180, 270} {
			for _, roll := range []float64{0, 30, 60} {
				t.Run(fmt.Sprintf("%s/scan%d/roll%.0f", name, rot, roll), func(t *testing.T) {
					c := preset(t, name, cw, ch)
					c.Display.ScanRotation = rot
					c.Geometry.Roll = roll
					// A rolled 16:9 screen must still fit the 16:9 capture.
					c.Geometry.Fill *= 0.6
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
					t.Logf("cells %d (mixed %d), max weight error %.4f, RMS %.4f, credited %d, wrong %d, smallest true weight credited %.4f, boundary directions %v",
						st.cells, st.mixed, st.maxErr, math.Sqrt(st.sumSq/float64(4*st.cells)), st.credited, st.wrong, st.minTrue, angles)
					if st.mixed == 0 || st.credited == 0 {
						t.Fatalf("%d mixed and %d credited cells: the case does not exercise the estimator", st.mixed, st.credited)
					}
					if st.maxErr > 0.05 {
						t.Errorf("max phase weight error %.4f above 0.05", st.maxErr)
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
