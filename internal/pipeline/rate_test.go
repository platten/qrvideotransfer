package pipeline

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/channel"
	"github.com/platten/qrvideotransfer/internal/prng"
	"github.com/platten/qrvideotransfer/internal/video"
)

// The frame-rate and glare rows of the assertion table of DESIGN.md section 15, decoded end to end.
// The long versions (QRVT_LONG_TESTS=1) film a 480p transfer of 30,000 bytes (212 x 120 grid,
// 4-pixel cells) shown at 30 fps with a 1600 x 900 camera unless stated: 6.8 camera pixels per cell
// on `tripod`, 6.0 on `handheld`. The always-on cases are the smallest that still show their
// property, 500 bytes (F = 4); the row's long-only parts have no always-on case (designer's ruling
// on the CI budget, 2026-11-19).

var (
	rateModName  = map[uint8]string{1: "bw", 2: "gray4", 3: "color"}
	rateProtName = map[uint8]string{2: "normal", 4: "robust"}
)

// rateLong reports whether the long versions run.
func rateLong() bool { return os.Getenv("QRVT_LONG_TESTS") == "1" }

// rateTransfer is an encoded transfer: the video's frames, the result and the files.
type rateTransfer struct {
	frames []*video.Frame
	res    *EncodeResult
	want   map[string][32]byte
}

// rateCode encodes size bytes at width x 480, 30 fps, parity 20, in modulation mod with inner code
// inner and a session fixed by both.
func rateCode(t *testing.T, mod, inner uint8, width, size int) rateTransfer {
	t.Helper()
	in, want := writeInputs(t, 40+uint64(mod), size)
	cfg := profile480(in)
	cfg.Width, cfg.FPS, cfg.Modulation, cfg.Inner = width, 30, mod, inner
	cfg.Random = &prngReader{prng.SplitMix64{State: 0x7a7e + 16*uint64(mod) + uint64(inner)}}
	path, res := encodeRaw(t, cfg)
	frames, _ := readFrames(t, video.NewRaw(), path)
	return rateTransfer{frames, res, want}
}

// decodeAll decodes rec and requires the transfer's files.
func (tr rateTransfer) decodeAll(t *testing.T, rec, what string) {
	t.Helper()
	out, dr, err := decodeTo(t, rec)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	checkOutput(t, out, tr.want)
	t.Logf("F %d, %d captures read, clean %.3f, raw bit errors %.2e", tr.res.Frames, dr.Stats.Captures, dr.Stats.CleanFraction, dr.Stats.RawBitErrors)
}

// TestCameraTwiceDisplay: with the camera at twice the display rate (60 against 30 fps), one pass
// of the loop (2F captures) decodes whatever the phase offset (DESIGN.md section 15). The long
// version runs black and white, four greys and colour, `normal` and `robust`, on `tripod` (4 ms
// exposure, 8 ms readout) and `handheld` (8 and 8), each at 8 offsets spread over one display
// period: 96 cases, all of which decoded when measured (2026-11-19). Always on: four greys
// `normal` on `tripod` at offset 1/8, on the smallest setting, a 480 x 480 code (120 x 120 grid)
// filmed at 908 x 908 (6.8 camera pixels per cell), where every offset decoded when measured.
// Statistical: skipped under the race detector.
func TestCameraTwiceDisplay(t *testing.T) {
	raceSkipStatistics(t)
	type combo struct{ mod, inner uint8 }
	combos, offsets, presets := []combo{{2, 2}}, []int{1}, []string{"tripod"}
	width, size, cw, ch := 480, 500, 908, 908
	if rateLong() {
		combos, offsets, presets = nil, []int{0, 1, 2, 3, 4, 5, 6, 7}, []string{"tripod", "handheld"}
		width, size, cw, ch = 848, 30000, 1600, 900
		for _, mod := range []uint8{1, 2, 3} {
			for _, inner := range []uint8{2, 4} {
				combos = append(combos, combo{mod, inner})
			}
		}
	}
	for _, cb := range combos {
		tr := rateCode(t, cb.mod, cb.inner, width, size)
		for _, name := range presets {
			for _, k := range offsets {
				t.Run(fmt.Sprintf("%s-%s/%s/offset%d", rateModName[cb.mod], rateProtName[cb.inner], name, k), func(t *testing.T) {
					c := preset(t, name, cw, ch)
					if c.Timing.FPS != 60 {
						t.Fatalf("camera at %d fps", c.Timing.FPS)
					}
					start := time.Duration(k) * time.Second / 30 / 8
					rec := filmFrames(t, tr.frames, 30, 4, c, channel.Run{Start: start, Captures: 2 * tr.res.Frames, Seed: 0x5eed + uint64(k)})
					tr.decodeAll(t, rec, fmt.Sprintf("one pass, offset %d/8", k))
				})
			}
		}
	}
}

