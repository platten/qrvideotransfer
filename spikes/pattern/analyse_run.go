package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/platten/qrvideotransfer/internal/video"
	"github.com/platten/qrvideotransfer/internal/vision"
)

// recording is what the analysis reports of the file itself.
type recording struct {
	Label       string  `json:"label"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	BitDepth    int     `json:"bit_depth"`
	Transfer    string  `json:"transfer"`
	FPS         float64 `json:"fps"`
	Seconds     float64 `json:"seconds"`
	Mbps        float64 `json:"mbit_per_s"`
	Decoded     int     `json:"frames_decoded"`
	WallSeconds float64 `json:"analysis_seconds"`
	PeakPending int     `json:"peak_open_code_frames"`
	report
}

// runAnalysis decodes the recording at path with the system ffmpeg (a recording the user made, as
// the native backend is meant for: docs/SECURITY.md section 4) and analyses every capture on
// workers goroutines. The file is named in nothing it writes: label stands for it.
func runAnalysis(ctx context.Context, path, patterns, label, jsonOut string, workers int, out io.Writer) error {
	plans, err := loadPlans(patterns)
	if err != nil {
		return err
	}
	if len(plans) == 0 {
		return errors.New("no pattern-*.json in " + patterns)
	}
	be, err := video.NewSystem("system")
	if err != nil {
		return err
	}
	info, err := be.Probe(ctx, path)
	if err != nil {
		return err
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	fps := float64(info.FPSNum) / float64(info.FPSDen)
	rec := recording{Label: label, Width: info.Width, Height: info.Height, BitDepth: info.BitDepth, Transfer: info.Transfer,
		FPS: fps, Seconds: info.Duration.Seconds(), Mbps: float64(st.Size()) * 8 / info.Duration.Seconds() / 1e6}
	dec, err := be.NewDecoder(ctx, path, video.DecodeConfig{BitDepth: info.BitDepth})
	if err != nil {
		return err
	}
	defer dec.Close()
	a := newAnalyser(plans)
	agg := newAggregator(a, fps)
	t0 := time.Now()
	frames := make(chan *video.Frame, workers)
	results := make(chan capResult, workers)
	pool := sync.Pool{New: func() any { return &video.Frame{} }}
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var det vision.FinderDetector
			for f := range frames {
				results <- a.capture(f, &det, f.Index)
				pool.Put(f)
			}
		}()
	}
	var decErr error
	go func() {
		defer close(frames)
		for i := 0; ; i++ {
			f := pool.Get().(*video.Frame)
			if err := dec.Next(f); err != nil {
				if !errors.Is(err, io.EOF) {
					decErr = err
				}
				return
			}
			f.Index = i
			frames <- f
		}
	}()
	go func() { wg.Wait(); close(results) }()
	for r := range results {
		agg.add(r)
		if n := agg.counts.captures; n%500 == 0 {
			fmt.Fprintf(os.Stderr, "%s: %d captures, %.0f s\n", label, n, time.Since(t0).Seconds())
		}
	}
	if decErr != nil {
		return decErr
	}
	rec.report = agg.finish()
	rec.Decoded = agg.counts.captures
	rec.WallSeconds = time.Since(t0).Seconds()
	rec.PeakPending = agg.maxPending
	printRecording(out, &rec)
	if jsonOut != "" {
		b, _ := json.MarshalIndent(rec, "", "  ")
		if err := os.WriteFile(jsonOut, append(b, '\n'), 0o600); err != nil {
			return err
		}
	}
	return nil
}

var modNames = map[uint8]string{1: "Gray2", 2: "Gray4", 3: "RGB8"}

// netRate is the estimated net rate of a segment in Mbit/s: delivered bits per cell x data cells per
// frame x display rate x 0.75 (docs/IMPLEMENTATION_PROMPT.md section 6), with the bits delivered
// over the located captures' passes.
func netRate(s *segReport) float64 {
	return s.Sum.Delivered * float64(s.Cells) * float64(s.Rate) * 0.75 / 1e6
}

func printRecording(w io.Writer, r *recording) {
	fmt.Fprintf(w, "%s: %dx%d, %d-bit %s, %.2f fps, %.1f s, %.1f Mbit/s; pattern %s; analysed in %.0f s\n\n",
		r.Label, r.Width, r.Height, r.BitDepth, r.Transfer, r.FPS, r.Seconds, r.Mbps, r.Plan, r.WallSeconds)
	c := r.Counts
	fmt.Fprintf(w, "Captures: %d; located %d; header read %d; placed %d (%d at a segment boundary); mesh fitted %d\n\n",
		c["total"], c["located"], c["header_read"], c["placed"], c["segment_boundary"], c["mesh_fitted"])
	fmt.Fprintln(w, "| Rate | Cell | Mod | Located | Camera/display | Px/cell | Clean/capture | Clean/code frame | Clipped | Glare | Bit errors | Delivered bits/cell (located) | Delivered bits/cell (all shown) | Net Mbit/s | Net/bitrate |")
	fmt.Fprintln(w, "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	for i := range r.Segments {
		s := &r.Segments[i]
		ber := 0.0
		if s.Sum.Stated > 0 {
			ber = float64(s.Sum.Wrong) / float64(s.Sum.Stated)
		}
		nr := netRate(s)
		fmt.Fprintf(w, "| %d | %d | %s | %d/%d | %.2f | %.2f | %.3f | %.3f | %.4f | %.4f | %.2e | %.3f | %.3f | %.2f | %.1f%% |\n",
			s.Rate, s.Cell, modNames[s.Modulation], s.Captures, s.ByTime, s.RateRatio, s.PxPerCell, s.CleanCapture, s.Sum.CleanFrame,
			s.Clipped, s.Glare, ber, s.Sum.Delivered, s.DeliveredAll, nr, 100*nr/r.Mbps)
	}
	fmt.Fprintln(w)
}
