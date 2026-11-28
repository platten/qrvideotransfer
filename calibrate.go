package qrvt

import (
	"context"
	"errors"
	"fmt"

	"github.com/platten/qrvideotransfer/internal/calib"
)

// PlanCalibration reports, without doing any work, the video [CalibrationVideo] would make for
// the monitor resolution of o (DESIGN.md section 13): its steps, length and the expected time to
// make it. It returns an error wrapping ErrNoCalibrationPlan when no step fits the resolution.
func PlanCalibration(o CalibrationOptions) (*CalibrationPlan, error) {
	p, err := calib.NewPlan(o.Width, o.Height)
	if err != nil {
		return nil, fmt.Errorf("%w: %dx%d", ErrNoCalibrationPlan, o.Width, o.Height)
	}
	return &CalibrationPlan{Width: p.Width, Height: p.Height, Steps: len(p.Segments), Frames: p.VideoFrames(),
		Duration: p.Duration(), Estimate: p.Estimate(o.Workers)}, nil
}

// CalibrationVideo writes the calibration video for the monitor resolution of o (DESIGN.md section
// 13): a 60 fps video stepping through display rates, cell sizes and modes, each step 4 seconds of
// code frames whose data cells hold their scramble bits alone. Temporary files go in a folder of
// their own under TempDir, removed on return, and the video is written beside the output and
// renamed into place, so a cancelled or failed call leaves neither. It returns an error wrapping
// ErrNoCalibrationPlan when no step fits the resolution.
func CalibrationVideo(ctx context.Context, o CalibrationOptions) error {
	if o.Output == "" {
		return errors.New("qrvt: no output file")
	}
	if _, err := PlanCalibration(o); err != nil {
		return err
	}
	be, err := backend(o.Backend, false)
	if err != nil {
		return err
	}
	return calib.Make(ctx, calib.MakeConfig{Output: o.Output, Width: o.Width, Height: o.Height, Backend: be,
		Workers: o.Workers, TempDir: o.TempDir, Progress: progress(o.Progress)})
}