// lockedCamera is `tripod` at 1600 x 900 with the camera locked to the display's 30 fps, the given
// exposure and an 8 ms readout.
func lockedCamera(t *testing.T, exposure time.Duration) channel.Config {
	c := preset(t, "tripod", 1600, 900)
	c.Timing.FPS, c.Timing.Exposure, c.Timing.Readout = 30, exposure, 8*time.Millisecond
	return c
}

// TestLockedRateSafety: with the camera locked to the display rate (30 against 30 fps), some phase
// offsets never decode, because every pass repeats the same frame mixing (DESIGN.md section 15).
// Every modulation and protection at 4 and 8 ms exposure and 8 offsets, filmed for 4 passes, must
// either give the files or end in a ShortError with nothing written: never a wrong result. Long
// only; the always-on safety case goes through the public API (the root package's
// TestLockedRateNotEnoughData). Statistical: skipped under the race detector.
func TestLockedRateSafety(t *testing.T) {
	raceSkipStatistics(t)
	if !rateLong() {
		t.Skip("long test: QRVT_LONG_TESTS=1")
	}
	for _, mod := range []uint8{1, 2, 3} {
		for _, inner := range []uint8{2, 4} {
			tr := rateCode(t, mod, inner, 848, 30000)
			for _, exp := range []time.Duration{4 * time.Millisecond, 8 * time.Millisecond} {
				for k := range 8 {
					t.Run(fmt.Sprintf("%s-%s/%v/offset%d", rateModName[mod], rateProtName[inner], exp, k), func(t *testing.T) {
						start := time.Duration(k) * time.Second / 30 / 8
						rec := filmFrames(t, tr.frames, 30, 4, lockedCamera(t, exp), channel.Run{Start: start, Captures: 4 * tr.res.Frames, Seed: 0x5eed + uint64(k)})
						out, dr, err := decodeTo(t, rec)
						var short *ShortError
						switch {
						case err == nil:
							checkOutput(t, out, tr.want)
						case errors.As(err, &short):
							if fs := filesUnder(t, out); len(fs) != 0 || dr != nil {
								t.Errorf("not enough data, yet files written %v or a result %+v", fs, dr)
							}
							t.Logf("not enough data: %v", err)
						default:
							t.Fatalf("got %v, want the files or a ShortError", err)
						}
					})
				}
			}
		}
	}
}

