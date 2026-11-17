package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/channel"
	"github.com/platten/qrvideotransfer/internal/payload"
	"github.com/platten/qrvideotransfer/internal/prng"
	"github.com/platten/qrvideotransfer/internal/video"
)

// TestLoopback decodes the code video itself, a perfect digital capture of every frame, for each
// modulation and protection at 480p. It is also the check DESIGN.md section 18 asks of M3: the
// glare rule discards about 2% of black-and-white cells of a perfect frame (section 10, step 4),
// and the loopback must still pass, here with one capture per frame.
func TestLoopback(t *testing.T) {
	type combo struct{ mod, inner uint8 }
	var combos []combo
	for _, mod := range []uint8{1, 2, 3} {
		for _, inner := range []uint8{1, 2, 4} {
			combos = append(combos, combo{mod, inner})
		}
	}
	if raceEnabled {
		combos = combos[4:5] // Gray4, normal
	}
	in, want := writeInputs(t, 1, 9000, 300, 0)
	for _, c := range combos {
		t.Run(fmt.Sprintf("mod%d-inner%d", c.mod, c.inner), func(t *testing.T) {
			cfg := profile480(in)
			cfg.Modulation, cfg.Inner = c.mod, c.inner
			path, res := encodeRaw(t, cfg)
			if res.Frames < 4 || res.Frames%4 != 0 {
				t.Fatalf("F = %d", res.Frames)
			}
			out, dr, err := decodeTo(t, path)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			checkOutput(t, out, want)
			if dr.Session != res.Session {
				t.Errorf("session %x, encoded %x", dr.Session, res.Session)
			}
			st := dr.Stats
			if st.FramesTotal != res.Frames || st.FramesSeen == 0 || st.BlocksReady != st.BlocksTotal || st.BlocksTotal != res.Blocks {
				t.Errorf("stats %+v for %d frames, %d blocks", st, res.Frames, res.Blocks)
			}
			t.Logf("F %d, C %d, clean %.4f, raw bit errors %.2e, codewords %d", res.Frames, res.Codewords, st.CleanFraction, st.RawBitErrors, st.CodewordsOK)
		})
	}
}

// lossPlan encodes, at 480p Gray4 `normal` with parity 20, a payload whose loop has at least
// minFrames frames and repeats fewer than C - 1 packets at its end, so that dropping a frame that
// carries no repeated packet costs exactly C - 1 shards. It returns the video and its frames.
func lossPlan(t *testing.T, minFrames int) (string, *EncodeResult, map[string][32]byte) {
	t.Helper()
	for size := minFrames * 28 * 128 * 5 / 6; size < minFrames*28*128*2; size += 128 {
		in, want := writeInputs(t, 7, size)
		cfg := profile480(in)
		cfg.DryRun = true
		_, res := encodeRaw(t, cfg)
		slack := res.Frames*(res.Codewords-1) - res.Blocks*(res.K+res.M)
		if res.Frames >= minFrames && res.Blocks == 1 && slack < res.Codewords-1 {
			cfg.DryRun = false
			path, res := encodeRaw(t, cfg)
			return path, res, want
		}
	}
	t.Fatal("no payload size found")
	return "", nil, nil
}

