package channel

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"math"
	"os"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/video"
)

var update = flag.Bool("update", false, "rewrite testdata/golden/channel.json")

const goldenPath = "../../testdata/golden/channel.json"

// goldenInput is the small fixed input of the golden hashes: four code frames of random cells at
// 12 fps, 64 x 36 video pixels with 4-pixel cells.
func goldenInput() Input {
	var fs []*video.Frame
	for i := range 4 {
		fs = append(fs, cells(64, 36, 4, uint64(100+i)))
	}
	return loop(time.Second/12, 4, fs...)
}

// goldenRun films goldenInput with a preset shrunk to 128 x 72 and hashes every capture and its
// ground truth.
func goldenRun(t testing.TB, name string, workers int, seed uint64) (string, int) {
	c, err := Preset(name)
	if err != nil {
		t.Fatal(err)
	}
	c = c.Resize(128, 72)
	fs, ts := shoot(t, goldenInput(), c, Run{Start: 40 * time.Millisecond, Captures: 8, Seed: seed, Workers: workers})
	return hashCaptures(fs, ts), len(fs)
}

func hashCaptures(fs []*video.Frame, ts []*Truth) string {
	h := sha256.New()
	put := func(v ...any) {
		for _, x := range v {
			_ = binary.Write(h, binary.LittleEndian, x)
		}
	}
	for i, f := range fs {
		put(int64(f.Index), int64(f.PTS), int64(f.W), int64(f.H), f.Bits, f.Y, f.Cb, f.Cr)
		tr := ts[i]
		put(int64(tr.Slot), int64(tr.Start), int64(tr.GridX), int64(tr.GridY))
		for gy := range tr.GridY {
			for gx := range tr.GridX {
				x, y := tr.Cell(gx, gy)
				put(x, y)
				for _, m := range tr.CellMix(gx, gy) {
					put(int64(m.Frame), m.W)
				}
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Identical output for identical seeds on every platform: the SHA-256 of each preset's captures
// and ground truth on a fixed input is frozen in testdata/golden/channel.json, which CI checks on
// all six runners.
func TestGoldenPresets(t *testing.T) {
	got := map[string]string{}
	for _, name := range Presets() {
		h, n := goldenRun(t, name, 2, 1)
		if n == 0 {
			t.Fatalf("%s: no captures", name)
		}
		got[name] = h
	}
	if *update {
		b, _ := json.MarshalIndent(map[string]any{
			"input":   "4 frames of random 4-pixel cells, 64 x 36, 12 fps; preset resized to 128 x 72; start 40 ms, 8 slots, seed 1",
			"presets": got,
		}, "", "  ")
		if err := os.WriteFile(goldenPath, append(b, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	var want struct{ Presets map[string]string }
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	if len(want.Presets) != len(got) {
		t.Errorf("golden file has %d presets, want %d", len(want.Presets), len(got))
	}
	for name, h := range got {
		if want.Presets[name] != h {
			t.Errorf("%s: hash %s, golden %s", name, h, want.Presets[name])
		}
	}
}

// The output depends on the seed only: not on the number of workers.
func TestDeterminism(t *testing.T) {
	a, _ := goldenRun(t, "harsh", 1, 7)
	b, _ := goldenRun(t, "harsh", 5, 7)
	c, _ := goldenRun(t, "harsh", 3, 8)
	if a != b {
		t.Error("output depends on the worker count")
	}
	if a == c {
		t.Error("output does not depend on the seed")
	}
}

func TestDropsAndLabels(t *testing.T) {
	c := still(16, 8)
	c.Timing.Drop = 0.4
	in := loop(time.Second/12, 2, flat(8, 4, 100), flat(8, 4, 50))
	r := Run{Captures: 40, Seed: 2, Workers: 3}
	fs, ts := shoot(t, in, c, r)
	want := 0
	for _, sh := range schedule(c.Timing, 0, 40, 2) {
		if !sh.dropped {
			want++
		}
	}
	if len(fs) != want || want == 40 || want == 0 {
		t.Fatalf("%d captures, want %d", len(fs), want)
	}
	for i, f := range fs {
		tr := ts[i]
		if f.Index != i || tr.Capture != i || f.PTS != tr.PTS || math.Abs(float64(f.PTS)-float64(tr.Slot)*1e9/30) > 1 {
			t.Errorf("capture %d: index %d, slot %d, PTS %v", i, f.Index, tr.Slot, f.PTS)
		}
	}
}

func TestSimulateErrors(t *testing.T) {
	good := func() (Input, Config, Run) {
		return loop(time.Second, 2, flat(8, 4, 16)), still(16, 8), Run{Captures: 2}
	}
	cases := []func(*Input, *Config, *Run){
		func(in *Input, _ *Config, _ *Run) { in.Frames = nil },
		func(in *Input, _ *Config, _ *Run) { in.Frames[0].W = 0 },
		func(in *Input, _ *Config, _ *Run) { in.Frames = append(in.Frames, flat(4, 4, 16)) },
		func(in *Input, _ *Config, _ *Run) { in.Cell = 5 },
		func(in *Input, _ *Config, _ *Run) { in.Period = 0 },
		func(in *Input, _ *Config, _ *Run) { in.Frames[0].PTS = time.Hour },
		func(_ *Input, c *Config, _ *Run) { c.Sensor.Bits = 9 },
		func(_ *Input, _ *Config, r *Run) { r.Captures = -1 },
	}
	for i, f := range cases {
		in, c, r := good()
		f(&in, &c, &r)
		if err := Simulate(context.Background(), in, c, r, func(*video.Frame, *Truth) error { return nil }); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	in, c, r := good()
	r.Captures = 50
	r.Workers = 4
	stop := errors.New("stop")
	n := 0
	err := Simulate(context.Background(), in, c, r, func(*video.Frame, *Truth) error {
		n++
		if n == 3 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || n != 3 {
		t.Errorf("emit error: %v after %d", err, n)
	}
	r.Codec = func(*video.Frame) error { return stop }
	if err := Simulate(context.Background(), in, c, r, func(*video.Frame, *Truth) error { return nil }); !errors.Is(err, stop) {
		t.Errorf("codec error: %v", err)
	}
	r.Codec = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Simulate(ctx, in, c, r, func(*video.Frame, *Truth) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	r.Captures = 0
	if err := Simulate(context.Background(), in, c, r, func(*video.Frame, *Truth) error { return errors.New("called") }); err != nil {
		t.Errorf("no captures: %v", err)
	}
}

func TestMetrics(t *testing.T) {
	if ber, err := RawBER([]uint8{0, 3, 1}, []uint8{1, 3, 2}, 2); err != nil || ber != 3.0/6 {
		t.Errorf("RawBER = %v, %v", ber, err)
	}
	if ber, err := RawBER([]uint8{0xff}, []uint8{0}, 1); err != nil || ber != 1 {
		t.Errorf("RawBER masks: %v, %v", ber, err)
	}
	if ber, err := RawBER(nil, nil, 1); err != nil || ber != 0 {
		t.Errorf("empty RawBER = %v, %v", ber, err)
	}
	if _, err := RawBER([]uint8{1}, nil, 1); err == nil {
		t.Error("length mismatch accepted")
	}
	if _, err := RawBER(nil, nil, 9); err == nil {
		t.Error("width 9 accepted")
	}
	c := still(64, 32)
	_, ts := shoot(t, loop(time.Second, 4, flat(32, 16, 16)), c, Run{Captures: 1})
	tr := ts[0]
	if f := CleanFraction(tr, CleanThreshold); f != 1 {
		t.Errorf("clean fraction of a still capture %v", f)
	}
	exact := func(gx, gy int) (float64, float64, bool) { x, y := tr.Cell(gx, gy); return x, y, true }
	if rms, n := CentreError(tr, exact); rms > 1e-12 || n != 32 {
		t.Errorf("exact estimate: %v over %d", rms, n)
	}
	off := func(gx, gy int) (float64, float64, bool) { x, y := tr.Cell(gx, gy); return x + 1, y, gx > 0 }
	if rms, n := CentreError(tr, off); math.Abs(rms-0.125) > 1e-9 || n != 28 {
		t.Errorf("one pixel off: %v over %d", rms, n)
	}
	if rms, n := CentreError(tr, func(int, int) (float64, float64, bool) { return 0, 0, false }); rms != 0 || n != 0 {
		t.Error("no estimates")
	}
	c.Geometry.Fill = 3 // most cells fall outside the capture
	_, ts = shoot(t, loop(time.Second, 4, flat(32, 16, 16)), c, Run{Captures: 1})
	if f := CleanFraction(ts[0], CleanThreshold); f >= 0.5 {
		t.Errorf("clean fraction with the screen overflowing %v", f)
	}
}

// BenchmarkCapture4K times one 4K capture of a 1080p display frame on the handheld preset.
func BenchmarkCapture4K(b *testing.B) {
	c, _ := Preset("handheld")
	in := loop(time.Second/12, 4, cells(1920, 1080, 4, 1), cells(1920, 1080, 4, 2))
	rc, err := newRecording(in, c, Run{Captures: 1})
	if err != nil {
		b.Fatal(err)
	}
	sh := shot{start: 0.08}
	occs := rc.tl.occurrences(rc.span(&sh))
	panels := make([]*panel, len(occs))
	for k, o := range occs {
		panels[k] = display(rc.frames[o.j], rc.scr)
	}
	q := Quantiser(QuantiserStep(c))
	b.ResetTimer()
	for range b.N {
		f := rc.capture(&sh, occs, panels)
		_ = q(f)
	}
}
