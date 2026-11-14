package video

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// tiny is the probe of a 4 x 2 test stream at 10 frames per second.
var tiny = probeInfo{ClipInfo: ClipInfo{Codec: "h264", Width: 4, Height: 2, BitDepth: 8, FPSNum: 10, FPSDen: 1}, PixFmt: "yuv420p", Size: 1000}

const tinyFrame = 4*2 + 2*2*1 // bytes of one yuv420p frame

// frameRunner writes n frames of the tiny stream (n < 0: until stopped), one every gap, each after
// its showinfo line unless log is false.
func frameRunner(n int, gap time.Duration, log bool) fakeRunner {
	return func(ctx context.Context, j *job, _ *meter) (int, error) {
		if log {
			_, _ = fmt.Fprintf(j.stderr, "[Parsed_showinfo_0 @ 0x1] config in time_base: 1/10, frame_rate: 10/1\n")
		}
		for i := 0; n < 0 || i < n; i++ {
			if log {
				_, _ = fmt.Fprintf(j.stderr, "[Parsed_showinfo_0 @ 0x1] n: %d pts: %d pts_time:0 pos: 0 fmt:yuv420p s:4x2 i:P\n", i, i)
			}
			if _, err := j.stdout.Write(make([]byte, tinyFrame)); err != nil {
				return 0, err
			}
			select {
			case <-ctx.Done():
				return 0, nil
			case <-time.After(gap):
			}
		}
		return 0, nil
	}
}

// nextWithin calls d.Next until it fails, and fails the test if that takes longer than limit.
func nextWithin(t *testing.T, d Decoder, limit time.Duration) (time.Duration, error) {
	t.Helper()
	start := time.Now()
	res := make(chan error, 1)
	go func() {
		var f Frame
		for {
			if err := d.Next(&f); err != nil {
				res <- err
				return
			}
		}
	}()
	select {
	case err := <-res:
		return time.Since(start), err
	case <-time.After(limit):
		t.Fatalf("the decoder still blocks after %v", limit)
		return 0, nil
	}
}

// shortStall shortens the watchdog's limit for one test; such tests are not parallel.
func shortStall(t *testing.T, d time.Duration) {
	t.Helper()
	stallTimeout = d
	t.Cleanup(func() { stallTimeout = EngineStallTimeout })
}

// TestDecoderFrameProgress: frames that keep coming are progress, for longer than the stall limit.
func TestDecoderFrameProgress(t *testing.T) {
	shortStall(t, 2*time.Second)
	d := startDecoder(t.Context(), frameRunner(40, 100*time.Millisecond, true), "x", tiny, DecodeConfig{}, "yuv420p")
	got := decodeAll(t, d)
	if len(got) != 40 || got[39].Index != 39 || got[39].PTS != 3900*time.Millisecond || len(got[0].Cb) != 2 {
		t.Fatalf("%d frames", len(got))
	}
}

// TestDecoderNoTimestamp: frames that come without a showinfo line stop the decode at the stall limit
// with an error naming it, although the engine is then blocked writing the next frame (SECURITY.md
// section 5: time qrvt waits on the engine is not time the engine waits on qrvt).
func TestDecoderNoTimestamp(t *testing.T) {
	shortStall(t, 2*time.Second)
	d := startDecoder(t.Context(), frameRunner(-1, 0, false), "x", tiny, DecodeConfig{}, "yuv420p")
	el, err := nextWithin(t, d, 15*time.Second)
	var le *LimitError
	if !errors.As(err, &le) || le.Limit != "EngineStallTimeout" || el > 5*time.Second {
		t.Errorf("got %v after %v", err, el)
	}
	_ = d.Close()
}

// TestDecoderTrickle: a frame trickled a byte at a time is not progress (SECURITY.md section 5:
// progress is an output frame).
func TestDecoderTrickle(t *testing.T) {
	shortStall(t, 2*time.Second)
	trickle := fakeRunner(func(ctx context.Context, j *job, _ *meter) (int, error) {
		_, _ = fmt.Fprintf(j.stderr, "[Parsed_showinfo_0 @ 0x1] n: 0 pts: 0 pts_time:0 pos: 0 fmt:yuv420p s:4x2 i:P\n")
		for {
			select {
			case <-ctx.Done():
				return 0, nil
			case <-time.After(500 * time.Millisecond):
			}
			if _, err := j.stdout.Write([]byte{0}); err != nil {
				return 0, err
			}
		}
	})
	d := startDecoder(t.Context(), trickle, "x", tiny, DecodeConfig{}, "yuv420p")
	el, err := nextWithin(t, d, 15*time.Second)
	var le *LimitError
	if !errors.As(err, &le) || le.Limit != "EngineStallTimeout" || el > 5*time.Second {
		t.Errorf("got %v after %v", err, el)
	}
	_ = d.Close()
}

