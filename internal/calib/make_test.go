package calib

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/pipeline"
	"github.com/platten/qrvideotransfer/internal/render"
	"github.com/platten/qrvideotransfer/internal/video"
)

// shortSlots shortens every slot to n code frames for the rest of the test, so that a test video
// stays small; Recover and the plan follow.
func shortSlots(t *testing.T, n int) {
	t.Helper()
	old, oldF := slots, TotalFrames
	slots = makeSlots(func(int) int { return n })
	TotalFrames = framesOf(slots)
	t.Cleanup(func() { slots, TotalFrames = old, oldF })
}

// readAll reads every frame of a video with backend b.
func readAll(t *testing.T, b video.Backend, path string) []*video.Frame {
	t.Helper()
	ctx := context.Background()
	info, err := b.Probe(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	d, err := b.NewDecoder(ctx, path, video.DecodeConfig{BitDepth: info.BitDepth})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	var out []*video.Frame
	for {
		f := &video.Frame{}
		if err := d.Next(f); errors.Is(err, io.EOF) {
			return out
		} else if err != nil {
			t.Fatal(err)
		}
		out = append(out, f)
	}
}

// codeFrame renders segment s's code frame idx as Make does.
func codeFrame(t *testing.T, s Segment, idx uint32) *video.Frame {
	t.Helper()
	r, err := render.New(s.GridX, s.GridY, s.Cell, s.Mode)
	if err != nil {
		t.Fatal(err)
	}
	m, err := frame.NewMapping(s.Header(idx))
	if err != nil {
		t.Fatal(err)
	}
	cws := make([][]byte, m.CodewordsPerFrame())
	for i := range cws {
		cws[i] = make([]byte, (m.Sent()+7)/8)
	}
	sym := make([]uint8, len(r.Layout().DataCells()))
	if err := render.NewMapper(m).Symbols(sym, idx, cws); err != nil {
		t.Fatal(err)
	}
	w, h := r.Size()
	f := &video.Frame{Y: make([]uint16, w*h), Cb: make([]uint16, w*h/4), Cr: make([]uint16, w*h/4)}
	if err := r.Render(f, s.Header(idx), sym); err != nil {
		t.Fatal(err)
	}
	return f
}

func sameFrame(a, b *video.Frame) bool {
	if a.W != b.W || a.H != b.H || len(a.Y) != len(b.Y) {
		return false
	}
	for i := range a.Y {
		if a.Y[i] != b.Y[i] {
			return false
		}
	}
	for i := range a.Cb {
		if a.Cb[i] != b.Cb[i] || a.Cr[i] != b.Cr[i] {
			return false
		}
	}
	return true
}

// Make writes every segment of the plan in order, each code frame shown 60 / rate times, its data
// cells holding their scramble bits alone, and every frame's header recovers to the plan: checked
// frame by frame against the renderer with the lossless raw backend, on 848 x 480 with slots of 4
// code frames (9 segments, 96 video frames). The progress events count every video frame, and the
// temporary folder is left empty.
func TestMake(t *testing.T) {
	shortSlots(t, 4)
	tmp := t.TempDir()
	out := filepath.Join(t.TempDir(), "cal.raw")
	be := video.NewRaw()
	var events []pipeline.Event
	if err := Make(context.Background(), MakeConfig{Output: out, Width: 848, Height: 480, Backend: be, Workers: 3, TempDir: tmp,
		Progress: func(e pipeline.Event) { events = append(events, e) }}); err != nil {
		t.Fatal(err)
	}
	p, _ := NewPlan(848, 480)
	frames := readAll(t, be, out)
	if len(frames) != p.VideoFrames() || p.VideoFrames() != 96 {
		t.Fatalf("%d video frames, the plan has %d", len(frames), p.VideoFrames())
	}
	v := 0
	for _, s := range p.Segments {
		for k := range s.CodeFrames {
			want := codeFrame(t, s, s.FirstIndex+uint32(k)) //nolint:gosec // small
			for range VideoFPS / s.Rate {
				if !sameFrame(frames[v], want) {
					t.Fatalf("video frame %d is not code frame %d of segment %+v", v, s.FirstIndex+uint32(k), s) //nolint:gosec // small
				}
				v++
			}
		}
		if _, rs, err := Recover(s.Header(s.FirstIndex)); err != nil || rs != s {
			t.Errorf("segment %+v: %v", s, err)
		}
	}
	last := events[len(events)-1]
	if last.Stage != pipeline.StageEncodeVideo || last.Kind != pipeline.KindFinished || last.Done != 96 || last.Total != 96 {
		t.Errorf("last event %+v", last)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Errorf("the temporary folder holds %v", left)
	}
	if beside, _ := os.ReadDir(filepath.Dir(out)); len(beside) != 1 {
		t.Errorf("beside the output: %v", beside)
	}
}

// A cancelled Make returns the context's error and leaves neither temporary files nor an output.
func TestMakeCancel(t *testing.T) {
	shortSlots(t, 4)
	tmp, dir := t.TempDir(), t.TempDir()
	out := filepath.Join(dir, "cal.raw")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := 0
	err := Make(ctx, MakeConfig{Output: out, Width: 848, Height: 480, Backend: video.NewRaw(), Workers: 2, TempDir: tmp,
		Progress: func(e pipeline.Event) {
			if e.Kind == pipeline.KindProgress {
				if n++; n == 5 {
					cancel()
				}
			}
		}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	for _, d := range []string{tmp, dir} {
		if left, _ := os.ReadDir(d); len(left) != 0 {
			t.Errorf("%s holds %v", d, left)
		}
	}
}

// Make refuses what it cannot make, before writing anything.
func TestMakeRefuses(t *testing.T) {
	tmp := t.TempDir()
	out := filepath.Join(tmp, "cal.raw")
	be := video.NewRaw()
	for name, c := range map[string]MakeConfig{
		"no output":  {Width: 848, Height: 480, Backend: be},
		"no backend": {Output: out, Width: 848, Height: 480},
		"too small":  {Output: out, Width: 400, Height: 300, Backend: be},
		"no joiner":  {Output: out, Width: 848, Height: 480, Backend: noJoin{be}},
	} {
		c.TempDir = tmp
		if err := Make(context.Background(), c); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if err := Make(context.Background(), MakeConfig{Output: out, Width: 400, Height: 300, Backend: be}); !errors.Is(err, ErrPlan) {
		t.Errorf("too small: %v, want ErrPlan", err)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Errorf("left %v", left)
	}
}

// noJoin is a backend that cannot join runs.
type noJoin struct{ video.Backend }

// A plan's length, frames and estimate: 4 s and 240 video frames per segment, EncodeCost per
// million pixels spread over at most one worker per segment.
func TestPlanLength(t *testing.T) {
	p, _ := NewPlan(1920, 1080)
	if p.VideoFrames() != 26*240 || p.Duration().Seconds() != 104 {
		t.Errorf("%d frames, %v", p.VideoFrames(), p.Duration())
	}
	one, all := p.Estimate(1), p.Estimate(16)
	if want := EncodeCost * 1920 * 1080 / 1e6 * 26 * 240; one.Seconds() < want*0.999 || one.Seconds() > want*1.001 || all*16 > one*1001/1000 || all*16 < one*999/1000 {
		t.Errorf("estimates %v and %v", one, all)
	}
	if p.Estimate(1000) != p.Estimate(26) || p.Estimate(0) <= 0 {
		t.Error("more workers than segments")
	}
}
