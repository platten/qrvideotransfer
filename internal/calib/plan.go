package calib

import (
	"errors"
	"fmt"

	"github.com/platten/qrvideotransfer/internal/frame"
)

// The calibration video's convention (DESIGN.md section 13). Its frames are FORMAT.md code frames
// whose data cells hold their scramble bits alone, as S2's pattern: session 0x5332, inner code 2,
// no stream descriptor. Nothing here is a value an ordinary decoder must recognise.
const (
	Session        = 0x5332 // the header's session field
	InnerCode      = 2      // the header's inner code
	VideoFPS       = 60     // the video's frame rate; display rate r repeats each frame 60 / r times
	SegmentSeconds = 4      // each segment's length
)

// The smallest grid a segment uses: smaller grids are not located reliably (DESIGN.md section 13).
const (
	MinGridX = 120
	MinGridY = 90
)

// Slot is a place in the plan's fixed order: for each display rate, each cell size of Cells in
// increasing size, each mode. Its frame indices are reserved whether or not the video's size
// gives it a segment, so the index alone names the rate, cell and mode.
type Slot struct {
	Rate, Cell int
	Mode       uint8
	FirstIndex uint32 // frame index of its first code frame
	CodeFrames int    // Rate x SegmentSeconds
}

// slotCells is Cells in increasing size, the order of the plan.
var slotCells = []int{4, 8, 12, 16}

// slots lists every slot in order; TotalFrames is the sum of their code frames.
var slots, TotalFrames = func() ([]Slot, uint32) {
	var out []Slot
	var idx uint32
	for _, r := range Rates {
		for _, c := range slotCells {
			for _, m := range Modes {
				n := r * SegmentSeconds
				out = append(out, Slot{Rate: r, Cell: c, Mode: m, FirstIndex: idx, CodeFrames: n})
				idx += uint32(n) //nolint:gosec // 36 slots of at most 240 frames
			}
		}
	}
	return out, idx
}()

// Segment is a slot the video shows.
type Segment struct {
	Slot
	GridX, GridY int
}

// Setting is the segment's setting with the plan's inner code; calibrate read decodes it with
// every inner code.
func (s Segment) Setting(inner uint8) Setting {
	return Setting{Rate: s.Rate, Cell: s.Cell, Mode: s.Mode, Inner: inner}
}

// Plan is the calibration video for one video size: the segments it shows, in order.
type Plan struct {
	Width, Height int
	Segments      []Segment
}

// ErrPlan is wrapped by the errors of NewPlan: no calibration video for that size.
var ErrPlan = errors.New("calib: no calibration plan")

// ErrNotCalibration is wrapped by the errors of Recover: the header is not one of a calibration
// video's.
var ErrNotCalibration = errors.New("calib: not a calibration frame")

// NewPlan returns the plan of a w x h video: every slot whose cell divides w and h, whose grid is
// at least MinGridX x MinGridY and within FORMAT.md's limits, and whose display video stays within
// condition 4's cap (DisplayBitrate).
func NewPlan(w, h int) (Plan, error) {
	p := Plan{Width: w, Height: h}
	if w <= 0 || h <= 0 {
		return p, fmt.Errorf("%w: video size %d x %d", ErrPlan, w, h)
	}
	for _, s := range slots {
		if seg, ok := segmentOf(s, w, h); ok {
			p.Segments = append(p.Segments, seg)
		}
	}
	if len(p.Segments) == 0 {
		return p, fmt.Errorf("%w: no cell of 4, 8, 12 or 16 pixels gives a %d x %d video a grid of at least %d x %d", ErrPlan, w, h, MinGridX, MinGridY)
	}
	return p, nil
}

// segmentOf returns slot s's segment in a w x h video, if it has one.
func segmentOf(s Slot, w, h int) (Segment, bool) {
	if w%s.Cell != 0 || h%s.Cell != 0 {
		return Segment{}, false
	}
	gx, gy := w/s.Cell, h/s.Cell
	if gx < MinGridX || gy < MinGridY || frame.CheckGrid(gx, gy) != nil {
		return Segment{}, false
	}
	if DisplayBitrate(gx, gy, s.Rate, s.Mode) > MaxDisplayBitrate {
		return Segment{}, false
	}
	return Segment{Slot: s, GridX: gx, GridY: gy}, true
}

// Header returns the header of the code frame with index idx, which must be in the segment.
func (s Segment) Header(idx uint32) frame.Header {
	return frame.Header{Version: frame.Version, Session: Session, FrameIndex: idx, TotalFrames: TotalFrames,
		GridX: uint16(s.GridX), GridY: uint16(s.GridY), Modulation: s.Mode, InnerCode: InnerCode} //nolint:gosec // grids within FORMAT.md's limits
}

// Seconds is the video's length.
func (p Plan) Seconds() int { return len(p.Segments) * SegmentSeconds }

// Recover returns the plan and segment of a calibration frame's header. The frame index names the
// slot, and so the rate, cell and mode; grid x cell gives the video's size, whose plan must show
// that slot with the header's grid. A header that fails Validate, or names another session, inner
// code or frame count, or a mode or grid the slot's segment does not have, is refused with an
// error wrapping ErrNotCalibration.
func Recover(h frame.Header) (Plan, Segment, error) {
	if err := h.Validate(); err != nil {
		return Plan{}, Segment{}, fmt.Errorf("%w: %w", ErrNotCalibration, err)
	}
	if h.Session != Session || h.InnerCode != InnerCode || h.TotalFrames != TotalFrames {
		return Plan{}, Segment{}, fmt.Errorf("%w: session %#04x, inner code %d, %d frames (a calibration video has %#04x, %d and %d)",
			ErrNotCalibration, h.Session, h.InnerCode, h.TotalFrames, Session, InnerCode, TotalFrames)
	}
	s := slotOf(h.FrameIndex)
	if h.Modulation != s.Mode {
		return Plan{}, Segment{}, fmt.Errorf("%w: frame %d is in a segment of mode %d, not %d", ErrNotCalibration, h.FrameIndex, s.Mode, h.Modulation)
	}
	w, ht := int(h.GridX)*s.Cell, int(h.GridY)*s.Cell
	seg, ok := segmentOf(s, w, ht)
	if !ok {
		return Plan{}, Segment{}, fmt.Errorf("%w: a %d x %d video has no segment of %d-pixel cells at %d fps, mode %d",
			ErrNotCalibration, w, ht, s.Cell, s.Rate, s.Mode)
	}
	p, err := NewPlan(w, ht)
	if err != nil {
		return Plan{}, Segment{}, fmt.Errorf("%w: %w", ErrNotCalibration, err)
	}
	return p, seg, nil
}

// slotOf returns the slot of frame index idx, which is below TotalFrames.
func slotOf(idx uint32) Slot {
	for i := len(slots) - 1; i > 0; i-- {
		if idx >= slots[i].FirstIndex {
			return slots[i]
		}
	}
	return slots[0]
}
