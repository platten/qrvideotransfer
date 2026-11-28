package calib

import (
	"context"
	"errors"
	"fmt"
	"math/bits"
	"os"
	"slices"
	"time"

	"github.com/platten/qrvideotransfer/internal/fec/ldpc"
	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/pipeline"
	"github.com/platten/qrvideotransfer/internal/video"
)

// AnalyzeConfig are the inputs of [Analyze].
type AnalyzeConfig struct {
	Recordings []string
	Backend    video.Backend
	Workers    int // 0: GOMAXPROCS, as pipeline.Scan's
	Progress   func(pipeline.Event)

	softCap int64 // tests: the soft-bit cap; 0 is pipeline.MaxSoftBytes
}

// Inners are the inner codes every segment is decoded with: fast, normal and robust.
var Inners = [3]uint8{Fast, Normal, Robust}

// Step is what Analyze measured of one segment, decoded with one inner code.
type Step struct {
	Measurement
	Standing    Standing
	Passed      bool    // the segment passes (Measurement.Passes)
	Margin      bool    // it passes with margin (Measurement.PassesWithMargin)
	BytesPerSec float64 // the planning rate scaled by the completeness measured
}

// Analysis is the outcome of [Analyze].
type Analysis struct {
	Plan           Plan
	Camera         Camera // as the recordings' probes and sizes give it
	Steps          []Step // every segment seen, by plan order, then fast, normal, robust
	Recommendation Recommendation
	Warnings       []pipeline.Warning
	Captures       int64
}

// Reason says why Analyze found no calibration video it could measure.
type Reason uint8

// The reasons, in the order Analyze checks them.
const (
	NoCode         Reason = iota + 1 // no capture showed a code's finders
	NoHeader                         // finders, but no frame header was read
	NotCalibration                   // headers were read, but none is a calibration frame's: a transfer
	MixedSizes                       // calibration headers recovering to more than one video size: not a calibration video
	NothingPassed                    // the calibration video's headers, but no segment passed: a poor recording, or a transfer that matches the pattern
)

// Error is the error of Analyze when no recommendation can be made.
type Error struct {
	Reason      Reason
	Captures    int64  // captures read
	Located     int64  // captures in which the code's finders were found
	HeadersRead int64  // captures whose frame header was read
	Calibration int64  // of those, captures whose header is a calibration frame's
	Sizes       []Size // the video sizes the calibration headers recover to
}

// ErrNoCalibration is wrapped by every Error.
var ErrNoCalibration = errors.New("calib: no calibration video found")

func (e *Error) Error() string {
	switch e.Reason {
	case NoCode:
		return fmt.Sprintf("%v: no code in any of %d captures", ErrNoCalibration, e.Captures)
	case NoHeader:
		return fmt.Sprintf("%v: the code was found in %d of %d captures, but no frame header was read", ErrNoCalibration, e.Located, e.Captures)
	case NotCalibration:
		return fmt.Sprintf("%v: %d captures show a code whose frame headers are not the calibration video's", ErrNoCalibration, e.HeadersRead)
	case MixedSizes:
		return fmt.Sprintf("%v: the frame headers recover to %d video sizes", ErrNoCalibration, len(e.Sizes))
	}
	return fmt.Sprintf("calib: no step of the calibration video passed in %d captures of it", e.Calibration)
}

func (e *Error) Unwrap() error {
	if e.Reason == NothingPassed {
		return nil
	}
	return ErrNoCalibration
}

// openFrame is one code frame's summed soft bits, by inner code, in codeword order.
type openFrame struct {
	seg  int
	soft [3][]int8
	last int // global index of the capture that last showed it
}

// frameState is what is kept of a code frame once its soft bits are decoded: whether it was
// seen, and which codewords decoded, by inner code. Recordings add up: a codeword counts once it
// decodes in any of them.
type frameState struct {
	dec [3][]uint64
}

