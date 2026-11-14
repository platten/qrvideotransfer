package video

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// engineFor returns the embedded backend's loaded engine.
func engineFor(t *testing.T) *engine {
	t.Helper()
	e, err := loadEngine(t.Context(), defaultCacheDir())
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// TestEngineModuleHash pins the engine's WebAssembly binary (docs/SECURITY.md section 8), and checks
// that the embedded copy is go-ffmpreg v0.6.22's, byte for byte, when the module cache has it.
func TestEngineModuleHash(t *testing.T) {
	t.Parallel()
	if EngineSHA256 != "357dd97d37f5fa01b45865bc5d6e90a9538e8bff236de7f1c143a3bc863bc766" {
		t.Fatal("the pinned engine hash changed")
	}
	if _, err := engineWasm(); err != nil {
		t.Fatal(err)
	}
	dirs := []string{os.Getenv("GOMODCACHE")}
	if gp := os.Getenv("GOPATH"); gp != "" {
		dirs = append(dirs, filepath.Join(gp, "pkg", "mod"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "go", "pkg", "mod"))
	}
	for _, d := range dirs {
		b, err := os.ReadFile(filepath.Join(d, "codeberg.org", "gruf", "go-ffmpreg@v0.6.22", "embed", "ffmpreg.wasm.gz")) //nolint:gosec // test
		if d == "" || err != nil {
			continue
		}
		if sha256.Sum256(b) != sha256.Sum256([]byte(engineGz)) {
			t.Fatal("internal/video/ffmpreg.wasm.gz differs from go-ffmpreg v0.6.22's embed/ffmpreg.wasm.gz")
		}
		return
	}
	t.Log("go-ffmpreg's module not found in the module cache; compared with the pinned hash only")
}

// TestInputFS checks the decoding instance's view of the host: one file, readable at any offset and
// seekable, with no write method at all.
func TestInputFS(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "v.mp4")
	if err := os.WriteFile(p, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int64
	v := inputFS{path: p, name: "input.mp4", reads: &reads}
	f, err := v.Open("input.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.(io.Writer); ok {
		t.Fatal("the input file implements io.Writer")
	}
	if _, ok := f.(io.WriterAt); ok {
		t.Fatal("the input file implements io.WriterAt")
	}
	ra, ok1 := f.(io.ReaderAt)
	sk, ok2 := f.(io.Seeker)
	if !ok1 || !ok2 {
		t.Fatal("the input file must implement io.ReaderAt and io.Seeker")
	}
	b := make([]byte, 3)
	if _, err := ra.ReadAt(b, 7); err != nil || string(b) != "789" {
		t.Fatalf("ReadAt: %q %v", b, err)
	}
	if _, err := sk.Seek(2, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Read(b); err != nil || string(b) != "234" {
		t.Fatalf("Read: %q %v", b, err)
	}
	if reads.Load() != 6 {
		t.Errorf("counted %d bytes read", reads.Load())
	}
	st, err := f.Stat()
	if err != nil || st.Size() != 10 || st.Mode().Perm() != 0o444 || st.Name() != "input.mp4" || st.IsDir() || st.Sys() != nil || st.ModTime().IsZero() {
		t.Errorf("stat %v %v", st, err)
	}
	_ = f.Close()
	if _, err := f.Stat(); err == nil {
		t.Error("stat after close")
	}
	for _, name := range []string{"other", "../v.mp4", p, "input.mp4/x"} {
		if _, err := v.Open(name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("open %q: %v", name, err)
		}
	}
	entries, err := fs.ReadDir(v, ".")
	if err != nil || len(entries) != 1 || entries[0].Name() != "input.mp4" {
		t.Errorf("readdir: %v %v", entries, err)
	}
	d, _ := v.Open(".")
	if _, err := d.Read(b); !errors.Is(err, io.EOF) {
		t.Errorf("reading the directory: %v", err)
	}
	rd := d.(fs.ReadDirFile)
	_, _ = rd.ReadDir(-1)
	if e, err := rd.ReadDir(-1); len(e) != 0 || err != nil {
		t.Errorf("second readdir(-1): %v %v", e, err)
	}
	if _, err := rd.ReadDir(1); !errors.Is(err, io.EOF) {
		t.Errorf("second readdir(1): %v", err)
	}
	if st, _ := d.Stat(); !st.IsDir() {
		t.Error("root is not a directory")
	}
	_ = d.Close()
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Open("input.mp4"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("open of a removed file: %v", err)
	}
}

// ffmpegJob runs the engine with arbitrary arguments, with input mounted as for a decode.
func ffmpegJob(ctx context.Context, e *engine, input string, args ...string) (string, error) {
	var log logTail
	err := run(ctx, e, job{prog: "ffmpeg", input: input, stderr: &log, progress: progOutput,
		args: func(in, _ string) []string {
			for i, a := range args {
				args[i] = strings.ReplaceAll(a, "$IN", in)
			}
			return args
		}})
	return log.String(), err
}

// TestSandboxRead: the engine, asked to read a path outside its view, fails (SECURITY.md 9.3).
func TestSandboxRead(t *testing.T) {
	t.Parallel()
	e := engineFor(t)
	dir := t.TempDir()
	in := copyFile(t, clip("hevc444.mp4"), dir)
	other := copyFile(t, clip("hevc8.mp4"), t.TempDir())
	abs, _ := filepath.Abs(other)
	for _, path := range []string{abs, filepath.ToSlash(abs), "/in/../" + filepath.Base(other), "/etc/passwd", "hevc8.mp4", "file:" + abs} {
		_, err := ffmpegJob(t.Context(), e, in, "-v", "error", "-i", path, "-f", "null", "-")
		if !errors.Is(err, ErrEngine) {
			t.Errorf("reading %q from inside the sandbox: %v", path, err)
		}
	}
	// The same arguments on the file in view work.
	if _, err := ffmpegJob(t.Context(), e, in, "-v", "error", "-i", "$IN", "-f", "null", "-"); err != nil {
		t.Fatal(err)
	}
}

// TestSandboxWrite: the engine, asked to write during a decode, fails, through the fs.FS view
// itself (SECURITY.md 9.3); the input file is unchanged.
func TestSandboxWrite(t *testing.T) {
	t.Parallel()
	e := engineFor(t)
	in := copyFile(t, clip("hevc444.mp4"), t.TempDir())
	before, _ := os.ReadFile(in)
	for _, out := range []string{"$IN", "/in/new.mp4", "/new.mp4", "new.mp4"} {
		if _, err := ffmpegJob(t.Context(), e, in, "-v", "error", "-i", "$IN", "-c", "copy", "-f", "mp4", "-y", out); !errors.Is(err, ErrEngine) {
			t.Errorf("writing %q during a decode: %v", out, err)
		}
	}
	after, _ := os.ReadFile(in)
	if !bytes.Equal(before, after) {
		t.Fatal("the input file changed")
	}
	if es, _ := os.ReadDir(filepath.Dir(in)); len(es) != 1 {
		t.Fatalf("files appeared beside the input: %v", es)
	}
}

// TestMemoryCeiling: an instance that needs more than its 512 MiB is stopped and reported, and the
// process carries on (SECURITY.md 9.3).
func TestMemoryCeiling(t *testing.T) {
	t.Parallel()
	e := engineFor(t)
	var p peakAlloc
	log, err := ffmpegJob(withPeak(t.Context(), &p), e, "", "-v", "error", "-f", "lavfi", "-i", "color=s=8192x8192:d=1",
		"-vf", "scale=11000:11000,format=rgba64le", "-frames:v", "1", "-f", "null", "-")
	if !errors.Is(err, ErrEngine) {
		t.Fatalf("over the ceiling: %v (%s)", err, log)
	}
	if p.peak.Load() > EngineMemoryLimit || !strings.Contains(err.Error(), "memory") {
		t.Errorf("peak linear memory %d", p.peak.Load())
	}
	t.Logf("reported: %v; peak linear memory %d MiB", err, p.peak.Load()>>20)
	// The engine still works.
	if _, err := ffmpegJob(t.Context(), e, "", "-v", "error", "-f", "lavfi", "-i", "color=s=64x64:d=0.1", "-f", "null", "-"); err != nil {
		t.Fatal(err)
	}
}

// spin is an instance that runs forever without output: an endless source whose every frame is
// dropped.
var spin = []string{"-v", "error", "-f", "lavfi", "-i", "nullsrc=s=16x16:r=1000", "-vf", "select=0", "-f", "rawvideo", "pipe:1"}

// TestWatchdog: a spinning instance is stopped within EngineStallTimeout plus 5 s, and the error
// names the limit (SECURITY.md 9.3).
func TestWatchdog(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("takes 30 s")
	}
	e := engineFor(t)
	start := time.Now()
	_, err := ffmpegJob(t.Context(), e, "", spin...)
	el := time.Since(start)
	var le *LimitError
	if !errors.As(err, &le) || le.Limit != "EngineStallTimeout" || !strings.Contains(err.Error(), "EngineStallTimeout") {
		t.Fatalf("got %v", err)
	}
	if el < EngineStallTimeout || el > EngineStallTimeout+5*time.Second {
		t.Errorf("stopped after %v", el)
	}
}

// TestCancel: cancelling stops every instance promptly: a spinning one, a decoder and an encoder.
func TestCancel(t *testing.T) {
	t.Parallel()
	e := engineFor(t)
	b := embedded(t)
	ctx, cancel := context.WithCancel(t.Context())
	dir := t.TempDir()
	d, err := b.NewDecoder(ctx, clip("hevc8.mp4"), DecodeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	enc, err := b.NewEncoder(ctx, filepath.Join(dir, "a.mp4"), EncodeConfig{Width: 640, Height: 480, FPS: 12})
	if err != nil {
		t.Fatal(err)
	}
	f, _ := codeFrame(640, 480, 4, 1, 0)
	if err := enc.WriteFrame(f); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 3)
	wg.Go(func() { _, errs[0] = ffmpegJob(ctx, e, "", spin...) })
	wg.Go(func() {
		var fr Frame
		for errs[1] == nil {
			errs[1] = d.Next(&fr)
			time.Sleep(50 * time.Millisecond) // keep the instance alive past the cancel
		}
	})
	wg.Go(func() {
		for errs[2] == nil {
			errs[2] = enc.WriteFrame(f)
		}
	})
	time.Sleep(500 * time.Millisecond)
	start := time.Now()
	cancel()
	wg.Wait()
	if el := time.Since(start); el > 5*time.Second {
		t.Errorf("instances stopped %v after the cancel", el)
	}
	if !errors.Is(errs[0], context.Canceled) || !errors.Is(errs[2], context.Canceled) {
		t.Errorf("errors after cancel: %v", errs)
	}
	if errs[1] == nil || errors.Is(errs[1], io.EOF) && !errors.Is(ctx.Err(), context.Canceled) {
		t.Errorf("decoder after cancel: %v", errs[1])
	}
	_ = d.Close()
	if err := enc.Close(); !errors.Is(err, context.Canceled) {
		t.Errorf("encoder close after cancel: %v", err)
	}
}

// TestMissingReferences: a crafted HEVC stream whose slices list 64 missing reference pictures at
// 3840x2160, 10 bits (25 MB each), decoded by the engine (the default path), stays within the 512 MiB ceiling and
// fails cleanly (SECURITY.md 9.3).
func TestMissingReferences(t *testing.T) {
	t.Parallel()
	e := engineFor(t)
	for _, refs := range [][3]int{{16, 16, 32}, {15, 15, 2}} {
		path := filepath.Join(t.TempDir(), "refs.hevc")
		if err := os.WriteFile(path, craftHEVC(refs[0], refs[1], refs[2], 2), 0o600); err != nil {
			t.Fatal(err)
		}
		var p peakAlloc
		start := time.Now()
		var out countWriter
		err := run(withPeak(t.Context(), &p), e, job{prog: "ffmpeg", input: path, stdout: &out, progress: progOutput,
			args: func(in, _ string) []string {
				return []string{"-v", "error", "-f", "hevc", "-i", in, "-f", "rawvideo", "-pix_fmt", "gray10le", "pipe:1"}
			}})
		t.Logf("refs %v: %v; peak linear memory %d MiB; %d bytes out; %v", refs, err, p.peak.Load()>>20, out.n, time.Since(start))
		if p.peak.Load() > EngineMemoryLimit {
			t.Errorf("peak linear memory %d over the ceiling", p.peak.Load())
		}
		if time.Since(start) > time.Minute {
			t.Errorf("took %v", time.Since(start))
		}
		var le *LimitError
		if err != nil && !errors.Is(err, ErrEngine) && !errors.As(err, &le) {
			t.Errorf("unexpected error %v", err)
		}
	}
}

type countWriter struct{ n int64 }

func (w *countWriter) Write(b []byte) (int, error) { w.n += int64(len(b)); return len(b), nil }

// bitWriter writes an HEVC RBSP.
type bitWriter struct {
	b    []byte
	cur  byte
	bits int
}

func (w *bitWriter) u(n int, v uint64) {
	for i := n - 1; i >= 0; i-- {
		w.cur = w.cur<<1 | byte(v>>uint(i)&1)
		w.bits++
		if w.bits == 8 {
			w.b, w.cur, w.bits = append(w.b, w.cur), 0, 0
		}
	}
}

func (w *bitWriter) ue(v uint64) {
	v++
	n := 0
	for x := v; x > 1; x >>= 1 {
		n++
	}
	w.u(n, 0)
	w.u(n+1, v)
}

func (w *bitWriter) se(v int64) {
	if v > 0 {
		w.ue(uint64(2*v - 1))
	} else {
		w.ue(uint64(-2 * v))
	}
}

// trailing writes rbsp_trailing_bits (or byte_alignment, which is the same).
func (w *bitWriter) trailing() []byte {
	w.u(1, 1)
	for w.bits != 0 {
		w.u(1, 0)
	}
	return w.b
}

// nal frames an RBSP as an Annex B NAL unit with emulation prevention.
func nal(typ int, rbsp []byte) []byte {
	out := []byte{0, 0, 0, 1, byte(typ << 1), 1}
	zeros := 0
	for _, c := range rbsp {
		if zeros >= 2 && c <= 3 {
			out, zeros = append(out, 3), 0
		}
		out = append(out, c)
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

func profileTierLevel(w *bitWriter) {
	w.u(2, 0)
	w.u(1, 0)
	w.u(5, 2)      // Main 10
	w.u(32, 1<<29) // compatible with profile 2
	w.u(4, 0b1001) // progressive, frame only
	w.u(44, 0)
	w.u(8, 153) // level 5.1
}

// craftHEVC writes a 3840x2160 10-bit stream: an IDR picture, then n P pictures whose slice headers
// name neg + pos short-term and lt long-term reference pictures that were never sent. The slice data
// is garbage.
func craftHEVC(neg, pos, lt, n int) []byte {
	var s []byte
	var w bitWriter
	w.u(4, 0)
	w.u(2, 3)
	w.u(6, 0)
	w.u(3, 0)
	w.u(1, 1)
	w.u(16, 0xffff)
	profileTierLevel(&w)
	w.u(1, 1)
	w.ue(15)
	w.ue(0)
	w.ue(0)
	w.u(6, 0)
	w.ue(0)
	w.u(1, 0)
	w.u(1, 0)
	s = append(s, nal(32, w.trailing())...) // VPS

	w = bitWriter{}
	w.u(4, 0)
	w.u(3, 0)
	w.u(1, 1)
	profileTierLevel(&w)
	w.ue(0)    // sps id
	w.ue(1)    // 4:2:0
	w.ue(3840) // width
	w.ue(2160) // height
	w.u(1, 0)
	w.ue(2) // luma 10 bits
	w.ue(2) // chroma 10 bits
	w.ue(4) // poc lsb 8 bits
	w.u(1, 1)
	w.ue(15) // a buffer of 16 pictures
	w.ue(0)
	w.ue(0)
	w.ue(0) // min CB 8
	w.ue(3) // CTB 64
	w.ue(0)
	w.ue(3)
	w.ue(0)
	w.ue(0)
	w.u(1, 0) // scaling lists
	w.u(1, 0) // amp
	w.u(1, 0) // sao
	w.u(1, 0) // pcm
	w.ue(0)   // no short-term sets in the SPS
	w.u(1, 1) // long-term references present
	w.ue(0)
	w.u(1, 0) // temporal mvp
	w.u(1, 0) // strong intra smoothing
	w.u(1, 0) // vui
	w.u(1, 0) // extensions
	s = append(s, nal(33, w.trailing())...)

	w = bitWriter{}
	w.ue(0)
	w.ue(0)
	w.u(1, 0)
	w.u(1, 0)
	w.u(3, 0)
	w.u(1, 0)
	w.u(1, 0)
	w.ue(0)
	w.ue(0)
	w.se(0)
	for range 3 {
		w.u(1, 0)
	}
	w.se(0)
	w.se(0)
	for range 10 {
		w.u(1, 0)
	}
	w.ue(0)
	w.u(1, 0)
	w.u(1, 0)
	s = append(s, nal(34, w.trailing())...) // PPS

	garbage := bytes.Repeat([]byte{0x80}, 16)
	w = bitWriter{}
	w.u(1, 1) // first slice
	w.u(1, 0) // no_output_of_prior_pics
	w.ue(0)
	w.ue(2) // I
	w.se(0)
	s = append(s, nal(19, append(w.trailing(), garbage...))...) // IDR_W_RADL

	for i := range n {
		w = bitWriter{}
		w.u(1, 1)
		w.ue(0)
		w.ue(1)                       // P
		w.u(8, uint64(100+i*40)&0xff) // poc lsb
		w.u(1, 0)                     // explicit short-term set
		w.ue(uint64(neg))
		w.ue(uint64(pos))
		for range neg {
			w.ue(0)
			w.u(1, 1)
		}
		for range pos {
			w.ue(0)
			w.u(1, 1)
		}
		w.ue(uint64(lt))
		for k := range lt {
			w.u(8, uint64(k*7+3)&0xff)
			w.u(1, 1)
			w.u(1, 0)
		}
		w.u(1, 0) // num_ref_idx_active_override
		w.ue(0)   // five_minus_max_num_merge_cand
		w.se(0)
		s = append(s, nal(1, append(w.trailing(), garbage...))...) // TRAIL_R
	}
	return s
}

// TestHostileFiles: truncated, zero-length, wrong-container and structurally corrupt files each end
// in a clear error within a time bound (SECURITY.md 9.2).
func TestHostileFiles(t *testing.T) {
	t.Parallel()
	b := embedded(t)
	dir := t.TempDir()
	good := filepath.Join(dir, "good.mp4")
	encodeCode(t, b, good, 640, 480, 4, 12, 4)
	data, _ := os.ReadFile(good)
	corrupt := bytes.Clone(data)
	corrupt = bytes.ReplaceAll(corrupt, []byte("trak"), []byte("junk")) // no track left
	badStsd := bytes.ReplaceAll(bytes.Clone(data), []byte("avcC"), []byte("junk"))
	files := map[string][]byte{
		"empty.mp4":     nil,
		"truncated.mp4": data[:len(data)/2],
		"text.mp4":      []byte("this is not a video file at all\n"),
		"png.mp4":       append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 100)...),
		"corrupt.mp4":   corrupt,
		"nostsd.mp4":    badStsd,
		"noise.hevc":    bytes.Repeat([]byte{0, 0, 1, 0x40, 1, 0xff, 0x13}, 1000),
		"hls.mp4":       []byte(hlsPlaylist),
	}
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, content, 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		start := time.Now()
		_, err := b.NewDecoder(ctx, p, DecodeConfig{})
		cancel()
		if !errors.Is(err, ErrInvalidVideo) {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if el := time.Since(start); el > 60*time.Second || name == "hls.mp4" && el > 5*time.Second {
			t.Errorf("%s: %v", name, el)
		}
		if strings.ContainsAny(err.Error(), "\x1b\x00\r\n") {
			t.Errorf("%s: control characters in %q", name, err)
		}
	}
	if _, err := b.Probe(t.Context(), dir); !errors.Is(err, ErrInvalidVideo) {
		t.Errorf("directory: %v", err)
	}
	if _, err := b.Probe(t.Context(), filepath.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
}

// hlsPlaylist is a 110-byte live HLS playlist. ffmpeg detects it whatever the file's extension and,
// without the format whitelist, rereads it every 29 s for ever, each read counting as progress.
const hlsPlaylist = "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:29\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:29.0,\ninput.m3u8\n"

// TestProbeBound: without the format whitelist, the playlist holds ffprobe until the probe's time
// bound stops it.
func TestProbeBound(t *testing.T) {
	t.Parallel()
	e := engineFor(t)
	p := filepath.Join(t.TempDir(), "x.mp4")
	if err := os.WriteFile(p, []byte(hlsPlaylist), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err := run(t.Context(), e, job{prog: "ffprobe", input: p, progress: progReads, check: probeLimits(int64(len(hlsPlaylist)), 3*time.Second),
		args: func(in, _ string) []string { return []string{"-v", "error", "-show_entries", "format=format_name", in} }})
	var le *LimitError
	if el := time.Since(start); !errors.As(err, &le) || le.Limit != "EngineProbeTime" || el > 8*time.Second {
		t.Errorf("got %v after %v", err, el)
	}
}

// TestFormatWhitelist: the whitelist names exactly the engine's demuxers for the containers qrvt
// reads (MP4 and MOV, Matroska and WebM, MPEG-TS, H.264 and HEVC elementary streams).
func TestFormatWhitelist(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	err := run(t.Context(), engineFor(t), job{prog: "ffmpeg", stdout: &out, progress: progReads,
		args: func(string, string) []string { return []string{"-hide_banner", "-demuxers"} }})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	table := false // the list follows a line "--"
	for line := range strings.Lines(out.String()) {
		f := strings.Fields(line)
		if len(f) == 1 && f[0] == "--" {
			table = true
		}
		if !table || len(f) < 2 {
			continue
		}
		for name := range strings.SplitSeq(f[1], ",") {
			if slices.Contains(strings.Split(formatWhitelist, ","), name) {
				got = append(got, f[1])
				break
			}
		}
	}
	want := []string{"h264", "hevc", "matroska,webm", "mov,mp4,m4a,3gp,3g2,mj2", "mpegts"}
	if slices.Sort(got); !slices.Equal(got, want) {
		t.Errorf("the whitelist matches %q, want %q", got, want)
	}
}

// TestMetadataInjection: container metadata shaped like a showinfo line, which ffmpeg prints with
// the input's description, does not shift the frames' timestamps.
func TestMetadataInjection(t *testing.T) {
	t.Parallel()
	e, b := engineFor(t), embedded(t)
	dir := t.TempDir()
	fake := "[Parsed_showinfo_0 @ 0x1] n:   0 pts:  999 pts_time:1 pos: 1 fmt:yuv420p s:384x216 i:P"
	err := run(t.Context(), e, job{prog: "ffmpeg", input: clip("hevc8.mp4"), outDir: dir, progress: progFiles,
		args: func(in, out string) []string {
			return []string{"-v", "error", "-i", in, "-map", "0", "-c", "copy", "-metadata", "title=" + fake,
				"-metadata:s:v", "comment=\n" + fake, "-f", "mp4", out + "/meta.mp4"}
		}})
	if err != nil {
		t.Fatal(err)
	}
	var got [2][]Frame
	for i, p := range []string{clip("hevc8.mp4"), filepath.Join(dir, "meta.mp4")} {
		d, err := b.NewDecoder(t.Context(), p, DecodeConfig{BrightnessOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		got[i] = decodeAll(t, d)
	}
	if len(got[0]) != 24 || len(got[1]) != 24 {
		t.Fatalf("%d and %d frames", len(got[0]), len(got[1]))
	}
	for i := range got[0] {
		if got[0][i].PTS != got[1][i].PTS || got[0][i].Index != got[1][i].Index {
			t.Fatalf("frame %d: %v, with the metadata %v", i, got[0][i].PTS, got[1][i].PTS)
		}
	}
}

// TestCaptureLimits: a picture wider or taller than the capture limit is refused from the probe,
// and a stream whose picture size changes stops when it does (SECURITY.md section 5).
func TestCaptureLimits(t *testing.T) {
	t.Parallel()
	e, b := engineFor(t), embedded(t)
	dir := t.TempDir()
	for _, c := range []struct{ size, limit string }{{"8194x16", "MaxCaptureWidth"}, {"16x8194", "MaxCaptureHeight"}} {
		err := run(t.Context(), e, job{prog: "ffmpeg", outDir: dir, progress: progFiles,
			args: func(_, out string) []string {
				return []string{"-v", "error", "-f", "lavfi", "-i", "color=s=" + c.size + ":r=10:d=0.2", "-c:v", "libx264",
					"-pix_fmt", "yuv420p", "-f", "mp4", out + "/" + c.size + ".mp4"}
			}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = b.Probe(t.Context(), filepath.Join(dir, c.size+".mp4"))
		var le *LimitError
		if !errors.As(err, &le) || le.Limit != c.limit || le.Value != 8194 {
			t.Errorf("%s: %v", c.size, err)
		}
	}
	var stream []byte
	for i, size := range []int{64, 32} {
		p := filepath.Join(dir, fmt.Sprintf("run%d.h264", i))
		encodeCode(t, b, p, size, size, 4, 12, 2)
		data, err := os.ReadFile(p) //nolint:gosec // test
		if err != nil {
			t.Fatal(err)
		}
		stream = append(stream, data...)
	}
	p := filepath.Join(dir, "change.h264")
	if err := os.WriteFile(p, stream, 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := b.NewDecoder(t.Context(), p, DecodeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var f Frame
	n := 0
	for err == nil {
		if err = d.Next(&f); err == nil {
			n++
		}
	}
	_ = d.Close()
	// ffprobe reports the stream's last parameter set, so the decode stops at the first frame or
	// the third.
	if !errors.Is(err, ErrInvalidVideo) || !strings.Contains(err.Error(), "picture size changed") || n > 2 {
		t.Errorf("size change after %d frames: %v", n, err)
	}
}

// TestSkipPictures: a small file of many skip pictures is refused from the probe by
// MaxDecodedPixelsPerInputByte; the same file in a container that lies about its length is stopped
// while decoding (SECURITY.md 9.5). Both stay under 1 GiB and finish within 60 s. Not parallel, so
// that the process's heap is this decode's: the Go heap peak, which includes the instance's whole
// 512 MiB reservation, is sampled while it runs.
func TestSkipPictures(t *testing.T) {
	b := embedded(t)
	_, err := b.NewDecoder(t.Context(), clip("skip.mkv"), DecodeConfig{BrightnessOnly: true})
	var le *LimitError
	if !errors.As(err, &le) || le.Limit != "MaxDecodedPixelsPerInputByte" || le.Value < 100_000 || le.Max != 10_000 ||
		!strings.Contains(err.Error(), "trim") {
		t.Fatalf("probe check: %v", err)
	}
	t.Log(err)

	// Rewrite the Matroska Duration (60,000 ms) to 100 ms: the probe then promises 3 frames.
	data, _ := os.ReadFile(clip("skip.mkv"))
	i := bytes.Index(data, []byte{0x44, 0x89, 0x88})
	if i < 0 {
		t.Fatal("no Duration element")
	}
	binary.BigEndian.PutUint64(data[i+3:], math.Float64bits(100))
	lying := filepath.Join(t.TempDir(), "lying.mkv")
	if err := os.WriteFile(lying, data, 0o600); err != nil { //nolint:gosec // test
		t.Fatal(err)
	}
	var p peakAlloc
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	heap0 := ms.HeapAlloc
	var peak atomic.Uint64
	stop := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			peak.Store(max(peak.Load(), m.HeapAlloc))
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	start := time.Now()
	d, err := b.NewDecoder(withPeak(t.Context(), &p), lying, DecodeConfig{BrightnessOnly: true})
	if err != nil {
		t.Fatalf("the lying file was refused from the probe: %v", err)
	}
	if d.Info().Frames != 3 {
		t.Errorf("probe of the lying file: %+v", d.Info())
	}
	var f Frame
	n := 0
	for err == nil {
		if err = d.Next(&f); err == nil {
			n++
		}
	}
	close(stop)
	<-sampled
	_ = d.Close()
	if !errors.As(err, &le) || le.Limit != "MaxDecodedPixelsPerInputByte" {
		t.Fatalf("decoding the lying file: %v after %d frames", err, n)
	}
	if el := time.Since(start); el > 60*time.Second {
		t.Errorf("took %v", el)
	}
	if used := peak.Load() - min(peak.Load(), heap0); used > 1<<30 || p.peak.Load() > EngineMemoryLimit {
		t.Errorf("peak heap growth %d MiB, linear memory %d MiB", used>>20, p.peak.Load()>>20)
	} else {
		t.Logf("peak Go heap growth %d MiB, linear memory included", used>>20)
	}
	if n > 50 {
		t.Errorf("%d frames decoded before the stop", n)
	}
	t.Logf("stopped after %d frames: %v; peak linear memory %d MiB", n, err, p.peak.Load()>>20)
}