// TestDecoderCancel: cancelling stops a decoder at once, both while qrvt waits for a timestamp and
// while the engine is blocked writing a frame nobody reads.
func TestDecoderCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	d := startDecoder(ctx, frameRunner(-1, 0, false), "x", tiny, DecodeConfig{}, "yuv420p")
	time.AfterFunc(300*time.Millisecond, cancel)
	el, err := nextWithin(t, d, 10*time.Second)
	if !errors.Is(err, context.Canceled) || el > 2*time.Second {
		t.Errorf("got %v after %v", err, el)
	}
	_ = d.Close()

	ctx, cancel = context.WithCancel(t.Context())
	d = startDecoder(ctx, frameRunner(-1, 0, true), "x", tiny, DecodeConfig{}, "yuv420p")
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-d.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the instance did not stop after the cancel")
	}
	_ = d.Close()
}

// TestDecoderExtraTimestamps: more timestamp lines than frames is refused, not taken as the end.
func TestDecoderExtraTimestamps(t *testing.T) {
	t.Parallel()
	extra := fakeRunner(func(ctx context.Context, j *job, _ *meter) (int, error) {
		for i := range 2 {
			_, _ = fmt.Fprintf(j.stderr, "[Parsed_showinfo_0 @ 0x1] n: %d pts: %d pts_time:0 pos: 0 fmt:yuv420p s:4x2 i:P\n", i, i)
		}
		_, err := j.stdout.Write(make([]byte, tinyFrame))
		return 0, err
	})
	d := startDecoder(t.Context(), extra, "x", tiny, DecodeConfig{}, "yuv420p")
	var f Frame
	if err := d.Next(&f); err != nil {
		t.Fatal(err)
	}
	if err := d.Next(&f); !errors.Is(err, ErrEngine) {
		t.Errorf("extra timestamp: %v", err)
	}
	_ = d.Close()
}

