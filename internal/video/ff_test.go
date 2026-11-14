package video

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// frameProps lists, per frame, what ffprobe says about a file's pictures.
func frameProps(t *testing.T, r runner, path string) (profile string, frames []map[string]any) {
	t.Helper()
	var out bytes.Buffer
	err := run(t.Context(), r, job{prog: "ffprobe", input: path, stdout: &out, progress: progReads,
		args: func(in, _ string) []string {
			return []string{"-v", "error", "-of", "json", "-select_streams", "v:0", "-show_entries",
				"stream=profile:frame=key_frame,pict_type,color_range,color_space,color_primaries,color_transfer", in}
		}})
	if err != nil {
		t.Fatal(err)
	}
	var j struct {
		Streams []struct {
			Profile string `json:"profile"`
		} `json:"streams"`
		Frames []map[string]any `json:"frames"`
	}
	if err := json.Unmarshal(out.Bytes(), &j); err != nil || len(j.Streams) != 1 {
		t.Fatalf("ffprobe output %q: %v", out.String(), err)
	}
	return j.Streams[0].Profile, j.Frames
}

// TestEncodeOutput checks the embedded encoder's file against FORMAT.md section 3.
func TestEncodeOutput(t *testing.T) {
	t.Parallel()
	b := embedded(t)
	path := filepath.Join(t.TempDir(), "code.mp4")
	encodeCode(t, b, path, 640, 480, 4, 12, 8)
	p, err := probe(t.Context(), b.r, path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Codec != "h264" || p.PixFmt != "yuv420p" || p.Width != 640 || p.Height != 480 || p.Frames != 8 ||
		p.FPSNum != 12 || p.FPSDen != 1 || p.Transfer != "bt709" || p.Range != "tv" || p.BitDepth != 8 ||
		p.Container != "mov,mp4,m4a,3gp,3g2,mj2" {
		t.Fatalf("probe: %+v", p)
	}
	if d := p.Duration - time.Second*8/12; d < -time.Millisecond || d > time.Millisecond {
		t.Errorf("duration %v", p.Duration)
	}
	profile, frames := frameProps(t, b.r, path)
	if !slices.Contains([]string{"Main", "High", "77", "100"}, profile) {
		t.Errorf("profile %q, want Main or High", profile)
	}
	if len(frames) != 8 {
		t.Fatalf("%d frames", len(frames))
	}
	for i, f := range frames {
		if f["key_frame"] != float64(1) || f["pict_type"] != "I" || f["color_range"] != "tv" ||
			f["color_space"] != "bt709" || f["color_primaries"] != "bt709" || f["color_transfer"] != "bt709" {
			t.Errorf("frame %d: %v", i, f)
		}
	}
}

// TestDigitalLoopback is the M1 stream D exit test: encode a Gray4 code at 480p with the embedded
// engine, decode it, and read every symbol back by the nearest of four greys at each cell centre.
func TestDigitalLoopback(t *testing.T) {
	t.Parallel()
	b := embedded(t)
	loopback(t, b, b)
}

func loopback(t *testing.T, enc, dec Backend) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "code.mp4")
	syms := encodeCode(t, enc, path, 640, 480, 4, 12, 12)
	for _, gray := range []bool{false, true} {
		d, err := dec.NewDecoder(t.Context(), path, DecodeConfig{BrightnessOnly: gray, BitDepth: 8})
		if err != nil {
			t.Fatal(err)
		}
		if d.Info().Codec != "h264" {
			t.Errorf("info %+v", d.Info())
		}
		frames := decodeAll(t, d)
		if len(frames) != len(syms) {
			t.Fatalf("decoded %d frames, want %d", len(frames), len(syms))
		}
		for i, f := range frames {
			if f.Index != i || f.PTS != time.Duration(i)*time.Second/12 || f.Bits != 8 || (f.Cb == nil) != gray {
				t.Fatalf("frame %d: index %d pts %v bits %d", i, f.Index, f.PTS, f.Bits)
			}
			if got := readSymbols(&f, 4); !bytes.Equal(got, syms[i]) {
				n := 0
				for k := range got {
					if got[k] != syms[i][k] {
						n++
					}
				}
				t.Fatalf("frame %d (gray %v): %d of %d symbols wrong", i, gray, n, len(got))
			}
		}
	}
}

