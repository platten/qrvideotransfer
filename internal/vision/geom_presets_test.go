package vision

import (
	"math"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/channel"
	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
)

// geomStats are the geometry assertions' counts over a set of poses.
type geomStats struct {
	poses       int
	found       int     // quads found with every centre within half a module of the truth
	wrong       int     // quads found elsewhere
	header      int     // captures whose header was read, with no wrong header
	wrongHeader int     // captures for which a wrong header was returned
	meshFailed  int     // captures whose mesh fit failed
	sumSq       float64 // squared centre errors, in cells squared, summed over the cells measured
	cells       int
	worst       float64 // largest per-capture RMS centre error, cells
	anchors     int     // anchors located
	anchorTotal int
	elapsed     time.Duration // vision time, summed over captures
}

func (s *geomStats) rms() float64 { return math.Sqrt(s.sumSq / float64(max(s.cells, 1))) }

// geomMeasured selects the cells whose centre error is measured: about one in eleven, spread over
// the grid, to keep the ground truth's cost down.
func geomMeasured(gx, gy int) bool { return (7*gx+3*gy)%11 == 0 }

// geomSweep runs detection, header reading and mesh fitting on n poses of the code filmed with cfg,
// and counts the outcomes against the ground truth. Each pose is filmed and measured by one worker
// and then dropped, so that memory stays bounded by the number of workers.
func geomSweep(t *testing.T, c *geomCode, cfg channel.Config, n int, seed uint64) geomStats {
	t.Helper()
	var mu sync.Mutex
	s := geomStats{poses: n}
	var filmErr error
	geomParallel(n, func(i int) {
		p, err := geomPose(c, cfg, n, i, seed)
		if err != nil {
			mu.Lock()
			filmErr = err
			mu.Unlock()
			return
		}
		var d FinderDetector
		t0 := time.Now()
		q, ok := d.Detect(p.f, nil)
		var hs []frame.Header
		var mesh *Mesh
		var anchors []AnchorObs
		var fitErr error
		if ok {
			hs, _ = SlotReader{}.Read(p.f, q)
			mesh, anchors, fitErr = MeshFitter{}.Fit(p.f, q, c.layout)
		}
		el := time.Since(t0)
		var r geomStats
		r.elapsed = el
		if !ok {
			t.Logf("pose %d: no quad", i)
		}
		if ok {
			want, mod := geomFinderTruth(c, p.tr)
			good := true
			for k := range 4 {
				if geomDist(q.Centres[k], want[k]) > mod[k]/2 {
					good = false
				}
			}
			if good {
				r.found++
			} else {
				r.wrong++
				t.Logf("pose %d: quad %v, want %v", i, q.Centres, want)
			}
			shown := geomShown(c, p.tr)
			bad := len(hs) == 0
			for _, h := range hs {
				if int(h.FrameIndex) >= len(c.headers) || h != c.headers[h.FrameIndex] || !slices.Contains(shown, int(h.FrameIndex)) {
					bad = true
					r.wrongHeader++
					break
				}
			}
			if !bad {
				r.header++
			}
			if fitErr != nil {
				r.meshFailed++
			} else {
				rms, m := channel.CentreError(p.tr, func(gx, gy int) (float64, float64, bool) {
					if !geomMeasured(gx, gy) {
						return 0, 0, false
					}
					pt := mesh.At(float64(gx)+0.5, float64(gy)+0.5)
					return pt.X, pt.Y, true
				})
				r.sumSq, r.cells, r.worst = rms*rms*float64(m), m, rms
				for _, a := range anchors {
					if a.Found {
						r.anchors++
					}
				}
				r.anchorTotal = len(anchors)
			}
		}
		mu.Lock()
		s.found += r.found
		s.wrong += r.wrong
		s.header += r.header
		s.wrongHeader += r.wrongHeader
		s.meshFailed += r.meshFailed
		s.sumSq += r.sumSq
		s.cells += r.cells
		s.worst = max(s.worst, r.worst)
		s.anchors += r.anchors
		s.anchorTotal += r.anchorTotal
		s.elapsed += r.elapsed
		mu.Unlock()
	})
	if filmErr != nil {
		t.Fatal(filmErr)
	}
	t.Logf("%s, %d poses: finders %d (%.2f%%), wrong %d; header %d (%.2f%%), wrong %d; mesh failed %d; "+
		"centre error %.4f cell RMS over %d cells, worst capture %.4f; anchors %d of %d; vision %.1f ms per capture",
		cfg.Name, n, s.found, 100*float64(s.found)/float64(n), s.wrong, s.header, 100*float64(s.header)/float64(n), s.wrongHeader,
		s.meshFailed, s.rms(), s.cells, s.worst, s.anchors, s.anchorTotal, float64(s.elapsed.Microseconds())/1000/float64(n))
	return s
}

// geomShown returns the code frames visible anywhere along the header copies of the capture.
func geomShown(c *geomCode, tr *channel.Truth) []int {
	var out []int
	q := geomPlainQuad(c)
	for cp := range 4 {
		for _, i := range []int{0, 29, 58} {
			pf := frame.HeaderSlot(cp, i, 0)
			u := q.Centres[0].X + pf.X*(q.Centres[1].X-q.Centres[0].X)
			v := q.Centres[0].Y + pf.Y*(q.Centres[2].Y-q.Centres[0].Y)
			for _, w := range tr.Mix(u, v) {
				if !slices.Contains(out, w.Frame) {
					out = append(out, w.Frame)
				}
			}
		}
	}
	return out
}

