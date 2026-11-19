package pipeline

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/channel"
	"github.com/platten/qrvideotransfer/internal/payload"
	"github.com/platten/qrvideotransfer/internal/prng"
	"github.com/platten/qrvideotransfer/internal/video"
)

// writeInputs writes a folder of pseudo-random files of the given sizes (incompressible, so that
// the stream length follows the sizes) and returns it with the SHA-256 of each file by its path
// relative to the folder's parent, as a decode recreates it. Their modification times are fixed
// (fixTimes), so the transfer repeats from run to run.
func writeInputs(t testing.TB, seed uint64, sizes ...int) (string, map[string][32]byte) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "payload")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	g := prng.SplitMix64{State: seed}
	want := map[string][32]byte{}
	for i, n := range sizes {
		b := make([]byte, n)
		for j := range b {
			b[j] = byte(g.Next())
		}
		name := filepath.Join("sub", "f"+string(rune('a'+i))+".bin")
		if i == 0 {
			name = "first.bin"
		}
		if err := os.WriteFile(filepath.Join(root, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
		want[filepath.ToSlash(filepath.Join("payload", name))] = sha256.Sum256(b)
	}
	fixTimes(t, root)
	return root, want
}

// fixTimes sets the modification time of everything under dir to a fixed instant. The manifest
// records modification times, so without it the transfer's bytes, and with them a filmed or noisy
// recording's outcome, would change from run to run.
func fixTimes(t testing.TB, dir string) {
	t.Helper()
	at := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	err = fs.WalkDir(r.FS(), ".", func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return r.Chtimes(p, at, at)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// checkOutput compares the regular files under dir with want.
func checkOutput(t testing.TB, dir string, want map[string][32]byte) {
	t.Helper()
	got := filesUnder(t, dir)
	if len(got) != len(want) {
		t.Errorf("%d files written, want %d: %v", len(got), len(want), got)
	}
	for p, h := range want {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		if sha256.Sum256(b) != h {
			t.Errorf("%s: SHA-256 differs", p)
		}
	}
}

// filesUnder lists the regular files under dir, relative and slash-separated.
func filesUnder(t testing.TB, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && p == dir {
				return filepath.SkipDir
			}
			return err
		}
		if d.Type().IsRegular() {
			r, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(r))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// prngReader is a repeatable source of random bytes for an encode's session and seed.
type prngReader struct{ g prng.SplitMix64 }

func (r *prngReader) Read(b []byte) (int, error) {
	for i := range b {
		b[i] = byte(r.g.Next())
	}
	return len(b), nil
}

// encodes counts encodeRaw's calls, so that each encode of a test run has its own repeatable
// session.
var encodes uint64

// encodeRaw encodes inputs as a raw-frame video (the test backend) and returns its path.
func encodeRaw(t testing.TB, c EncodeConfig) (string, *EncodeResult) {
	t.Helper()
	if c.Output == "" {
		c.Output = filepath.Join(t.TempDir(), "code.raw")
	}
	if c.Backend == nil {
		c.Backend = video.NewRaw()
	}
	if c.TempDir == "" {
		c.TempDir = t.TempDir()
	}
	if c.Random == nil {
		encodes++
		c.Random = &prngReader{prng.SplitMix64{State: 0x5e55 + encodes}}
	}
	res, err := Encode(context.Background(), c)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return c.Output, res
}

// profile480 is a 480p Gray4 `normal` encode of inputs at 12 fps, parity 20.
func profile480(inputs string) EncodeConfig {
	return EncodeConfig{Inputs: []string{inputs}, Width: 848, Height: 480, Cell: 4, FPS: 12, Modulation: 2, Inner: 2,
		Parity: 20, Compression: payload.CompressNone}
}

// readFrames reads every frame of a video file with backend b.
func readFrames(t testing.TB, b video.Backend, path string) ([]*video.Frame, video.ClipInfo) {
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
			break
		} else if err != nil {
			t.Fatal(err)
		}
		out = append(out, f)
	}
	return out, info
}

// writeRaw writes frames to a raw-frame file at fps and returns its path.
func writeRaw(t testing.TB, frames []*video.Frame, fps int) string {
	t.Helper()
	return writeRawIn(t, t.TempDir(), frames, fps)
}

// writeRawIn writes frames to rec.raw in dir at fps and returns its path.
func writeRawIn(t testing.TB, dir string, frames []*video.Frame, fps int) string {
	t.Helper()
	path := filepath.Join(dir, "rec.raw")
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

// film records the looping code video at path (a raw-frame file with cells of cell pixels) with
// the simulated camera c and returns the recording, a raw-frame file at the camera's rate.
func film(t testing.TB, path string, cell int, c channel.Config, r channel.Run) string {
	t.Helper()
	frames, info := readFrames(t, video.NewRaw(), path)
	return filmFrames(t, frames, info.FPSNum/info.FPSDen, cell, c, r)
}

// filmFrames records the looping video frames, shown at fps, as film does.
func filmFrames(t testing.TB, frames []*video.Frame, fps, cell int, c channel.Config, r channel.Run) string {
	t.Helper()
	return filmPeriod(t, frames, time.Second/time.Duration(fps), cell, c, r)
}

// filmPeriod records the looping video frames, each shown for period, as film does.
func filmPeriod(t testing.TB, frames []*video.Frame, period time.Duration, cell int, c channel.Config, r channel.Run) string {
	t.Helper()
	for i, f := range frames {
		f.Index, f.PTS = i, time.Duration(i)*period
	}
	in := channel.Input{Frames: frames, Period: time.Duration(len(frames)) * period, Cell: cell}
	if r.Workers == 0 {
		r.Workers = runtime.GOMAXPROCS(0)
	}
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
		t.Fatal("no capture emitted")
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}

// preset returns a simulator preset, its camera resized to w x h when w is not zero.
func preset(t testing.TB, name string, w, h int) channel.Config {
	t.Helper()
	c, err := channel.Preset(name)
	if err != nil {
		t.Fatal(err)
	}
	if w != 0 {
		c = c.Resize(w, h)
	}
	return c
}

// decodeWith decodes with c and returns the decoder's final state.
func decodeWith(t testing.TB, c DecodeConfig) (*decoder, error) {
	t.Helper()
	var d *decoder
	_, err := decode(context.Background(), c, &d)
	return d, err
}

// decodeTo decodes recordings into a new folder and returns it with the result.
func decodeTo(t testing.TB, recs ...string) (string, *DecodeResult, error) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "out")
	res, err := Decode(context.Background(), DecodeConfig{Recordings: recs, Backend: video.NewRaw(), TempDir: t.TempDir(),
		Unpack: payload.UnpackOptions{OutputDir: out}})
	return out, res, err
}