// TestSlicedDecode decodes time slices and checks that together they cover every frame once.
func TestSlicedDecode(t *testing.T) {
	t.Parallel()
	b := embedded(t)
	path := filepath.Join(t.TempDir(), "code.mp4")
	encodeCode(t, b, path, 640, 480, 4, 12, 36)
	checkSlices(t, b, path, 36, []time.Duration{0, 1100 * time.Millisecond, 2 * time.Second, 2500 * time.Millisecond})
	// HEVC with B-frames and open GOPs (random access points whose leading pictures need the
	// previous GOP): seeking must not lose them.
	checkSlices(t, b, clip("hevc8.mp4"), 24, []time.Duration{0, 300 * time.Millisecond, 410 * time.Millisecond, 700 * time.Millisecond})
}

func checkSlices(t *testing.T, b Backend, path string, n int, starts []time.Duration) {
	t.Helper()
	seen := make([]int, n)
	for k, s := range starts {
		c := DecodeConfig{Start: s, BrightnessOnly: true}
		if k+1 < len(starts) {
			c.Length = starts[k+1] - s
		}
		d, err := b.NewDecoder(t.Context(), path, c)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range decodeAll(t, d) {
			if f.PTS < c.Start || (c.Length > 0 && f.PTS >= c.Start+c.Length) || f.Index < 0 || f.Index >= n {
				t.Fatalf("slice %v+%v gave frame %d at %v", c.Start, c.Length, f.Index, f.PTS)
			}
			seen[f.Index]++
		}
	}
	for i, c := range seen {
		if c != 1 {
			t.Errorf("%s: frame %d decoded %d times", filepath.Base(path), i, c)
		}
	}
}

