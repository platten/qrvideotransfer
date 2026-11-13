//go:build unsandboxedhevc

package video

import (
	"bytes"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
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