// segStats is what is counted per segment.
type segStats struct {
	captures int64
	pixels   float64
	seen     int
}

// analysis is one Analyze run's state.
type analysis struct {
	c       AnalyzeConfig
	plan    *Plan
	sizes   []Size
	maps    map[[2]int]*frame.Mapping // by segment and inner-code index
	codes   [3]*ldpc.Code
	coset   [3][]byte
	llr     []int8
	shard   []byte
	open    map[uint32]*openFrame
	held    int64
	done    map[uint32]*frameState
	segs    []segStats
	errs    Error
	clipped float64
	located int64
	exposed bool
	warn    func(code, text string)
	pts     map[int]*ptsSpan // by recording: the span of its captures' presentation times
	softCap int64
}

// ptsSpan is the span of one recording's presentation times and the captures in it.
type ptsSpan struct {
	first, last time.Duration
	n           int64
}

// Analyze reads recordings of the calibration video through pipeline.Scan (DESIGN.md section 13).
// Every capture's first header must be a calibration frame's (Recover), all of one video size;
// each code frame's soft bits are summed across the captures that show it, adjacent captures at
// half weight (DESIGN.md section 7, rule 7), and the frame is decoded with every inner code once
// two captures have passed without it. A codeword counts as decoded only when it decodes to the
// all-zero codeword (on the coset of the all-zero shard's codeword), never merely when the inner
// code converges. Several recordings add up per segment. The summed soft bits held stay within
// pipeline.MaxSoftBytes: the frames seen longest ago are decoded first. It returns the steps and
// Recommend's choice, or an *Error when there is none.
func Analyze(ctx context.Context, c AnalyzeConfig) (*Analysis, error) {
	a := &analysis{c: c, maps: map[[2]int]*frame.Mapping{}, open: map[uint32]*openFrame{}, done: map[uint32]*frameState{},
		pts: map[int]*ptsSpan{}, softCap: c.softCap}
	if a.softCap <= 0 {
		a.softCap = pipeline.MaxSoftBytes
	}
	for k, id := range Inners {
		a.codes[k], _ = ldpc.New(id)
		zero := make([]byte, a.codes[k].ShardSize())
		a.coset[k] = make([]byte, (a.codes[k].N()+7)/8)
		a.codes[k].Encode(a.coset[k], zero)
	}
	a.llr = make([]int8, frame.LDPCN)
	a.shard = make([]byte, 256)
	var warnings []pipeline.Warning
	a.warn = func(code, text string) {
		for _, w := range warnings {
			if w.Code == code {
				return
			}
		}
		w := pipeline.Warning{Code: code, Text: text}
		warnings = append(warnings, w)
		if c.Progress != nil {
			c.Progress(pipeline.Event{Time: time.Now(), Stage: pipeline.StageDemodulate, Kind: pipeline.KindWarning, Warn: &w})
		}
	}
	base, last, rec := 0, -1, -1
	var mixed bool
	res, err := pipeline.Scan(ctx, pipeline.ScanConfig{Recordings: c.Recordings, Backend: c.Backend, Workers: c.Workers, Progress: c.Progress},
		func(cp *pipeline.Capture) (bool, error) {
			if cp.Recording != rec {
				base, rec = last+3, cp.Recording // captures of different recordings are never adjacent
			}
			idx := base + cp.Index
			last = idx
			a.capture(cp, idx)
			if len(a.sizes) > 1 {
				mixed = true // not a calibration video: stop reading
				return true, nil
			}
			a.finalise(idx, false)
			return false, nil
		})
	if err != nil {
		return nil, err
	}
	a.errs.Captures = res.Captures
	warnings = append(warnings, res.Warnings...)
	a.finalise(0, true)
	e := a.errs
	e.Sizes = a.sizes
	switch {
	case mixed:
		e.Reason = MixedSizes
	case e.Located == 0:
		e.Reason = NoCode
	case e.HeadersRead == 0:
		e.Reason = NoHeader
	case e.Calibration == 0:
		e.Reason = NotCalibration
	}
	if e.Reason != 0 {
		return nil, &e
	}
	out := &Analysis{Plan: *a.plan, Camera: camera(c.Recordings, res.Probes), Captures: res.Captures}
	a.warnings(res.Probes)
	var ms []Measurement
	for si, s := range a.plan.Segments {
		st := a.segs[si]
		if st.captures == 0 && st.seen == 0 {
			continue
		}
		for k, inner := range Inners {
			m := a.measure(si, s, k, inner)
			ms = append(ms, m)
			step := Step{Measurement: m, Standing: StandingOf(m.Mode, m.Inner, m.Pixels, out.Camera.Floor), Passed: m.Passes(), Margin: m.PassesWithMargin()}
			step.BytesPerSec = PlanningRate(m.Setting, m.GridX, m.GridY, m.Completeness())
			out.Steps = append(out.Steps, step)
		}
	}
	a.downscaled(out.Steps, out.Camera.Floor)
	out.Warnings = warnings
	r, ok := Recommend(out.Camera, ms)
	if !ok {
		e.Reason = NothingPassed
		return out, &e
	}
	out.Recommendation = r
	return out, nil
}

