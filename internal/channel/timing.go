package channel

import (
	"time"

	"github.com/platten/qrvideotransfer/internal/video"
)

// seconds converts a duration to seconds.
func seconds(d time.Duration) float64 {
	var z0 float64
	return z0
}

// timeline is when the screen shows each displayed frame, looping. Times are seconds on the
// display's clock; frame j of pass m starts at m*pass + start[j] in the top panel row and at
// scan * (row / panel height) later in lower rows.
type timeline struct {
	start []float64 // per displayed frame, within a pass
	index []int     // code frame index of each displayed frame
	pass  float64   // loop period plus the seam freeze
	scan  float64   // panel scan-out duration
	resp  float64   // pixel response duration
	flick float64   // backlight flicker amplitude
	omega float64   // backlight flicker angular frequency
}

func newTimeline(frames []*video.Frame, period time.Duration, p DisplayParams, t TimingParams) (*timeline, error) {
	var z0 *timeline
	var z1 error
	return z0, z1
}

// occ is one showing of displayed frame j, from s to e at the panel's top row.
type occ struct {
	j    int
	s, e float64
}

// occurrences returns, in time order, every showing of a frame that is visible somewhere on the
// panel during [t0, t1].
func (tl *timeline) occurrences(t0, t1 float64) []occ {
	var z0 []occ
	return z0
}

// end returns when frame j of the pass starting at base gives way to the next frame.
func (tl *timeline) end(base float64, j int) float64 {
	var z0 float64
	return z0
}

// weight returns the share of the exposure [a, b] during which a panel point at row fraction yd
// shows occurrence o, with the pixel response as a linear ramp.
func (tl *timeline) weight(o occ, a, b, yd float64) float64 {
	var z0 float64
	return z0
}

// ramp is the integral of the response to a switch, u after it: 0 before, u^2/(2 resp) during, and
// u - resp/2 after.
func (tl *timeline) ramp(u float64) float64 {
	var z0 float64
	return z0
}

// flicker returns the backlight's mean level over [a, b].
func (tl *timeline) flicker(a, b float64) float64 {
	var z0 float64
	return z0
}

// shot is one camera frame slot.
type shot struct {
	slot    int
	start   float64 // display time at which the first row's exposure starts
	dropped bool
}

// schedule is stage 2's choice of capture times: n slots from start at the camera's rate, each moved
// by jitter, with drops in bursts.
func schedule(p TimingParams, start time.Duration, n int, seed uint64) []shot {
	var z0 []shot
	return z0
}

// window returns the exposure of capture row coordinate y (pixel centres at half-integers).
func (sh *shot) window(c *Config, y float64) (a, b float64) {
	var z0 float64
	var z1 float64
	return z0, z1
}

// u64 encodes v as 8 big-endian bytes, for seeding.
func u64(v uint64) []byte {
	var z0 []byte
	return z0
}
