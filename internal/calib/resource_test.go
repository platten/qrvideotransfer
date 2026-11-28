package calib

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/prng"
	"github.com/platten/qrvideotransfer/internal/video"
)

// The bounds of SECURITY.md section 9.5.
const (
	noiseTime = 60 * time.Second
	noiseHeap = 1 << 30
)

// heapPeak runs run and returns how long it took and the largest growth of the Go heap in use
// above where it started, sampled every 10 ms.
func heapPeak(t *testing.T, run func()) (time.Duration, uint64) {
	t.Helper()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	base := ms.HeapInuse
	var peak atomic.Uint64
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			if m.HeapInuse > base && m.HeapInuse-base > peak.Load() {
				peak.Store(m.HeapInuse - base)
			}
			select {
			case <-done:
				return
			case <-tick.C:
			}
		}
	}()
	t0 := time.Now()
	run()
	d := time.Since(t0)
	close(done)
	<-stopped
	return d, peak.Load()
}

// noiseFrames returns n frames of uniform noise.
func noiseFrames(n, w, h int, seed uint64) []*video.Frame {
	g := prng.SplitMix64{State: seed}
	var out []*video.Frame
	for range n {
		f := &video.Frame{W: w, H: h, Bits: 8, Y: make([]uint16, w*h), Cb: make([]uint16, w*h/4), Cr: make([]uint16, w*h/4)}
		for _, p := range [][]uint16{f.Y, f.Cb, f.Cr} {
			for i := range p {
				p[i] = uint16(g.Next() & 0xff)
			}
		}
		out = append(out, f)
	}
	return out
}

// TestAnalyzeNoiseResources (SECURITY.md section 9.5): calibrate read on noise ends within 60 s and
// 1 GiB of Go heap growth with the typed error and no recommendation, 16 frames at 848 x 480, then
// 32 at 3840 x 2160 with GOMAXPROCS 16 and 32, as inspect's TestInspectNoiseResources. Long only
// (QRVT_LONG_TESTS=1), for the always-on budget; always, the root package's
// TestAnalyzeCalibrationNoise bounds 3 captures at 480 x 480.
func TestAnalyzeNoiseResources(t *testing.T) {
	if os.Getenv("QRVT_LONG_TESTS") != "1" {
		t.Skip("long only")
	}
	run := func(n, w, h int) {
		rec := writeRaw(t, noiseFrames(n, w, h, 9), 30)
		var err error
		var a *Analysis
		d, peak := heapPeak(t, func() {
			a, err = Analyze(context.Background(), AnalyzeConfig{Recordings: []string{rec}, Backend: video.NewRaw()})
		})
		var e *Error
		if !errors.As(err, &e) || e.Reason != NoCode || a != nil || e.Captures != int64(n) {
			t.Errorf("noise: %v, %+v", err, a)
		}
		t.Logf("calibrate read, %d noise frames at %d x %d, GOMAXPROCS %d: %.1f s, heap growth %.1f MiB", n, w, h,
			runtime.GOMAXPROCS(0), d.Seconds(), float64(peak)/(1<<20))
		if d > noiseTime || peak > noiseHeap {
			t.Errorf("%.1f s and %d bytes of heap growth, bounds %v and %d", d.Seconds(), peak, noiseTime, noiseHeap)
		}
	}
	run(16, 848, 480)
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(0))
	for _, procs := range []int{16, 32} {
		runtime.GOMAXPROCS(procs)
		run(32, 3840, 2160)
	}
}