// TestFrameLoss drops code frames at random from a perfect recording of every frame (DESIGN.md
// section 15): with parity 20, whose budget is 16.7% of frames, losing 14.7% of the frames
// succeeds; losing 18.7% fails with the short block named and nothing written.
func TestFrameLoss(t *testing.T) {
	minFrames := 100
	if raceEnabled {
		minFrames = 28
	}
	path, res, want := lossPlan(t, minFrames)
	frames, info := readFrames(t, video.NewRaw(), path)
	per := res.Codewords - 1
	slack := res.Frames*per - (res.K + res.M)
	// Frames whose packets are all distinct, neither repeated at the end nor repeating at the start.
	var droppable []int
	for f := range res.Frames {
		if f*per >= slack && (f+1)*per <= res.K+res.M {
			droppable = append(droppable, f)
		}
	}
	g := prng.SplitMix64{State: 99}
	for i := len(droppable) - 1; i > 0; i-- {
		j := int(g.Next() % uint64(i+1)) //nolint:gosec // small
		droppable[i], droppable[j] = droppable[j], droppable[i]
	}
	for _, c := range []struct {
		share float64
		ok    bool
	}{{0.147, true}, {0.187, false}} {
		t.Run(fmt.Sprintf("%.1f%%", 100*c.share), func(t *testing.T) {
			n := int(c.share * float64(res.Frames))
			if !c.ok {
				n = int(c.share*float64(res.Frames) + 0.999)
			}
			if lost := n * per; (lost <= res.M) != c.ok {
				t.Fatalf("test design: %d frames lose %d shards against M = %d", n, lost, res.M)
			}
			drop := map[int]bool{}
			for _, f := range droppable[:n] {
				drop[f] = true
			}
			var kept []*video.Frame
			for i, f := range frames {
				if !drop[i] {
					kept = append(kept, f)
				}
			}
			out, _, err := decodeTo(t, writeRaw(t, kept, info.FPSNum/info.FPSDen))
			if c.ok {
				if err != nil {
					t.Fatalf("%d of %d frames lost: %v", n, res.Frames, err)
				}
				checkOutput(t, out, want)
				return
			}
			var short *ShortError
			if !errors.As(err, &short) {
				t.Fatalf("%d of %d frames lost: %v, want a ShortError", n, res.Frames, err)
			}
			wantShort := []Shortfall{{Block: 0, Have: res.K + res.M - n*per, Need: res.K}}
			if !slices.Equal(short.Blocks, wantShort) {
				t.Errorf("short blocks %+v, want %+v", short.Blocks, wantShort)
			}
			if fs := filesUnder(t, out); len(fs) != 0 {
				t.Errorf("files written: %v", fs)
			}
		})
	}
}

// TestPartialRecordingsAddUp: two recordings that each show 60% of the loop, from either end,
// each fail alone and succeed together (DESIGN.md section 15, "Two partial recordings").
func TestPartialRecordingsAddUp(t *testing.T) {
	path, res, want := lossPlan(t, 20)
	frames, info := readFrames(t, video.NewRaw(), path)
	cut := res.Frames * 6 / 10
	a := writeRaw(t, frames[:cut], info.FPSNum/info.FPSDen)
	b := writeRaw(t, frames[res.Frames-cut:], info.FPSNum/info.FPSDen)
	for _, rec := range []string{a, b} {
		var short *ShortError
		if _, _, err := decodeTo(t, rec); !errors.As(err, &short) || len(short.Blocks) == 0 {
			t.Errorf("one recording alone: %v, want a ShortError naming blocks", err)
		}
	}
	out, dr, err := decodeTo(t, a, b)
	if err != nil {
		t.Fatalf("both recordings: %v", err)
	}
	checkOutput(t, out, want)
	if dr.Stats.FramesSeen != res.Frames {
		t.Errorf("%d frames seen of %d", dr.Stats.FramesSeen, res.Frames)
	}
}

// simCamera is the `ideal` preset with its camera at 1080p, which keeps the tests fast and still
// gives a 480p code about 8 camera pixels per cell.
func simCamera(t testing.TB) channel.Config { return preset(t, "ideal", 1920, 1080) }

// TestStartMidLoop films a recording that starts half-way through the loop and lasts a little
// more than one pass.
func TestStartMidLoop(t *testing.T) {
	in, want := writeInputs(t, 3, 20000)
	path, res := encodeRaw(t, profile480(in))
	period := time.Duration(res.Frames) * time.Second / 12
	c := simCamera(t)
	captures := int(1.25*period.Seconds()*float64(c.Timing.FPS)) + 2
	rec := film(t, path, 4, c, channel.Run{Start: period/2 + 7*time.Millisecond, Captures: captures, Seed: 5})
	out, dr, err := decodeTo(t, rec)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	checkOutput(t, out, want)
	t.Logf("F %d, %d captures, clean %.3f", res.Frames, dr.Stats.Captures, dr.Stats.CleanFraction)
}

