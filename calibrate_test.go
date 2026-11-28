package qrvt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/frame"
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
