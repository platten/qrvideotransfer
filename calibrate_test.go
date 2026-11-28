package qrvt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/calib"
	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/pipeline"
	"github.com/platten/qrvideotransfer/internal/prng"
	"github.com/platten/qrvideotransfer/internal/render"
	"github.com/platten/qrvideotransfer/internal/video"
	"github.com/platten/qrvideotransfer/internal/vision"
)

// PlanCalibration reports the plan of DESIGN.md section 13 before any work, and refuses a monitor
// resolution no step fits.
func TestPlanCalibration(t *testing.T) {
	p, err := PlanCalibration(CalibrationOptions{Width: 1920, Height: 1080, Workers: 4})
	if err != nil || p.Steps != 26 || p.Frames != 26*240 || p.Duration != 104*time.Second || p.Estimate <= 0 {
		t.Fatalf("%+v, %v", p, err)
	}
	if q, _ := PlanCalibration(CalibrationOptions{Width: 1920, Height: 1080, Workers: 1}); q.Estimate <= p.Estimate {
		t.Errorf("one worker %v, four %v", q.Estimate, p.Estimate)
	}
	for _, sz := range [][2]int{{400, 300}, {1366, 768}, {0, 0}} {
		if _, err := PlanCalibration(CalibrationOptions{Width: sz[0], Height: sz[1]}); !errors.Is(err, ErrNoCalibrationPlan) {
			t.Errorf("%v: %v", sz, err)
		}
	}
}

// CalibrationVideo refuses what it cannot make before writing anything.
func TestCalibrationVideoRefuses(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "cal.mp4")
	ctx := context.Background()
	if err := CalibrationVideo(ctx, CalibrationOptions{Width: 1920, Height: 1080, Backend: testSpec}); err == nil {
		t.Error("no output accepted")
	}
	if err := CalibrationVideo(ctx, CalibrationOptions{Output: out, Width: 400, Height: 300, Backend: testSpec}); !errors.Is(err, ErrNoCalibrationPlan) {
		t.Errorf("400x300: %v", err)
	}
	if err := CalibrationVideo(ctx, CalibrationOptions{Output: out, Width: 1920, Height: 1080, Backend: "/nonexistent/ffmpeg"}); err == nil {
		t.Error("a missing engine accepted")
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("left %v", left)
	}
}

