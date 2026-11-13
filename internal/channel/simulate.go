package channel

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/platten/qrvideotransfer/internal/prng"
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
	if err := c.validate(); err != nil {
		return nil, err
	}
	if len(in.Frames) == 0 || in.Frames[0] == nil {
		return nil, fmt.Errorf("channel: no frames")
	}
	vw, vh := in.Frames[0].W, in.Frames[0].H
	if vw < 1 || vh < 1 || vw > maxDimension || vh > maxDimension {
		return nil, fmt.Errorf("channel: video size %dx%d out of range", vw, vh)
	}
	for _, f := range in.Frames {
		if err := checkFrame(f, vw, vh); err != nil {
			return nil, err
		}
	}
	switch {
	case in.Cell < 1 || in.Cell > min(vw, vh):
		return nil, fmt.Errorf("channel: cell size %d out of range", in.Cell)
	case r.Captures < 0 || r.Captures > maxCaptures:
		return nil, fmt.Errorf("channel: %d captures out of range", r.Captures)
	case in.Period <= 0:
		return nil, fmt.Errorf("channel: period %v must be positive", in.Period)
	}
	tl, err := newTimeline(in.Frames, in.Period, c.Display, c.Timing)
	if err != nil {
		return nil, err
	}
	rc := &recording{
		cfg: c, seed: r.Seed, cell: in.Cell, gridX: vw / in.Cell, gridY: vh / in.Cell,
		frames: in.Frames, tl: tl,
		scr:   newScreen(vw, vh, c.Display),
		track: shake(c.Geometry, c.Timing.FPS, r.Start, r.Captures, r.Seed),
		tone:  toneCurve(c.Sensor.HLG),
	}
	rc.cam = newCamera(c.Geometry, rc.scr, r.Seed)
	if c.Sensor.Fringe != 0 {
		rc.fringe = newFringe(c.Geometry.Width, c.Geometry.Height, r.Seed)
	}
	g := prng.SplitMix64{State: prng.Seed("qrvt/channel/glare", u64(r.Seed))}
	r0 := math.Sqrt(float64(c.Optics.Glare*float64(vw*vh)) / math.Pi)
	rw, rh := math.Min(r0, float64(float64(vw)/2)), math.Min(r0, float64(float64(vh)/2)) // the patch lies on the code
	rc.glareR = r0
	rc.glareAt[0], rc.glareAt[1] = rc.scr.toPanel(lerp(rw, float64(vw)-rw, uniform(&g)), lerp(rh, float64(vh)-rh, uniform(&g)))
	return rc, nil
}

// span returns the display times during which some row of the capture is exposed.
func (rc *recording) span(sh *shot) (t0, t1 float64) {
	return sh.start, sh.start + seconds(rc.cfg.Timing.Readout) + seconds(rc.cfg.Timing.Exposure)
}

// project maps a panel point to the capture: the camera's geometry at the time the row that sees
// the point is exposed, found by iteration because that row depends on the pose.
func (rc *recording) project(sh *shot, px, py float64) (x, y float64) {
	half := float64(seconds(rc.cfg.Timing.Exposure) / 2)
	x, y = rc.cam.cx, rc.cam.cy
	for range 50 {
		a, _ := sh.window(&rc.cfg, y)
		q, _ := rc.track.at(a + half)
		hm := rc.cam.hom(q)
		nx, ny := rc.cam.distort(apply(&hm, px, py))
		done := math.Abs(ny-y) < 1e-9 && math.Abs(nx-x) < 1e-9
		x, y = nx, ny
		if done {
			break
		}
	}
	return x, y
}

// capture runs stages 2 to 5 for one shot, with buf's buffers.
func (rc *recording) capture(sh *shot, occs []occ, panels []*panel, buf *scratch) *video.Frame {
	img := buf.image(rc.cam.w, rc.cam.h)
	rc.render(img, sh, occs, panels)
	rc.optics(img, sh)
	return rc.sensor(img, sh, buf)
}