// TestGlareDecode: decoding succeeds with a specular glare patch on the code (DESIGN.md section 15):
// `normal` with a patch over 5% of the code at settings with a margin over section 11's condition
// 2, and `robust` with 20%. Four greys `normal` on `handheld` fails with a 5% patch at 6.0 camera
// pixels per cell, condition 2's minimum (0 of 4 patch positions, short at 0.66 to 0.82 of the
// shards needed when measured, 2026-11-19), and decodes at 6.4 (camera 1696 x 954), where it is
// asserted. These margins were measured on the 212 x 120 grid and do not carry over to the
// smallest grids (section 15). One pass at a 60 fps camera (2F + 2 captures). Always on: four
// greys `normal` at 6.4 on `handheld`, one patch position, 500 bytes. The long version runs four
// positions each of that case, `robust` at 20% for every modulation on `tripod` and `handheld`, and
// `normal` at 5% for every modulation on `tripod` and black and white and colour on `handheld` at
// 1600 x 900. Statistical: skipped under the race detector.
func TestGlareDecode(t *testing.T) {
	raceSkipStatistics(t)
	type glareCase struct {
		preset     string
		w, h       int
		mod, inner uint8
		glare      float64
	}
	cases, positions, size := []glareCase{{"handheld", 1696, 954, 2, 2, 0.05}}, 1, 500
	if rateLong() {
		positions, size = 4, 30000
		cases = append(cases, glareCase{"tripod", 1600, 900, 2, 2, 0.05})
		for _, mod := range []uint8{1, 2, 3} {
			for _, p := range []string{"tripod", "handheld"} {
				cases = append(cases, glareCase{p, 1600, 900, mod, 4, 0.20})
				if mod != 2 {
					cases = append(cases, glareCase{p, 1600, 900, mod, 2, 0.05})
				}
			}
		}
	}
	codes := map[[2]uint8]rateTransfer{}
	for _, gc := range cases {
		key := [2]uint8{gc.mod, gc.inner}
		if _, ok := codes[key]; !ok {
			codes[key] = rateCode(t, gc.mod, gc.inner, 848, size)
		}
		tr := codes[key]
		for pos := range positions {
			t.Run(fmt.Sprintf("%s-%s/%s-%dx%d/glare%.0f%%/position%d", rateModName[gc.mod], rateProtName[gc.inner], gc.preset, gc.w, gc.h, 100*gc.glare, pos), func(t *testing.T) {
				c := preset(t, gc.preset, gc.w, gc.h)
				c.Optics.Glare = gc.glare
				rec := filmFrames(t, tr.frames, 30, 4, c, channel.Run{Captures: 2*tr.res.Frames + 2, Seed: 0x6a0 + uint64(pos)})
				tr.decodeAll(t, rec, fmt.Sprintf("%.0f%% glare", 100*gc.glare))
			})
		}
	}
}

// driftPeriod is the display frame period that gives a camera at 30 fps the drift of a 29.97 fps
// camera against a 30 fps display: the display runs 30/29.97 times faster.
const driftPeriod = time.Duration(float64(time.Second) * 29.97 / 900)

// TestDriftBestEffort: a camera at the display's rate that drifts against it, 29.97 against 30
// fps, decodes `robust` at every phase offset within 8 passes, as the drift carries the mixed band
// across the code (DESIGN.md section 15; simulated, best effort, not a recommended setting). Black
// and white, four greys and colour at 4 and 8 ms exposure (8 ms readout, `tripod` otherwise) and 8
// offsets: when measured (2026-11-19) the slowest needed 7 passes. Long only: where the locked
// rate fails, the drift needs 30 to 40 captures to move the mixed band, too many for an always-on
// case. Statistical: skipped under the race detector.
func TestDriftBestEffort(t *testing.T) {
	raceSkipStatistics(t)
	if !rateLong() {
		t.Skip("long test: QRVT_LONG_TESTS=1")
	}
	const passes = 8
	for _, mod := range []uint8{1, 2, 3} {
		tr := rateCode(t, mod, 4, 848, 30000)
		for _, exp := range []time.Duration{4 * time.Millisecond, 8 * time.Millisecond} {
			for k := range 8 {
				t.Run(fmt.Sprintf("%s-robust/%v/offset%d", rateModName[mod], exp, k), func(t *testing.T) {
					start := time.Duration(k) * driftPeriod / 8
					// Five passes first, then all eight only if those fall short: the captures are
					// prefix-stable (the root package's TestFilmPrefix) and decoding stops once
					// complete, so the outcome is the eight passes' at less filming.
					var (
						out string
						dr  *DecodeResult
						err error
					)
					for _, p := range []int{5, passes} {
						rec := filmPeriod(t, tr.frames, driftPeriod, 4, lockedCamera(t, exp), channel.Run{Start: start, Captures: p * tr.res.Frames, Seed: 0x5eed + uint64(k)})
						var short *ShortError
						if out, dr, err = decodeTo(t, rec); !errors.As(err, &short) {
							break
						}
					}
					if err != nil {
						t.Fatalf("%d passes drifting at 29.97 against 30 fps: %v", passes, err)
					}
					checkOutput(t, out, tr.want)
					t.Logf("F %d, decoded after %d of at most %d captures", tr.res.Frames, dr.Stats.Captures, passes*tr.res.Frames)
				})
			}
		}
	}
}
