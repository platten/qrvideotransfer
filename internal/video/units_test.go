package video

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestRawRoundTrip: frames written by the raw-frame backend come back identical, whole or in slices.
func TestRawRoundTrip(t *testing.T) {
	t.Parallel()
	r := NewRaw()
	for _, bits := range []int{8, 10} {
		path := filepath.Join(t.TempDir(), "a.raw")
		e, err := r.NewEncoder(t.Context(), path, EncodeConfig{Width: 32, Height: 16, FPS: 12, BitDepth: bits})
		if err != nil {
			t.Fatal(err)
		}
		var want []*Frame
		for i := range 24 {
			f, _ := codeFrame(32, 16, 4, 3, i)
			if bits == 10 {
				for k := range f.Y {
					f.Y[k] = f.Y[k]*4 + uint16(k%4)
				}
			}
			f.Cb[0], f.Bits = uint16(i), uint8(bits)
			if err := e.WriteFrame(f); err != nil {
				t.Fatal(err)
			}
			want = append(want, f)
		}
		if err := e.Close(); err != nil {
			t.Fatal(err)
		}
		info, err := r.Probe(t.Context(), path)
		if err != nil || info.Frames != 24 || info.Width != 32 || info.BitDepth != bits || info.Duration != 2*time.Second || info.FPSNum != 12 {
			t.Fatalf("probe %+v %v", info, err)
		}
		d, err := r.NewDecoder(t.Context(), path, DecodeConfig{})
		if err != nil {
			t.Fatal(err)
		}
		if d.Info() != info {
			t.Errorf("info %+v", d.Info())
		}
		got := decodeAll(t, d)
		if len(got) != 24 {
			t.Fatalf("%d frames", len(got))
		}
		for i, f := range got {
			if f.Index != i || f.PTS != time.Duration(i)*time.Second/12 || int(f.Bits) != bits ||
				!slices.Equal(f.Y, want[i].Y) || !slices.Equal(f.Cb, want[i].Cb) || !slices.Equal(f.Cr, want[i].Cr) {
				t.Fatalf("%d bits: frame %d differs", bits, i)
			}
		}
		checkSlices(t, r, path, 24, []time.Duration{0, 400 * time.Millisecond, time.Second, 1999 * time.Millisecond})
		d, err = r.NewDecoder(t.Context(), path, DecodeConfig{BrightnessOnly: true, Start: 1900 * time.Millisecond, Length: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		if got := decodeAll(t, d); len(got) != 1 || got[0].Cb != nil || got[0].Index != 23 {
			t.Errorf("brightness-only tail: %d frames", len(got))
		}
		if _, err := r.NewDecoder(t.Context(), path, DecodeConfig{BitDepth: 18 - bits}); !errors.Is(err, ErrUnsupported) {
			t.Errorf("wrong depth: %v", err)
		}
	}
}

// TestRawErrors covers the raw-frame backend's refusals.
func TestRawErrors(t *testing.T) {
	t.Parallel()
	r := NewRaw()
	dir := t.TempDir()
	good := filepath.Join(dir, "good.raw")
	e, err := r.NewEncoder(t.Context(), good, EncodeConfig{Width: 8, Height: 8, FPS: 10})
	if err != nil {
		t.Fatal(err)
	}
	f, _ := codeFrame(8, 8, 4, 1, 0)
	for range 2 {
		if err := e.WriteFrame(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.WriteFrame(&Frame{W: 8, H: 8}); !errors.Is(err, ErrFrame) {
		t.Errorf("bad frame: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(good)
	bad := map[string][]byte{
		"short":     data[:10],
		"magic":     append([]byte("XXXXXXXX"), data[8:]...),
		"partial":   data[:len(data)-1],
		"bits":      patch(data, 16, 9),
		"planes":    patch(data, 17, 1),
		"odd":       patch(data, 8, 7),
		"fps":       patch(data, 20, 0),
		"huge":      patch(data, 9, 0x40), // width 16392
		"zerowidth": patch(patch(data, 8, 0), 9, 0),
	}
	for name, b := range bad {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := r.Probe(t.Context(), p)
		var le *LimitError
		if !errors.Is(err, ErrInvalidVideo) && !errors.As(err, &le) {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := r.NewDecoder(t.Context(), p, DecodeConfig{}); err == nil {
			t.Errorf("%s: decoder opened", name)
		}
	}
	if _, err := r.Probe(t.Context(), filepath.Join(dir, "missing")); err == nil {
		t.Error("missing file probed")
	}
	if _, err := r.NewEncoder(t.Context(), filepath.Join(dir, "x", "y"), EncodeConfig{Width: 8, Height: 8, FPS: 10}); err == nil {
		t.Error("encoder in a missing directory")
	}
	if _, err := r.NewEncoder(t.Context(), good, EncodeConfig{Width: 8, Height: 8}); err == nil {
		t.Error("zero frame rate accepted")
	}
	// A file that loses its tail after opening is reported while reading.
	d, err := r.NewDecoder(t.Context(), good, DecodeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(good, rawHeaderLen+10); err != nil {
		t.Fatal(err)
	}
	var fr Frame
	if err := d.Next(&fr); !errors.Is(err, ErrInvalidVideo) {
		t.Errorf("truncated while open: %v", err)
	}
	_ = d.Close()
	if err := os.WriteFile(good, data, 0o600); err != nil { //nolint:gosec // test
		t.Fatal(err)
	}

	// Concat.
	all := filepath.Join(dir, "all.raw")
	if err := r.Concat(t.Context(), []string{good, good}, all, 10); err != nil {
		t.Fatal(err)
	}
	if info, err := r.Probe(t.Context(), all); err != nil || info.Frames != 4 {
		t.Errorf("concat: %+v %v", info, err)
	}
	other := filepath.Join(dir, "other.raw")
	e, _ = r.NewEncoder(t.Context(), other, EncodeConfig{Width: 8, Height: 8, FPS: 10, BitDepth: 10})
	f10 := *f
	f10.Bits = 10
	_ = e.WriteFrame(&f10)
	_ = e.Close()
	if err := r.Concat(t.Context(), []string{good, other}, all, 10); !errors.Is(err, ErrFrame) {
		t.Errorf("concat of different formats: %v", err)
	}
	if err := r.Concat(t.Context(), []string{filepath.Join(dir, "short")}, all, 10); err == nil {
		t.Error("concat of a bad file")
	}
	if err := r.Concat(t.Context(), nil, filepath.Join(dir, "x", "y"), 10); err == nil {
		t.Error("concat into a missing directory")
	}
	if got := frameTime(5, 0, 1); got != 0 {
		t.Errorf("frameTime without a rate: %v", got)
	}
}

func patch(b []byte, i int, v byte) []byte {
	b = slices.Clone(b)
	b[i] = v
	return b
}

// TestNew covers backend selection.
func TestNew(t *testing.T) {
	for _, spec := range []string{"", "embedded"} {
		b, err := New(spec, Options{})
		if _, ok := b.(*Embedded); !ok || err != nil {
			t.Errorf("%q: %T %v", spec, b, err)
		}
	}
	if _, err := New("system", Options{UnsandboxedHEVC: true}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("fast path with system: %v", err)
	}
	if _, err := New(filepath.Join(t.TempDir(), "no-ffmpeg"), Options{}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("missing ffmpeg: %v", err)
	}
	if _, err := NewEmbedded(Options{UnsandboxedHEVC: true}); FastPathBuilt != (err == nil) {
		t.Errorf("fast path option: %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := New("system", Options{}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("no ffmpeg on PATH: %v", err)
	}
}

// TestSystemProbeSibling: an ffmpeg path without an ffprobe beside it or on PATH is refused.
func TestSystemProbeSibling(t *testing.T) {
	dir := t.TempDir()
	name := "ffmpeg"
	if filepath.Separator == '\\' {
		name += ".exe"
	}
	ff := filepath.Join(dir, name)
	if err := os.WriteFile(ff, []byte("#!/bin/sh\nexit 3\n"), 0o700); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := NewSystem(ff); !errors.Is(err, ErrUnsupported) {
		t.Errorf("no ffprobe: %v", err)
	}
	probe := strings.Replace(ff, "ffmpeg", "ffprobe", 1)
	if err := os.WriteFile(probe, []byte("#!/bin/sh\nexit 3\n"), 0o700); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	s, err := NewSystem(ff)
	if err != nil {
		t.Skip("cannot run a test executable here: ", err)
	}
	clipPath, _ := filepath.Abs(clip("hevc444.mp4"))
	if _, err := s.Probe(t.Context(), clipPath); !errors.Is(err, ErrInvalidVideo) {
		t.Errorf("failing ffprobe: %v", err)
	}
}

// TestParseProbe feeds the probe parser hostile and edge-case output.
func TestParseProbe(t *testing.T) {
	t.Parallel()
	ok := `{"streams":[{"codec_name":"hevc","profile":"Main 10","width":3840,"height":2160,"pix_fmt":"yuv420p10le",
		"r_frame_rate":"60/1","avg_frame_rate":"0/0","duration":"2.5","start_time":"0.1","color_transfer":"arib-std-b67",
		"color_range":"tv","tags":{"rotate":"90"},
		"side_data_list":[{"side_data_type":"Display Matrix","rotation":-90},{"side_data_type":"DOVI configuration record"}]}],
		"format":{"format_name":"mov","duration":"9","bit_rate":"53000000"}}`
	p, err := parseProbe([]byte(ok))
	if err != nil {
		t.Fatal(err)
	}
	if p.BitDepth != 10 || p.FPSNum != 60 || p.FPSDen != 1 || p.Frames != 150 || p.Rotation != 270 || !p.DolbyVis ||
		p.BitRate != 53_000_000 || p.Start != 0.1 || p.Duration != 2500*time.Millisecond || p.Profile != "Main 10" {
		t.Errorf("%+v", p)
	}
	for _, bad := range []string{`{`, `{"streams":[]}`, `{"streams":[{"width":0,"height":10}]}`,
		`{"streams":[{"width":10000,"height":10}]}`, `{"streams":[{"width":10,"height":9000}]}`} {
		if _, err := parseProbe([]byte(bad)); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	// 4:2:0 chroma planes of an odd-sized picture would be mis-framed.
	for _, odd := range []string{`{"streams":[{"width":385,"height":216}]}`, `{"streams":[{"width":384,"height":215}]}`} {
		if _, err := parseProbe([]byte(odd)); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: %v", odd, err)
		}
	}
	p, err = parseProbe([]byte(`{"streams":[{"codec_name":"x\u001b[31m","width":2,"height":2,"r_frame_rate":"1000001/1",
		"nb_frames":"-5","duration":"NaN","bit_rate":"-1","tags":{"rotate":"-90"},"side_data_list":[{"rotation":1e300}]}],
		"format":{"duration":"5"}}`))
	if err != nil || p.Codec != "x [31m" || p.FPSNum != 0 || p.Frames != 0 || p.Duration != 5*time.Second || p.BitRate != 0 || p.Rotation != 0 {
		t.Errorf("%+v %v", p, err)
	}
	for f, d := range map[string]int{"yuv420p": 8, "yuv420p10le": 10, "gray10le": 10, "yuv422p12le": 12, "p010le": 10, "nv12": 8} {
		if pixFmtDepth(f) != d {
			t.Errorf("%s: %d", f, pixFmtDepth(f))
		}
	}
	for _, s := range []string{"", "30", "30/0", "0/1", "a/b", "30000/1"} {
		if n, d := rate(s); n != 0 || d != 0 {
			t.Errorf("rate %q: %d/%d", s, n, d)
		}
	}
	if n, d := rate("30000/1001"); n != 30000 || d != 1001 {
		t.Errorf("29.97: %d/%d", n, d)
	}
	for _, s := range []string{"-1", "1e10", "x", "NaN"} {
		if seconds(s) != 0 {
			t.Errorf("seconds %q", s)
		}
	}
	var c capped
	c.max = 4
	_, _ = c.Write([]byte("abc"))
	_, _ = c.Write([]byte("de"))
	if !c.over || c.b.String() != "abc" {
		t.Errorf("capped %q %v", c.b.String(), c.over)
	}
}

// TestShowinfo feeds the log parser the lines it must handle.
func TestShowinfo(t *testing.T) {
	t.Parallel()
	s := newShowinfo()
	log := "[Parsed_showinfo_0 @ 0x1] config in time_base: 1/15360, frame_rate: 30/1\r\n" +
		"[Parsed_showinfo_0 @ 0x1] n:   0 pts:   6144 pts_time:0.4 pos: 1 fmt:yuv420p s:384x216 i:P\n" +
		strings.Repeat("x", 2*maxLogLine) + "\n" + "other line\n" +
		"[Parsed_showinfo_0 @ 0x1] n:   1 pts:   bad pts_time:0.4 s:384x216\n" +
		"[Parsed_showinfo_0 @ 0x1]   side data\n"
	for i := 0; i < len(log); i += 7 {
		_, _ = s.Write([]byte(log[i:min(i+7, len(log))]))
	}
	done := make(chan struct{})
	close(done)
	a, err := s.pop(t.Context(), done, time.Minute)
	if err != nil || a.w != 384 || a.h != 216 || a.at() != 400*time.Millisecond {
		t.Errorf("first: %+v %v", a, err)
	}
	b, err := s.pop(t.Context(), done, time.Minute)
	if err != nil || b.w != -1 {
		t.Errorf("unparsable: %+v %v", b, err)
	}
	if _, err := s.pop(t.Context(), done, time.Minute); !errors.Is(err, errNoTimestamp) {
		t.Errorf("a third timestamp: %v", err)
	}
	// Only a line that starts with the filter's own prefix is a frame: container metadata that
	// ffmpeg prints (indented) cannot inject one. A native ffmpeg may print the address without 0x.
	for _, l := range []string{
		"    title           : [Parsed_showinfo_0 @ 0x1] n:   0 pts:   9 pts_time:0 s:384x216",
		"x[Parsed_showinfo_0 @ 0x1] n: 0 pts: 9 s:2x2",
		"[Parsed_showinfo_0 @ 0x1]   side data n: 0 pts: 9 s:2x2",
		"[Parsed_showinfo_x @ 0x1] n: 0 pts: 9 s:2x2",
	} {
		s.parse(l)
		if len(s.q) != 0 {
			t.Fatalf("%q gave a timestamp", l)
		}
	}
	s.parse("[Parsed_showinfo_12 @ 000001C2F3A0B2C0] n: 0 pts: 9 s:2x2")
	if c, err := s.pop(t.Context(), done, time.Minute); err != nil || c.pts != 9 {
		t.Errorf("native line: %+v %v", c, err)
	}
	// Waiting for a line ends at the stall limit, or at once when the context ends.
	open := make(chan struct{})
	start := time.Now()
	var le *LimitError
	if _, err := s.pop(t.Context(), open, 200*time.Millisecond); !errors.As(err, &le) || le.Limit != "EngineStallTimeout" ||
		time.Since(start) < 200*time.Millisecond {
		t.Errorf("no line: %v", err)
	}
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(io.ErrClosedPipe)
	if _, err := s.pop(ctx, open, time.Minute); !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("cancelled: %v", err)
	}
	if !strings.Contains(s.String(), "other line") || len(s.String()) > logTailLen {
		t.Errorf("tail %q", s.String())
	}
	for range 1100 {
		s.parse("[Parsed_showinfo_0 @ 0x1] n: 1 pts: 1 s:2x2")
	}
	if !s.overflow() {
		t.Error("no overflow")
	}
	for _, c := range []struct {
		ts   frameTS
		want time.Duration
	}{
		{frameTS{pts: -512, tbN: 1, tbD: 15360}, -time.Second / 30},
		{frameTS{pts: 5, tbN: 0, tbD: 1}, 0},
		{frameTS{pts: math.MaxInt64, tbN: 1, tbD: 1}, 0},
		{frameTS{pts: 1 << 40, tbN: 1 << 20, tbD: 1}, 0},
		{frameTS{pts: 90000 * 3600, tbN: 1, tbD: 90000}, time.Hour},
	} {
		if got := c.ts.at(); got != c.want {
			t.Errorf("%+v: %v", c.ts, got)
		}
	}
	if frameIndex(time.Second, 0, 1) != 0 || frameIndex(time.Second/30*7, 30, 1) != 7 {
		t.Error("frameIndex")
	}
}

// TestHelpers covers the small helpers.
func TestHelpers(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"a.mp4": "input.mp4", "a.MOV": "input.MOV", "a": "input", "a.m-v": "input",
		"a.toolongext": "input", "a.": "input"} {
		if got := inputName(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
	if got := sanitise("a\x1b[0m\nb\u0085c", 100); got != "a [0m b c" {
		t.Errorf("sanitise %q", got)
	}
	if got := sanitise("abcdef", 3); got != "def" {
		t.Errorf("sanitise tail %q", got)
	}
	if checkWork(1, 0) == nil || checkWork(10_000, 1) != nil || checkWork(10_001, 1) == nil {
		t.Error("checkWork")
	}
	if e := (&LimitError{Limit: "X", Value: 1.25, Max: 2}).Error(); e != "video: X exceeded: 1.2, limit 2" && e != "video: X exceeded: 1.3, limit 2" {
		t.Error(e)
	}
	var l logTail
	for range 100 {
		_, _ = l.Write([]byte("0123456789"))
	}
	if len(l.String()) != logTailLen {
		t.Errorf("tail %d", len(l.String()))
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a"), []byte("abc"), 0o600)
	_ = os.Mkdir(filepath.Join(dir, "d"), 0o700)
	if dirSize(dir) != 3 {
		t.Errorf("dirSize %d", dirSize(dir))
	}
	if _, err := containerOf("x.264"); err != nil {
		t.Error(err)
	}
	if defaultCacheDir() == "" {
		t.Log("no user cache directory")
	}
}

// TestRunErrors covers the runner's error paths with a fake runner.
func TestRunErrors(t *testing.T) {
	t.Parallel()
	fail := fakeRunner(func(ctx context.Context, j *job, m *meter) (int, error) { return 0, io.ErrUnexpectedEOF })
	if err := run(t.Context(), fail, job{args: func(string, string) []string { return nil }}); !errors.Is(err, ErrEngine) {
		t.Errorf("exec error: %v", err)
	}
	// A short watchdog limit: a job that waits on its output pipe is not stalled; one that does
	// nothing is.
	slow := fakeRunner(func(ctx context.Context, j *job, m *meter) (int, error) {
		_, _ = j.stdout.Write([]byte("x"))
		<-ctx.Done()
		return 0, nil
	})
	start := time.Now()
	err := run(t.Context(), slow, job{timeout: 2 * time.Second, progress: progFiles, outDir: t.TempDir()})
	var le *LimitError
	if !errors.As(err, &le) || time.Since(start) > 15*time.Second {
		t.Errorf("stall: %v", err)
	}
	pr, pw := io.Pipe()
	blocked := fakeRunner(func(ctx context.Context, j *job, m *meter) (int, error) {
		_, err := j.stdout.Write([]byte("x")) // blocks until read
		return 0, err
	})
	go func() {
		time.Sleep(3500 * time.Millisecond)
		_, _ = io.ReadAll(pr)
	}()
	if err := run(t.Context(), blocked, job{timeout: 2 * time.Second, stdout: pw}); err != nil {
		t.Errorf("a job waiting on its consumer was stopped: %v", err)
	}
	in := fakeRunner(func(ctx context.Context, j *job, m *meter) (int, error) {
		_, err := io.ReadAll(j.stdin)
		return 0, err
	})
	if err := run(t.Context(), in, job{progress: progInput, stdin: strings.NewReader("abc")}); err != nil {
		t.Error(err)
	}
}

type fakeRunner func(ctx context.Context, j *job, m *meter) (int, error)

func (f fakeRunner) exec(ctx context.Context, j *job, m *meter) (int, error) { return f(ctx, j, m) }