// geomCheck asserts the geometry rows of DESIGN.md section 15 on s: finders in at least minFound of
// the poses, never a wrong quad, the header in at least minHeader of them (when minHeader > 0),
// never a wrong header, and a centre error of at most 0.15 cell RMS.
func geomCheck(t *testing.T, s geomStats, minFound, minHeader float64) {
	t.Helper()
	n := float64(s.poses)
	if float64(s.found) < minFound*n || s.wrong > 0 {
		t.Errorf("finders found in %d of %d poses (%d wrong), want at least %.1f%% and none wrong", s.found, s.poses, s.wrong, 100*minFound)
	}
	if minHeader > 0 && float64(s.header) < minHeader*n {
		t.Errorf("header read in %d of %d captures, want at least %.1f%%", s.header, s.poses, 100*minHeader)
	}
	if s.wrongHeader > 0 {
		t.Errorf("a wrong header was returned for %d captures", s.wrongHeader)
	}
	if s.meshFailed > 0 {
		t.Errorf("mesh fit failed for %d captures", s.meshFailed)
	}
	if r := s.rms(); !(r <= 0.15) {
		t.Errorf("cell-centre error %.4f cell RMS, want at most 0.15", r)
	}
}

// geomTilt25 is the handheld preset with the screen tilted by up to 25 degrees, the steepest
// perspective of the M2 assertion.
func geomTilt25(t testing.TB, w, h int) channel.Config {
	cfg := geomConfig(t, "handheld", w, h)
	cfg.Name, cfg.Geometry.Tilt = "handheld-tilt25", 25
	return cfg
}

// TestGeometryPresetsSmall asserts the M2 geometry rows on 24 poses of each of tripod, handheld and
// handheld tilted up to 25 degrees, with the camera shrunk to 1280 x 720 and a 212 x 120 grid at
// 4 pixels per cell (about 5 camera pixels per cell), seeds 1000 to 1023. With 24 poses every pose
// must succeed.
func TestGeometryPresetsSmall(t *testing.T) {
	c := geomNewCode(t, 212, 120, 4, 2, 4, 11)
	for _, cfg := range []channel.Config{geomConfig(t, "tripod", 1280, 720), geomConfig(t, "handheld", 1280, 720), geomTilt25(t, 1280, 720)} {
		t.Run(cfg.Name, func(t *testing.T) {
			geomCheck(t, geomSweep(t, c, cfg, 24, 1000), 1, 1)
		})
	}
}

// TestGeometryPresets4K asserts the same on 4 full-size 4K poses per preset with a 480 x 270 grid
// at 4 pixels per cell (1080p video), seeds 2000 to 2003.
func TestGeometryPresets4K(t *testing.T) {
	c := geomNewCode(t, 480, 270, 4, 2, 4, 12)
	for _, cfg := range []channel.Config{geomConfig(t, "tripod", 0, 0), geomConfig(t, "handheld", 0, 0), geomTilt25(t, 0, 0)} {
		t.Run(cfg.Name, func(t *testing.T) {
			geomCheck(t, geomSweep(t, c, cfg, 4, 2000), 1, 1)
		})
	}
}

// TestGeometryPresetsLong is the full assertion of IMPLEMENTATION_PROMPT.md section 5 (M2): 1,000
// random 4K poses per preset (seeds 10000 to 10999), 480 x 270 grid at 4 pixels per cell. Finders in
// at least 99.9% of poses on tripod and handheld, the header in at least 99.5% of captures on
// handheld, and a centre error of at most 0.15 cell RMS on each, including handheld tilted up to 25
// degrees. It runs with QRVT_LONG_TESTS=1.
func TestGeometryPresetsLong(t *testing.T) {
	if !geomLong() {
		t.Skip("set QRVT_LONG_TESTS=1")
	}
	c := geomNewCode(t, 480, 270, 4, 2, 4, 13)
	for _, cfg := range []channel.Config{geomConfig(t, "tripod", 0, 0), geomConfig(t, "handheld", 0, 0), geomTilt25(t, 0, 0)} {
		t.Run(cfg.Name, func(t *testing.T) {
			minHeader := 0.0
			if cfg.Name == "handheld" {
				minHeader = 0.995
			}
			geomCheck(t, geomSweep(t, c, cfg, 1000, 10000), 0.999, minHeader)
		})
	}
}

// TestGeometry16x10 runs the same assertions on a 16:10 grid (432 x 270, so Ux differs from Uy)
// with the handheld preset at 1920 x 1080, 12 poses from seed 3000. The screen fills 0.7 of the
// width instead of 0.8, so that the taller code stays inside a 16:9 capture.
func TestGeometry16x10(t *testing.T) {
	c := geomNewCode(t, 432, 270, 4, 2, 4, 14)
	cfg := geomConfig(t, "handheld", 1920, 1080)
	cfg.Geometry.Fill = 0.7
	geomCheck(t, geomSweep(t, c, cfg, 12, 3000), 1, 1)
}

// BenchmarkGeometry4K times detection, header reading, mesh fitting and sampling of every data
// cell for one 4K handheld capture of a 480 x 270 grid, on one goroutine.
func BenchmarkGeometry4K(b *testing.B) {
	c := geomNewCode(b, 480, 270, 4, 2, 1, 15)
	p := geomPoses(b, c, geomConfig(b, "handheld", 0, 0), 1, 5000)[0]
	cells := c.layout.DataCells()
	obs := make([]modem.Obs, len(cells))
	var d FinderDetector
	for b.Loop() {
		q, ok := d.Detect(p.f, nil)
		if !ok {
			b.Fatal("not found")
		}
		if _, ok := (SlotReader{}).Read(p.f, q); !ok {
			b.Fatal("no header")
		}
		m, _, err := MeshFitter{}.Fit(p.f, q, c.layout)
		if err != nil {
			b.Fatal(err)
		}
		FootprintSampler{}.Sample(obs, p.f, m, cells)
	}
}
