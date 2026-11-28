package calib

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/channel"
	"github.com/platten/qrvideotransfer/internal/fec/ldpc"
	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/prng"
	"github.com/platten/qrvideotransfer/internal/render"
	"github.com/platten/qrvideotransfer/internal/video"
)

// writeRaw writes frames to a new raw-frame file at fps.
func writeRaw(t *testing.T, frames []*video.Frame, fps int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rec.raw")
	f0 := frames[0]
	e, err := video.NewRaw().NewEncoder(context.Background(), path, video.EncodeConfig{Width: f0.W, Height: f0.H, FPS: fps, BitDepth: max(int(f0.Bits), 8)})
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range frames {
		f.Index = i
		if err := e.WriteFrame(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// makeRaw makes the calibration video of a w x h monitor with the raw backend, keeping only the
// segments keep accepts (all when nil), and returns its frames.
func makeRaw(t *testing.T, w, h int, keep func(Segment) bool) []*video.Frame {
	t.Helper()
	out := filepath.Join(t.TempDir(), "cal.raw")
	be := video.NewRaw()
	if err := Make(context.Background(), MakeConfig{Output: out, Width: w, Height: h, Backend: be, TempDir: t.TempDir(), keep: keep}); err != nil {
		t.Fatal(err)
	}
	return readAll(t, be, out)
}

// analyzeRaw analyses raw recordings.
func analyzeRaw(t *testing.T, recs ...string) (*Analysis, error) {
	t.Helper()
	return Analyze(context.Background(), AnalyzeConfig{Recordings: recs, Backend: video.NewRaw()})
}

// stepOf returns the analysis's step of a setting.
func stepOf(t *testing.T, a *Analysis, s Setting) Step {
	t.Helper()
	for _, st := range a.Steps {
		if st.Setting == s {
			return st
		}
	}
	t.Fatalf("no step %+v", s)
	return Step{}
}

// TestAnalyzeFilmed is the always-on filmed run (DESIGN.md section 15): a 1280 x 720 monitor's
// calibration video, its 30 fps steps of 4- and 8-pixel cells in black and white and four greys,
// 4 code frames each (32 video frames at 60 fps), filmed once through by `ideal` at 1920 x 1080 and
// 60 fps, 34 captures. The plan is recovered and the recommendation is exactly the rules' choice:
// four greys `fast` on 4-pixel cells (8,480 bytes per frame, the fastest step), not established, so
// only because its segment passed with margin: measured on the user's device.
func TestAnalyzeFilmed(t *testing.T) {
	shortSlots(t, 4)
	frames := makeRaw(t, 1280, 720, func(s Segment) bool {
		return s.Rate == 30 && (s.Cell == 4 || s.Cell == 8) && s.Mode != ModeColor
	})
	if len(frames) != 32 {
		t.Fatalf("%d video frames", len(frames))
	}
	c, err := channel.Preset("ideal")
	if err != nil {
		t.Fatal(err)
	}
	c = c.Resize(1920, 1080)
	rec := filmRaw(t, frames, c, 34)
	a, err := analyzeRaw(t, rec)
	if err != nil {
		t.Fatal(err)
	}
	if a.Plan.Width != 1280 || a.Plan.Height != 720 || a.Captures != 34 {
		t.Fatalf("plan %dx%d, %d captures", a.Plan.Width, a.Plan.Height, a.Captures)
	}
	for _, s := range a.Steps {
		t.Logf("%+v: %d of %d frames, %d of %d codewords, %.2f px/cell, passed %v, margin %v, %.0f B/s", s.Setting, s.Seen, s.Frames,
			s.Decoded, s.Codewords, s.Pixels, s.Passed, s.Margin, s.BytesPerSec)
	}
	r := a.Recommendation
	if want := (Setting{Rate: 30, Cell: 4, Mode: ModeGray4, Inner: Fast}); r.Setting != want || !r.Measured || r.GridX != 320 {
		t.Errorf("recommended %+v (measured %v), want %+v measured on the device", r.Setting, r.Measured, want)
	}
	if s := stepOf(t, a, Setting{30, 4, ModeBW, Normal}); !s.Passed || s.Standing != EstablishedAt {
		t.Errorf("black and white normal at cell 4: %+v", s)
	}
}

// filmRaw films looping 60 fps frames with camera c into a raw recording of n captures.
func filmRaw(t *testing.T, frames []*video.Frame, c channel.Config, n int) string {
	t.Helper()
	period := time.Second / VideoFPS
	for i, f := range frames {
		f.Index, f.PTS = i, time.Duration(i)*period
	}
	in := channel.Input{Frames: frames, Period: time.Duration(len(frames)) * period, Cell: 4}
	out := filepath.Join(t.TempDir(), "rec.raw")
	var e video.Encoder
	err := channel.Simulate(context.Background(), in, c, channel.Run{Captures: n, Seed: 0x5eed, Workers: runtime.GOMAXPROCS(0)},
		func(f *video.Frame, _ *channel.Truth) error {
			if e == nil {
				var err error
				if e, err = video.NewRaw().NewEncoder(context.Background(), out, video.EncodeConfig{Width: f.W, Height: f.H,
					FPS: c.Timing.FPS, BitDepth: int(f.Bits)}); err != nil {
					return err
				}
			}
			return e.WriteFrame(f)
		})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}

// Several recordings add up per segment: each of two shows half of the segment's code frames,
// too few alone (50% of the frames seen), and together they pass; the same recording twice adds
// nothing. The recordings are the code video's own frames, one capture of each code frame, of an
// 848 x 480 monitor's 12 fps black and white step with 4 code frames, at a camera's 60 fps so that
// condition 1 holds. The run of both is made with a soft-bit cap of one frame's, so that the frame
// seen longest ago is decoded early, with the same outcome.
func TestAnalyzeAddsUp(t *testing.T) {
	shortSlots(t, 4)
	frames := makeRaw(t, 848, 480, func(s Segment) bool { return s.Rate == 12 && s.Mode == ModeBW })
	if len(frames) != 20 {
		t.Fatalf("%d video frames", len(frames))
	}
	a, b := writeRaw(t, []*video.Frame{frames[0], frames[5]}, 60), writeRaw(t, []*video.Frame{frames[10], frames[15]}, 60)
	s := Setting{12, 4, ModeBW, Normal}
	an, err := analyzeRaw(t, a, a)
	var e *Error
	if !errors.As(err, &e) || e.Reason != NothingPassed || an == nil {
		t.Fatalf("half the frames: %v", err)
	}
	if st := stepOf(t, an, s); st.Seen != 2 || st.Decoded != st.Codewords || st.Passed {
		t.Errorf("half the frames, twice: %+v", st)
	}
	an, err = Analyze(context.Background(), AnalyzeConfig{Recordings: []string{a, b}, Backend: video.NewRaw(), softCap: 1})
	if err != nil {
		t.Fatal(err)
	}
	if st := stepOf(t, an, s); st.Seen != 4 || st.Frames != 4 || st.Decoded != st.Codewords || !st.Passed {
		t.Errorf("both: %+v", st)
	}
}

// transferFrames renders frames of a transfer with session and F frames, inner code 2, on grid
// gx x gy at cell 4 in mode, at frame indices idxs: real codewords of random shards, including
// codeword 0, so that no descriptor decodes and no codeword is the all-zero one. With session
// 0x5332 and F 4,896 its headers match the calibration pattern.
func transferFrames(t *testing.T, session uint16, f uint32, gx, gy int, mode uint8, idxs []uint32) []*video.Frame {
	t.Helper()
	r, err := render.New(gx, gy, 4, mode)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := ldpc.New(InnerCode)
	g := prng.SplitMix64{State: 7}
	var out []*video.Frame
	for _, idx := range idxs {
		h := frame.Header{Version: frame.Version, Session: session, FrameIndex: idx, TotalFrames: f,
			GridX: uint16(gx), GridY: uint16(gy), Modulation: mode, InnerCode: InnerCode}
		m, err := frame.NewMapping(h)
		if err != nil {
			t.Fatal(err)
		}
		cws := make([][]byte, m.CodewordsPerFrame())
		for i := range cws {
			sh := make([]byte, code.ShardSize())
			for j := range sh {
				sh[j] = byte(g.Next())
			}
			cws[i] = make([]byte, (code.N()+7)/8)
			code.Encode(cws[i], sh)
		}
		sym := make([]uint8, len(r.Layout().DataCells()))
		if err := render.NewMapper(m).Symbols(sym, idx, cws); err != nil {
			t.Fatal(err)
		}
		w, hh := r.Size()
		vf := &video.Frame{Y: make([]uint16, w*hh), Cb: make([]uint16, w*hh/4), Cr: make([]uint16, w*hh/4)}
		if err := r.Render(vf, h, sym); err != nil {
			t.Fatal(err)
		}
		out = append(out, vf)
	}
	return out
}

// slotOfSetting returns the slot of a rate, cell and mode.
func slotOfSetting(t *testing.T, rate, cell int, mode uint8) Slot {
	t.Helper()
	for _, s := range slots {
		if s.Rate == rate && s.Cell == cell && s.Mode == mode {
			return s
		}
	}
	t.Fatal("no slot")
	return Slot{}
}

// Required test 3 (designer's ruling, M4): a transfer whose headers match the calibration pattern
// and whose descriptor is destroyed reads as a calibration in which no segment passes: no
// recommendation, and the error is NothingPassed, which says the input may be a transfer. Its
// frames lie in the 12 fps four-grey slot of 4-pixel cells of an 848 x 480 monitor.
func TestAnalyzeTransferPattern(t *testing.T) {
	s := slotOfSetting(t, 12, 4, ModeGray4)
	frames := transferFrames(t, Session, TotalFrames, 212, 120, ModeGray4, []uint32{s.FirstIndex, s.FirstIndex + 1})
	a, err := analyzeRaw(t, writeRaw(t, frames, 12))
	var e *Error
	if !errors.As(err, &e) || e.Reason != NothingPassed || e.Calibration != 2 || errors.Is(err, ErrNoCalibration) {
		t.Fatalf("%v (%+v)", err, e)
	}
	if a == nil || a.Recommendation.Setting != (Setting{}) {
		t.Errorf("a recommendation: %+v", a)
	}
	for _, st := range a.Steps {
		if st.Decoded != 0 || st.Passed {
			t.Errorf("%+v decoded", st)
		}
	}
	if !strings.Contains(err.Error(), "no step of the calibration video passed") {
		t.Errorf("message %q", err)
	}
}

// Required test 4: headers recovering to more than one video size are not a calibration video.
// A fixed 212 x 120 grid in the four-grey slots of 4- and 8-pixel cells recovers to 848 x 480 and
// to 1696 x 960.
func TestAnalyzeMixedSizes(t *testing.T) {
	a, b := slotOfSetting(t, 12, 4, ModeGray4), slotOfSetting(t, 12, 8, ModeGray4)
	frames := transferFrames(t, Session, TotalFrames, 212, 120, ModeGray4, []uint32{a.FirstIndex, b.FirstIndex})
	_, err := analyzeRaw(t, writeRaw(t, frames, 12))
	var e *Error
	if !errors.As(err, &e) || e.Reason != MixedSizes || len(e.Sizes) != 2 || !errors.Is(err, ErrNoCalibration) {
		t.Fatalf("%v (%+v)", err, e)
	}
	if e.Sizes[0] != (Size{848, 480}) || e.Sizes[1] != (Size{1696, 960}) || !strings.Contains(err.Error(), "2 video sizes") {
		t.Errorf("%+v: %v", e.Sizes, err)
	}
}

// Required test 2 at this level: a transfer's headers are not a calibration video's
// (NotCalibration), and noise shows no code (NoCode).
func TestAnalyzeNotCalibration(t *testing.T) {
	frames := transferFrames(t, 0x1234, 8, 212, 120, ModeGray4, []uint32{0, 1})
	_, err := analyzeRaw(t, writeRaw(t, frames, 12))
	var e *Error
	if !errors.As(err, &e) || e.Reason != NotCalibration || e.HeadersRead != 2 || e.Calibration != 0 ||
		!strings.Contains(err.Error(), "not the calibration video's") {
		t.Errorf("a transfer: %v (%+v)", err, e)
	}
	g := prng.SplitMix64{State: 3}
	var noise []*video.Frame
	for range 3 {
		f := &video.Frame{W: 480, H: 480, Bits: 8, Y: make([]uint16, 480*480), Cb: make([]uint16, 240*240), Cr: make([]uint16, 240*240)}
		for _, p := range [][]uint16{f.Y, f.Cb, f.Cr} {
			for i := range p {
				p[i] = uint16(g.Next() & 0xff)
			}
		}
		noise = append(noise, f)
	}
	_, err = analyzeRaw(t, writeRaw(t, noise, 30))
	if !errors.As(err, &e) || e.Reason != NoCode || e.Captures != 3 || !strings.Contains(err.Error(), "no code in any of 3 captures") {
		t.Errorf("noise: %v", err)
	}
}