// TestCalibrationVideoEmbedded makes the whole 848 x 480 calibration video with the embedded
// engine (long only: about 9 s on 16 threads) and checks it as a player sees it: 60 fps, every
// segment's frames, and the header of the first frame of each segment read back by the vision
// chain's header reader.
func TestCalibrationVideoEmbedded(t *testing.T) {
	if os.Getenv("QRVT_LONG_TESTS") != "1" {
		t.Skip("long test: QRVT_LONG_TESTS=1")
	}
	out := filepath.Join(t.TempDir(), "cal.mp4")
	if err := CalibrationVideo(context.Background(), CalibrationOptions{Output: out, Width: 848, Height: 480, TempDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	be, err := video.NewEmbedded(video.Options{})
	if err != nil {
		t.Fatal(err)
	}
	frames := readFrames(t, be, out)
	p, _ := PlanCalibration(CalibrationOptions{Width: 848, Height: 480})
	if len(frames) != p.Frames {
		t.Fatalf("%d frames, the plan has %d", len(frames), p.Frames)
	}
	var det vision.FinderDetector
	for v := 0; v < len(frames); v += 240 {
		q, ok := det.Detect(frames[v], nil)
		if !ok {
			t.Fatalf("frame %d: no code", v)
		}
		hs, _ := vision.SlotReader{}.ReadReason(frames[v], q)
		if len(hs) == 0 || hs[0].Session != 0x5332 || hs[0].TotalFrames != 4896 || hs[0].InnerCode != 2 || hs[0].Version != frame.Version {
			t.Errorf("frame %d: headers %+v", v, hs)
		}
	}
}

// calibrationFrames renders the first n code frames of the first segment of the w x h monitor's
// calibration video, as calib.Make does: FORMAT.md frames whose codewords are all zero.
func calibrationFrames(t *testing.T, w, h, n int) []*video.Frame {
	t.Helper()
	p, err := calib.NewPlan(w, h)
	if err != nil {
		t.Fatal(err)
	}
	s := p.Segments[0]
	r, err := render.New(s.GridX, s.GridY, s.Cell, s.Mode)
	if err != nil {
		t.Fatal(err)
	}
	var out []*video.Frame
	for k := range n {
		idx := s.FirstIndex + uint32(k)
		m, err := frame.NewMapping(s.Header(idx))
		if err != nil {
			t.Fatal(err)
		}
		cws := make([][]byte, m.CodewordsPerFrame())
		for i := range cws {
			cws[i] = make([]byte, (m.Sent()+7)/8)
		}
		sym := make([]uint8, len(r.Layout().DataCells()))
		if err := render.NewMapper(m).Symbols(sym, idx, cws); err != nil {
			t.Fatal(err)
		}
		f := &video.Frame{Y: make([]uint16, w*h), Cb: make([]uint16, w*h/4), Cr: make([]uint16, w*h/4)}
		if err := r.Render(f, s.Header(idx), sym); err != nil {
			t.Fatal(err)
		}
		out = append(out, f)
	}
	return out
}

// Required test 1 (designer's ruling, M4): decode given the calibration video, or a recording of
// it, falls short as any recording without a descriptor does (exit 3, NotEnoughDataError), with
// Calibration set from calib.Recover on the headers it read, so that the guidance points to qrvt
// calibrate read. Inspect does the same. 4 code frames of the 848 x 480 video.
func TestDecodeCalibrationVideo(t *testing.T) {
	rec := writeRawFrames(t, calibrationFrames(t, 848, 480, 4), 60)
	_, _, err := decodeTest(t, DecodeOptions{Recordings: []string{rec}})
	var short *NotEnoughDataError
	if !errors.As(err, &short) || !short.Calibration || short.Descriptor || short.HeadersRead != 4 {
		t.Fatalf("decode: %v (%+v)", err, short)
	}
	_, err = Inspect(context.Background(), InspectOptions{Path: rec, Backend: testSpec})
	if !errors.As(err, &short) || !short.Calibration {
		t.Errorf("inspect: %v (%+v)", err, short)
	}
	// A transfer's shortfall is not a calibration's.
	err = convertShort(&pipeline.ShortError{HeadersRead: 1, Headers: []frame.Header{{Version: 1, Session: 7, TotalFrames: 8,
		GridX: 212, GridY: 120, Modulation: 2, InnerCode: 2}}})
	if !errors.As(err, &short) || short.Calibration {
		t.Errorf("a transfer's headers: %+v", short)
	}
}

// calibrationHeaders accepts only headers that all recover, to one size.
func TestCalibrationHeaders(t *testing.T) {
	p, _ := calib.NewPlan(848, 480)
	q, _ := calib.NewPlan(1696, 960)
	a, b := p.Segments[0].Header(0), q.Segments[len(q.Segments)-1].Header(q.Segments[len(q.Segments)-1].FirstIndex)
	if !calibrationHeaders([]frame.Header{a, a}) || calibrationHeaders(nil) || calibrationHeaders([]frame.Header{a, b}) {
		t.Error("calibrationHeaders")
	}
	bad := a
	bad.Session++
	if calibrationHeaders([]frame.Header{a, bad}) {
		t.Error("a header that does not recover")
	}
}

// Required test 2: calibrate read given a transfer, here a small transfer's own code video, finds
// no calibration video: CalibrationTransfer, whose guidance (internal/cli) points to qrvt decode.
func TestAnalyzeCalibrationTransfer(t *testing.T) {
	in, _ := payloadDir(t, 3, 300)
	path, _ := encodeTest(t, EncodeOptions{Inputs: []string{in}})
	_, err := AnalyzeCalibration(context.Background(), AnalyzeOptions{Recordings: []string{path}, Backend: testSpec})
	var ce *CalibrationError
	if !errors.As(err, &ce) || ce.Reason != CalibrationTransfer || ce.HeadersRead == 0 || ce.Calibration != 0 {
		t.Fatalf("%v (%+v)", err, ce)
	}
}

// analyzeNoiseAlloc is the allocation ceiling of calibrate read on the small noise recording: as
// generous as inspect's (inspectNoiseAlloc).
const analyzeNoiseAlloc = 128 << 20

// AnalyzeCalibration on noise (SECURITY.md section 9.5, the always-on case; the 4K bound is
// internal/calib's long TestAnalyzeNoiseResources): 3 captures at 480 x 480 give the typed error,
// CalibrationNoCode, nothing is written in the temporary folder or beside the recording, and the
// allocations stay under analyzeNoiseAlloc.
func TestAnalyzeCalibrationNoise(t *testing.T) {
	g := prng.SplitMix64{State: 9}
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
	rec := writeRawFrames(t, noise, 30)
	tmp := t.TempDir()
	for _, v := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(v, tmp)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	r, err := AnalyzeCalibration(context.Background(), AnalyzeOptions{Recordings: []string{rec}, Backend: testSpec})
	runtime.ReadMemStats(&after)
	var ce *CalibrationError
	if !errors.As(err, &ce) || r != nil || ce.Reason != CalibrationNoCode || ce.Captures != 3 {
		t.Errorf("noise: %+v, %v", r, err)
	}
	if left, err := os.ReadDir(tmp); err != nil || len(left) != 0 {
		t.Errorf("the temporary folder holds %v (%v)", left, err)
	}
	if beside, err := os.ReadDir(filepath.Dir(rec)); err != nil || len(beside) != 1 {
		t.Errorf("beside the recording: %v (%v)", beside, err)
	}
	alloc := after.TotalAlloc - before.TotalAlloc
	t.Logf("3 noise captures at 480 x 480: %.1f MiB allocated", float64(alloc)/(1<<20))
	if alloc > analyzeNoiseAlloc {
		t.Errorf("%d bytes allocated, ceiling %d", alloc, analyzeNoiseAlloc)
	}
	if _, err := AnalyzeCalibration(context.Background(), AnalyzeOptions{}); err == nil {
		t.Error("no recordings accepted")
	}
	if _, err := AnalyzeCalibration(context.Background(), AnalyzeOptions{Recordings: []string{rec}, Backend: "/nonexistent/ffmpeg"}); err == nil {
		t.Error("a missing engine accepted")
	}
}

// AnalyzeCalibration turns calib's analysis into the root types: the profile at the monitor's
// size, the encode command, the steps and the label. calib.Analyze is replaced by a canned
// analysis here; its own tests film the video.
func TestAnalyzeCalibrationConverts(t *testing.T) {
	m := calib.Measurement{Setting: calib.Setting{Rate: 30, Cell: 4, Mode: calib.ModeGray4, Inner: calib.Fast}, GridX: 320, GridY: 180,
		Pixels: 5.4, Frames: 4, Seen: 4, Codewords: 216, Decoded: 216}
	a := &calib.Analysis{Plan: calib.Plan{Width: 1280, Height: 720}, Steps: []calib.Step{{Measurement: m, Passed: true, Margin: true,
		Standing: calib.NotEstablished, BytesPerSec: 152640}},
		Recommendation: calib.Recommendation{Measurement: m, BytesPerSec: 152640, Measured: true},
		Warnings:       []pipeline.Warning{{Code: calib.WarnClipping, Text: "clipped"}}}
	defer func(f func(context.Context, calib.AnalyzeConfig) (*calib.Analysis, error)) { analyze = f }(analyze)
	analyze = func(context.Context, calib.AnalyzeConfig) (*calib.Analysis, error) { return a, nil }
	r, err := AnalyzeCalibration(context.Background(), AnalyzeOptions{Recordings: []string{"x"}, Backend: testSpec})
	if err != nil {
		t.Fatal(err)
	}
	want := Profile{Width: 1280, Height: 720, Cell: 4, FPS: 30, Modulation: Gray4, Protection: Fast, ParityPercent: 20}
	if r.Profile != want || !r.Measured || r.BytesPerSec != 152640 || len(r.Steps) != 1 || r.Warnings[0].Code != WarnClipping ||
		!strings.Contains(r.Command, "--resolution 1280x720 --cell 4 --fps 30 --mode gray4 --protection fast") {
		t.Errorf("%+v", r)
	}
	if s := r.Steps[0]; s.FPS != 30 || s.Protection != Fast || s.Decoded != 216 || !s.Margin || s.Established || s.PixelsPerCell != 5.4 {
		t.Errorf("step %+v", s)
	}
	analyze = func(context.Context, calib.AnalyzeConfig) (*calib.Analysis, error) {
		return a, &calib.Error{Reason: calib.NothingPassed, Calibration: 9, Sizes: []calib.Size{{W: 1280, H: 720}}}
	}
	_, err = AnalyzeCalibration(context.Background(), AnalyzeOptions{Recordings: []string{"x"}, Backend: testSpec})
	var ce *CalibrationError
	// The analysis's warnings, which may explain why nothing passed, ride on the error.
	if !errors.As(err, &ce) || ce.Reason != CalibrationNothingPassed || ce.Calibration != 9 || ce.Sizes != 1 ||
		len(ce.Warnings) != 1 || ce.Warnings[0].Code != WarnClipping {
		t.Errorf("%v (%+v)", err, ce)
	}
	analyze = func(context.Context, calib.AnalyzeConfig) (*calib.Analysis, error) { return nil, context.Canceled }
	if _, err = AnalyzeCalibration(context.Background(), AnalyzeOptions{Recordings: []string{"x"}, Backend: testSpec}); !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
}
