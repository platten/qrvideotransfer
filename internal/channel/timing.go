package channel

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/platten/qrvideotransfer/internal/prng"
	"github.com/platten/qrvideotransfer/internal/video"
)

// seconds converts a duration to seconds.
func seconds(d time.Duration) float64 { return float64(float64(d) / 1e9) }

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
	tl := &timeline{
		start: make([]float64, len(frames)),
		index: make([]int, len(frames)),
		pass:  seconds(period + t.SeamFreeze),
		scan:  seconds(p.ScanOut),
		resp:  seconds(p.Response),
		flick: p.FlickerAmp,
		omega: float64(float64(2*math.Pi) * p.FlickerHz),
	}
	for j, f := range frames {
		if f.PTS < 0 || f.PTS >= period || (j > 0 && f.PTS <= frames[j-1].PTS) {
			return nil, fmt.Errorf("channel: frame %d time %v is not increasing within the period %v", j, f.PTS, period)
		}
		tl.start[j] = seconds(f.PTS)
		tl.index[j] = f.Index
	}
	return tl, nil
}

// occ is one showing of displayed frame j, from s to e at the panel's top row.
type occ struct {
	j    int
	s, e float64
}

// occurrences returns, in time order, every showing of a frame that is visible somewhere on the
// panel during [t0, t1].
func (tl *timeline) occurrences(t0, t1 float64) []occ {
	n := len(tl.start)
	var out []occ
	for m := math.Floor(float64((t0-tl.scan-tl.resp)/tl.pass)) - 1; m <= math.Floor(float64(t1/tl.pass))+1; m++ {
		base := float64(m * tl.pass)
		// The first frame whose showing ends after t0 - scan - resp.
		lo := sort.Search(n, func(j int) bool { return tl.end(base, j)+tl.scan+tl.resp >= t0 })
		for j := lo; j < n; j++ {
			s := base + tl.start[j]
			if s > t1 {
				break
			}
			out = append(out, occ{j: j, s: s, e: tl.end(base, j)})
		}
	}
	return out
}

// end returns when frame j of the pass starting at base gives way to the next frame.
func (tl *timeline) end(base float64, j int) float64 {
	if j+1 < len(tl.start) {
		return base + tl.start[j+1]
	}
	return base + tl.pass + tl.start[0]
}

// weight returns the share of the exposure [a, b] during which a panel point at row fraction yd
// shows occurrence o, with the pixel response as a linear ramp.
func (tl *timeline) weight(o occ, a, b, yd float64) float64 {
	d := float64(yd * tl.scan)
	s, e := o.s+d, o.e+d
	switch {
	case b <= s || a >= e+tl.resp:
		return 0 // not shown during the exposure
	case a >= s+tl.resp && b <= e:
		return 1 // shown fully throughout
	}
	w := float64((tl.ramp(b-s) - tl.ramp(a-s) - tl.ramp(b-e) + tl.ramp(a-e)) / (b - a))
	if w < 1e-12 {
		return 0 // rounding left over where the showing is not seen at all
	}
	return w
}

// ramp is the integral of the response to a switch, u after it: 0 before, u^2/(2 resp) during, and
// u - resp/2 after.
func (tl *timeline) ramp(u float64) float64 {
	switch {
	case u <= 0:
		return 0
	case u < tl.resp:
		return float64(float64(u*u) / float64(2*tl.resp))
	default:
		return u - float64(tl.resp/2)
	}
}

// flicker returns the backlight's mean level over [a, b].
func (tl *timeline) flicker(a, b float64) float64 {
	if tl.flick == 0 || tl.omega == 0 {
		return 1
	}
	_, ca := dsincos(float64(tl.omega * a))
	_, cb := dsincos(float64(tl.omega * b))
	return 1 + float64(float64(tl.flick*(ca-cb))/float64(tl.omega*(b-a)))
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
	g := prng.SplitMix64{State: prng.Seed("qrvt/channel/timing", u64(seed))}
	burst := max(p.DropBurst, 1)
	jit := seconds(p.Jitter)
	t0 := seconds(start)
	out := make([]shot, n)
	left := 0
	for i := range out {
		out[i] = shot{slot: i, start: t0 + float64(float64(i)/float64(p.FPS)) + float64(jit*clamp(gauss(&g), -4, 4))}
		u := uniform(&g)
		switch {
		case left > 0:
			out[i].dropped = true
			left--
		case u < p.Drop:
			out[i].dropped = true
			left = burst - 1
		}
	}
	return out
}

// window returns the exposure of capture row coordinate y (pixel centres at half-integers).
func (sh *shot) window(c *Config, y float64) (a, b float64) {
	a = sh.start + float64(float64(y/float64(c.Geometry.Height))*seconds(c.Timing.Readout))
	return a, a + seconds(c.Timing.Exposure)
}

// u64 encodes v as 8 big-endian bytes, for seeding.
func u64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }
