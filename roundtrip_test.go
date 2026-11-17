package qrvt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/channel"
	"github.com/platten/qrvideotransfer/internal/prng"
	"github.com/platten/qrvideotransfer/internal/video"
)

// readFrames reads every frame of a video with backend b.
func readFrames(t testing.TB, b video.Backend, path string) []*video.Frame {
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

func readRaw(t testing.TB, path string) []*video.Frame { return readFrames(t, video.NewRaw(), path) }

// writeRawFrames writes frames to a new raw-frame file at fps.
func writeRawFrames(t testing.TB, frames []*video.Frame, fps int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rec.raw")
	f0 := frames[0]
	e, err := video.NewRaw().NewEncoder(context.Background(), path, video.EncodeConfig{Width: f0.W, Height: f0.H, FPS: fps, BitDepth: int(f0.Bits)})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range frames {
		if err := e.WriteFrame(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// matrixRow is one row of the round-trip matrix of DESIGN.md section 15.
type matrixRow struct {
	preset     string
	w, h, fps  int
	mod        Modulation
	protection Protection
	always     bool // runs without QRVT_LONG_TESTS: one row per preset
}

var modName = map[Modulation]string{Gray2: "bw", Gray4: "gray4", RGB8: "color"}
var protName = map[Protection]string{Fast: "fast", Normal: "normal", Robust: "robust"}

func (r matrixRow) name() string {
	return fmt.Sprintf("%s-%dp%d-%s-%s", r.preset, r.h, r.fps, modName[r.mod], protName[r.protection])
}

// matrix lists every row of the round-trip matrix.
func matrix() []matrixRow {
	var rows []matrixRow
	for _, m := range []Modulation{Gray2, Gray4, RGB8} {
		for _, p := range []Protection{Fast, Normal, Robust} {
			rows = append(rows, matrixRow{"ideal", 848, 480, 12, m, p, m == Gray4 && p == Normal})
		}
	}
	rows = append(rows,
		matrixRow{"tripod", 1920, 1080, 30, Gray4, Fast, false},
		matrixRow{"tripod", 1920, 1080, 30, Gray4, Normal, true},
		matrixRow{"tripod", 1920, 1080, 30, RGB8, Normal, false})
	for _, m := range []Modulation{Gray2, Gray4} {
		for _, p := range []Protection{Normal, Robust} {
			rows = append(rows, matrixRow{"handheld", 1920, 1080, 30, m, p, m == Gray2 && p == Normal})
		}
	}
	for _, m := range []Modulation{Gray2, Gray4} {
		for _, p := range []Protection{Normal, Robust} {
			rows = append(rows, matrixRow{"budget-android", 1280, 720, 12, m, p, m == Gray4 && p == Normal})
		}
	}
	return append(rows,
		matrixRow{"iphone-hdr", 1920, 1080, 30, Gray4, Normal, true},
		matrixRow{"harsh", 1280, 720, 12, Gray2, Robust, true})
}

// prngReader is a repeatable source of random bytes for an encode's session and seed.
type prngReader struct{ g prng.SplitMix64 }

func (r *prngReader) Read(b []byte) (int, error) {
	for i := range b {
		b[i] = byte(r.g.Next())
	}
	return len(b), nil
}

// matrixSessions is the number of sessions each row is run with in the long version; the short
// version runs the always rows with the first.
const matrixSessions = 5

// TestRoundTripMatrix is the SHA-256 round trip of DESIGN.md section 15 through the public API:
// the files are encoded to an MP4 by the embedded engine, the video is decoded and filmed by the
// simulated camera of the row's preset for two passes of the loop (three on `harsh`), with the
// pure-Go recorder stand-in (not x264 at the preset's bitrate), and the recording is decoded.
// Each run uses a fixed session id and outer order seed, so that it can be repeated; the long
// version (QRVT_LONG_TESTS=1) runs every row with matrixSessions sessions, the short version the
// rows marked always with one.
func TestRoundTripMatrix(t *testing.T) {
	if raceEnabled {
		t.Skip("the matrix is too slow under the race detector; the pipeline's tests cover its concurrency")
	}
	long := os.Getenv("QRVT_LONG_TESTS") == "1"
	defer func() { testRandom = nil }()
	for _, r := range matrix() {
		t.Run(r.name(), func(t *testing.T) {
			if !r.always && !long {
				t.Skip("long test: QRVT_LONG_TESTS=1")
			}
			sessions, failed := 1, 0
			if long {
				sessions = matrixSessions
			}
			for s := range sessions {
				testRandom = &prngReader{prng.SplitMix64{State: uint64(1000 + s)}}
				if err := roundTrip(t, r); err != nil {
					failed++
					t.Errorf("session %d: %v", s, err)
				}
			}
			t.Logf("%d of %d sessions decoded", sessions-failed, sessions)
		})
	}
}

// roundTrip runs one row once and returns why the decode failed, if it did.
func roundTrip(t *testing.T, r matrixRow) error {
	t.Helper()
	in, want := payloadDir(t, 20, 6000, 500)
	video1 := filepath.Join(t.TempDir(), "code.mp4")
	p := Profile{Width: r.w, Height: r.h, Cell: 4, FPS: r.fps, Modulation: r.mod, Protection: r.protection, ParityPercent: 20}
	t0 := time.Now()
	rep, err := Encode(context.Background(), EncodeOptions{Inputs: []string{in}, Output: video1, Profile: p, TempDir: t.TempDir()})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	tEnc := time.Since(t0)
	be, err := video.NewEmbedded(video.Options{})
	if err != nil {
		t.Fatal(err)
	}
	frames := readFrames(t, be, video1)
	if len(frames) != rep.Frames {
		t.Fatalf("%d frames in the video, report says %d", len(frames), rep.Frames)
	}
	c, err := channel.Preset(r.preset)
	if err != nil {
		t.Fatal(err)
	}
	passes := 2.0
	if r.preset == "harsh" {
		passes = 3
	}
	period := time.Duration(rep.Frames) * time.Second / time.Duration(r.fps)
	captures := int(passes*period.Seconds()*float64(c.Timing.FPS)) + 2
	t0 = time.Now()
	rec := film(t, frames, r.fps, c, channel.Run{Captures: captures, Seed: 0x5eed})
	tFilm := time.Since(t0)
	t0 = time.Now()
	out, dr, err := decodeTest(t, DecodeOptions{Recordings: []string{rec}})
	tDec := time.Since(t0)
	if err != nil {
		return fmt.Errorf("decode (%d frames, %d captures): %w", rep.Frames, captures, err)
	}
	sameFiles(t, out, want)
	t.Logf("F %d, %d captures, clean %.3f; encode %.1f s, film %.1f s, decode %.1f s", rep.Frames, dr.Captures, dr.CleanFraction,
		tEnc.Seconds(), tFilm.Seconds(), tDec.Seconds())
	return nil
}

// film records the looping frames, shown at fps, with the simulated camera c into a raw-frame file.
func film(t testing.TB, frames []*video.Frame, fps int, c channel.Config, r channel.Run) string {
	t.Helper()
	period := time.Second / time.Duration(fps)
	for i, f := range frames {
		f.Index, f.PTS = i, time.Duration(i)*period
	}
	in := channel.Input{Frames: frames, Period: time.Duration(len(frames)) * period, Cell: 4}
	r.Workers = runtime.GOMAXPROCS(0)
	out := filepath.Join(t.TempDir(), "rec.raw")
	var e video.Encoder
	err := channel.Simulate(context.Background(), in, c, r, func(f *video.Frame, _ *channel.Truth) error {
		if e == nil {
			var err error
			if e, err = video.NewRaw().NewEncoder(context.Background(), out, video.EncodeConfig{Width: f.W, Height: f.H,
				FPS: c.Timing.FPS, BitDepth: int(f.Bits)}); err != nil {
				return err
			}
		}
		return e.WriteFrame(f)
	})
	if err != nil {
		t.Fatal(err)
	}
	if e == nil {
		t.Fatal("no capture")
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}