// capture adds one capture.
func (a *analysis) capture(cp *pipeline.Capture, idx int) {
	if cp.Finders {
		a.errs.Located++
	}
	sp := a.pts[cp.Recording]
	if sp == nil {
		sp = &ptsSpan{first: cp.PTS}
		a.pts[cp.Recording] = sp
	}
	sp.last, sp.n = cp.PTS, sp.n+1
	if len(cp.Headers) == 0 {
		return
	}
	a.errs.HeadersRead++
	p, seg, err := Recover(cp.Headers[0])
	if err != nil {
		return
	}
	a.errs.Calibration++
	sz := Size{p.Width, p.Height}
	if !slices.Contains(a.sizes, sz) {
		a.sizes = append(a.sizes, sz)
	}
	if a.plan == nil {
		a.plan = &p
		a.segs = make([]segStats, len(p.Segments))
	}
	if len(a.sizes) > 1 || !cp.Located {
		return
	}
	si := slices.Index(a.plan.Segments, seg)
	a.located++
	a.clipped += cp.PaletteClipped
	if cp.Cells > 0 && cp.Clean == 0 && !a.exposed {
		a.exposed = true
		a.warn(pipeline.WarnExposureTooLong, "No part of a capture shows a single code frame: the exposure is too long for "+
			"the display rate. Brighten the screen or film in more light.")
	}
	a.segs[si].captures++
	a.segs[si].pixels += cp.PixelsPerCell
	for _, ph := range cp.Phases {
		if ph.Frame < seg.FirstIndex || ph.Frame >= seg.FirstIndex+uint32(seg.CodeFrames) { //nolint:gosec // small
			continue // a frame of the next or previous segment, whose grid the header does not give
		}
		a.add(si, seg, ph, idx)
	}
}

// mapping returns the mapping of segment si decoded with inner code k.
func (a *analysis) mapping(si int, seg Segment, k int) *frame.Mapping {
	key := [2]int{si, k}
	m := a.maps[key]
	if m == nil {
		h := seg.Header(seg.FirstIndex)
		h.InnerCode = Inners[k]
		m, _ = frame.NewMapping(h) // a plan's segment fits every inner code
		a.maps[key] = m
	}
	return m
}

