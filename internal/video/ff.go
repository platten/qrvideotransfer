package video

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ff is the part shared by the embedded and the native backend: both drive ffmpeg and ffprobe with
// the same arguments, through a runner.
type ff struct {
	r runner
	// fast selects the HEVC fast path (embedded backend, unsandboxedhevc builds only).
	fast bool
}

// DefaultQuantiser is the x264 quantiser used when EncodeConfig.Quantiser is 0 (FORMAT.md section 3).
const DefaultQuantiser = 32

// x264Params are the tuned settings of DESIGN.md section 12: every frame an IDR frame, CABAC, no
// deblocking, adaptive quantisation, psychovisual tuning or trellis, 4x4 intra partitions only and no
// 8x8 transform (which makes the stream Main profile).
const x264Params = "keyint=1:min-keyint=1:scenecut=0:bframes=0:cabac=1:no-deblock=1:aq-mode=0:psy=0:trellis=0:partitions=i4x4:no-8x8dct=1"

// bt709 tags the input and the output as BT.709, limited range, so that ffmpeg converts nothing.
var bt709 = []string{"-color_range", "tv", "-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709"}

func encodeArgs(c EncodeConfig, out, format string) []string {
	pix, q := "yuv420p", c.Quantiser
	if c.BitDepth == 10 {
		pix = "yuv420p10le"
	}
	if q == 0 {
		q = DefaultQuantiser
	}
	a := []string{"-nostdin", "-hide_banner", "-nostats", "-loglevel", "error",
		"-f", "rawvideo", "-pix_fmt", pix, "-s", fmt.Sprintf("%dx%d", c.Width, c.Height), "-framerate", strconv.Itoa(c.FPS)}
	a = append(a, bt709...)
	a = append(a, "-i", "pipe:0", "-map", "0:v:0", "-c:v", "libx264", "-preset", "veryfast", "-qp", strconv.Itoa(q),
		"-x264-params", x264Params)
	if c.BitDepth == 10 {
		a = append(a, "-profile:v", "high10")
	}
	a = append(a, bt709...)
	a = append(a, "-pix_fmt", pix)
	if format == "mp4" {
		a = append(a, "-use_editlist", "0")
	}
	return append(a, "-f", format, "-y", out)
}

// containerOf maps an output path's extension to an ffmpeg muxer.
func containerOf(path string) (string, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4":
		return "mp4", nil
	case ".h264", ".264":
		return "h264", nil
	}
	return "", fmt.Errorf("%w: output %q: use .mp4, or .h264 for an elementary-stream run", ErrUnsupported, filepath.Ext(path))
}

// Probe implements [Backend].
func (b *ff) Probe(ctx context.Context, path string) (ClipInfo, error) {
	p, err := probe(ctx, b.r, path)
	return p.ClipInfo, err
}

// NewEncoder implements [Backend]: x264 with the settings of DESIGN.md section 12, from yuv420p
// planes built by the caller, to an MP4 file, or to an H.264 elementary stream (".h264") holding one
// run of an even number of frames for [ff.Concat]. The engine sees one new, empty directory beside
// path, from which the file is moved into place by Close.
func (b *ff) NewEncoder(ctx context.Context, path string, c EncodeConfig) (Encoder, error) {
	if err := checkEncodeConfig(c); err != nil {
		return nil, err
	}
	format, err := containerOf(path)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(path), ".qrvt-encode-")
	if err != nil {
		return nil, err
	}
	name := "out." + format
	pr, pw := io.Pipe()
	e := &pipeEncoder{c: c, bits: max(c.BitDepth, 8), pw: pw, tmp: tmp, out: filepath.Join(tmp, name), path: path,
		even: format == "h264", done: make(chan struct{})}
	go func() {
		e.err = run(ctx, b.r, job{prog: "ffmpeg", args: func(_, out string) []string {
			return encodeArgs(c, out+"/"+name, format)
		}, outDir: tmp, stdin: pr, progress: progInput})
		_ = pr.CloseWithError(errors.Join(e.err, io.ErrClosedPipe))
		close(e.done)
	}()
	return e, nil
}

type pipeEncoder struct {
	c        EncodeConfig
	bits     int
	pw       *io.PipeWriter
	tmp, out string
	path     string
	even     bool
	n        int
	buf      []byte
	done     chan struct{}
	err      error
	closed   bool
}

