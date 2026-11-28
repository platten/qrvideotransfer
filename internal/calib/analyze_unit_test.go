package calib

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/video"
)

// collect returns an analysis whose warnings are gathered in *got.
func collect(got *[]string) *analysis {
	return &analysis{pts: map[int]*ptsSpan{}, warn: func(code, _ string) { *got = append(*got, code) }}
}

// The recording-wide warnings: clipping over ClipWarn, and frames dropped over DropWarn of a
// recording's frames at the probe's rate; none at or below them.
func TestAnalyzeWarnings(t *testing.T) {
	var got []string
	a := collect(&got)
	a.located, a.clipped = 10, 0.6 // 6% of patches clip on average
	a.pts[0] = &ptsSpan{first: 0, last: 99 * time.Second / 30, n: 90}
	a.warnings([]video.ClipInfo{{FPSNum: 30, FPSDen: 1}, {FPSNum: 0}})
	if strings.Join(got, ",") != WarnClipping+","+WarnFramesDropped {
		t.Errorf("warnings %v", got)
	}
	got = nil
	a = collect(&got)
	a.located, a.clipped = 10, 0.5
	a.pts[0] = &ptsSpan{first: 0, last: 99 * time.Second / 30, n: 100}
	a.warnings([]video.ClipInfo{{FPSNum: 30, FPSDen: 1}})
	if len(got) != 0 {
		t.Errorf("warnings %v at the thresholds", got)
	}
}

// Downscaled: 4-pixel cells failing with every code where 8-pixel cells pass, at enough camera
// pixels per cell; not when the 4-pixel cells had too few pixels, or pass with some code.
func TestAnalyzeDownscaled(t *testing.T) {
	step := func(cell int, inner uint8, px float64, passed bool) Step {
		return Step{Measurement: Measurement{Setting: Setting{Rate: 12, Cell: cell, Mode: ModeBW, Inner: inner}, Pixels: px}, Passed: passed}
	}
	for _, c := range []struct {
		steps []Step
		want  bool
	}{
		{[]Step{step(4, Robust, 5, false), step(4, Normal, 5, false), step(8, Robust, 10, true)}, true},
		{[]Step{step(4, Robust, 3, false), step(8, Robust, 6, true)}, false},
		{[]Step{step(4, Robust, 5, false), step(4, Fast, 5, true), step(8, Robust, 10, true)}, false},
		{[]Step{step(4, Robust, 5, false), step(8, Robust, 10, false)}, false},
	} {
		var got []string
		collect(&got).downscaled(c.steps, FloorHandheld)
		if (len(got) == 1 && got[0] == WarnDownscaled) != c.want || len(got) > 1 {
			t.Errorf("%+v: %v", c.steps, got)
		}
	}
}

// The camera the recordings show: the lowest frame rate and bitrate, H.264 if any is, and HLG's
// floor if any is HLG.
func TestCamera(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.WriteFile(a, make([]byte, 1000), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, make([]byte, 500), 0o600); err != nil {
		t.Fatal(err)
	}
	c := camera([]string{a, b}, []video.ClipInfo{
		{Codec: "hevc", Width: 3840, FPSNum: 60, FPSDen: 1, Duration: time.Second, Transfer: "arib-std-b67"},
		{Codec: "h264", Width: 1920, FPSNum: 30000, FPSDen: 1001, Duration: time.Second},
	})
	if c.FPS < 29.9 || c.FPS > 30 || c.Bitrate != 4000 || !c.H264 || c.Floor != FloorHLG || c.Width != 3840 {
		t.Errorf("%+v", c)
	}
	if c := camera([]string{a}, []video.ClipInfo{{Codec: "hevc", FPSNum: 60, FPSDen: 1}}); c.Bitrate != 0 || c.H264 || c.Floor != FloorHandheld {
		t.Errorf("no duration: %+v", c)
	}
}

func TestErrorText(t *testing.T) {
	e := &Error{Reason: NoHeader, Located: 2, Captures: 3}
	if !strings.Contains(e.Error(), "found in 2 of 3 captures, but no frame header") || !errors.Is(e, ErrNoCalibration) {
		t.Errorf("%v", e)
	}
	if e := (&Error{Reason: NoCode, Captures: 3}); !strings.Contains(e.Error(), "no code in any of 3 captures") {
		t.Errorf("%v", e)
	}
	if neg(-128) != 127 || neg(5) != -5 {
		t.Error("neg")
	}
}

// Analyze passes on what Scan refuses.
func TestAnalyzeScanError(t *testing.T) {
	if _, err := Analyze(context.Background(), AnalyzeConfig{Backend: video.NewRaw()}); err == nil {
		t.Error("no recordings accepted")
	}
}
