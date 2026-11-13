// Command pattern writes the S2 test pattern pattern-<W>x<H>.mp4 (see
// README.md and docs/IMPLEMENTATION_PROMPT.md section 5): real FORMAT.md
// code frames whose data cells hold only their scramble bits, in segments
// of about 4 s for each display rate, cell size and modulation, encoded
// at 60 fps with the embedded engine. It then verifies the file.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const fps = 60

// segment is one run of code frames with one display rate, cell size and
// modulation.
type segment struct {
	Rate       int     `json:"rate"`        // code frames per second
	Cell       int     `json:"cell"`        // pitch P in pixels
	Modulation uint8   `json:"modulation"`  // FORMAT.md section 7
	FirstIndex uint32  `json:"first_index"` // frame index of its first code frame
	CodeFrames int     `json:"code_frames"`
	FirstVideo int     `json:"first_video_frame"`
	Start      float64 `json:"start_seconds"`
	GridX      int     `json:"grid_x"`
	GridY      int     `json:"grid_y"`
}

// plan is the whole file.
type plan struct {
	Width       int       `json:"width"`
	Height      int       `json:"height"`
	FPS         int       `json:"fps"`
	Session     string    `json:"session"`
	InnerCode   int       `json:"inner_code"`
	TotalFrames uint32    `json:"total_frames"` // F
	VideoFrames int       `json:"video_frames"`
	Segments    []segment `json:"segments"`
}

func makePlan(w, h int, seconds int) plan {
	pl := plan{Width: w, Height: h, FPS: fps, Session: "5332", InnerCode: innerCode}
	var idx uint32
	video := 0
	for _, r := range []int{12, 30, 60} {
		for _, p := range []int{4, 8, 12} {
			if w%p != 0 || h%p != 0 {
				continue
			}
			for _, m := range []uint8{1, 2, 3} {
				n := r * seconds
				pl.Segments = append(pl.Segments, segment{Rate: r, Cell: p, Modulation: m, FirstIndex: idx,
					CodeFrames: n, FirstVideo: video, Start: float64(video) / fps, GridX: w / p, GridY: h / p})
				idx += uint32(n) //nolint:gosec // small
				video += n * fps / r
			}
		}
	}
	// F is the code-frame count rounded up to a multiple of 4; the last
	// segment is lengthened to match.
	last := &pl.Segments[len(pl.Segments)-1]
	for idx%4 != 0 {
		last.CodeFrames++
		idx++
		video += fps / last.Rate
	}
	pl.TotalFrames, pl.VideoFrames = idx, video
	return pl
}

// videoFrame returns the segment and frame index of video frame v.
func (pl *plan) videoFrame(v int) (int, uint32) {
	for i := len(pl.Segments) - 1; i >= 0; i-- {
		s := pl.Segments[i]
		if v >= s.FirstVideo {
			return i, s.FirstIndex + uint32((v-s.FirstVideo)/(fps/s.Rate)) //nolint:gosec // small
		}
	}
	panic("video frame before the first segment")
}

func main() {
	w := flag.Int("w", 1920, "width")
	h := flag.Int("h", 1080, "height")
	out := flag.String("out", "spikes/pattern/out", "output directory")
	seconds := flag.Int("seconds", 4, "seconds per segment")
	workers := flag.Int("workers", max(1, runtime.NumCPU()/2), "parallel engine instances")
	qp := flag.Int("qp", 32, "x264 quantiser")
	verifyOnly := flag.Bool("verify-only", false, "only verify an existing file")
	table := flag.Bool("table", false, "print the segment table as Markdown and exit")
	flag.Parse()
	if *table {
		printTable(makePlan(*w, *h, *seconds))
		return
	}
	if err := run(*w, *h, *out, *seconds, *workers, *qp, *verifyOnly); err != nil {
		fmt.Fprintln(os.Stderr, "pattern:", err)
		os.Exit(1)
	}
}

func run(w, h int, out string, seconds, workers, qp int, verifyOnly bool) error {
	ctx := context.Background()
	out, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	pl := makePlan(w, h, seconds)
	name := fmt.Sprintf("pattern-%dx%d", w, h)
	mp4 := filepath.Join(out, name+".mp4")
	painters, err := makePainters(&pl)
	if err != nil {
		return err
	}
	if !verifyOnly {
		e, err := newEngine(ctx)
		if err != nil {
			return err
		}
		t0 := time.Now()
		if err := encode(ctx, e, &pl, painters, out, name, workers, qp); err != nil {
			return err
		}
		fmt.Printf("%s: encoded %d video frames (%d code frames) in %s\n", name, pl.VideoFrames, pl.TotalFrames, time.Since(t0).Round(time.Second))
		j, _ := json.MarshalIndent(pl, "", "  ")
		if err := os.WriteFile(filepath.Join(out, name+".json"), append(j, '\n'), 0o644); err != nil {
			return err
		}
	}
	return verify(ctx, &pl, painters, mp4)
}