func (e *pipeEncoder) WriteFrame(f *Frame) error {
	if e.closed {
		return fmt.Errorf("%w: encoder closed", ErrFrame)
	}
	var err error
	if e.buf, err = packFrame(e.buf[:0], f, e.c.Width, e.c.Height, e.bits); err != nil {
		return err
	}
	if _, err := e.pw.Write(e.buf); err != nil {
		<-e.done
		if e.err != nil {
			return e.err
		}
		return err
	}
	e.n++
	return nil
}

func (e *pipeEncoder) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	_ = e.pw.Close()
	<-e.done
	defer func() { _ = os.RemoveAll(e.tmp) }()
	switch {
	case e.err != nil:
		return e.err
	case e.n == 0:
		return fmt.Errorf("%w: no frames written", ErrFrame)
	case e.even && e.n%2 != 0:
		return fmt.Errorf("%w: %d frames", ErrOddRun, e.n)
	}
	return os.Rename(e.out, e.path)
}

// Concat wraps H.264 elementary-stream runs, in order, into one MP4 file at path without re-encoding
// (DESIGN.md section 12: all-intra runs with identical parameter sets concatenate validly).
func (b *ff) Concat(ctx context.Context, runs []string, path string, fps int) error {
	if fps <= 0 || fps > 1000 || len(runs) == 0 {
		return fmt.Errorf("%w: concat of %d runs at %d fps", ErrUnsupported, len(runs), fps)
	}
	var rs []io.Reader
	for _, r := range runs {
		f, err := os.Open(r) //nolint:gosec // the caller names the files
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		rs = append(rs, f)
	}
	tmp, err := os.MkdirTemp(filepath.Dir(path), ".qrvt-concat-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	err = run(ctx, b.r, job{prog: "ffmpeg", args: func(_, out string) []string {
		return []string{"-nostdin", "-hide_banner", "-nostats", "-loglevel", "error", "-fflags", "+genpts",
			"-f", "h264", "-framerate", strconv.Itoa(fps), "-i", "pipe:0", "-map", "0:v:0", "-c", "copy",
			"-use_editlist", "0", "-f", "mp4", "-y", out + "/out.mp4"}
	}, outDir: tmp, stdin: io.MultiReader(rs...), progress: progInput})
	if err != nil {
		return err
	}
	return os.Rename(filepath.Join(tmp, "out.mp4"), path)
}

// prerollTime is how far before a slice's start decoding begins, so that the pictures that a seek to
// an open-GOP random access point would lose all lie before the slice.
const prerollTime = time.Second

// outputFormat chooses the raw output format: the source's own, so that ffmpeg converts neither range
// nor depth, or its brightness plane alone.
func outputFormat(p probeInfo, c DecodeConfig) (string, error) {
	if p.BitDepth != 8 && p.BitDepth != 10 {
		return "", fmt.Errorf("%w: %d-bit video", ErrUnsupported, p.BitDepth)
	}
	if c.BitDepth != 0 && c.BitDepth != p.BitDepth {
		return "", fmt.Errorf("%w: %d-bit output from a %d-bit source", ErrUnsupported, c.BitDepth, p.BitDepth)
	}
	if c.BrightnessOnly {
		if !strings.HasPrefix(p.PixFmt, "yuv") && !strings.HasPrefix(p.PixFmt, "gray") {
			return "", fmt.Errorf("%w: pixel format %q", ErrUnsupported, p.PixFmt)
		}
		if p.BitDepth == 10 {
			return "gray10le", nil
		}
		return "gray", nil
	}
	switch p.PixFmt {
	case "yuv420p", "yuvj420p", "yuv420p10le":
		return p.PixFmt, nil
	}
	return "", fmt.Errorf("%w: pixel format %q (4:2:0 at 8 or 10 bits only)", ErrUnsupported, p.PixFmt)
}

func decodeArgs(in string, p probeInfo, c DecodeConfig, pix string) []string {
	a := []string{"-nostdin", "-hide_banner", "-nostats", "-loglevel", "info", "-noautorotate", "-copyts"}
	if seek := c.Start - prerollTime; seek > 0 {
		a = append(a, "-noaccurate_seek", "-ss", strconv.FormatFloat(seek.Seconds(), 'f', 6, 64))
	}
	vf := "showinfo=checksum=0"
	if c.BrightnessOnly {
		vf += ",extractplanes=y"
	}
	return append(a, "-i", in, "-map", "0:v:0", "-an", "-sn", "-dn", "-vf", vf, "-fps_mode", "passthrough",
		"-pix_fmt", pix, "-f", "rawvideo", "pipe:1")
}

