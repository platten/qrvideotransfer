package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/video"
)

// ScanConfig are the inputs of [Scan].
type ScanConfig struct {
	Recordings []string
	Backend    video.Backend
	Workers    int // 0: GOMAXPROCS, reduced to fit the decode's memory budget as Decode's are
	Progress   func(Event)
}

// Capture is what [Scan] reports of one capture: what the vision chain of DESIGN.md section 10
// found in it, before anything is summed across captures.
type Capture struct {
	Recording int           // index of the recording in ScanConfig.Recordings
	Index     int           // the capture's place in its recording, from 0
	PTS       time.Duration // its presentation time, as the video backend reports it
	// Finders is set when a quad's header was tried, whether or not one was read.
	Finders bool
	// Headers are the frame headers read, every copy that decoded and validated (FORMAT.md section
	// 6); nil when none was. Reject is then the strongest refusal (vision.SlotReader.ReadReason).
	Headers []frame.Header
	Reject  frame.HeaderError
	// Located is set when a header was read and the mesh fitted to its grid; the fields below are
	// zero otherwise.
	Located bool
	// Phases are the code frames the capture shows, of the first header's transfer.
	Phases []Phase
	// Cells are the data cells of the header's grid, and Clean those the phase field credits to a
	// single code frame, summed over the phases.
	Cells, Clean int
	// PixelsPerCell is the camera pixels per cell side, from the fitted mesh: the square root of
	// the camera pixels the grid covers over its cells.
	PixelsPerCell float64
	// PaletteClipped is the share of the 32 palette patches (FORMAT.md section 4.3) whose sampling
	// clipped (modem.Obs.Saturated).
	PaletteClipped float64
}

// Phase is one code frame's soft bits in a capture.
type Phase struct {
	Frame uint32 // the code frame's index
	// Soft are its soft bits by bit position (FORMAT.md section 9), descrambled: positive means
	// codeword bit 0. Bit t of codeword c is at frame.Mapping.Position(Frame, c, t).
	Soft []int8
}

// ScanResult describes a finished scan.
type ScanResult struct {
	Captures int64            // captures delivered to the sink
	Probes   []video.ClipInfo // each recording read, in order
	Warnings []Warning
}

// Scan reads the recordings through the vision workers as [Decode] does and hands every capture
// to sink in order, recording after recording. Sink runs on the goroutine that called Scan; it may
// keep the Capture and its slices. Reading stops, and Scan returns, when sink returns stop or an
// error. Scan sums nothing and writes nothing: what to keep is the sink's.
func Scan(ctx context.Context, c ScanConfig, sink func(*Capture) (stop bool, err error)) (*ScanResult, error) {
	switch {
	case len(c.Recordings) == 0:
		return nil, errors.New("pipeline: no recordings")
	case c.Backend == nil:
		return nil, errors.New("pipeline: no video backend")
	case sink == nil:
		return nil, errors.New("pipeline: no sink")
	}
	res := &ScanResult{}
	s := &scanRun{backend: c.Backend, workers: c.Workers, emit: emitter(c.Progress), process: scanCapture}
	s.warn = func(st Stage, code, text string) {
		w := Warning{Code: code, Text: text}
		res.Warnings = append(res.Warnings, w)
		s.emit.warn(st, w)
	}
	for i, rec := range c.Recordings {
		stopped := false
		base := s.captures
		read, info, err := s.recording(ctx, rec, func(r *captureResult, read, frames int64) (bool, error) {
			stop, err := sink(r.capture(i, r.index-base))
			if err != nil {
				return false, err
			}
			res.Captures++
			s.emit.stage(StageDecodeVideo, KindProgress, read, frames, UnitFrames)
			stopped = stop
			return stop, nil
		})
		if info != nil {
			res.Probes = append(res.Probes, *info)
		}
		if err != nil {
			return res, err
		}
		s.emit.stage(StageDecodeVideo, KindFinished, read, info.Frames, UnitFrames)
		if stopped {
			break
		}
	}
	if eb, ok := c.Backend.(*video.Embedded); ok {
		if err := eb.InProcess(); err != nil {
			s.warn(StageDecodeVideo, WarnEngineInProcess, err.Error())
		}
	}
	return res, nil
}

// capture is r as Scan reports it, the capture being the index-th of recording rec.
func (r *captureResult) capture(rec, index int) *Capture {
	c := &Capture{Recording: rec, Index: index, PTS: r.pts, Finders: r.finders, Headers: r.headers, Reject: r.reject,
		Located: r.located, Cells: r.cells, Clean: r.clean, PixelsPerCell: r.pixelsPerCell, PaletteClipped: r.clipped}
	for _, ph := range r.phases {
		c.Phases = append(c.Phases, Phase{Frame: ph.frame, Soft: ph.soft})
	}
	return c
}

// scanCapture runs the vision chain on one capture for Scan; tests replace it.
var scanCapture = (*captureWorker).scan

