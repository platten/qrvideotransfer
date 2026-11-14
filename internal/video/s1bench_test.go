//go:build s1bench

package video

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

// TestS1CloseOnContextDone re-runs the spike S1 decode and encode figures with the runtime's
// WithCloseOnContextDone on and off (DESIGN.md section 12 asks for its cost). Clips come from
// QRVT_S1_CLIPS (see docs/spikes/S1.md section 10 for how to make them). Runs alternate on and off
// to share the machine's drift.
//
//	QRVT_S1_CLIPS=dir go test -tags s1bench -run S1 -v -timeout 2h ./internal/video/
func TestS1CloseOnContextDone(t *testing.T) {
	dir := os.Getenv("QRVT_S1_CLIPS")
	if dir == "" {
		t.Skip("QRVT_S1_CLIPS not set")
	}
	reps, _ := strconv.Atoi(os.Getenv("QRVT_S1_REPS"))
	reps = max(reps, 3)
	engines := map[bool]*engine{}
	for _, on := range []bool{false, true} {
		e, err := newEngine(t.Context(), defaultCacheDir(), on)
		if err != nil {
			t.Fatal(err)
		}
		engines[on] = e
	}
	type measure struct {
		name string
		run  func(e *engine) (float64, error)
	}
	decode := func(file, pix string, frames int) func(e *engine) (float64, error) {
		return func(e *engine) (float64, error) {
			var out countWriter
			start := time.Now()
			err := run(t.Context(), e, job{prog: "ffmpeg", input: filepath.Join(dir, file), stdout: &out, progress: progOutput,
				args: func(in, _ string) []string {
					return []string{"-v", "error", "-noautorotate", "-i", in, "-frames:v", strconv.Itoa(frames),
						"-vf", "extractplanes=y", "-pix_fmt", pix, "-f", "rawvideo", "pipe:1"}
				}})
			return float64(frames) / time.Since(start).Seconds(), err
		}
	}
	encode := func(w, h, frames int) func(e *engine) (float64, error) {
		var raw []byte
		for i := range 8 {
			f, _ := codeFrame(w, h, 4, 1, i)
			raw, _ = packFrame(raw, f, w, h, 8)
		}
		return func(e *engine) (float64, error) {
			out := t.TempDir()
			var in bytes.Buffer
			for i := range frames {
				n := len(raw) / 8
				in.Write(raw[(i%8)*n : (i%8+1)*n])
			}
			c := EncodeConfig{Width: w, Height: h, FPS: 12}
			start := time.Now()
			err := run(t.Context(), e, job{prog: "ffmpeg", outDir: out, stdin: &in, progress: progInput,
				args: func(_, o string) []string { return encodeArgs(c, o+"/x.mp4", "mp4") }})
			return float64(frames) / time.Since(start).Seconds(), err
		}
	}
	ms := []measure{
		{"decode 1080p30 H.264, luma, 300 frames", decode("c1080p30_h264.mp4", "gray", 300)},
		{"decode 4K60 HEVC 8-bit, luma, 120 frames", decode("c4k60_hevc8.mp4", "gray", 120)},
		{"decode 4K60 HEVC 10-bit HLG, luma, 120 frames", decode("c4k60_hevc10_hlg.mp4", "gray10le", 120)},
		{"encode 1080p tuned x264 qp 32, 60 frames", encode(1920, 1080, 60)},
		{"encode 4K tuned x264 qp 32, 16 frames", encode(3840, 2160, 16)},
	}
	for _, m := range ms {
		res := map[bool][]float64{}
		for range reps {
			for _, on := range []bool{false, true} {
				fps, err := m.run(engines[on])
				if err != nil {
					t.Fatalf("%s: %v", m.name, err)
				}
				res[on] = append(res[on], fps)
			}
		}
		off, on := median(res[false]), median(res[true])
		t.Logf("RESULT %s | off %.2f fps (%s) | on %.2f fps (%s) | cost %.0f%%", m.name, off, list(res[false]),
			on, list(res[true]), 100*(off-on)/off)
	}
}

func median(v []float64) float64 {
	s := slices.Clone(v)
	slices.Sort(s)
	return s[len(s)/2]
}

func list(v []float64) string {
	var b bytes.Buffer
	for i, x := range v {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%.2f", x)
	}
	return b.String()
}
