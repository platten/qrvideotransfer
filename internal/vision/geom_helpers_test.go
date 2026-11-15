package vision

import (
	"context"
	"math"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/channel"
	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/prng"
	"github.com/platten/qrvideotransfer/internal/render"
	"github.com/platten/qrvideotransfer/internal/video"
)

// geomLong reports whether the long tests run (QRVT_LONG_TESTS=1).
func geomLong() bool { return os.Getenv("QRVT_LONG_TESTS") != "" }

// geomCode is a looping test video: n code frames of one grid, with their headers and symbols.
type geomCode struct {
	layout  *frame.Layout
	pitch   int
	mod     uint8
	headers []frame.Header
	symbols [][]uint8
	input   channel.Input
}

// geomNewCode renders n code frames of a gridX x gridY grid at the given pitch and modulation, shown
// at 12 fps, with random symbols from seed.
func geomNewCode(t testing.TB, gridX, gridY, pitch int, mod uint8, n int, seed uint64) *geomCode {
	t.Helper()
	r, err := render.New(gridX, gridY, pitch, mod)
	if err != nil {
		t.Fatal(err)
	}
	w, h := r.Size()
	c := &geomCode{layout: r.Layout(), pitch: pitch, mod: mod}
	g := prng.SplitMix64{State: seed}
	total := uint32((n + 3) / 4 * 4) //nolint:gosec // small
	for i := range n {
		hd := frame.Header{Version: frame.Version, Session: uint16(seed), FrameIndex: uint32(i), TotalFrames: total, //nolint:gosec // small
			GridX: uint16(gridX), GridY: uint16(gridY), Modulation: mod, InnerCode: 2} //nolint:gosec // grid <= 2047
		syms := make([]uint8, len(c.layout.DataCells()))
		for k := range syms {
			syms[k] = uint8(g.Next() >> 56)
		}
		f := &video.Frame{Y: make([]uint16, w*h), Cb: make([]uint16, w*h/4), Cr: make([]uint16, w*h/4)}
		if err := r.Render(f, hd, syms); err != nil {
			t.Fatal(err)
		}
		f.Index, f.PTS = i, time.Duration(i)*time.Second/12
		c.headers = append(c.headers, hd)
		c.symbols = append(c.symbols, syms)
		c.input.Frames = append(c.input.Frames, f)
	}
	c.input.Period = time.Duration(n) * time.Second / 12
	c.input.Cell = pitch
	return c
}

// geomConfig returns the preset, resized to w x h camera pixels unless w is 0.
func geomConfig(t testing.TB, preset string, w, h int) channel.Config {
	t.Helper()
	cfg, err := channel.Preset(preset)
	if err != nil {
		t.Fatal(err)
	}
	if w != 0 {
		cfg = cfg.Resize(w, h)
	}
	return cfg
}

// geomFilm records the code with cfg and returns the captures and their truths.
func geomFilm(t testing.TB, c *geomCode, cfg channel.Config, start time.Duration, captures int, seed uint64) ([]*video.Frame, []*channel.Truth) {
	t.Helper()
	fs, ts, err := geomFilmErr(c, cfg, start, captures, seed)
	if err != nil {
		t.Fatal(err)
	}
	return fs, ts
}

// geomFilmErr is geomFilm reporting its error, for use off the test's goroutine.
func geomFilmErr(c *geomCode, cfg channel.Config, start time.Duration, captures int, seed uint64) ([]*video.Frame, []*channel.Truth, error) {
	var fs []*video.Frame
	var ts []*channel.Truth
	run := channel.Run{Start: start, Captures: captures, Seed: seed}
	err := channel.Simulate(context.Background(), c.input, cfg, run, func(f *video.Frame, tr *channel.Truth) error {
		fs = append(fs, f)
		ts = append(ts, tr)
		return nil
	})
	return fs, ts, err
}

// geomFinderTruth returns the true capture positions of the four finder centres and the true
// finder module size in capture pixels at each.
func geomFinderTruth(c *geomCode, tr *channel.Truth) (centres [4]Point, module [4]float64) {
	gx, gy := float64(c.layout.GridX()), float64(c.layout.GridY())
	uy := gy / 135
	c0 := 4.5 * uy
	p := float64(c.pitch)
	cells := [4][2]float64{{c0, c0}, {gx - c0, c0}, {c0, gy - c0}, {gx - c0, gy - c0}}
	for k, v := range cells {
		x, y := tr.Point(v[0]*p, v[1]*p)
		x1, y1 := tr.Point((v[0]+uy)*p, v[1]*p)
		x2, y2 := tr.Point(v[0]*p, (v[1]+uy)*p)
		centres[k] = Point{x, y}
		module[k] = math.Sqrt(math.Abs((x1-x)*(y2-y) - (x2-x)*(y1-y)))
	}
	return centres, module
}

// geomPose is one simulated pose: a capture of the code and its truth.
type geomPose struct {
	f  *video.Frame
	tr *channel.Truth
}

// geomPoses films n independent poses of the code with cfg, one capture each, with seeds
// seed, seed+1, ..., in parallel. A pose whose capture was dropped is filmed again with the next
// seed past the range.
func geomPoses(t testing.TB, c *geomCode, cfg channel.Config, n int, seed uint64) []geomPose {
	t.Helper()
	out := make([]geomPose, n)
	errs := make([]error, n)
	geomParallel(n, func(i int) {
		s := seed + uint64(i) //nolint:gosec // i >= 0
		for k := 1; ; k++ {
			g := prng.SplitMix64{State: s}
			start := time.Duration(g.Next() % uint64(c.input.Period)) //nolint:gosec // positive
			fs, ts, err := geomFilmErr(c, cfg, start, 1, s)
			if err != nil || len(fs) == 1 {
				if err == nil {
					out[i] = geomPose{fs[0], ts[0]}
				}
				errs[i] = err
				return
			}
			s += uint64(n * k) //nolint:gosec // small
		}
	})
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// geomParallel runs fn(i) for i in 0..n-1 on GOMAXPROCS goroutines.
func geomParallel(n int, fn func(i int)) {
	var wg sync.WaitGroup
	next := make(chan int)
	for range runtime.GOMAXPROCS(0) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				fn(i)
			}
		}()
	}
	for i := range n {
		next <- i
	}
	close(next)
	wg.Wait()
}

// geomDist returns the distance between two points.
func geomDist(a, b Point) float64 { return math.Hypot(a.X-b.X, a.Y-b.Y) }

// geomPlain returns frame k of the code as a capture: the rendered video itself.
func geomPlain(c *geomCode, k int) *video.Frame {
	f := *c.input.Frames[k]
	f.Y = append([]uint16(nil), f.Y...)
	f.Cb = append([]uint16(nil), f.Cb...)
	f.Cr = append([]uint16(nil), f.Cr...)
	return &f
}

// geomPlainQuad returns the exact finder quad of a rendered frame of the code.
func geomPlainQuad(c *geomCode) Quad {
	gx, gy := float64(c.layout.GridX()), float64(c.layout.GridY())
	uy := gy / 135
	c0 := 4.5 * uy
	p := float64(c.pitch)
	return Quad{
		Centres: [4]Point{{c0 * p, c0 * p}, {(gx - c0) * p, c0 * p}, {c0 * p, (gy - c0) * p}, {(gx - c0) * p, (gy - c0) * p}},
		Module:  uy * p,
	}
}
