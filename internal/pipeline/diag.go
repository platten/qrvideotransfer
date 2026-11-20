package pipeline

import (
	"math"

	"github.com/platten/qrvideotransfer/internal/frame"
)

// Failure diagnostics (DESIGN.md section 13, failure guidance).
const (
	// agreeCaptures is the fewest captures that must give the same header refusal, the same reason
	// and value, for it to be reported. A noise pattern can pass the header's CRC-16 by chance and
	// then fail Validate with a random value; the same value in three captures does not happen by
	// chance. The refusal must also be that of at least half of the captures whose finders were
	// found, and no header may have been read.
	agreeCaptures = 3
	// agreeKinds is the most distinct refusals counted; later ones are ignored, so that a hostile
	// recording cannot grow the count.
	agreeKinds = 8

	// lockSample bounds the frames the locked-rate check follows: every frame of a loop of up to
	// lockSample frames, else every ceil(F / lockSample)th.
	lockSample = 4096
	// lockTolerance is how far the later passes' best clean fractions may lie from the first
	// pass's, on average over the frames compared, and still count as unchanged. One capture's
	// clean fraction varies with the noise of the phase field: in locked simulated recordings by up
	// to 0.03 from pass to pass on a 120 x 120 grid and up to 0.1 on a 212 x 120 grid (2026-11-20),
	// which the average over the frames removes; a drift that moves the mixing shifts them all.
	lockTolerance = 0.05
	// lockMinPairs is the fewest comparisons of a later pass with the first that the check needs.
	lockMinPairs = 4
	// lockMixed is the mean first-pass clean fraction below which part of every capture is mixed:
	// at or above it, more passes add clean data and the generic guidance holds.
	lockMixed = 0.9
)

// diagnostics counts what the captures showed short of a code.
type diagnostics struct {
	finders, headers, other int64
	refusals                []refusalCount
}

// refusalCount is a header refusal, the first seen, and how many captures gave it.
type refusalCount struct {
	e     frame.HeaderError
	value uint32
	n     int64
}

// note counts capture r; key is the transfer decoded, nil before one is.
func (g *diagnostics) note(r *captureResult, key *params) {
	if r.finders {
		g.finders++
	}
	switch {
	case r.key != params{}:
		g.headers++
		if key != nil && r.key != *key {
			g.other++
		}
	case r.reject.Reason >= frame.HeaderReserved: // a Validate failure: the CRC passed
		v := refusedValue(r.reject)
		for i := range g.refusals {
			if c := &g.refusals[i]; c.e.Reason == r.reject.Reason && c.value == v {
				c.n++
				return
			}
		}
		if len(g.refusals) < agreeKinds {
			g.refusals = append(g.refusals, refusalCount{r.reject, v, 1})
		}
	}
}

// refusedValue returns the value a Validate refusal names.
func refusedValue(e frame.HeaderError) uint32 {
	h := e.Header
	switch e.Reason {
	case frame.HeaderReserved:
		return uint32(h.Reserved)
	case frame.HeaderVersion:
		return uint32(h.Version)
	case frame.HeaderFrameCount, frame.HeaderFrameIndex:
		return h.TotalFrames
	case frame.HeaderModulation:
		return uint32(h.Modulation)
	case frame.HeaderInner:
		return uint32(h.InnerCode)
	}
	return uint32(h.GridX)<<11 | uint32(h.GridY)
}

// agreed returns the refusal the captures agree on (agreeCaptures), or nil.
func (g *diagnostics) agreed() *frame.HeaderError {
	if g.headers > 0 {
		return nil
	}
	var best *refusalCount
	for i := range g.refusals {
		if best == nil || g.refusals[i].n > best.n {
			best = &g.refusals[i]
		}
	}
	if best == nil || best.n < agreeCaptures || 2*best.n < g.finders {
		return nil
	}
	e := best.e
	return &e
}

// lockTrack tells a camera locked to the display rate from the captures of the decoded transfer
// (DESIGN.md section 13). A pass over a frame is a run of consecutive captures whose first header
// names it; for each followed frame it keeps the best clean fraction of its first pass and of its
// latest. The camera is locked when, for the frames seen in more than one pass, the later passes'
// bests differ from the first's by at most lockTolerance on average (signed, so that the noise of
// single captures cancels), over at least lockMinPairs comparisons, and the first passes' mean is
// under lockMixed.
type lockTrack struct {
	stride uint32
	frames []lockFrame // followed frames, frame k at k / stride
	pairs  int         // comparisons of finished later passes with the first
	diff   float64     // their differences from the first pass, summed
}

// lockFrame is one followed frame's passes.
type lockFrame struct {
	first, cur float32 // best clean fraction of the first pass and of the latest
	last       int     // index of the last capture
	runs       int     // passes seen
}

// newLockTrack follows a loop of f frames.
func newLockTrack(f uint32) lockTrack {
	stride := (f + lockSample - 1) / lockSample
	return lockTrack{stride: stride, frames: make([]lockFrame, (f+stride-1)/stride)}
}

// see adds capture index, whose first header names frame k, with its clean fraction.
func (t *lockTrack) see(k uint32, index int, clean float32) {
	if t.frames == nil || k%t.stride != 0 || int(k/t.stride) >= len(t.frames) {
		return
	}
	e := &t.frames[k/t.stride]
	switch {
	case e.runs > 0 && index == e.last+1: // the same pass
		e.cur = max(e.cur, clean)
	default:
		if e.runs >= 2 {
			t.compare(e)
		}
		e.runs++
		e.cur = clean
	}
	if e.runs == 1 {
		e.first = e.cur
	}
	e.last = index
}

// compare counts the comparison of e's latest pass with its first.
func (t *lockTrack) compare(e *lockFrame) {
	t.pairs++
	t.diff += float64(e.cur - e.first)
}

// locked reports whether the camera is locked to the display rate.
func (t *lockTrack) locked() bool {
	u := *t
	sum, n := 0.0, 0
	for i := range t.frames {
		if e := &t.frames[i]; e.runs >= 2 {
			u.compare(e) // the latest pass, not yet compared
			sum += float64(e.first)
			n++
		}
	}
	return u.pairs >= lockMinPairs && math.Abs(u.diff) <= lockTolerance*float64(u.pairs) && sum < lockMixed*float64(n)
}