// softNoise is the sensor noise, in 8-bit code values, at which one recording's soft bits are too
// weak for the inner code and two recordings' summed soft bits suffice (TestSoftBitsAddUp).
const softNoise = 30

// TestSoftBitsAddUp: two recordings of the whole loop, each too noisy to decode alone, decode
// together, because their soft bits are summed per code frame (DESIGN.md section 7, rule 7).
func TestSoftBitsAddUp(t *testing.T) {
	if raceEnabled {
		t.Skip("statistical; not in the race smoke subset")
	}
	in, want := writeInputs(t, 4, 6000)
	cfg := profile480(in)
	cfg.Modulation = 2
	path, res := encodeRaw(t, cfg)
	period := time.Duration(res.Frames) * time.Second / 12
	c := simCamera(t)
	c.Sensor.Noise = softNoise
	captures := int(period.Seconds()*float64(c.Timing.FPS)) + 1
	var recs []string
	for seed := range uint64(2) {
		recs = append(recs, film(t, path, 4, c, channel.Run{Captures: captures, Seed: 40 + seed}))
	}
	for _, r := range recs {
		var short *ShortError
		if _, _, err := decodeTo(t, r); !errors.As(err, &short) {
			t.Errorf("one recording alone: %v, want a ShortError", err)
		}
	}
	out, _, err := decodeTo(t, recs...)
	if err != nil {
		t.Fatalf("both recordings: %v", err)
	}
	checkOutput(t, out, want)
}

// TestOtherSessionIgnored: a recording that shows another transfer first locks onto that one
// (SECURITY.md T2) and ignores the frames of the second.
func TestOtherSessionIgnored(t *testing.T) {
	inA, wantA := writeInputs(t, 5, 3000)
	inB, _ := writeInputs(t, 6, 3000)
	pa, _ := encodeRaw(t, profile480(inA))
	pb, _ := encodeRaw(t, profile480(inB))
	fa, info := readFrames(t, video.NewRaw(), pa)
	fb, _ := readFrames(t, video.NewRaw(), pb)
	out, _, err := decodeTo(t, writeRaw(t, append(fa, fb...), info.FPSNum/info.FPSDen))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	checkOutput(t, out, wantA)
}

// TestNoCode: a recording with no code in it is short with no blocks known.
func TestNoCode(t *testing.T) {
	var frames []*video.Frame
	for range 4 {
		f := &video.Frame{W: 848, H: 480, Bits: 8, Y: make([]uint16, 848*480), Cb: make([]uint16, 424*240), Cr: make([]uint16, 424*240)}
		for i := range f.Y {
			f.Y[i] = 126
		}
		for i := range f.Cb {
			f.Cb[i], f.Cr[i] = 128, 128
		}
		frames = append(frames, f)
	}
	_, dr, err := decodeTo(t, writeRaw(t, frames, 12))
	var short *ShortError
	if !errors.As(err, &short) || len(short.Blocks) != 0 || dr != nil {
		t.Fatalf("got %v, %+v; want a ShortError with no blocks", err, dr)
	}
	if short.Error() == "" {
		t.Error("empty message")
	}
}

// TestEncodeConfig: invalid profiles and inputs are refused before anything is written.
func TestEncodeConfig(t *testing.T) {
	in, _ := writeInputs(t, 8, 100)
	cases := map[string]func(*EncodeConfig){
		"cell":           func(c *EncodeConfig) { c.Cell = 5 },
		"width":          func(c *EncodeConfig) { c.Width = 850 },
		"short":          func(c *EncodeConfig) { c.Height = 400 },
		"fps":            func(c *EncodeConfig) { c.FPS = 0 },
		"fast fps":       func(c *EncodeConfig) { c.FPS = 241 },
		"modulation":     func(c *EncodeConfig) { c.Modulation = 4 },
		"inner":          func(c *EncodeConfig) { c.Inner = 0 },
		"parity":         func(c *EncodeConfig) { c.Parity = 4 },
		"high parity":    func(c *EncodeConfig) { c.Parity = 101 },
		"no inputs":      func(c *EncodeConfig) { c.Inputs = nil },
		"missing input":  func(c *EncodeConfig) { c.Inputs = []string{filepath.Join(in, "nothing")} },
		"no output":      func(c *EncodeConfig) { c.Output = "" },
		"no backend":     func(c *EncodeConfig) { c.Backend = nil },
		"wide grid":      func(c *EncodeConfig) { c.Width = 8192 },
		"output in file": func(c *EncodeConfig) { c.Output = filepath.Join(in, "first.bin", "x.raw") },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			tmp := t.TempDir()
			c := profile480(in)
			c.Output, c.Backend, c.TempDir = filepath.Join(t.TempDir(), "x.raw"), video.NewRaw(), tmp
			edit(&c)
			if _, err := Encode(context.Background(), c); err == nil {
				t.Fatal("accepted")
			}
			if c.Output != "" {
				if _, err := os.Stat(c.Output); err == nil {
					t.Error("output written")
				}
			}
			if fs := filesUnder(t, tmp); len(fs) != 0 {
				t.Errorf("temporary files left: %v", fs)
			}
		})
	}
}

