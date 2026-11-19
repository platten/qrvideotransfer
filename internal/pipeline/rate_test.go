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

// The frame-rate and glare rows of the assertion table of DESIGN.md section 15, decoded end to end
// on a 480p transfer (212 x 120 grid, 4-pixel cells) shown at 30 fps and filmed with a 1600 x 900
// camera unless stated: 6.8 camera pixels per cell on `tripod`, 6.0 on `handheld`.

var (
	rateModName  = map[uint8]string{1: "bw", 2: "gray4", 3: "color"}
	rateProtName = map[uint8]string{2: "normal", 4: "robust"}
)

// rateBytes is the transfer's size: 30,000 bytes in the long versions (four greys `normal`: F = 12),
// 6,000 in the short ones, which film fewer captures (F = 4).
func rateBytes() int {
	if rateLong() {
		return 30000
	}
	return 6000
}

// rateCode encodes rateBytes() at 848 x 480, 30 fps, parity 20, in modulation mod with inner code
// inner and a session fixed by both, and returns the video's frames, the result and the files.
func rateCode(t *testing.T, mod, inner uint8) ([]*video.Frame, *EncodeResult, map[string][32]byte) {
	t.Helper()
	in, want := writeInputs(t, 40+uint64(mod), rateBytes())
	cfg := profile480(in)
	cfg.FPS, cfg.Modulation, cfg.Inner = 30, mod, inner
	cfg.Random = &prngReader{prng.SplitMix64{State: 0x7a7e + 16*uint64(mod) + uint64(inner)}}
	path, res := encodeRaw(t, cfg)
	frames, _ := readFrames(t, video.NewRaw(), path)
	return frames, res, want
}

// rateLong reports whether the long versions run.
func rateLong() bool { return os.Getenv("QRVT_LONG_TESTS") == "1" }