// Simulate films the looping input with the simulated camera c and calls emit once per capture
// that was not dropped, in capture order, with the capture (Index counting emitted captures from
// 0, PTS from the first slot) and its ground truth. Captures are simulated by r.Workers goroutines;
// the output depends only on the input, c and r.Seed. Simulate stops at the first error from emit,
// from the codec or from ctx.
func Simulate(ctx context.Context, in Input, c Config, r Run, emit func(*video.Frame, *Truth) error) error {
	rc, err := newRecording(in, c, r)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	shots := schedule(c.Timing, r.Start, r.Captures, r.Seed)
	codec := r.Codec
	if codec == nil {
		codec = Quantiser(QuantiserStep(c))
	}
	workers := max(r.Workers, 1)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type job struct {
		n      int
		sh     shot
		occs   []occ
		panels []*panel
	}
	type result struct {
		job
		f   *video.Frame
		err error
	}
	jobs := make(chan job)
	results := make(chan result, workers)
	tokens := make(chan struct{}, 2*workers) // bounds captures held in memory
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var buf scratch
			for j := range jobs {
				f := rc.capture(&j.sh, j.occs, j.panels, &buf)
				err := codec(f)
				select {
				case results <- result{job: j, f: f, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		cache := map[int]*panel{} // panels of the previous capture, by displayed frame
		n := 0
		for _, sh := range shots {
			if sh.dropped {
				continue
			}
			occs := rc.tl.occurrences(rc.span(&sh))
			next := make(map[int]*panel, len(occs))
			panels := make([]*panel, len(occs))
			for k, o := range occs {
				p := next[o.j]
				if p == nil {
					p = cache[o.j]
				}
				if p == nil {
					p = display(rc.frames[o.j], rc.scr)
				}
				next[o.j], panels[k] = p, p
			}
			cache = next
			select {
			case tokens <- struct{}{}:
			case <-ctx.Done():
				return
			}
			select {
			case jobs <- job{n: n, sh: sh, occs: occs, panels: panels}:
			case <-ctx.Done():
				return
			}
			n++
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	want := 0
	for _, sh := range shots {
		if !sh.dropped {
			want++
		}
	}
	pending := map[int]result{}
	done := 0
	var first error
	for res := range results {
		if first != nil {
			continue
		}
		pending[res.n] = res
		for first == nil {
			p, ok := pending[done]
			if !ok {
				break
			}
			delete(pending, done)
			first = p.err
			if first == nil {
				first = emit(rc.finish(p.f, &p.sh, done, shots[0].start))
			}
			if first != nil {
				cancel()
				break
			}
			<-tokens
			done++
		}
	}
	if first == nil && done < want {
		first = ctx.Err()
	}
	return first
}

// finish labels a capture and builds its ground truth.
func (rc *recording) finish(f *video.Frame, sh *shot, n int, t0 float64) (*video.Frame, *Truth) {
	f.Index = n
	f.PTS = duration(sh.start - t0)
	return f, &Truth{Capture: n, Slot: sh.slot, PTS: f.PTS, Start: duration(sh.start), GridX: rc.gridX, GridY: rc.gridY, rc: rc, sh: *sh}
}

func duration(s float64) time.Duration { return time.Duration(math.Floor(float64(s*1e9) + 0.5)) }

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
	px, py := t.rc.scr.toPanel(u, v)
	return t.rc.project(&t.sh, px, py)
}

// Cell returns the capture position of a cell's centre.
func (t *Truth) Cell(gx, gy int) (x, y float64) {
	c := float64(t.rc.cell)
	return t.Point(float64((float64(gx)+0.5)*c), float64((float64(gy)+0.5)*c))
}

// Mix returns, for the display position (u, v), the code frames seen there during the exposure of
// the capture row that sees it, with their weights, in order of frame index. The weights sum to 1.
func (t *Truth) Mix(u, v float64) []Weight {
	rc := t.rc
	px, py := rc.scr.toPanel(u, v)
	_, y := rc.project(&t.sh, px, py)
	a, b := t.sh.window(&rc.cfg, y)
	var out []Weight
	for _, o := range rc.tl.occurrences(a, b) {
		w := rc.tl.weight(o, a, b, py/float64(rc.scr.ph))
		if w <= 0 {
			continue
		}
		f := rc.tl.index[o.j]
		if k := slices.IndexFunc(out, func(x Weight) bool { return x.Frame == f }); k >= 0 {
			out[k].W += w
		} else {
			out = append(out, Weight{Frame: f, W: w})
		}
	}
	slices.SortFunc(out, func(a, b Weight) int { return a.Frame - b.Frame })
	return out
}

// CellMix returns Mix at a cell's centre.
func (t *Truth) CellMix(gx, gy int) []Weight {
	c := float64(t.rc.cell)
	return t.Mix(float64((float64(gx)+0.5)*c), float64((float64(gy)+0.5)*c))
}