// NewDecoder implements [Backend]. It probes the file, refuses it if it exceeds a limit of
// docs/SECURITY.md section 5, and starts one engine instance that writes raw frames to a pipe.
func (b *ff) NewDecoder(ctx context.Context, path string, c DecodeConfig) (Decoder, error) {
	p, err := probe(ctx, b.r, path)
	if err != nil {
		return nil, err
	}
	pix, err := outputFormat(p, c)
	if err != nil {
		return nil, err
	}
	if err := checkWork(float64(p.Frames)*float64(p.Width)*float64(p.Height), p.Size); err != nil {
		return nil, err
	}
	if b.fast && p.Codec == "hevc" {
		return newFastDecoder(ctx, b, path, p, c)
	}
	return startDecoder(ctx, b.r, path, p, c, pix), nil
}

// startDecoder starts an instance that decodes path to raw frames in format pix.
func startDecoder(ctx context.Context, r runner, path string, p probeInfo, c DecodeConfig, pix string) *pipeDecoder {
	ctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	n := p.Width * p.Height
	if !c.BrightnessOnly {
		n += 2 * (p.Width / 2) * (p.Height / 2)
	}
	if p.BitDepth > 8 {
		n *= 2
	}
	d := &pipeDecoder{p: p, c: c, pr: pr, cancel: cancel, buf: make([]byte, n), ts: newShowinfo(), done: make(chan struct{})}
	go func() {
		err := run(ctx, r, job{prog: "ffmpeg", args: func(in, _ string) []string { return decodeArgs(in, p, c, pix) },
			input: path, stdout: pw, stderr: d.ts, progress: progOutput})
		if err == nil && d.ts.overflow() {
			err = fmt.Errorf("%w: frame timestamps lost", ErrEngine)
		}
		d.runErr = err
		_ = pw.CloseWithError(err)
		close(d.done)
	}()
	return d
}

// pipeDecoder reads length-checked raw frames from an engine instance.
type pipeDecoder struct {
	p      probeInfo
	c      DecodeConfig
	pr     *io.PipeReader
	cancel context.CancelFunc
	buf    []byte
	ts     *showinfo
	done   chan struct{}
	runErr error
	pixels float64
	fail   error // sticky error or io.EOF
}

func (d *pipeDecoder) Info() ClipInfo { return d.p.ClipInfo }

func (d *pipeDecoder) Next(f *Frame) error {
	for d.fail == nil {
		d.fail = d.next(f)
	}
	err := d.fail
	if errors.Is(err, errFrameReady) {
		d.fail = nil
		return nil
	}
	return err
}

var errFrameReady = errors.New("frame ready")

func (d *pipeDecoder) stop(err error) error {
	d.cancel()
	return err
}

// next reads one frame. It returns errFrameReady when f holds a frame of the slice, nil to skip a
// frame before the slice, or the error that ends decoding.
func (d *pipeDecoder) next(f *Frame) error {
	if _, err := io.ReadFull(d.pr, d.buf); err != nil {
		<-d.done
		switch {
		case err == io.EOF:
			return io.EOF
		case errors.Is(err, io.ErrUnexpectedEOF):
			return fmt.Errorf("%w: the last frame is truncated", ErrInvalidVideo)
		}
		return err
	}
	t, ok := d.ts.pop(d.done)
	if !ok {
		return d.stop(fmt.Errorf("%w: a frame without a timestamp", ErrEngine))
	}
	if t.w != d.p.Width || t.h != d.p.Height {
		return d.stop(fmt.Errorf("%w: picture size changed from %dx%d to %dx%d", ErrInvalidVideo, d.p.Width, d.p.Height, t.w, t.h))
	}
	d.pixels += float64(t.w) * float64(t.h)
	if err := checkWork(d.pixels, d.p.Size); err != nil {
		return d.stop(err)
	}
	pts := t.at() - time.Duration(d.p.Start*float64(time.Second))
	if pts < d.c.Start {
		return nil
	}
	if d.c.Length > 0 && pts >= d.c.Start+d.c.Length {
		return d.stop(io.EOF)
	}
	unpackFrame(f, d.buf, d.p.Width, d.p.Height, d.p.BitDepth, d.c.BrightnessOnly)
	f.PTS, f.Index = pts, frameIndex(pts, d.p.FPSNum, d.p.FPSDen)
	return errFrameReady
}

