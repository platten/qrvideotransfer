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

// analyze is calib.Analyze; tests replace it.
var analyze = calib.Analyze

var (
	calModes = map[uint8]string{calib.ModeBW: "bw", calib.ModeGray4: "gray4", calib.ModeColor: "color"}
	calProt  = map[uint8]string{calib.Fast: "fast", calib.Normal: "normal", calib.Robust: "robust"}
)

// AnalyzeCalibration measures recordings of the calibration video and recommends a profile
// (DESIGN.md section 13): every step's frames are summed across the captures that show them and
// across the recordings, decoded with each protection level, and the fastest step that passes,
// is feasible for the camera and is established (or passed with margin: Measured) is chosen. When
// it can recommend nothing it returns a *CalibrationError saying why.
func AnalyzeCalibration(ctx context.Context, o AnalyzeOptions) (*Recommendation, error) {
	if len(o.Recordings) == 0 {
		return nil, errors.New("qrvt: no recordings")
	}
	be, err := backend(o.Backend, o.UnsandboxedHEVC)
	if err != nil {
		return nil, err
	}
	a, err := analyze(ctx, calib.AnalyzeConfig{Recordings: o.Recordings, Backend: be, Workers: o.Workers,
		Progress: progress(o.Progress)})
	var ce *calib.Error
	if errors.As(err, &ce) {
		return nil, &CalibrationError{Reason: CalibrationReason(ce.Reason), Captures: ce.Captures, Located: ce.Located,
			HeadersRead: ce.HeadersRead, Calibration: ce.Calibration, Sizes: len(ce.Sizes)}
	}
	if err != nil {
		return nil, convertError(err)
	}
	r := a.Recommendation
	rec := &Recommendation{Measured: r.Measured, BytesPerSec: r.BytesPerSec, Warnings: convertWarnings(a.Warnings),
		Profile: Profile{Width: a.Plan.Width, Height: a.Plan.Height, Cell: r.Cell, FPS: r.Rate, Modulation: Modulation(r.Mode),
			Protection: Protection(r.Inner), ParityPercent: calib.ParityPercent}}
	rec.Command = fmt.Sprintf("qrvt encode <files> -o transfer.mp4 --resolution %dx%d --cell %d --fps %d --mode %s --protection %s",
		a.Plan.Width, a.Plan.Height, r.Cell, rec.Profile.FPS, calModes[r.Mode], calProt[r.Inner])
	for _, s := range a.Steps {
		rec.Steps = append(rec.Steps, CalibrationStep{FPS: s.Rate, Cell: s.Cell, Modulation: Modulation(s.Mode),
			Protection: Protection(s.Inner), GridX: s.GridX, GridY: s.GridY, PixelsPerCell: s.Pixels, Frames: s.Frames,
			FramesSeen: s.Seen, Codewords: s.Codewords, Decoded: s.Decoded, Passed: s.Passed, Margin: s.Margin,
			Established: s.Standing == calib.EstablishedAt, BytesPerSec: s.BytesPerSec})
	}
	return rec, nil
}
