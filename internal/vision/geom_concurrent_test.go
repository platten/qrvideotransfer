package vision

import (
	"slices"
	"sync"
	"testing"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
	"github.com/platten/qrvideotransfer/internal/video"
)

// The concurrency tests run under -race in full: they are what the race step is for. Shared state
// that is filled lazily (a cache, a scratch buffer, a counter) races only while it is being filled,
// so the concurrent phase comes first, before any serial use of the code under test, and the
// serial reference is computed after it. Every goroutine repeats its work and alternates between
// two codes of different grids, so that state keyed on the grid is rewritten while other goroutines
// read it.

// geomConcurrency is the number of goroutines of a concurrency test, and geomRepeats the number of
// times each repeats its work.
const (
	geomConcurrency = 8
	geomRepeats     = 4
)

// geomTogether runs fn(i) for i in 0..n-1 on n goroutines released at the same moment.
func geomTogether(n int, fn func(i int)) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			fn(i)
		}()
	}
	close(start)
	wg.Wait()
}

// geomConcurrentShot is a handheld capture of a code, filmed without any vision code running.
type geomConcurrentShot struct {
	c *geomCode
	f *video.Frame
}

// geomConcurrentShots films one capture of each of two codes of different grids.
func geomConcurrentShots(t *testing.T) [2]geomConcurrentShot {
	t.Helper()
	var out [2]geomConcurrentShot
	for k, g := range []struct{ gx, gy int }{{212, 120}, {240, 135}} {
		c := geomNewCode(t, g.gx, g.gy, 4, 2, 1, 61+uint64(k))
		fs, _ := geomFilm(t, c, geomConfig(t, "handheld", 1280, 720), 0, 1, 62+uint64(k))
		out[k] = geomConcurrentShot{c, fs[0]}
	}
	return out
}

// geomVisionResult is what one run of Detect, Read and Fit gives.
type geomVisionResult struct {
	q       Quad
	ok      bool
	headers []frame.Header
	mesh    *Mesh
	err     error
}

// geomVisionRun runs Detect, Read and Fit with detector d on a capture of code c.
func geomVisionRun(d *FinderDetector, c *geomCode, f *video.Frame) geomVisionResult {
	var r geomVisionResult
	r.q, r.ok = d.Detect(f, nil)
	if r.ok {
		r.headers, _ = SlotReader{}.Read(f, r.q)
		r.mesh, _, r.err = MeshFitter{}.Fit(f, r.q, c.layout)
	}
	return r
}

// TestDetectConcurrent: detectors, header readers and mesh fitters on separate goroutines, each
// goroutine with its own detector, read two shared captures of different grids, alternately and
// repeatedly, and agree with a serial run made afterwards.
func TestDetectConcurrent(t *testing.T) {
	shots := geomConcurrentShots(t)
	got := make([][geomRepeats]geomVisionResult, geomConcurrency)
	geomTogether(geomConcurrency, func(i int) {
		var d FinderDetector
		for r := range geomRepeats {
			s := shots[(i+r)%2]
			got[i][r] = geomVisionRun(&d, s.c, s.f)
		}
	})
	var want [2]geomVisionResult
	for k, s := range shots {
		var d FinderDetector
		want[k] = geomVisionRun(&d, s.c, s.f)
		if !want[k].ok || want[k].err != nil || len(want[k].headers) == 0 {
			t.Fatalf("code %d serially: found %v, %d headers, fit %v", k, want[k].ok, len(want[k].headers), want[k].err)
		}
	}
	for i := range got {
		for r, g := range got[i] {
			k := (i + r) % 2
			w := want[k]
			switch {
			case !g.ok:
				t.Errorf("goroutine %d run %d: not found", i, r)
				continue
			case !slices.Equal(g.headers, w.headers):
				t.Errorf("goroutine %d run %d: headers %v, serially %v", i, r, g.headers, w.headers)
			case g.err != nil:
				t.Errorf("goroutine %d run %d: %v", i, r, g.err)
				continue
			}
			for c := range 4 {
				if geomDist(g.q.Centres[c], w.q.Centres[c]) > w.q.Module/100 {
					t.Errorf("goroutine %d run %d: centre %d at %v, serially %v", i, r, c, g.q.Centres[c], w.q.Centres[c])
				}
			}
			gx, gy := float64(shots[k].c.layout.GridX()), float64(shots[k].c.layout.GridY())
			for _, p := range [][2]float64{{0.5, 0.5}, {gx / 2, gy / 2}, {gx - 0.5, gy - 0.5}} {
				if e := geomDist(g.mesh.At(p[0], p[1]), w.mesh.At(p[0], p[1])); e > 0.01 {
					t.Errorf("goroutine %d run %d: mesh at %v off by %.4f px", i, r, p, e)
				}
			}
		}
	}
}

// TestSampleConcurrent: one FootprintSampler, documented as safe for concurrent use, samples two
// shared captures through their shared meshes on several goroutines at once, alternately and
// repeatedly, and every goroutine's observations agree with a serial run made afterwards. The
// meshes are the codes' exact geometry on rendered frames, so that no vision code runs before the
// concurrent phase.
func TestSampleConcurrent(t *testing.T) {
	var cs [2]*geomCode
	var fs [2]*video.Frame
	var ms [2]*Mesh
	var cells [2][]frame.Cell
	for k, g := range []struct{ gx, gy int }{{212, 120}, {240, 135}} {
		cs[k] = geomNewCode(t, g.gx, g.gy, 4, 2, 1, 63+uint64(k))
		fs[k], ms[k], cells[k] = geomPlain(cs[k], 0), geomPlainMesh(cs[k]), cs[k].layout.DataCells()
	}
	var s FootprintSampler
	got := make([][geomRepeats][]modem.Obs, geomConcurrency)
	geomTogether(geomConcurrency, func(i int) {
		for r := range geomRepeats {
			k := (i + r) % 2
			got[i][r] = make([]modem.Obs, len(cells[k]))
			s.Sample(got[i][r], fs[k], ms[k], cells[k])
		}
	})
	var want [2][]modem.Obs
	for k := range want {
		want[k] = make([]modem.Obs, len(cells[k]))
		s.Sample(want[k], fs[k], ms[k], cells[k])
	}
	for i := range got {
		for r, obs := range got[i] {
			w := want[(i+r)%2]
			for j := range obs {
				if !geomObsNear(obs[j], w[j]) {
					t.Errorf("goroutine %d run %d cell %d: %+v, serially %+v", i, r, j, obs[j], w[j])
					break
				}
			}
		}
	}
}