// add sums one phase's soft bits into its frame.
func (a *analysis) add(si int, seg Segment, ph pipeline.Phase, idx int) {
	fr := a.open[ph.Frame]
	if fr == nil {
		fr = &openFrame{seg: si, last: -2}
		for k := range Inners {
			m := a.mapping(si, seg, k)
			fr.soft[k] = make([]int8, m.CodewordsPerFrame()*m.Sent())
			a.held += int64(len(fr.soft[k]))
		}
		a.open[ph.Frame] = fr
	}
	w := 2 // in halves: adjacent captures of one frame count half (DESIGN.md section 7, rule 7)
	if fr.last == idx-1 {
		w = 1
	}
	for k := range Inners {
		m := a.mapping(si, seg, k)
		sent, dst := m.Sent(), fr.soft[k]
		for c := range m.CodewordsPerFrame() {
			row := dst[c*sent : (c+1)*sent]
			for t := range row {
				s := int(ph.Soft[m.Position(ph.Frame, c, t)])
				row[t] = int8(max(-127, min(127, int(row[t])+s*w/2)))
			}
		}
	}
	fr.last = idx
	for a.held > a.softCap && len(a.open) > 0 {
		a.finaliseOldest()
	}
}

// finalise decodes every open frame that two captures have passed without, or every one with all.
func (a *analysis) finalise(idx int, all bool) {
	for f, fr := range a.open {
		if all || fr.last <= idx-2 {
			a.decode(f, fr)
		}
	}
}

// finaliseOldest decodes the open frame seen longest ago, the lowest index of those, to stay
// within the soft-bit cap. A cap below one frame's soft bits decodes each frame as it comes.
func (a *analysis) finaliseOldest() {
	var oldest uint32
	best := -1
	for f, fr := range a.open {
		if best < 0 || fr.last < best || fr.last == best && f < oldest {
			oldest, best = f, fr.last
		}
	}
	a.decode(oldest, a.open[oldest])
}

// decode decodes frame f's summed soft bits with every inner code on the coset of the all-zero
// shard's codeword and records which codewords decoded to zeros.
func (a *analysis) decode(f uint32, fr *openFrame) {
	delete(a.open, f)
	seg := a.plan.Segments[fr.seg]
	st := a.done[f]
	if st == nil {
		st = &frameState{}
		a.done[f] = st
		a.segs[fr.seg].seen++
	}
	for k := range Inners {
		a.held -= int64(len(fr.soft[k]))
		m := a.mapping(fr.seg, seg, k)
		cpf, sent := m.CodewordsPerFrame(), m.Sent()
		if st.dec[k] == nil {
			st.dec[k] = make([]uint64, (cpf+63)/64)
		}
		code, coset := a.codes[k], a.coset[k]
		sh := a.shard[:code.ShardSize()]
		for c := range cpf {
			if st.dec[k][c/64]>>(c%64)&1 == 1 {
				continue
			}
			src := fr.soft[k][c*sent : (c+1)*sent]
			nz := false
			for t, v := range src {
				if coset[t/8]>>(7-t%8)&1 == 1 {
					v = neg(v)
				}
				a.llr[t] = v
				nz = nz || v != 0
			}
			if !nz {
				continue
			}
			if _, ok := code.Decode(sh, a.llr[:sent]); ok && !slices.ContainsFunc(sh, func(b byte) bool { return b != 0 }) {
				st.dec[k][c/64] |= 1 << (c % 64)
			}
		}
	}
}

// neg negates a soft bit, keeping it within -127..127.
func neg(v int8) int8 {
	if v == -128 {
		return 127
	}
	return -v
}

// measure returns segment si's measurement with inner code index k.
func (a *analysis) measure(si int, s Segment, k int, inner uint8) Measurement {
	st := a.segs[si]
	m := Measurement{Setting: s.Setting(inner), GridX: s.GridX, GridY: s.GridY, Frames: s.CodeFrames, Seen: st.seen}
	if st.captures > 0 {
		m.Pixels = st.pixels / float64(st.captures)
	}
	cpf := a.mapping(si, s, k).CodewordsPerFrame()
	for f := s.FirstIndex; f < s.FirstIndex+uint32(s.CodeFrames); f++ { //nolint:gosec // small
		if fs := a.done[f]; fs != nil {
			m.Codewords += cpf
			for _, w := range fs.dec[k] {
				m.Decoded += bits.OnesCount64(w)
			}
		}
	}
	return m
}

