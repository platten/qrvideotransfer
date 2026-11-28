package calib

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/pipeline"
	"github.com/platten/qrvideotransfer/internal/render"
	"github.com/platten/qrvideotransfer/internal/video"
)

// MakeConfig are the inputs of [Make].
type MakeConfig struct {
	Output        string
	Width, Height int // the monitor's resolution, which the video is made for
	Backend       video.Backend
	Workers       int    // 0: GOMAXPROCS
	TempDir       string // "": the system temporary directory
	Progress      func(pipeline.Event)

	keep func(Segment) bool // tests: only these segments are shown
}

// joiner is what Make needs of the backend besides encoding: joining the segments' runs.
type joiner interface {
	Concat(ctx context.Context, runs []string, path string, fps int) error
}

// VideoFrames is the number of video frames of a plan's video: each segment's code frames, each
// shown VideoFPS / rate times.
func (p Plan) VideoFrames() int {
	n := 0
	for _, s := range p.Segments {
		n += s.videoFrames()
	}
	return n
}

func (s Segment) videoFrames() int { return s.CodeFrames * (VideoFPS / s.Rate) }

// Duration is the length of a plan's video.
func (p Plan) Duration() time.Duration {
	return time.Duration(p.VideoFrames()) * time.Second / VideoFPS
}

// EncodeCost is the measured cost of making the calibration video with the embedded engine: worker
// seconds per million pixels of video frame (DESIGN.md section 19, measured 2026-11-28 on a
// 16-thread machine: 1920 x 1080 in 59 s with 16 workers).
const EncodeCost = 0.073

// Estimate is how long Make is expected to take for the plan with the given workers (0:
// GOMAXPROCS): EncodeCost per million pixels of every video frame, spread over the workers, of
// which at most one per segment is busy.
func (p Plan) Estimate(workers int) time.Duration {
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	workers = max(1, min(workers, len(p.Segments)))
	mp := float64(p.Width*p.Height) / 1e6 * float64(p.VideoFrames())
	return time.Duration(EncodeCost * mp / float64(workers) * float64(time.Second))
}

// Make writes the calibration video of a Width x Height monitor (DESIGN.md section 13): every
// segment of NewPlan's plan, in order, each code frame shown VideoFPS / rate times, in one
// VideoFPS video. Workers each render and encode whole segments to files of their own in a
// temporary folder under TempDir, removed on return; the runs are joined beside the output and
// renamed into place, so a cancelled or failed Make leaves neither its temporary files nor a
// partial output.
func Make(ctx context.Context, c MakeConfig) error {
	switch {
	case c.Output == "":
		return errors.New("calib: no output")
	case c.Backend == nil:
		return errors.New("calib: no video backend")
	}
	join, ok := c.Backend.(joiner)
	if !ok {
		return errors.New("calib: the video backend cannot join runs")
	}
	p, err := NewPlan(c.Width, c.Height)
	if err != nil {
		return err
	}
	var segs []Segment
	for _, s := range p.Segments {
		if c.keep == nil || c.keep(s) {
			segs = append(segs, s)
		}
	}
	total := 0
	for _, s := range segs {
		total += s.videoFrames()
	}
	tmp, err := os.MkdirTemp(c.TempDir, "qrvt-calibrate-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	ext := ".h264"
	if _, raw := c.Backend.(*video.Raw); raw {
		ext = ".raw"
	}
	emit := func(st pipeline.Stage, k pipeline.Kind, done int) {
		if c.Progress != nil {
			c.Progress(pipeline.Event{Time: time.Now(), Stage: st, Kind: k, Done: int64(done), Total: int64(total), Unit: pipeline.UnitFrames})
		}
	}
	emit(pipeline.StageRender, pipeline.KindStarted, 0)
	emit(pipeline.StageEncodeVideo, pipeline.KindStarted, 0)
	workers := c.Workers
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	workers = max(1, min(workers, len(segs)))
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	runs := make([]string, len(segs))
	jobs := make(chan int)
	done := make(chan error)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if err := writeSegment(rctx, c, segs[i], runs[i], done); err != nil {
					done <- err
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for i := range segs {
			runs[i] = filepath.Join(tmp, fmt.Sprintf("run-%03d%s", i, ext))
			select {
			case jobs <- i:
			case <-rctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(done) }()
	var firstErr error
	n := 0
	for err := range done {
		if err != nil {
			if firstErr == nil {
				firstErr = err
				cancel()
			}
			continue
		}
		n++
		emit(pipeline.StageRender, pipeline.KindProgress, n)
		emit(pipeline.StageEncodeVideo, pipeline.KindProgress, n)
	}
	if firstErr == nil {
		firstErr = ctx.Err()
	}
	if firstErr != nil {
		return firstErr
	}
	emit(pipeline.StageRender, pipeline.KindFinished, total)
	part := filepath.Join(filepath.Dir(c.Output), "."+filepath.Base(tmp)+".part")
	defer func() { _ = os.Remove(part) }()
	if err := join.Concat(ctx, runs, part, VideoFPS); err != nil {
		return err
	}
	if err := os.Rename(part, c.Output); err != nil {
		return err
	}
	emit(pipeline.StageEncodeVideo, pipeline.KindFinished, total)
	return nil
}

// writeSegment renders and encodes segment s to path, sending nil on done for each video frame
// written. Every code frame is FORMAT.md's, its codewords all zero, so that its data cells hold
// their scramble bits alone.
func writeSegment(ctx context.Context, c MakeConfig, s Segment, path string, done chan<- error) (err error) {
	r, err := render.New(s.GridX, s.GridY, s.Cell, s.Mode)
	if err != nil {
		return err
	}
	m, err := frame.NewMapping(s.Header(s.FirstIndex))
	if err != nil {
		return err
	}
	w, h := r.Size()
	enc, err := c.Backend.NewEncoder(ctx, path, video.EncodeConfig{Width: w, Height: h, FPS: VideoFPS,
		Quantiser: video.DefaultQuantiser, BitDepth: 8})
	if err != nil {
		return err
	}
	defer func() {
		if cerr := enc.Close(); err == nil {
			err = cerr
		}
	}()
	cws := make([][]byte, m.CodewordsPerFrame())
	for i := range cws {
		cws[i] = make([]byte, (m.Sent()+7)/8)
	}
	mapper := render.NewMapper(m)
	symbols := make([]uint8, len(r.Layout().DataCells()))
	f := &video.Frame{Y: make([]uint16, w*h), Cb: make([]uint16, w*h/4), Cr: make([]uint16, w*h/4)}
	repeat := VideoFPS / s.Rate
	v := 0
	for k := range s.CodeFrames {
		idx := s.FirstIndex + uint32(k) //nolint:gosec // within the slot
		// The symbols, codewords and frame are sized for the mapping and renderer, so neither fails.
		_ = mapper.Symbols(symbols, idx, cws)
		_ = r.Render(f, s.Header(idx), symbols)
		for range repeat {
			if err := ctx.Err(); err != nil {
				return err
			}
			f.Index = v
			v++
			if err := enc.WriteFrame(f); err != nil {
				return err
			}
			done <- nil
		}
	}
	return nil
}
