package vision

import (
	"sync"
	"testing"

	"github.com/platten/qrvideotransfer/internal/modem"
	"github.com/platten/qrvideotransfer/internal/video"
)

// The concurrency tests run under -race in full: they are what the race step is for. Each shares one
// simulated capture between goroutines that start together, and compares every result with a serial
// one.

// geomConcurrency is the number of goroutines of a concurrency test.
const geomConcurrency = 8

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

// geomConcurrentShot films one handheld capture of a code and locates it serially.
func geomConcurrentShot(t *testing.T) (*geomCode, *video.Frame, Quad, *Mesh) {
	t.Helper()
	c := geomNewCode(t, 212, 120, 4, 2, 1, 61)
	fs, _ := geomFilm(t, c, geomConfig(t, "handheld", 1280, 720), 0, 1, 62)
	var d FinderDetector
	q, ok := d.Detect(fs[0], nil)
	if !ok {
		t.Fatal("not found")
	}
	m, _, err := MeshFitter{}.Fit(fs[0], q, c.layout)
	if err != nil {
		t.Fatal(err)
	}
	return c, fs[0], q, m
}

// TestDetectConcurrent: detectors, header readers and mesh fitters on separate goroutines, each
// goroutine with its own detector, read one shared capture and agree with a serial run.
func TestDetectConcurrent(t *testing.T) {
	c, f, want, wantMesh := geomConcurrentShot(t)
	wantHeaders, _ := SlotReader{}.Read(f, want)
	type result struct {
		q       Quad
		ok      bool
		headers int
		mesh    *Mesh
		err     error
	}
	got := make([]result, geomConcurrency)
	geomTogether(geomConcurrency, func(i int) {
		var d FinderDetector
		r := &got[i]
		r.q, r.ok = d.Detect(f, nil)
		if !r.ok {
			return
		}
		hs, _ := SlotReader{}.Read(f, r.q)
		r.headers = len(hs)
		r.mesh, _, r.err = MeshFitter{}.Fit(f, r.q, c.layout)
	})
	for i, r := range got {
		switch {
		case !r.ok:
			t.Errorf("goroutine %d: not found", i)
			continue
		case r.headers != len(wantHeaders):
			t.Errorf("goroutine %d: %d headers, want %d", i, r.headers, len(wantHeaders))
		case r.err != nil:
			t.Errorf("goroutine %d: %v", i, r.err)
			continue
		}
		for k := range 4 {
			if geomDist(r.q.Centres[k], want.Centres[k]) > want.Module/100 {
				t.Errorf("goroutine %d: centre %d at %v, serially %v", i, k, r.q.Centres[k], want.Centres[k])
			}
		}
		for _, p := range [][2]float64{{0.5, 0.5}, {106, 60}, {211.5, 119.5}} {
			if e := geomDist(r.mesh.At(p[0], p[1]), wantMesh.At(p[0], p[1])); e > 0.01 {
				t.Errorf("goroutine %d: mesh at %v off by %.4f px", i, p, e)
			}
		}
	}
}

// TestSampleConcurrent: one FootprintSampler, documented as safe for concurrent use, samples one
// shared capture through one shared mesh on several goroutines at once, and every goroutine's
// observations agree with a serial run.
func TestSampleConcurrent(t *testing.T) {
	c, f, _, m := geomConcurrentShot(t)
	cells := c.layout.DataCells()
	want := make([]modem.Obs, len(cells))
	var s FootprintSampler
	s.Sample(want, f, m, cells)
	got := make([][]modem.Obs, geomConcurrency)
	geomTogether(geomConcurrency, func(i int) {
		got[i] = make([]modem.Obs, len(cells))
		s.Sample(got[i], f, m, cells)
	})
	for i, obs := range got {
		for j := range obs {
			if !geomObsNear(obs[j], want[j]) {
				t.Errorf("goroutine %d cell %d: %+v, serially %+v", i, j, obs[j], want[j])
				break
			}
		}
	}
}