// scan is process for Scan: the soft bits stay by bit position.
func (w *captureWorker) scan(f *video.Frame, index int) captureResult {
	r := captureResult{index: index, pts: f.PTS}
	w.see(f, &r, func(_ *geometry, idx uint32, soft []int8) {
		r.phases = append(r.phases, phaseBits{frame: idx, soft: append([]int8(nil), soft...)})
	})
	return r
}

// scanRun reads recordings through the vision workers, for Decode and Scan alike. Only the
// goroutine that runs it touches it.
type scanRun struct {
	backend video.Backend
	workers int // asked for; 0: GOMAXPROCS
	emit    emitter
	warn    func(s Stage, code, text string)
	// process runs the vision chain on one capture; nil: processCapture, read when the workers
	// start.
	process func(*captureWorker, *video.Frame, int) captureResult

	captures   int        // captures processed, across recordings, with a gap between recordings
	maxWaiting int        // most results held for in-order application at once
	gate       *stageGate // the last recording's grid-stage gate, for tests
}

// recording reads one recording through the vision workers and hands every capture's result to
// apply in capture order, its index counted across recordings, with the captures read so far and
// the frames the probe announced. Once apply returns stop or an error, reading stops and the
// results still on their way are dropped. It returns the captures read and the probe's report
// (nil when the probe failed). A read error is returned only when apply did not stop.
func (s *scanRun) recording(ctx context.Context, path string, apply func(r *captureResult, read, frames int64) (stop bool, err error)) (int64, *video.ClipInfo, error) {
	info, err := s.backend.Probe(ctx, path)
	if err != nil {
		return 0, nil, err
	}
	// The workers' captures and scratch must fit the decode's memory budget (memory.go).
	workers, reduced := decodeWorkers(s.workers, info.Width, info.Height)
	if reduced {
		s.warn(StageDecodeVideo, WarnWorkersReduced, fmt.Sprintf("%d workers asked for; %d, the most whose %d x %d captures fit "+
			"the decode's memory budget, are used.", s.workers, workers, info.Width, info.Height))
	}
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	dec, err := s.backend.NewDecoder(rctx, path, video.DecodeConfig{BitDepth: info.BitDepth})
	if err != nil {
		return 0, &info, err
	}
	defer func() { _ = dec.Close() }()
	s.emit.stage(StageDecodeVideo, KindStarted, 0, info.Frames, UnitFrames)

	type job struct {
		f     *video.Frame
		index int
	}
	free := make(chan *video.Frame, workers+2)
	for range workers + 2 {
		free <- &video.Frame{}
	}
	// A capture is read only with a token, returned once its result has been applied in order, so
	// a stalled worker holds back the reading and at most workers+2 results wait to be applied.
	tokens := make(chan struct{}, workers+2)
	jobs := make(chan job)
	results := make(chan captureResult, workers)
	var readErr error
	go func() {
		defer close(jobs)
		for i := 0; ; i++ {
			select {
			case tokens <- struct{}{}:
			case <-rctx.Done():
				return
			}
			var f *video.Frame
			select {
			case f = <-free:
			case <-rctx.Done():
				return
			}
			if err := dec.Next(f); err != nil {
				if !errors.Is(err, io.EOF) {
					readErr = err
				}
				return
			}
			select {
			case jobs <- job{f, i}:
			case <-rctx.Done():
				return
			}
		}
	}()
	// The grid stage's memory is admitted by a gate sized from what the captures leave of the
	// budget (memory.go), against each capture's own grid: a grid change mid-recording cannot raise
	// it.
	gate := newStageGate(stageBudget(workers, info.Width, info.Height))
	s.gate = gate
	process := s.process
	if process == nil {
		process = processCapture
	}
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := captureWorker{enter: func(cells int) (func(), bool) {
				release, err := gate.enter(rctx, cells)
				return release, err == nil
			}}
			for j := range jobs {
				r := process(&w, j.f, j.index)
				free <- j.f
				results <- r
			}
		}()
	}
	go func() { wg.Wait(); close(results) }()

	var applyErr error
	stopped := false
	base := s.captures
	waiting := map[int]captureResult{}
	next, read := 0, int64(0)
	for r := range results {
		waiting[r.index] = r
		s.maxWaiting = max(s.maxWaiting, len(waiting))
		for {
			r, ok := waiting[next]
			if !ok {
				break
			}
			delete(waiting, next)
			<-tokens
			next++
			read++
			if stopped || rctx.Err() != nil {
				continue // drain
			}
			r.index += base
			stop, err := apply(&r, read, info.Frames)
			if err != nil {
				cancel()
				applyErr = err
				continue
			}
			if stop {
				stopped = true
				cancel()
			}
		}
	}
	s.captures = base + next + 2 // captures of different recordings are never adjacent
	if err := ctx.Err(); err != nil {
		return read, &info, err
	}
	if applyErr != nil {
		return read, &info, applyErr
	}
	if readErr != nil && !stopped {
		return read, &info, readErr
	}
	return read, &info, nil
}
