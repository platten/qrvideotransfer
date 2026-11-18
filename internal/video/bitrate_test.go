package video

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestEncodeArgsQuantiserUnchanged: without a bitrate the encoder's arguments are exactly those
// the display video has always used (constant quantiser, intra only), so the golden vectors and
// the MP4s already probed are untouched.
func TestEncodeArgsQuantiserUnchanged(t *testing.T) {
	got := encodeArgs(EncodeConfig{Width: 1920, Height: 1080, FPS: 12}, "o/x.mp4", "mp4")
	want := []string{"-nostdin", "-hide_banner", "-nostats", "-loglevel", "error",
		"-f", "rawvideo", "-pix_fmt", "yuv420p", "-s", "1920x1080", "-framerate", "12",
		"-color_range", "tv", "-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709",
		"-i", "pipe:0", "-map", "0:v:0", "-c:v", "libx264", "-preset", "veryfast", "-qp", "32",
		"-x264-params", "keyint=1:min-keyint=1:scenecut=0:bframes=0:cabac=1:no-deblock=1:aq-mode=0:psy=0:trellis=0:partitions=i4x4:no-8x8dct=1",
		"-color_range", "tv", "-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709",
		"-pix_fmt", "yuv420p", "-use_editlist", "0", "-f", "mp4", "-y", "o/x.mp4"}
	if !slices.Equal(got, want) {
		t.Errorf("arguments changed:\n got %q\nwant %q", got, want)
	}
}

// TestEncodeArgsBitrate: with a bitrate the encoder stands in for a phone's recorder (DESIGN.md
// section 15): x264's defaults at veryfast, a GOP of one second at the frame rate, single-pass
// average bitrate capped at the bitrate with a two-second buffer, no lookahead and no B-frames
// (forced by the engine's 512 MiB memory bound at 4K, DESIGN.md section 15), the BT.709 tags,
// High 10 for 10-bit input; neither the constant quantiser nor the intra-only parameters.
func TestEncodeArgsBitrate(t *testing.T) {
	for _, bits := range []int{8, 10} {
		a := encodeArgs(EncodeConfig{Width: 3840, Height: 2160, FPS: 60, BitDepth: bits, Bitrate: 74_000_000}, "o/x.mp4", "mp4")
		s := strings.Join(a, " ")
		for _, want := range []string{"-c:v libx264", "-preset veryfast", "-b:v 74000000", "-maxrate 74000000", "-bufsize 148000000",
			"-g 60", "-x264-params rc-lookahead=0:bframes=0", "-color_trc bt709"} {
			if !strings.Contains(s, want) {
				t.Errorf("%d-bit: arguments lack %q: %s", bits, want, s)
			}
		}
		if slices.Contains(a, "-qp") || strings.Contains(s, "keyint=1") {
			t.Errorf("%d-bit: constant quantiser or intra-only parameters with a bitrate: %s", bits, s)
		}
		if high10 := strings.Contains(s, "-profile:v high10"); high10 != (bits == 10) {
			t.Errorf("%d-bit: High 10 %v", bits, high10)
		}
	}
}

// TestEncodeBitrateRoundTrip: the engine takes the recorder's arguments: 8- and 10-bit clips at a
// bitrate are written, probed and read back whole.
func TestEncodeBitrateRoundTrip(t *testing.T) {
	ctx := context.Background()
	b := embedded(t)
	for _, bits := range []int{8, 10} {
		path := filepath.Join(t.TempDir(), "rec.mp4")
		c := EncodeConfig{Width: 320, Height: 240, FPS: 30, BitDepth: bits, Bitrate: 2_000_000}
		e, err := b.NewEncoder(ctx, path, c)
		if err != nil {
			t.Fatal(err)
		}
		for i := range 12 {
			f := &Frame{W: 320, H: 240, Bits: uint8(bits), Y: make([]uint16, 320*240), Cb: make([]uint16, 160*120), Cr: make([]uint16, 160*120)}
			for j := range f.Y {
				f.Y[j] = uint16((16 + (j+i*7)%200) << (bits - 8))
			}
			for j := range f.Cb {
				f.Cb[j], f.Cr[j] = 128<<(bits-8), 128<<(bits-8)
			}
			if err := e.WriteFrame(f); err != nil {
				t.Fatal(err)
			}
		}
		if err := e.Close(); err != nil {
			t.Fatal(err)
		}
		info, err := b.Probe(ctx, path)
		if err != nil || info.BitDepth != bits || info.Width != 320 {
			t.Fatalf("%d-bit probe %+v: %v", bits, info, err)
		}
		d, err := b.NewDecoder(ctx, path, DecodeConfig{BitDepth: bits})
		if err != nil {
			t.Fatal(err)
		}
		if got := decodeAll(t, d); len(got) != 12 {
			t.Errorf("%d-bit: %d frames read back, want 12", bits, len(got))
		}
	}
}

// TestEncodeConfigBitrate: a bitrate must be positive and at most MaxBitrate, comes without a
// quantiser, and the raw-frame backend, which cannot compress, refuses it.
func TestEncodeConfigBitrate(t *testing.T) {
	ok := EncodeConfig{Width: 640, Height: 480, FPS: 30, Bitrate: 8_000_000}
	if err := checkEncodeConfig(ok); err != nil {
		t.Errorf("valid bitrate refused: %v", err)
	}
	for name, c := range map[string]EncodeConfig{
		"negative":       {Width: 640, Height: 480, FPS: 30, Bitrate: -1},
		"too high":       {Width: 640, Height: 480, FPS: 30, Bitrate: MaxBitrate + 1},
		"with quantiser": {Width: 640, Height: 480, FPS: 30, Bitrate: 8_000_000, Quantiser: 20},
	} {
		if err := checkEncodeConfig(c); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, err := NewRaw().NewEncoder(context.Background(), filepath.Join(t.TempDir(), "x.raw"), ok)
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("raw backend with a bitrate: %v", err)
	}
}
