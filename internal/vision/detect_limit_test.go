package vision

import (
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/platten/qrvideotransfer/internal/channel"
)

// geomDetectRate films n poses of the code with cfg and counts the quads located (exact or
// imprecise, as geomJudge judges them, the header's frame and the mesh's centre error checked only
// when it matters), of those the imprecise ones, the wrong ones, and the smallest true finder
// module seen, in camera pixels.
func geomDetectRate(t *testing.T, c *geomCode, cfg channel.Config, n int, seed uint64) (found, imprecise, wrong int, minMod float64) {
	t.Helper()
	var mu sync.Mutex
	var filmErr error
	minMod = math.Inf(1)
	geomParallel(n, func(i int) {
		p, err := geomPose(c, cfg, n, i, seed)
		if err != nil {
			mu.Lock()
			filmErr = err
			mu.Unlock()
			return
		}
		defer geomVisionLock()()
		var d FinderDetector
		q, ok := d.Detect(p.f, nil)
		_, mod := geomFinderTruth(c, p.tr)
		good, inexact := false, false
		if ok {
			// Judge first as if the capture were usable; check that only for an imprecise quad.
			v := geomJudge(c, p.tr, q, d.hidden, true, true, 0)
			if v == geomImprecise {
				hs, hok := SlotReader{}.Read(p.f, q)
				m, _, err := MeshFitter{}.Fit(p.f, q, c.layout)
				rms := math.Inf(1)
				if err == nil {
					rms, _ = channel.CentreError(p.tr, func(gx, gy int) (float64, float64, bool) {
						pt := m.At(float64(gx)+0.5, float64(gy)+0.5)
						return pt.X, pt.Y, geomMeasured(gx, gy)
					})
				}
				hok = hok && int(hs[0].FrameIndex) < len(c.headers) && hs[0] == c.headers[hs[0].FrameIndex]
				v = geomJudge(c, p.tr, q, d.hidden, hok, err == nil, rms)
			}
			good, inexact = v != geomWrong, v == geomImprecise
		}
		mu.Lock()
		defer mu.Unlock()
		for _, m := range mod {
			minMod = math.Min(minMod, m)
		}
		switch {
		case good:
			found++
			if inexact {
				imprecise++
			}
		case ok:
			wrong++
		}
	})
	if filmErr != nil {
		t.Fatal(filmErr)
	}
	return found, imprecise, wrong, minMod
}

// geomModuleCode is the code of the module-size tests: a 240 x 135 grid, whose finder module is
// exactly one cell, at 8 video pixels per cell (1920 x 1080 video).
func geomModuleCode(t *testing.T) *geomCode { return geomNewCode(t, 240, 135, 8, 2, 4, 16) }

// geomModuleConfig is a preset, unscaled blur and all, filmed at 1920 x 1080 with the screen
// filling the given share of the width: the untilted finder module (one cell of the 240 x 135
// code) then measures 8 fill camera pixels.
func geomModuleConfig(t *testing.T, preset string, fill float64) channel.Config {
	cfg := geomConfig(t, preset, 1920, 1080)
	cfg.Geometry.Fill = fill
	return cfg
}

// geomModuleLimit is the smallest untilted finder module, in camera pixels, at which detection is
// asserted under the handheld preset's blur (DESIGN.md section 10, step 1); on the 240 x 135 code
// a module is one cell. It is set from the sweep of TestDetectModuleSweep, where 200 of 200 poses
// were found at 4.0 and 199 at 3.6. The 10-bit HLG preset needs more: 4.8 (199 of 200 at 4.4).
const (
	geomModuleLimit    = 4.0
	geomModuleLimitHLG = 4.8
)

// TestDetectModuleLimit: at the measured limits, with each preset's unscaled blur (1.5 camera
// pixels), the finders are found in every pose and never wrongly: handheld at geomModuleLimit
// (fill 0.5) and at 4.8 pixels per cell (fill 0.6, the case of the PR #30 review), and iphone-hdr
// at geomModuleLimitHLG. 12 poses per case always (seeds 6000 to 6011); 24 (seeds 6000 to 6023)
// with QRVT_LONG_TESTS=1.
func TestDetectModuleLimit(t *testing.T) {
	raceSkipStatistics(t)
	c := geomModuleCode(t)
	n := geomLongSize(24, 12)
	for _, tc := range []struct {
		preset string
		fill   float64
	}{{"handheld", geomModuleLimit / 8}, {"handheld", 0.6}, {"iphone-hdr", geomModuleLimitHLG / 8}} {
		found, imprecise, wrong, minMod := geomDetectRate(t, c, geomModuleConfig(t, tc.preset, tc.fill), n, 6000)
		t.Logf("%s fill %.2f: found %d of %d (%d imprecise), wrong %d; smallest true module %.2f px",
			tc.preset, tc.fill, found, n, imprecise, wrong, minMod)
		if found != n || wrong != 0 || float64(imprecise) > geomMaxImprecise*float64(found) {
			t.Errorf("%s fill %.2f: found %d of %d, %d imprecise, %d wrong", tc.preset, tc.fill, found, n, imprecise, wrong)
		}
	}
}

// TestDetectModuleSweep measures geometry against the cell size under each preset's own blur, on
// the 240 x 135 code (finder module = one cell) filmed at 1920 x 1080: 200 poses (seeds 7000 to
// 7199) at each fill from 0.35 to 0.9, untilted cells of 2.8 to 7.2 camera pixels. It reports the
// finders found (a quad counts only when its header decodes), wrong quads and headers, and the
// cell-centre error. It only logs; it runs with QRVT_LONG_TESTS=1.
func TestDetectModuleSweep(t *testing.T) {
	raceSkipStatistics(t)
	if !geomLong() {
		t.Skip("set QRVT_LONG_TESTS=1")
	}
	c := geomModuleCode(t)
	presets := []string{"tripod", "handheld", "budget-android", "iphone-hdr", "harsh"}
	if p := os.Getenv("QRVT_SWEEP_PRESETS"); p != "" { // a comma-separated subset, for local runs
		presets = strings.Split(p, ",")
	}
	for _, preset := range presets {
		for _, fill := range []float64{0.35, 0.4, 0.45, 0.5, 0.55, 0.6, 0.75, 0.9} {
			cfg := geomModuleConfig(t, preset, fill)
			cfg.Name = fmt.Sprintf("SWEEP %s, %.1f px per cell", preset, 8*fill)
			geomSweep(t, c, cfg, 200, 7000)
		}
	}
}
