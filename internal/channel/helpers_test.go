package channel

import (
	"context"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/prng"
	"github.com/platten/qrvideotransfer/internal/video"
)

// grey returns an 8-bit frame whose brightness is y(x, row) and whose colour is neutral.
func grey(w, h int, y func(x, row int) uint16) *video.Frame {
	f := &video.Frame{W: w, H: h, Bits: 8, Y: make([]uint16, w*h), Cb: make([]uint16, (w+1)/2*((h+1)/2)), Cr: make([]uint16, (w+1)/2*((h+1)/2))}
	for r := range h {
		for x := range w {
			f.Y[r*w+x] = y(x, r)
		}
	}
	for i := range f.Cb {
		f.Cb[i], f.Cr[i] = 128, 128
	}
	return f
}

// flat returns a uniform grey frame.
func flat(w, h int, y uint16) *video.Frame {
	return grey(w, h, func(int, int) uint16 { return y })
}

// cells returns a frame of random four-grey cells of p pixels with random colour cells.
func cells(w, h, p int, seed uint64) *video.Frame {
	g := prng.SplitMix64{State: seed}
	levels := []uint16{16, 89, 162, 235}
	gx := (w + p - 1) / p
	sym := make([]uint64, gx*((h+p-1)/p))
	for i := range sym {
		sym[i] = g.Next()
	}
	f := grey(w, h, func(x, r int) uint16 { return levels[sym[(r/p)*gx+x/p]&3] })
	for i := range f.Cb {
		cx, cy := i%((w+1)/2), i/((w+1)/2)
		s := sym[(2*cy/p)*gx+2*cx/p]
		f.Cb[i], f.Cr[i] = uint16(96+(s>>2&1)*64), uint16(96+(s>>3&1)*64)
	}
	return f
}

// still returns the plain camera: no impairment at all, at the given size.
func still(w, h int) Config {
	return Config{
		Name:     "still",
		Display:  DisplayParams{},
		Timing:   TimingParams{FPS: 30, Exposure: time.Millisecond},
		Geometry: GeometryParams{Width: w, Height: h, Fill: 1},
		Sensor:   SensorParams{Bits: 8, Gain: 1},
	}
}

// loop shows the frames for d each.
func loop(d time.Duration, cell int, frames ...*video.Frame) Input {
	for i, f := range frames {
		f.PTS = time.Duration(i) * d
		f.Index = i
	}
	return Input{Frames: frames, Period: time.Duration(len(frames)) * d, Cell: cell}
}

// shoot simulates and collects every capture and truth.
func shoot(t testing.TB, in Input, c Config, r Run) ([]*video.Frame, []*Truth) {
	t.Helper()
	var fs []*video.Frame
	var ts []*Truth
	err := Simulate(context.Background(), in, c, r, func(f *video.Frame, tr *Truth) error {
		fs = append(fs, f)
		ts = append(ts, tr)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return fs, ts
}

// mustRecording builds a recording or fails.
func mustRecording(t testing.TB, in Input, c Config, r Run) *recording {
	t.Helper()
	rc, err := newRecording(in, c, r)
	if err != nil {
		t.Fatal(err)
	}
	return rc
}