func (d *pipeDecoder) Close() error {
	d.cancel()
	_ = d.pr.CloseWithError(io.ErrClosedPipe)
	<-d.done
	if d.fail == nil {
		d.fail = io.ErrClosedPipe
	}
	return nil
}

// frameIndex is the frame number at time t for a constant frame rate.
func frameIndex(t time.Duration, fpsN, fpsD int) int {
	if fpsN <= 0 || fpsD <= 0 {
		return 0
	}
	return int((float64(t)*float64(fpsN)/float64(fpsD) + float64(time.Second)/2) / float64(time.Second))
}

// frameTS is one frame's timestamp and size as showinfo reports them.
type frameTS struct {
	pts      int64
	tbN, tbD int64
	w, h     int
}

// at converts the timestamp to a duration exactly, with 128-bit intermediates.
func (t frameTS) at() time.Duration {
	if t.tbN <= 0 || t.tbD <= 0 {
		return 0
	}
	neg, v := t.pts < 0, uint64(t.pts) //nolint:gosec // the sign is handled below
	if neg {
		v = uint64(-t.pts)
	}
	hi, lo := bits.Mul64(v, uint64(t.tbN)*uint64(time.Second))
	if hi >= uint64(t.tbD) {
		return 0 // beyond the range of time.Duration
	}
	q, _ := bits.Div64(hi, lo, uint64(t.tbD))
	if q > 1<<62 {
		return 0
	}
	if neg {
		return -time.Duration(q)
	}
	return time.Duration(q)
}

// showinfo parses ffmpeg's log as it is written: the showinfo filter's line for each frame, which
// precedes the frame on the output, and its time base. Everything else goes to a short tail for error
// messages.
type showinfo struct {
	mu       sync.Mutex
	line     []byte
	tbN, tbD int64
	q        chan frameTS
	over     bool
	tail     logTail
}

func newShowinfo() *showinfo { return &showinfo{q: make(chan frameTS, 1024)} }

const maxLogLine = 4096

func (s *showinfo) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range p {
		if c != '\n' && c != '\r' {
			if len(s.line) < maxLogLine {
				s.line = append(s.line, c)
			}
			continue
		}
		s.parse(string(s.line))
		s.line = s.line[:0]
	}
	return len(p), nil
}

func (s *showinfo) parse(l string) {
	if !strings.Contains(l, "Parsed_showinfo_") {
		if l != "" {
			_, _ = s.tail.Write([]byte(l + "\n"))
		}
		return
	}
	if _, v, ok := strings.Cut(l, "config in time_base: "); ok {
		a, b, _ := strings.Cut(strings.TrimSuffix(field(v), ","), "/")
		s.tbN, _ = strconv.ParseInt(a, 10, 64)
		s.tbD, _ = strconv.ParseInt(b, 10, 64)
		return
	}
	ok1 := strings.Contains(l, "] n:")
	_, pts, ok2 := strings.Cut(l, " pts:")
	_, size, ok3 := strings.Cut(l, " s:")
	if !ok1 || !ok2 || !ok3 {
		return
	}
	t := frameTS{tbN: s.tbN, tbD: s.tbD}
	var err1, err2, err3 error
	t.pts, err1 = strconv.ParseInt(field(pts), 10, 64)
	w, h, _ := strings.Cut(field(size), "x")
	t.w, err2 = strconv.Atoi(w)
	t.h, err3 = strconv.Atoi(h)
	if err1 != nil || err2 != nil || err3 != nil {
		t.w, t.h = -1, -1 // reported as a size change
	}
	select {
	case s.q <- t:
	default:
		s.over = true
	}
}

// field is the first space-separated word of s.
func field(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// String returns the end of the log, without the showinfo lines.
func (s *showinfo) String() string { return s.tail.String() }

func (s *showinfo) overflow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.over
}

// pop returns the next frame's timestamp, waiting for it until done is closed.
func (s *showinfo) pop(done <-chan struct{}) (frameTS, bool) {
	select {
	case t := <-s.q:
		return t, true
	default:
	}
	select {
	case t := <-s.q:
		return t, true
	case <-done:
		select {
		case t := <-s.q:
			return t, true
		default:
			return frameTS{}, false
		}
	}
}