// TestDecodeConfig: a decode needs recordings, a backend and an output folder, and fails on a
// recording that cannot be read.
func TestDecodeConfig(t *testing.T) {
	ctx := context.Background()
	out := filepath.Join(t.TempDir(), "out")
	good := DecodeConfig{Recordings: []string{"x"}, Backend: video.NewRaw(), TempDir: t.TempDir(), Unpack: payload.UnpackOptions{OutputDir: out}}
	for name, edit := range map[string]func(*DecodeConfig){
		"no recordings": func(c *DecodeConfig) { c.Recordings = nil },
		"no backend":    func(c *DecodeConfig) { c.Backend = nil },
		"no output":     func(c *DecodeConfig) { c.Unpack.OutputDir = "" },
		"missing":       func(c *DecodeConfig) { c.Recordings = []string{filepath.Join(t.TempDir(), "none.raw")} },
	} {
		c := good
		edit(&c)
		if _, err := Decode(ctx, c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestDryRun plans the encode without writing a video or leaving temporary files.
func TestDryRun(t *testing.T) {
	in, _ := writeInputs(t, 9, 50000)
	c := profile480(in)
	c.DryRun, c.TempDir, c.Backend = true, t.TempDir(), video.NewRaw()
	c.Output = filepath.Join(t.TempDir(), "x.raw")
	res, err := Encode(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if res.Frames < 4 || res.StreamLen < 50000 || res.VideoBytes <= 0 || res.K < 1 || res.M < 1 || res.Blocks != 1 || res.Codewords != 29 {
		t.Errorf("result %+v", res)
	}
	if _, err := os.Stat(c.Output); err == nil {
		t.Error("video written")
	}
	if fs := filesUnder(t, c.TempDir); len(fs) != 0 {
		t.Errorf("temporary files left: %v", fs)
	}
}

// TestEvents: progress arrives from one goroutine at a time, each stage starts before it
// finishes, and the stages are the expected ones.
func TestEvents(t *testing.T) {
	in, want := writeInputs(t, 10, 20000)
	var busy atomic.Int32
	record := func(evs *[]Event) func(Event) {
		return func(e Event) {
			if busy.Add(1) != 1 {
				t.Error("progress called concurrently")
			}
			*evs = append(*evs, e)
			busy.Add(-1)
		}
	}
	var enc, dec []Event
	c := profile480(in)
	c.Progress = record(&enc)
	path, _ := encodeRaw(t, c)
	out := filepath.Join(t.TempDir(), "out")
	res, err := Decode(context.Background(), DecodeConfig{Recordings: []string{path}, Backend: video.NewRaw(), TempDir: t.TempDir(),
		Unpack: payload.UnpackOptions{OutputDir: out}, Progress: record(&dec)})
	if err != nil {
		t.Fatal(err)
	}
	checkOutput(t, out, want)
	check := func(name string, evs []Event, stages ...Stage) {
		started := map[Stage]bool{}
		seen := map[Stage]bool{}
		for _, e := range evs {
			if e.Time.IsZero() {
				t.Errorf("%s: event without a time", name)
			}
			switch e.Kind {
			case KindStarted:
				started[e.Stage] = true
			case KindFinished:
				if !started[e.Stage] {
					t.Errorf("%s: stage %d finished before it started", name, e.Stage)
				}
				seen[e.Stage] = true
			case KindStats:
				if e.Stats == nil {
					t.Errorf("%s: stats event without stats", name)
				}
			case KindWarning:
				if e.Warn == nil {
					t.Errorf("%s: warning event without a warning", name)
				}
			}
		}
		for _, s := range stages {
			if !seen[s] {
				t.Errorf("%s: stage %d never finished", name, s)
			}
		}
	}
	check("encode", enc, StagePack, StageProtect, StageRender, StageEncodeVideo)
	check("decode", dec, StageDecodeVideo, StageDemodulate, StageRepair, StageVerify, StageUnpack)
	var last *Stats
	for _, e := range dec {
		if e.Kind == KindStats {
			last = e.Stats
		}
	}
	if last == nil || last.BlocksReady != last.BlocksTotal || *last != res.Stats {
		t.Errorf("last stats %+v, result %+v", last, res.Stats)
	}
}

// TestCancel: a cancelled encode or decode stops with the context's error and leaves no temporary
// file, no video and no output file.
func TestCancel(t *testing.T) {
	in, _ := writeInputs(t, 11, 60000)
	for _, stage := range []Stage{StagePack, StageProtect, StageRender, StageEncodeVideo} {
		t.Run(fmt.Sprintf("encode-%d", stage), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c := profile480(in)
			c.Output, c.Backend, c.TempDir = filepath.Join(t.TempDir(), "x.raw"), video.NewRaw(), t.TempDir()
			c.Progress = func(e Event) {
				if e.Stage == stage && e.Kind == KindStarted {
					cancel()
				}
			}
			if _, err := Encode(ctx, c); !errors.Is(err, context.Canceled) {
				t.Fatalf("got %v", err)
			}
			if _, err := os.Stat(c.Output); err == nil {
				t.Error("video written")
			}
			if fs := filesUnder(t, c.TempDir); len(fs) != 0 {
				t.Errorf("temporary files left: %v", fs)
			}
			if fs := filesUnder(t, filepath.Dir(c.Output)); len(fs) != 0 {
				t.Errorf("files beside the output: %v", fs)
			}
		})
	}
	path, _ := encodeRaw(t, profile480(in))
	for _, stage := range []Stage{StageDecodeVideo, StageDemodulate, StageRepair, StageUnpack} {
		t.Run(fmt.Sprintf("decode-%d", stage), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tmp, out := t.TempDir(), filepath.Join(t.TempDir(), "out")
			_, err := Decode(ctx, DecodeConfig{Recordings: []string{path}, Backend: video.NewRaw(), TempDir: tmp,
				Unpack: payload.UnpackOptions{OutputDir: out}, Progress: func(e Event) {
					if e.Stage == stage && e.Kind == KindStarted {
						cancel()
					}
				}})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("got %v", err)
			}
			if fs := filesUnder(t, tmp); len(fs) != 0 {
				t.Errorf("temporary files left: %v", fs)
			}
			if fs := filesUnder(t, out); len(fs) != 0 {
				t.Errorf("output files: %v", fs)
			}
		})
	}
}
