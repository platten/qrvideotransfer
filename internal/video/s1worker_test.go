//go:build s1bench

package video

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestS1Workers compares the engine in worker processes with the engine in process, both with
// close-on-cancel off, on the clips of TestS1CloseOnContextDone; it also measures worker start-up
// and, on Linux, each worker's peak resident memory.
//
//	QRVT_S1_CLIPS=dir go test -tags s1bench -run S1Workers -v -timeout 2h ./internal/video/
func TestS1Workers(t *testing.T) {
	dir := os.Getenv("QRVT_S1_CLIPS")
	if dir == "" {
		t.Skip("QRVT_S1_CLIPS not set")
	}
	inproc, err := loadEngine(t.Context(), defaultCacheDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := testWorkers(t)
	var peak atomic.Int64
	p.started = func(pid int) { go watchRSS(pid, &peak) }
	workers := workerRunner{p}
	// Start-up: an empty job, from exec to the worker's exit message, module from the cache.
	var ups []float64
	for range 5 {
		start := time.Now()
		_ = run(t.Context(), workers, job{prog: "ffprobe", args: func(string, string) []string { return []string{"-version"} }})
		ups = append(ups, time.Since(start).Seconds())
	}
	t.Logf("RESULT worker start-up and ffprobe -version | %s s", list(ups))
	decode := func(r runner, file, pix string, frames int) (float64, error) {
		var out countWriter
		start := time.Now()
		err := run(t.Context(), r, job{prog: "ffmpeg", input: filepath.Join(dir, file), stdout: &out, progress: progOutput,
			args: func(in, _ string) []string {
				return []string{"-v", "error", "-noautorotate", "-i", in, "-frames:v", strconv.Itoa(frames),
					"-vf", "extractplanes=y", "-pix_fmt", pix, "-f", "rawvideo", "pipe:1"}
			}})
		return float64(frames) / time.Since(start).Seconds(), err
	}
	for _, c := range []struct {
		file, pix string
		n         int
	}{{"c1080p30_h264.mp4", "gray", 300}, {"c4k60_hevc8.mp4", "gray", 120}, {"c4k60_hevc10_hlg.mp4", "gray10le", 120}} {
		var in, wk []float64
		peak.Store(0)
		for range 3 {
			a, err := decode(inproc, c.file, c.pix, c.n)
			if err != nil {
				t.Fatal(err)
			}
			b, err := decode(workers, c.file, c.pix, c.n)
			if err != nil {
				t.Fatal(err)
			}
			in, wk = append(in, a), append(wk, b)
		}
		t.Logf("RESULT %s | in process %.2f fps (%s) | worker %.2f fps (%s) | cost %.0f%% | worker peak RSS %d MB",
			c.file, median(in), list(in), median(wk), list(wk), 100*(median(in)-median(wk))/median(in), peak.Load()>>20)
	}
	// Encoding 1080p through a worker against in process.
	var in, wk []float64
	for range 3 {
		for k, r := range []runner{inproc, workers} {
			fps, err := encodeBench(t, r)
			if err != nil {
				t.Fatal(err)
			}
			if k == 0 {
				in = append(in, fps)
			} else {
				wk = append(wk, fps)
			}
		}
	}
	t.Logf("RESULT encode 1080p | in process %.2f fps (%s) | worker %.2f fps (%s) | cost %.0f%%",
		median(in), list(in), median(wk), list(wk), 100*(median(in)-median(wk))/median(in))
}

func encodeBench(t *testing.T, r runner) (float64, error) {
	var raw []byte
	for i := range 8 {
		f, _ := codeFrame(1920, 1080, 4, 1, i)
		raw, _ = packFrame(raw, f, 1920, 1080, 8)
	}
	pr, pw := io.Pipe()
	go func() {
		n := len(raw) / 8
		for i := range 60 {
			_, _ = pw.Write(raw[(i%8)*n : (i%8+1)*n])
		}
		_ = pw.Close()
	}()
	c := EncodeConfig{Width: 1920, Height: 1080, FPS: 12}
	out := t.TempDir()
	start := time.Now()
	err := run(t.Context(), r, job{prog: "ffmpeg", outDir: out, stdin: pr, progress: progInput,
		args: func(_, o string) []string { return encodeArgs(c, o+"/x.mp4", "mp4") }})
	return 60 / time.Since(start).Seconds(), err
}

// watchRSS records a process's largest resident set (Linux).
func watchRSS(pid int, peak *atomic.Int64) {
	for {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
		if err != nil {
			return
		}
		for _, l := range strings.Split(string(b), "\n") {
			if f := strings.Fields(l); len(f) >= 2 && f[0] == "VmHWM:" {
				v, _ := strconv.ParseInt(f[1], 10, 64)
				if v*1024 > peak.Load() {
					peak.Store(v * 1024)
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}
