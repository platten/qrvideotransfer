//go:build unsandboxedhevc

package video

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gen2brain/h265/hevc"
)

func fastBackend(t *testing.T) *Embedded {
	t.Helper()
	b, err := NewEmbedded(Options{UnsandboxedHEVC: true})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// decodeWith decodes a clip and reports whether the fast path fell back to the engine.
func decodeWith(t *testing.T, b Backend, path string, c DecodeConfig) ([]Frame, error) {
	t.Helper()
	d, err := b.NewDecoder(t.Context(), path, c)
	if err != nil {
		t.Fatal(err)
	}
	var fell error
	if fd, ok := d.(*fastDecoder); ok {
		defer func() { fell = fd.fellBack }()
	}
	frames := decodeAll(t, d)
	if fd, ok := d.(*fastDecoder); ok {
		fell = fd.fellBack
	}
	return frames, fell
}

func sameFrames(t *testing.T, name string, a, b []Frame) {
	t.Helper()
	if len(a) != len(b) {
		t.Fatalf("%s: %d and %d frames", name, len(a), len(b))
	}
	for i := range a {
		if a[i].Index != b[i].Index || a[i].PTS != b[i].PTS || a[i].Bits != b[i].Bits ||
			!slices.Equal(a[i].Y, b[i].Y) || !slices.Equal(a[i].Cb, b[i].Cb) || !slices.Equal(a[i].Cr, b[i].Cr) {
			t.Fatalf("%s: frame %d differs (index %d/%d, pts %v/%v)", name, i, a[i].Index, b[i].Index, a[i].PTS, b[i].PTS)
		}
	}
}

// TestFastPathMatchesEngine: the pure-Go decoder and the engine give identical frames on the test
// clips, whole and in slices (SECURITY.md 9.3).
func TestFastPathMatchesEngine(t *testing.T) {
	t.Parallel()
	e, f := embedded(t), fastBackend(t)
	for _, name := range []string{"hevc8.mp4", "hevc10hlg.mp4"} {
		for _, c := range []DecodeConfig{{}, {BrightnessOnly: true}, {Start: 300 * time.Millisecond, Length: 200 * time.Millisecond},
			{Start: 410 * time.Millisecond}} {
			want, _ := decodeWith(t, e, clip(name), c)
			got, fell := decodeWith(t, f, clip(name), c)
			if fell != nil {
				t.Fatalf("%s %+v: fell back: %v", name, c, fell)
			}
			sameFrames(t, name, want, got)
		}
	}
	checkSlices(t, f, clip("hevc8.mp4"), 24, []time.Duration{0, 300 * time.Millisecond, 410 * time.Millisecond, 700 * time.Millisecond})
}

// TestFastPathRefusal: a stream the parameter-set check refuses (4:4:4) is decoded by the engine.
func TestFastPathRefusal(t *testing.T) {
	t.Parallel()
	e, f := embedded(t), fastBackend(t)
	c := DecodeConfig{BrightnessOnly: true}
	want, _ := decodeWith(t, e, clip("hevc444.mp4"), c)
	got, fell := decodeWith(t, f, clip("hevc444.mp4"), c)
	if !errors.Is(fell, errRefused) {
		t.Errorf("4:4:4 not refused: %v", fell)
	}
	sameFrames(t, "hevc444", want, got)
	// H.264 never takes the fast path.
	d, err := f.NewDecoder(t.Context(), clip("skip.mkv"), DecodeConfig{})
	if d != nil || err == nil {
		t.Error("skip.mkv opened")
	}
}

// TestFastPathThreads: exactly one thread; 0 (GOMAXPROCS) and 2 are refused.
func TestFastPathThreads(t *testing.T) {
	t.Parallel()
	for _, n := range []int{0, 2, -1} {
		if _, err := newH265(n); !errors.Is(err, ErrUnsupported) {
			t.Errorf("Threads(%d) accepted", n)
		}
	}
	if _, err := newH265(1); err != nil {
		t.Fatal(err)
	}
}

// badDecoder wraps the real decoder and misbehaves inside DecodeNAL after n calls.
type badDecoder struct {
	d     *hevc.Decoder
	n     int
	block chan struct{}
}

func (b *badDecoder) DecodeNAL(nal hevc.NALUnit) ([]*hevc.Picture, error) {
	if b.n--; b.n == 0 {
		if b.block != nil {
			<-b.block
		}
		var p *hevc.Picture
		_ = p.CropW // a nil dereference inside the decoder's call path
	}
	return b.d.DecodeNAL(nal)
}

func (b *badDecoder) Flush() []*hevc.Picture { return b.d.Flush() }

// TestFastPathPanic: a panic inside the decoder's call path is recovered and the rest of the slice
// is decoded by the engine, giving the same frames as the engine alone. Not parallel: it replaces
// newNALDecoder.
func TestFastPathPanic(t *testing.T) {
	e, f := embedded(t), fastBackend(t)
	want, _ := decodeWith(t, e, clip("hevc8.mp4"), DecodeConfig{})
	newNALDecoder = func(n int) (nalDecoder, error) {
		d, err := newH265(n)
		return &badDecoder{d: d, n: 20}, err
	}
	defer func() { newNALDecoder = func(n int) (nalDecoder, error) { return newH265(n) } }()
	got, fell := decodeWith(t, f, clip("hevc8.mp4"), DecodeConfig{})
	if fell == nil || !strings.Contains(fell.Error(), "panic") {
		t.Errorf("no recovered panic: %v", fell)
	}
	sameFrames(t, "after a panic", want, got)
}

// TestFastPathTimeLimit: a picture over the time limit stops the whole decode with an error naming
// the limit, and decoding does not continue. Not parallel: it shortens the limit.
func TestFastPathTimeLimit(t *testing.T) {
	f := fastBackend(t)
	block := make(chan struct{})
	defer close(block)
	newNALDecoder = func(n int) (nalDecoder, error) {
		d, err := newH265(n)
		return &badDecoder{d: d, n: 10, block: block}, err
	}
	pictureTime = time.Second
	defer func() {
		newNALDecoder = func(n int) (nalDecoder, error) { return newH265(n) }
		pictureTime = FastPathPictureTime
	}()
	d, err := f.NewDecoder(t.Context(), clip("hevc8.mp4"), DecodeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var fr Frame
	start := time.Now()
	for err == nil {
		err = d.Next(&fr)
	}
	var le *LimitError
	if !errors.As(err, &le) || le.Limit != "FastPathPictureTime" || time.Since(start) > 3*time.Second {
		t.Fatalf("got %v after %v", err, time.Since(start))
	}
	if err2 := d.Next(&fr); !errors.As(err2, &le) {
		t.Errorf("decoding continued: %v", err2)
	}
	if fd := d.(*fastDecoder); fd.engine != nil {
		t.Error("the engine took over after an abandoned picture")
	}
	_ = d.Close()
}

// slowDecoder takes d over every call and decodes nothing.
type slowDecoder struct{ d time.Duration }

func (s slowDecoder) DecodeNAL(hevc.NALUnit) ([]*hevc.Picture, error) {
	time.Sleep(s.d)
	return nil, nil
}
func (slowDecoder) Flush() []*hevc.Picture { return nil }

// TestFastPathSharedDeadline: the slices of one picture share its time (93b40a6): two slices of
// 0.6 s each exceed a 1 s limit, although each alone is within it. Not parallel: it shortens the
// limit.
func TestFastPathSharedDeadline(t *testing.T) {
	pictureTime = time.Second
	defer func() { pictureTime = FastPathPictureTime }()
	stream := append(nal(1, []byte{0x80, 0x10}), nal(1, []byte{0x00, 0x10})...) // first_slice_segment_in_pic_flag 1, then 0
	d := &fastDecoder{ctx: t.Context(), nals: &nalReader{r: bytes.NewReader(stream)}, w: startWorker(slowDecoder{600 * time.Millisecond}), last: -1}
	var f Frame
	start := time.Now()
	err := d.Next(&f)
	var le *LimitError
	if el := time.Since(start); !errors.As(err, &le) || le.Limit != "FastPathPictureTime" || el < time.Second || el > 5*time.Second {
		t.Errorf("got %v after %v", err, el)
	}
	_ = d.Close()
}

// flushDecoder fails at its fifth call; its Flush takes 5 s and is recorded.
type flushDecoder struct {
	calls   int
	flushed atomic.Bool
}

func (f *flushDecoder) DecodeNAL(hevc.NALUnit) ([]*hevc.Picture, error) {
	if f.calls++; f.calls == 5 {
		return nil, errors.New("a bad slice")
	}
	return nil, nil
}

func (f *flushDecoder) Flush() []*hevc.Picture {
	f.flushed.Store(true)
	time.Sleep(5 * time.Second)
	return nil
}

// TestFastPathRelease: when the engine takes over, the old decoder is abandoned without a flush,
// which could take a picture's whole time. Not parallel: it replaces newNALDecoder.
func TestFastPathRelease(t *testing.T) {
	f := fastBackend(t)
	dec := &flushDecoder{}
	newNALDecoder = func(int) (nalDecoder, error) { return dec, nil }
	defer func() { newNALDecoder = func(n int) (nalDecoder, error) { return newH265(n) } }()
	d, err := f.NewDecoder(t.Context(), clip("hevc8.mp4"), DecodeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var fr Frame
	start := time.Now()
	if err := d.Next(&fr); err != nil || d.(*fastDecoder).fellBack == nil {
		t.Fatalf("no fallback: %v", err)
	}
	if el := time.Since(start); dec.flushed.Load() || el > 10*time.Second {
		t.Errorf("the old decoder was flushed (%v), the first frame took %v", dec.flushed.Load(), el)
	}
	_ = d.Close()
}

// TestFastPathCopyLimit: the copy instance is stopped when its temporary directory grows beyond
// twice the input's size plus FastPathCopySize.
func TestFastPathCopyLimit(t *testing.T) {
	t.Parallel()
	grow := fakeRunner(func(ctx context.Context, j *job, _ *meter) (int, error) {
		f, err := os.Create(filepath.Join(j.outDir, "s.hevc"))
		if err != nil {
			return 0, err
		}
		defer func() { _ = f.Close() }()
		chunk := make([]byte, 1<<20)
		for n := 0; ctx.Err() == nil; n++ {
			if n < 200 {
				_, _ = f.Write(chunk)
			}
			time.Sleep(10 * time.Millisecond)
		}
		return 0, nil
	})
	p := probeInfo{ClipInfo: ClipInfo{Codec: "hevc", Width: 64, Height: 64, BitDepth: 8}, PixFmt: "yuv420p", Size: 1000}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	start := time.Now()
	_, err := newFastDecoder(ctx, &ff{r: grow, fast: true}, "x", p, DecodeConfig{})
	var le *LimitError
	if el := time.Since(start); !errors.As(err, &le) || le.Limit != "FastPathCopySize" || le.Max != 2*1000+FastPathCopySize || el > 15*time.Second {
		t.Errorf("got %v after %v", err, el)
	}
	if a := copyArgs("IN", "OUT", p, DecodeConfig{}); slices.Index(a, "-format_whitelist") > slices.Index(a, "IN") ||
		a[slices.Index(a, "-format_whitelist")+1] != formatWhitelist || a[slices.Index(a, "-protocol_whitelist")+1] != "file,pipe" {
		t.Errorf("copy arguments %q", a)
	}
}

// TestSPSLimits checks the parameter-set parser against the committed clips and crafted sets.
func TestSPSLimits(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"hevc8.mp4", "hevc10hlg.mp4"} {
		data := annexB(t, name)
		r := &nalReader{r: bytes.NewReader(data)}
		p := probeInfo{ClipInfo: ClipInfo{BitDepth: 8}}
		if strings.Contains(name, "10") {
			p.BitDepth = 10
		}
		if err := checkStream(r, p); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		p.BitDepth = 12
		if err := checkStream(&nalReader{r: bytes.NewReader(data)}, p); !errors.Is(err, errRefused) {
			t.Errorf("%s at the wrong depth: %v", name, err)
		}
	}
	// The crafted 4K stream declares a buffer of 16 pictures.
	if err := checkStream(&nalReader{r: bytes.NewReader(craftHEVC(1, 0, 0, 1))}, probeInfo{ClipInfo: ClipInfo{BitDepth: 10}}); !errors.Is(err, errRefused) ||
		!strings.Contains(err.Error(), "buffer of 16") {
		t.Errorf("crafted: %v", err)
	}
	if err := checkStream(&nalReader{r: strings.NewReader("\x00\x00\x01\x40\x01")}, probeInfo{}); !errors.Is(err, errRefused) {
		t.Errorf("no SPS: %v", err)
	}
	if _, _, _, err := parseSPSLimits([]byte{0x01}); !errors.Is(err, errRefused) {
		t.Errorf("short SPS: %v", err)
	}
	// Sub-layers: two, with ordering info for each; the larger buffer counts.
	var w bitWriter
	w.u(4, 0)
	w.u(3, 1)
	w.u(1, 1)
	w.u(96, 0)
	w.u(2, 0b11)
	for range 7 {
		w.u(2, 0)
	}
	w.u(96, 0)
	w.ue(0)
	w.ue(3)
	w.u(1, 0)
	w.ue(64)
	w.ue(64)
	w.u(1, 1)
	for range 7 { // conformance window, bit depths, log2_max_pic_order_cnt_lsb_minus4
		w.ue(0)
	}
	w.u(1, 1)
	w.ue(2)
	w.ue(0)
	w.ue(0)
	w.ue(6)
	w.ue(0)
	w.ue(0)
	if pw, ph, dpb, err := parseSPSLimits(w.trailing()); err != nil || pw != 64 || ph != 64 || dpb != 7 {
		t.Errorf("sub-layers: %dx%d dpb %d %v", pw, ph, dpb, err)
	}
	// The capture limit in either orientation: portrait-coded 4320 x 7680 passes, and one sample
	// over on the longer or the shorter side is refused.
	for _, c := range []struct {
		w, h int
		ok   bool
	}{{4320, 7680, true}, {7680, 4320, true}, {7681, 4320, false}, {4320, 7681, false}, {4321, 7680, false}, {4322, 4322, false}} {
		_, _, _, err := parseSPSLimits(spsOfSize(c.w, c.h))
		if (err == nil) != c.ok || err != nil && !errors.Is(err, errRefused) {
			t.Errorf("%dx%d: %v", c.w, c.h, err)
		}
	}
}

// spsOfSize is the RBSP of a one-sub-layer SPS declaring a w x h picture.
func spsOfSize(w, h int) []byte {
	var b bitWriter
	b.u(4, 0)
	b.u(3, 0)
	b.u(1, 1)
	b.u(96, 0) // profile_tier_level(1, 0)
	b.ue(0)
	b.ue(1) // 4:2:0
	b.ue(uint64(w))
	b.ue(uint64(h))
	b.u(1, 0)
	for range 3 { // bit depths, log2_max_pic_order_cnt_lsb_minus4
		b.ue(0)
	}
	b.u(1, 1)
	b.ue(4)
	b.ue(0)
	b.ue(0)
	return b.trailing()
}

// annexB returns a committed clip's stream in Annex B form, copied by the engine.
func annexB(t *testing.T, name string) []byte {
	t.Helper()
	dir := t.TempDir()
	err := run(t.Context(), engineFor(t), job{prog: "ffmpeg", input: clip(name), outDir: dir, progress: progFiles,
		args: func(in, out string) []string {
			return []string{"-v", "error", "-i", in, "-c", "copy", "-bsf:v", "hevc_mp4toannexb", "-f", "hevc", out + "/s.hevc"}
		}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dir + "/s.hevc")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestNALReader splits streams across read boundaries.
func TestNALReader(t *testing.T) {
	t.Parallel()
	data := annexB(t, "hevc8.mp4")
	want := hevc.SplitAnnexB(data)
	r := &nalReader{r: io.MultiReader(bytes.NewReader(data[:1000]), bytes.NewReader(data[1000:]))}
	n := 0
	for {
		b, err := r.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		nal, ok := hevc.ParseNAL(b)
		if !ok || n >= len(want) || nal.Type != want[n].Type || !bytes.Equal(nal.RBSP, want[n].RBSP) {
			t.Fatalf("NAL %d differs", n)
		}
		n++
	}
	if n != len(want) {
		t.Errorf("%d NAL units, want %d", n, len(want))
	}
	if _, err := (&nalReader{r: errReader{}}).next(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("read error: %v", err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// FuzzFastPathNAL decodes arbitrary bytes as an Annex B stream on the fast path (SECURITY.md 9.1):
// no panic escapes the recover boundary, a refused stream is reported as refused, and no picture
// larger than 7680 x 4320 comes out.
func FuzzFastPathNAL(f *testing.F) {
	for _, name := range []string{"hevc8.mp4", "hevc10hlg.mp4"} {
		data, err := os.ReadFile(clip(name))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data[:min(len(data), 4096)])
	}
	f.Add(craftHEVC(2, 1, 1, 1))
	f.Fuzz(func(t *testing.T, data []byte) {
		if err := checkStream(&nalReader{r: bytes.NewReader(data)}, probeInfo{ClipInfo: ClipInfo{BitDepth: 8}}); err != nil &&
			!errors.Is(err, errRefused) {
			t.Fatalf("check: %v", err)
		}
		dec, _ := newH265(1)
		w := startWorker(dec)
		defer w.stop()
		r := &nalReader{r: bytes.NewReader(data)}
		for {
			b, err := r.next()
			flush := errors.Is(err, io.EOF)
			nal, _ := hevc.ParseNAL(b)
			res, err := w.do(nal, flush, time.Now().Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range res.pics {
				if p.Width*p.Height > fastMaxPixels {
					t.Fatalf("picture %dx%d", p.Width, p.Height)
				}
				p.Release()
			}
			if flush {
				return
			}
		}
	})
}
