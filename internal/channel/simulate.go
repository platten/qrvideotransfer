package channel

import (
	"context"
	"time"

	"github.com/platten/qrvideotransfer/internal/video"
)

// Input is what the screen shows: the displayed video, looping.
type Input struct {
	// Frames are the displayed video frames in order, all the same size. PTS is when each starts
	// within the loop (strictly increasing, from 0 up to Period) and Index is its code frame index;
	// consecutive frames may share an index.
	Frames []*video.Frame
	Period time.Duration // length of one pass of the loop
	Cell   int           // video pixels per cell, for the ground truth
}

// Run says what to record.
type Run struct {
	Start    time.Duration // display time at which the recording starts
	Captures int           // camera frame slots, dropped ones included
	Seed     uint64
	Workers  int   // captures simulated at once; 0 means 1
	Codec    Codec // nil means the stand-in Quantiser(QuantiserStep(c))
}

// maxCaptures bounds Run.Captures, which sizes the schedule and the shake track.
const maxCaptures = 1 << 24

// recording holds everything that is fixed for one recording. It is read-only once built, so
// captures can be simulated concurrently.
type recording struct {
	cfg          Config
	seed         uint64
	cell         int
	gridX, gridY int
	frames       []*video.Frame
	scr          *screen
	tl           *timeline
	cam          *camera
	track        track
	tone         *[toneSteps + 1]float32
	fringe       *fringe
	glareAt      [2]float64 // panel coordinates of the glare patch's centre
	glareR       float64    // its radius, video pixels
}

func newRecording(in Input, c Config, r Run) (*recording, error) {
	var z0 *recording
	var z1 error
	return z0, z1
}

// span returns the display times during which some row of the capture is exposed.
func (rc *recording) span(sh *shot) (t0, t1 float64) {
	var z0 float64
	var z1 float64
	return z0, z1
}

// project maps a panel point to the capture: the camera's geometry at the time the row that sees
// the point is exposed, found by iteration because that row depends on the pose.
func (rc *recording) project(sh *shot, px, py float64) (x, y float64) {
	var z0 float64
	var z1 float64
	return z0, z1
}

// capture runs stages 2 to 5 for one shot.
func (rc *recording) capture(sh *shot, occs []occ, panels []*panel) *video.Frame {
	var z0 *video.Frame
	return z0
}

// Simulate films the looping input with the simulated camera c and calls emit once per capture
// that was not dropped, in capture order, with the capture (Index counting emitted captures from
// 0, PTS from the first slot) and its ground truth. Captures are simulated by r.Workers goroutines;
// the output depends only on the input, c and r.Seed. Simulate stops at the first error from emit,
// from the codec or from ctx.
func Simulate(ctx context.Context, in Input, c Config, r Run, emit func(*video.Frame, *Truth) error) error {
	var z0 error
	return z0
}

// finish labels a capture and builds its ground truth.
func (rc *recording) finish(f *video.Frame, sh *shot, n int, t0 float64) (*video.Frame, *Truth) {
	var z0 *video.Frame
	var z1 *Truth
	return z0, z1
}

func duration(s float64) time.Duration {
	var z0 time.Duration
	return z0
}

// Truth is the ground truth of one capture. Display coordinates are video pixels and capture
// coordinates camera pixels, both with pixel centres at half-integers; cell (gx, gy) has its centre
// at display coordinates ((gx + 0.5) Cell, (gy + 0.5) Cell).
type Truth struct {
	Capture      int           // index of the emitted capture
	Slot         int           // camera frame slot, counting dropped captures
	PTS          time.Duration // as on the capture
	Start        time.Duration // display time at which the first row's exposure starts
	GridX, GridY int           // cells across and down the video
	rc           *recording
	sh           shot
}

// Weight is the share of a capture position's exposure during which code frame Frame was shown.
type Weight struct {
	Frame int
	W     float64
}

// Point maps a display position to the capture position that sees it, lens distortion and
// rolling shutter included.
func (t *Truth) Point(u, v float64) (x, y float64) {
	var z0 float64
	var z1 float64
	return z0, z1
}

// Cell returns the capture position of a cell's centre.
func (t *Truth) Cell(gx, gy int) (x, y float64) {
	var z0 float64
	var z1 float64
	return z0, z1
}

// Mix returns, for the display position (u, v), the code frames seen there during the exposure of
// the capture row that sees it, with their weights, in order of frame index. The weights sum to 1.
func (t *Truth) Mix(u, v float64) []Weight {
	var z0 []Weight
	return z0
}

// CellMix returns Mix at a cell's centre.
func (t *Truth) CellMix(gx, gy int) []Weight {
	var z0 []Weight
	return z0
}