// TestCameraTwiceDisplay: with the camera at twice the display rate (60 against 30 fps), one pass
// of the loop (2F captures) decodes whatever the phase offset (DESIGN.md section 15). The long
// version (QRVT_LONG_TESTS=1) runs black and white, four greys and colour, `normal` and `robust`,
// on `tripod` (4 ms exposure, 8 ms readout) and `handheld` (8 and 8), each at 8 offsets spread
// over one display period: 96 cases, all of which decoded when measured (2026-11-19), 30,000
// bytes each. The short version runs four greys `normal` on `tripod` at offset 1/8, where the clean
// fraction is lowest (0.67), with 6,000 bytes. Statistical: skipped under the race detector.
func TestCameraTwiceDisplay(t *testing.T) {
	raceSkipStatistics(t)
	type combo struct{ mod, inner uint8 }
	combos, offsets, presets := []combo{{2, 2}}, []int{1}, []string{"tripod"}
	if rateLong() {
		combos, offsets, presets = nil, []int{0, 1, 2, 3, 4, 5, 6, 7}, []string{"tripod", "handheld"}
		for _, mod := range []uint8{1, 2, 3} {
			for _, inner := range []uint8{2, 4} {
				combos = append(combos, combo{mod, inner})
			}
		}
	}
	for _, cb := range combos {
		frames, res, want := rateCode(t, cb.mod, cb.inner)
		for _, name := range presets {
			for _, k := range offsets {
				t.Run(fmt.Sprintf("%s-%s/%s/offset%d", rateModName[cb.mod], rateProtName[cb.inner], name, k), func(t *testing.T) {
					c := preset(t, name, 1600, 900)
					if c.Timing.FPS != 60 {
						t.Fatalf("camera at %d fps", c.Timing.FPS)
					}
					start := time.Duration(k) * time.Second / 30 / 8
					rec := filmFrames(t, frames, 30, 4, c, channel.Run{Start: start, Captures: 2 * res.Frames, Seed: 0x5eed + uint64(k)})
					out, dr, err := decodeTo(t, rec)
					if err != nil {
						t.Fatalf("one pass, offset %d/8: %v", k, err)
					}
					checkOutput(t, out, want)
					t.Logf("F %d, clean %.3f, raw bit errors %.2e", res.Frames, dr.Stats.CleanFraction, dr.Stats.RawBitErrors)
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
// A recording that does not decode must end in the typed not-enough-data error (ShortError, which
// the root package returns as NotEnoughDataError and the command line explains with its guidance
// sentence) and write nothing; one that decodes must give the files. The short version films four
// greys `normal` at 4 ms exposure and offset 1/8 for one pass, which never decodes (no codeword in 4
// passes when measured). The long version (QRVT_LONG_TESTS=1) films every modulation and
// protection at 4 and 8 ms exposure and 8 offsets for 4 passes and requires one of the two outcomes
// in every case. Statistical: skipped under the race detector.
func TestLockedRateSafety(t *testing.T) {
	raceSkipStatistics(t)
	type combo struct{ mod, inner uint8 }
	combos, offsets, exposures, passes := []combo{{2, 2}}, []int{1}, []time.Duration{4 * time.Millisecond}, 1
	if rateLong() {
		combos, offsets, exposures, passes = nil, []int{0, 1, 2, 3, 4, 5, 6, 7}, []time.Duration{4 * time.Millisecond, 8 * time.Millisecond}, 4
		for _, mod := range []uint8{1, 2, 3} {
			for _, inner := range []uint8{2, 4} {
				combos = append(combos, combo{mod, inner})
			}
		}
	}
	for _, cb := range combos {
		frames, res, want := rateCode(t, cb.mod, cb.inner)
		for _, exp := range exposures {
			for _, k := range offsets {
				t.Run(fmt.Sprintf("%s-%s/%v/offset%d", rateModName[cb.mod], rateProtName[cb.inner], exp, k), func(t *testing.T) {
					start := time.Duration(k) * time.Second / 30 / 8
					rec := filmFrames(t, frames, 30, 4, lockedCamera(t, exp), channel.Run{Start: start, Captures: passes * res.Frames, Seed: 0x5eed + uint64(k)})
					out, dr, err := decodeTo(t, rec)
					var short *ShortError
					switch {
					case err == nil:
						checkOutput(t, out, want)
						if !rateLong() {
							t.Error("decoded where it was measured never to decode")
						}
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

// TestGlareDecode: decoding succeeds with a specular glare patch on the code (DESIGN.md section 15):
// `robust` with a patch over 20% of the code, `normal` with 5% at settings with a margin over
// section 11's condition 2. Four greys `normal` on `handheld` fails with a 5% patch at 6.0 camera
// pixels per cell, condition 2's minimum (0 of 4 patch positions, short at 0.66 to 0.82 of the
// shards needed when measured, 2026-11-19), and decodes at 6.4, so it is asserted at 6.4
// (camera 1696 x 954). One pass at a 60 fps camera (2F + 2 captures). The short version runs one
// patch position each of four greys `normal` at 6.4 on `handheld` and `robust` at 20% on
// `handheld`; the long version (QRVT_LONG_TESTS=1) four positions each, and adds black and white
// and colour `robust` at 20%, and `normal` at 5% for every modulation on `tripod` and for black and
// white and colour on `handheld` at 1600 x 900. Statistical: skipped under the race detector.
func TestGlareDecode(t *testing.T) {
	raceSkipStatistics(t)
	type glareCase struct {
		preset      string
		w, h        int
		mod, inner  uint8
		glare       float64
		alwaysFirst bool // its first position runs in the short version
	}
	cases := []glareCase{
		{"handheld", 1696, 954, 2, 2, 0.05, true},
		{"handheld", 1600, 900, 2, 4, 0.20, true},
		{"tripod", 1600, 900, 2, 4, 0.20, false},
	}
	for _, mod := range []uint8{1, 3} {
		for _, p := range []string{"tripod", "handheld"} {
			cases = append(cases, glareCase{p, 1600, 900, mod, 4, 0.20, false}, glareCase{p, 1600, 900, mod, 2, 0.05, false})
		}
	}
	cases = append(cases, glareCase{"tripod", 1600, 900, 2, 2, 0.05, false})
	positions := 4
	if !rateLong() {
		positions = 1
	}
	type transfer struct {
		frames []*video.Frame
		res    *EncodeResult
		want   map[string][32]byte
	}
	codes := map[[2]uint8]transfer{}
	for _, gc := range cases {
		if !gc.alwaysFirst && !rateLong() {
			continue
		}
		key := [2]uint8{gc.mod, gc.inner}
		if _, ok := codes[key]; !ok {
			f, r, w := rateCode(t, gc.mod, gc.inner)
			codes[key] = transfer{f, r, w}
		}
		code := codes[key]
		for pos := range positions {
			t.Run(fmt.Sprintf("%s-%s/%s-%dx%d/glare%.0f%%/position%d", rateModName[gc.mod], rateProtName[gc.inner], gc.preset, gc.w, gc.h, 100*gc.glare, pos), func(t *testing.T) {
				c := preset(t, gc.preset, gc.w, gc.h)
				c.Optics.Glare = gc.glare
				rec := filmFrames(t, code.frames, 30, 4, c, channel.Run{Captures: 2*code.res.Frames + 2, Seed: 0x6a0 + uint64(pos)})
				out, dr, err := decodeTo(t, rec)
				if err != nil {
					t.Fatalf("%.0f%% glare: %v", 100*gc.glare, err)
				}
				checkOutput(t, out, code.want)
				t.Logf("F %d, clean %.3f, raw bit errors %.2e", code.res.Frames, dr.Stats.CleanFraction, dr.Stats.RawBitErrors)
			})
		}
	}
}

// driftPeriod is the display frame period that gives a camera at 30 fps the drift of a 29.97 fps
// camera against a 30 fps display: the display runs 30/29.97 times faster.
const driftPeriod = time.Duration(float64(time.Second) * 29.97 / 900)

// TestDriftBestEffort: a camera at the display's rate that drifts against it, 29.97 against 30
// fps, decodes `robust` at every phase offset within 8 passes, as the drift carries the mixed band
// across the code (DESIGN.md section 15; simulated, best effort, not a recommended setting). The
// long version (QRVT_LONG_TESTS=1) films black and white, four greys and colour at 4 and 8 ms
// exposure (8 ms readout, `tripod` otherwise) and 8 offsets with 30,000 bytes for 8 passes: when
// measured (2026-11-19) the slowest needed 7. The short version films four greys at 4 ms and offset
// 1/8, where the locked rate never decodes, with 6,000 bytes. Statistical: skipped under the race
// detector.
func TestDriftBestEffort(t *testing.T) {
	raceSkipStatistics(t)
	mods, offsets, exposures := []uint8{2}, []int{1}, []time.Duration{4 * time.Millisecond}
	if rateLong() {
		mods, offsets, exposures = []uint8{1, 2, 3}, []int{0, 1, 2, 3, 4, 5, 6, 7}, []time.Duration{4 * time.Millisecond, 8 * time.Millisecond}
	}
	const passes = 8
	for _, mod := range mods {
		frames, res, want := rateCode(t, mod, 4)
		for _, exp := range exposures {
			for _, k := range offsets {
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
						rec := filmPeriod(t, frames, driftPeriod, 4, lockedCamera(t, exp), channel.Run{Start: start, Captures: p * res.Frames, Seed: 0x5eed + uint64(k)})
						var short *ShortError
						if out, dr, err = decodeTo(t, rec); !errors.As(err, &short) {
							break
						}
					}
					if err != nil {
						t.Fatalf("%d passes drifting at 29.97 against 30 fps: %v", passes, err)
					}
					checkOutput(t, out, want)
					t.Logf("F %d, decoded after %d of at most %d captures, clean %.3f", res.Frames, dr.Stats.Captures, passes*res.Frames, dr.Stats.CleanFraction)
				})
			}
		}
	}
}