// camera returns the camera the recordings show: the lowest frame rate and bitrate of them, H.264
// if any is, and the geometry floor of 10-bit HLG if any is HLG, else the handheld floor.
func camera(recs []string, probes []video.ClipInfo) Camera {
	c := Camera{Floor: FloorHandheld}
	for i, p := range probes {
		fps := float64(p.FPSNum) / float64(max(p.FPSDen, 1))
		if i == 0 || fps < c.FPS {
			c.FPS = fps
		}
		var br float64
		if st, err := os.Stat(recs[i]); err == nil && p.Duration > 0 {
			br = float64(st.Size()) * 8 / p.Duration.Seconds()
		}
		if i == 0 || br < c.Bitrate {
			c.Bitrate = br
		}
		c.H264 = c.H264 || p.Codec == "h264"
		if p.Transfer == "arib-std-b67" {
			c.Floor = FloorHLG
		}
		c.Width = max(c.Width, p.Width)
	}
	return c
}

// Warning thresholds.
const (
	// ClipWarn is the mean share of clipped palette patches, over the located captures, above which
	// Analyze warns of clipping.
	ClipWarn = 0.05
	// DropWarn is the share of a recording's frames missing from its presentation times above which
	// Analyze warns of dropped frames.
	DropWarn = 0.01
)

// The warning codes Analyze raises besides pipeline.WarnExposureTooLong; the root package has the
// same strings.
const (
	WarnClipping      = "Clipping"
	WarnFramesDropped = "FramesDropped"
	WarnDownscaled    = "Downscaled"
)

// warnings adds the recording-wide warnings: clipping and dropped frames. A recording drops frames
// when its captures' presentation times span more frame periods, at the probe's frame rate, than it
// has captures.
func (a *analysis) warnings(probes []video.ClipInfo) {
	if a.located > 0 {
		if share := a.clipped / float64(a.located); share > ClipWarn {
			a.warn(WarnClipping, fmt.Sprintf("Highlights clip in %.0f%% of the palette patches: lower the screen's brightness or "+
				"the camera's exposure.", 100*share))
		}
	}
	dropped, total := int64(0), int64(0)
	for i, p := range probes {
		sp := a.pts[i]
		if sp == nil || p.FPSNum <= 0 || p.FPSDen <= 0 {
			continue
		}
		period := time.Second * time.Duration(p.FPSDen) / time.Duration(p.FPSNum)
		want := int64((sp.last-sp.first+period/2)/period) + 1
		dropped, total = dropped+max(want-sp.n, 0), total+want
	}
	if total > 0 && float64(dropped) > DropWarn*float64(total) {
		a.warn(WarnFramesDropped, fmt.Sprintf("The recording skipped %d of %d frames: close other apps on the phone, or record "+
			"at a lower frame rate.", dropped, total))
	}
}

// downscaled warns when a step of 4-pixel cells fails with every inner code where the step of
// 8-pixel cells at the same rate and mode passes, though the 4-pixel cells had the camera pixels
// per cell that `robust` needs: the player is likely scaling the video down.
func (a *analysis) downscaled(steps []Step, floor float64) {
	pass := map[[3]int]bool{}
	for _, s := range steps {
		k := [3]int{s.Rate, s.Cell, int(s.Mode)}
		pass[k] = pass[k] || s.Passed
	}
	for _, s := range steps {
		if s.Cell != 4 || s.Inner != Robust || pass[[3]int{s.Rate, 4, int(s.Mode)}] || !pass[[3]int{s.Rate, 8, int(s.Mode)}] ||
			s.Pixels < Condition2(s.Mode, Robust, floor) {
			continue
		}
		a.warn(WarnDownscaled, "The steps with 4-pixel cells fail where 8-pixel cells pass, with enough camera pixels per cell: "+
			"the player may be scaling the video. Play it full screen, made with the monitor's own --resolution.")
		return
	}
}