func printTable(pl plan) {
	fmt.Printf("`pattern-%dx%d.mp4`: %d video frames (%.1f s), F = %d code frames.\n\n", pl.Width, pl.Height, pl.VideoFrames, float64(pl.VideoFrames)/fps, pl.TotalFrames)
	fmt.Println("| # | Start (s) | Rate (fps) | Cell (px) | Grid | Modulation | First frame index | Code frames |")
	fmt.Println("| --- | --- | --- | --- | --- | --- | --- | --- |")
	names := map[uint8]string{1: "1 Gray2", 2: "2 Gray4", 3: "3 RGB8"}
	for i, s := range pl.Segments {
		fmt.Printf("| %d | %.1f | %d | %d | %d x %d | %s | %d | %d |\n", i, s.Start, s.Rate, s.Cell, s.GridX, s.GridY, names[s.Modulation], s.FirstIndex, s.CodeFrames)
	}
	fmt.Println()
}

func makePainters(pl *plan) ([]*painter, error) {
	ps := make([]*painter, len(pl.Segments))
	for i, s := range pl.Segments {
		p, err := newPainter(pl.Width, pl.Height, s.Cell, s.Modulation, pl.TotalFrames)
		if err != nil {
			return nil, fmt.Errorf("segment %d: %w", i, err)
		}
		ps[i] = p
	}
	return ps, nil
}

// chunkLen is the number of video frames each engine instance encodes in
// one run. It is even: x264 alternates idr_pic_id between 0 and 1, so
// concatenated chunks never place two IDR pictures with the same id next
// to each other.
const chunkLen = 120

// encode paints and encodes the video in chunks on workers instances,
// concatenates the elementary streams and wraps them in MP4 with one copy
// pass (DESIGN.md section 12).
func encode(ctx context.Context, e *engine, pl *plan, painters []*painter, out, name string, workers, qp int) error {
	tmp := filepath.Join(out, name+".chunks")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	tags := []string{"-color_range", "tv", "-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709"}
	nChunks := (pl.VideoFrames + chunkLen - 1) / chunkLen
	jobs := make(chan int)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	var done sync.Mutex
	finished := 0
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				if err := encodeChunk(ctx, e, pl, painters, tmp, c, qp, tags); err != nil {
					errs <- err
					return
				}
				done.Lock()
				finished++
				if finished%10 == 0 || finished == nChunks {
					fmt.Printf("%s: %d/%d chunks\n", name, finished, nChunks)
				}
				done.Unlock()
			}
		}()
	}
	var err error
feed:
	for c := range nChunks {
		select {
		case jobs <- c:
		case err = <-errs:
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if err == nil {
		select {
		case err = <-errs:
		default:
		}
	}
	if err != nil {
		return err
	}
	all, err := os.Create(filepath.Join(tmp, "all.h264"))
	if err != nil {
		return err
	}
	for c := range nChunks {
		f, err := os.Open(filepath.Join(tmp, fmt.Sprintf("c%05d.h264", c)))
		if err != nil {
			return err
		}
		_, err = io.Copy(all, f)
		f.Close()
		if err != nil {
			return err
		}
	}
	if err := all.Close(); err != nil {
		return err
	}
	var eb bytes.Buffer
	rc, err := e.run(ctx, "ffmpeg", out, []string{"-nostdin", "-hide_banner", "-loglevel", "error",
		"-framerate", fmt.Sprint(fps), "-f", "h264", "-i", "/d/" + name + ".chunks/all.h264",
		"-c", "copy", "-an", "-use_editlist", "0", "-movflags", "+faststart", "-y", "/d/" + name + ".mp4"}, nil, nil, &eb)
	if err != nil || rc != 0 {
		return fmt.Errorf("mux: rc=%d err=%v %s", rc, err, eb.String())
	}
	return nil
}

func encodeChunk(ctx context.Context, e *engine, pl *plan, painters []*painter, dir string, c, qp int, tags []string) error {
	v0, v1 := c*chunkLen, min((c+1)*chunkLen, pl.VideoFrames)
	pr, pw := io.Pipe()
	go func() {
		buf := make([]byte, painters[0].frameSize())
		lastSeg, lastIdx := -1, uint32(0)
		for v := v0; v < v1; v++ {
			si, idx := pl.videoFrame(v)
			if si != lastSeg || idx != lastIdx {
				painters[si].render(idx, buf)
				lastSeg, lastIdx = si, idx
			}
			if _, err := pw.Write(buf); err != nil {
				return
			}
		}
		pw.Close()
	}()
	args := []string{"-hide_banner", "-loglevel", "error",
		"-f", "rawvideo", "-pix_fmt", "yuv420p", "-s", fmt.Sprintf("%dx%d", pl.Width, pl.Height), "-r", fmt.Sprint(fps)}
	args = append(args, tags...)
	args = append(args, "-i", "pipe:0", "-c:v", "libx264", "-preset", "veryfast", "-profile:v", "high", "-qp", fmt.Sprint(qp),
		"-x264-params", "keyint=1:scenecut=0:bframes=0:cabac=1:no-deblock=1:aq-mode=0:psy=0:trellis=0:partitions=i4x4:no-8x8dct=1")
	args = append(args, tags...)
	args = append(args, "-pix_fmt", "yuv420p", "-f", "h264", "-y", fmt.Sprintf("/d/c%05d.h264", c))
	var eb bytes.Buffer
	rc, err := e.run(ctx, "ffmpeg", dir, args, pr, nil, &eb)
	pr.CloseWithError(errors.New("encoder finished"))
	if err != nil || rc != 0 {
		return fmt.Errorf("chunk %d: rc=%d err=%v %s", c, rc, err, strings.TrimSpace(eb.String()))
	}
	return nil
}