// TestDecodeHEVC decodes the committed HEVC clips with the engine (the default path for HEVC).
func TestDecodeHEVC(t *testing.T) {
	t.Parallel()
	b := embedded(t)
	for _, tc := range []struct {
		name     string
		bits     int
		transfer string
	}{{"hevc8.mp4", 8, "bt709"}, {"hevc10hlg.mp4", 10, "arib-std-b67"}} {
		d, err := b.NewDecoder(t.Context(), clip(tc.name), DecodeConfig{BitDepth: tc.bits})
		if err != nil {
			t.Fatal(err)
		}
		in := d.Info()
		if in.Codec != "hevc" || in.BitDepth != tc.bits || in.Transfer != tc.transfer || in.Width != 384 || in.Frames != 24 {
			t.Errorf("%s: %+v", tc.name, in)
		}
		frames := decodeAll(t, d)
		if len(frames) != 24 {
			t.Fatalf("%s: %d frames", tc.name, len(frames))
		}
		for _, f := range frames {
			if int(f.Bits) != tc.bits || len(f.Cb) != 192*108 || slices.Max(f.Y) >= 1<<tc.bits {
				t.Fatalf("%s: frame %d bits %d", tc.name, f.Index, f.Bits)
			}
		}
	}
	// 10-bit output only from a 10-bit source, and 4:4:4 is not read in full colour.
	if _, err := b.NewDecoder(t.Context(), clip("hevc8.mp4"), DecodeConfig{BitDepth: 10}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("10-bit from 8-bit: %v", err)
	}
	if _, err := b.NewDecoder(t.Context(), clip("hevc444.mp4"), DecodeConfig{}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("4:4:4: %v", err)
	}
	d, err := b.NewDecoder(t.Context(), clip("hevc444.mp4"), DecodeConfig{BrightnessOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(decodeAll(t, d)); n != 4 {
		t.Errorf("4:4:4 luma: %d frames", n)
	}
}

// TestNativeBackend runs the same checks with a native ffmpeg, and requires its decode of the
// engine's file to equal the engine's own. Skipped when no ffmpeg is on PATH.
func TestNativeBackend(t *testing.T) {
	t.Parallel()
	s := system(t)
	e := embedded(t)
	loopback(t, s, s)
	loopback(t, e, s)
	loopback(t, s, e)
	for _, name := range []string{"hevc8.mp4", "hevc10hlg.mp4"} {
		var got [2][]Frame
		for i, b := range []Backend{e, s} {
			d, err := b.NewDecoder(t.Context(), clip(name), DecodeConfig{})
			if err != nil {
				t.Fatal(err)
			}
			got[i] = decodeAll(t, d)
		}
		if len(got[0]) != len(got[1]) {
			t.Fatalf("%s: %d and %d frames", name, len(got[0]), len(got[1]))
		}
		for i := range got[0] {
			a, b := got[0][i], got[1][i]
			if a.Index != b.Index || a.PTS != b.PTS || !slices.Equal(a.Y, b.Y) || !slices.Equal(a.Cb, b.Cb) || !slices.Equal(a.Cr, b.Cr) {
				t.Fatalf("%s: frame %d differs between the engine and the native ffmpeg", name, i)
			}
		}
	}
	p := filepath.Join(t.TempDir(), "x.mp4")
	encodeCode(t, s, p, 64, 64, 4, 12, 2)
	if _, err := s.Probe(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	// The native ffmpeg gets the same whitelists: the HLS playlist is refused at once, by the format
	// whitelist (named .m3u8, which native ffmpeg 6.1 would otherwise open as HLS; named .mp4, it
	// detects mov and fails without the whitelist being tested).
	hls := filepath.Join(t.TempDir(), "x.m3u8")
	if err := os.WriteFile(hls, []byte(hlsPlaylist), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	start := time.Now()
	if _, err := s.Probe(ctx, hls); !errors.Is(err, ErrInvalidVideo) || !strings.Contains(err.Error(), "not on whitelist") ||
		time.Since(start) > 15*time.Second {
		t.Errorf("HLS playlist: %v after %v", err, time.Since(start))
	}
}

// TestConcat joins two even-length elementary-stream runs into one MP4 and refuses an odd run.
func TestConcat(t *testing.T) {
	t.Parallel()
	b := embedded(t)
	dir := t.TempDir()
	var runs []string
	var syms [][]uint8
	for i := range 2 {
		p := filepath.Join(dir, "run"+string(rune('0'+i))+".h264")
		syms = append(syms, encodeCode(t, b, p, 640, 480, 4, 12, 4)...)
		runs = append(runs, p)
	}
	out := filepath.Join(dir, "all.mp4")
	if err := b.Concat(t.Context(), runs, out, 12); err != nil {
		t.Fatal(err)
	}
	d, err := b.NewDecoder(t.Context(), out, DecodeConfig{BrightnessOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if d.Info().Frames != 8 {
		t.Errorf("info %+v", d.Info())
	}
	frames := decodeAll(t, d)
	if len(frames) != 8 {
		t.Fatalf("%d frames", len(frames))
	}
	for i, f := range frames {
		if !bytes.Equal(readSymbols(&f, 4), syms[i]) {
			t.Fatalf("frame %d wrong", i)
		}
	}
	_, props := frameProps(t, b.r, out)
	for i, f := range props {
		if f["key_frame"] != float64(1) {
			t.Errorf("frame %d not a key frame", i)
		}
	}

	odd := filepath.Join(dir, "odd.h264")
	e, err := b.NewEncoder(t.Context(), odd, EncodeConfig{Width: 64, Height: 64, FPS: 12})
	if err != nil {
		t.Fatal(err)
	}
	f, _ := codeFrame(64, 64, 4, 1, 0)
	if err := e.WriteFrame(f); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); !errors.Is(err, ErrOddRun) {
		t.Errorf("odd run: %v", err)
	}
	if _, err := os.Stat(odd); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("odd run left a file: %v", err)
	}
	if err := b.Concat(t.Context(), nil, out, 12); !errors.Is(err, ErrUnsupported) {
		t.Errorf("empty concat: %v", err)
	}
	if err := b.Concat(t.Context(), []string{filepath.Join(dir, "missing")}, out, 12); err == nil {
		t.Error("concat of a missing run succeeded")
	}
	if err := b.Concat(t.Context(), []string{out}, filepath.Join(dir, "bad.mp4"), 12); !errors.Is(err, ErrEngine) {
		t.Errorf("concat of an MP4 as a run: %v", err)
	}
}

// TestEncoderErrors covers the encoder's refusals.
func TestEncoderErrors(t *testing.T) {
	t.Parallel()
	b := embedded(t)
	dir := t.TempDir()
	for _, c := range []EncodeConfig{{Width: 63, Height: 64, FPS: 12}, {Width: 64, Height: 64}, {Width: 9000, Height: 64, FPS: 1},
		{Width: 64, Height: 64, FPS: 12, BitDepth: 9}, {Width: 64, Height: 64, FPS: 12, Quantiser: 52}} {
		if _, err := b.NewEncoder(t.Context(), filepath.Join(dir, "a.mp4"), c); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
	if _, err := b.NewEncoder(t.Context(), filepath.Join(dir, "a.avi"), EncodeConfig{Width: 64, Height: 64, FPS: 12}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("avi: %v", err)
	}
	if _, err := b.NewEncoder(t.Context(), filepath.Join(dir, "missing", "a.mp4"), EncodeConfig{Width: 64, Height: 64, FPS: 12}); err == nil {
		t.Error("encoder in a missing directory")
	}
	e, err := b.NewEncoder(t.Context(), filepath.Join(dir, "a.mp4"), EncodeConfig{Width: 64, Height: 64, FPS: 12})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.WriteFrame(&Frame{W: 32, H: 32}); !errors.Is(err, ErrFrame) {
		t.Errorf("wrong frame: %v", err)
	}
	f, _ := codeFrame(64, 64, 4, 1, 0)
	f.Y[0] = 256
	if err := e.WriteFrame(f); !errors.Is(err, ErrFrame) {
		t.Errorf("9-bit sample: %v", err)
	}
	if err := e.Close(); !errors.Is(err, ErrFrame) {
		t.Errorf("close with no frames: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
	if err := e.WriteFrame(f); !errors.Is(err, ErrFrame) {
		t.Errorf("write after close: %v", err)
	}

	// 10-bit output (experimental HDR) is High 10.
	p := filepath.Join(dir, "hdr.mp4")
	e, err = b.NewEncoder(t.Context(), p, EncodeConfig{Width: 64, Height: 64, FPS: 12, BitDepth: 10, Quantiser: 20})
	if err != nil {
		t.Fatal(err)
	}
	f, _ = codeFrame(64, 64, 4, 1, 0)
	for i := range f.Y {
		f.Y[i] *= 4
	}
	for i := range f.Cb {
		f.Cb[i], f.Cr[i] = 512, 512
	}
	f.Bits = 10
	for range 2 {
		if err := e.WriteFrame(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	d, err := b.NewDecoder(t.Context(), p, DecodeConfig{BitDepth: 10})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeAll(t, d)
	if len(got) != 2 || got[0].Bits != 10 || abs(int(got[0].Y[0])-int(f.Y[0])) > 4 {
		t.Errorf("10-bit round trip: %d frames, first sample %d", len(got), got[0].Y[0])
	}
}

// TestDecoderClose checks that closing a decoder early stops its instance, and that Next afterwards
// fails.
func TestDecoderClose(t *testing.T) {
	t.Parallel()
	b := embedded(t)
	d, err := b.NewDecoder(t.Context(), clip("hevc8.mp4"), DecodeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var f Frame
	if err := d.Next(&f); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.Next(&f); err == nil {
		t.Error("Next after Close succeeded")
	}
	if err := d.Next(&f); errors.Is(err, io.EOF) {
		t.Error("Next after Close reports EOF")
	}
}