// TestDecoderSliceEnd: at the end of a slice, more timestamp lines waiting than the one the engine
// may have logged ahead is refused, not taken as the slice's end.
func TestDecoderSliceEnd(t *testing.T) {
	t.Parallel()
	ahead := fakeRunner(func(ctx context.Context, j *job, _ *meter) (int, error) {
		_, _ = fmt.Fprintf(j.stderr, "[Parsed_showinfo_0 @ 0x1] config in time_base: 1/10, frame_rate: 10/1\n")
		line := func(i int) {
			_, _ = fmt.Fprintf(j.stderr, "[Parsed_showinfo_0 @ 0x1] n: %d pts: %d pts_time:0 pos: 0 fmt:yuv420p s:4x2 i:P\n", i, i)
		}
		for _, lines := range [][]int{{0}, {1}, {2, 3, 4}} { // three lines before the third frame
			for _, l := range lines {
				line(l)
			}
			if _, err := j.stdout.Write(make([]byte, tinyFrame)); err != nil {
				return 0, err
			}
		}
		<-ctx.Done()
		return 0, nil
	})
	d := startDecoder(t.Context(), ahead, "x", tiny, DecodeConfig{Length: 150 * time.Millisecond}, "yuv420p")
	var f Frame
	for range 2 {
		if err := d.Next(&f); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Next(&f); !errors.Is(err, ErrEngine) {
		t.Errorf("slice end with two lines ahead: %v", err)
	}
	_ = d.Close()
	// One line ahead is normal.
	d = startDecoder(t.Context(), frameRunner(-1, 0, true), "x", tiny, DecodeConfig{Length: 150 * time.Millisecond}, "yuv420p")
	if got := decodeAll(t, d); len(got) != 2 {
		t.Errorf("%d frames in the slice", len(got))
	}
}

// TestDecoderBuffer:the decoder reads frames straight into the caller's planes; it keeps no
// frame-sized buffer of its own (100 MB at the capture limit in 10 bits).
func TestDecoderBuffer(t *testing.T) {
	t.Parallel()
	p := tiny
	p.Width, p.Height, p.BitDepth, p.PixFmt = MaxCaptureLongSide, MaxCaptureShortSide, 10, "yuv420p10le"
	d := startDecoder(t.Context(), frameRunner(0, 0, false), "x", p, DecodeConfig{}, p.PixFmt)
	if n := len(d.buf); n > 1<<20 {
		t.Errorf("a %d-byte buffer", n)
	}
	_ = d.Close()
	// A frame split across many reads arrives whole, in 8 and 10 bits.
	for _, bits := range []int{8, 10} {
		p := tiny
		p.Width, p.Height, p.BitDepth = 1024, 512, bits
		frame := make([]byte, 1024*512*3/2*(1+bits/10))
		for i := range frame {
			frame[i] = byte(i % 251)
			if bits == 10 && i%2 == 1 {
				frame[i] &= 3
			}
		}
		r := fakeRunner(func(ctx context.Context, j *job, _ *meter) (int, error) {
			_, _ = fmt.Fprintf(j.stderr, "[Parsed_showinfo_0 @ 0x1] n: 0 pts: 0 pts_time:0 pos: 0 fmt:x s:1024x512 i:P\n")
			_, err := j.stdout.Write(frame)
			return 0, err
		})
		d := startDecoder(t.Context(), r, "x", p, DecodeConfig{}, "x")
		got := decodeAll(t, d)
		var want Frame
		unpackFrame(&want, frame, 1024, 512, bits, false)
		if len(got) != 1 || !slices.Equal(got[0].Y, want.Y) || !slices.Equal(got[0].Cb, want.Cb) || !slices.Equal(got[0].Cr, want.Cr) {
			t.Errorf("%d bits: the frame differs", bits)
		}
	}
	// A truncated frame is reported as such.
	r := fakeRunner(func(ctx context.Context, j *job, _ *meter) (int, error) {
		_, err := j.stdout.Write(make([]byte, tinyFrame-1))
		return 0, err
	})
	d = startDecoder(t.Context(), r, "x", tiny, DecodeConfig{}, "yuv420p")
	var f Frame
	if err := d.Next(&f); !errors.Is(err, ErrInvalidVideo) {
		t.Errorf("truncated: %v", err)
	}
	_ = d.Close()
}

// TestProbeLimits: a probe may read at most 4 times the file's size plus MaxProbeReads, and run for
// at most EngineProbeTime, not counting time spent waiting for an instance slot.
func TestProbeLimits(t *testing.T) {
	t.Parallel()
	check := probeLimits(100, time.Second)
	var m meter
	m.reads.Store(4*100 + MaxProbeReads)
	if err := check(&m, time.Second-time.Nanosecond); err != nil {
		t.Errorf("within the limits: %v", err)
	}
	var le *LimitError
	m.reads.Add(1)
	if err := check(&m, 0); !errors.As(err, &le) || le.Limit != "MaxProbeReads" {
		t.Errorf("reads: %v", err)
	}
	m.reads.Store(0)
	if err := check(&m, time.Second); !errors.As(err, &le) || le.Limit != "EngineProbeTime" {
		t.Errorf("time: %v", err)
	}

	// Through probe: an engine that rereads its input without end is stopped by the read bound.
	path := filepath.Join(t.TempDir(), "x.mp4")
	if err := os.WriteFile(path, make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	reread := fakeRunner(func(ctx context.Context, j *job, m *meter) (int, error) {
		for ctx.Err() == nil {
			m.reads.Add(1 << 20)
			time.Sleep(10 * time.Millisecond)
		}
		return 0, nil
	})
	start := time.Now()
	if _, err := probe(t.Context(), reread, path); !errors.As(err, &le) || le.Limit != "MaxProbeReads" || time.Since(start) > 5*time.Second {
		t.Errorf("rereading probe: %v after %v", err, time.Since(start))
	}
	// One that reads slowly for ever is stopped by the time bound; time waiting is not counted.
	slow := fakeRunner(func(ctx context.Context, j *job, m *meter) (int, error) {
		m.waiting.Add(1)
		time.Sleep(2500 * time.Millisecond) // waiting for a slot
		m.waiting.Add(-1)
		for ctx.Err() == nil {
			m.reads.Add(1)
			time.Sleep(100 * time.Millisecond)
		}
		return 0, nil
	})
	start = time.Now()
	err := run(t.Context(), slow, job{check: probeLimits(100, 2*time.Second)})
	if el := time.Since(start); !errors.As(err, &le) || le.Limit != "EngineProbeTime" || el < 4*time.Second || el > 8*time.Second {
		t.Errorf("slow probe: %v after %v", err, el)
	}
}

// TestWhitelists: every job that reads a recording passes the protocol and format whitelists as
// input options, on the embedded engine and on a native ffmpeg alike.
func TestWhitelists(t *testing.T) {
	t.Parallel()
	for name, a := range map[string][]string{
		"probe":  probeArgs("IN"),
		"decode": decodeArgs("IN", tiny, DecodeConfig{Start: 5 * time.Second}, "yuv420p"),
	} {
		in := slices.Index(a, "IN")
		p := slices.Index(a, "-protocol_whitelist")
		f := slices.Index(a, "-format_whitelist")
		if in < 0 || p < 0 || f < 0 || p > in || f > in || a[p+1] != "file,pipe" || a[f+1] != formatWhitelist {
			t.Errorf("%s: %q", name, a)
		}
	}
}
