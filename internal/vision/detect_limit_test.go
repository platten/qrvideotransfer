package vision

import (
	"math"
	"sync"
	"testing"

	"github.com/platten/qrvideotransfer/internal/channel"
)

// geomDetectRate films n poses of the code with cfg and counts the quads found with every centre
// within half a module of the truth, the wrong quads, and the smallest true finder module seen,
// in camera pixels.
func geomDetectRate(t *testing.T, c *geomCode, cfg channel.Config, n int, seed uint64) (found, wrong int, minMod float64) {
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
		var d FinderDetector
		q, ok := d.Detect(p.f, nil)
		want, mod := geomFinderTruth(c, p.tr)
		good := ok
		for k := range 4 {
			if ok && geomDist(q.Centres[k], want[k]) > mod[k]/2 {
				good = false
			}
		}
		mu.Lock()
		defer mu.Unlock()
		for _, m := range mod {
			minMod = math.Min(minMod, m)
		}
		switch {
		case good:
			found++
		case ok:
			wrong++
		}
	})
	if filmErr != nil {
		t.Fatal(filmErr)
	}
	return found, wrong, minMod
}

// geomModuleCode is the code of the module-size tests: a 240 x 135 grid, whose finder module is
// exactly one cell, at 8 video pixels per cell (1920 x 1080 video).
func geomModuleCode(t *testing.T) *geomCode { return geomNewCode(t, 240, 135, 8, 2, 4, 16) }

// geomModuleConfig is the handheld preset, unscaled blur (1.5 camera pixels) and all, filmed at
// 1920 x 1080 with the screen filling the given share of the width: the untilted finder module
// then measures 8 fill camera pixels.
func geomModuleConfig(t *testing.T, fill float64) channel.Config {
	cfg := geomConfig(t, "handheld", 1920, 1080)
	cfg.Geometry.Fill = fill
	return cfg
}

// geomModuleLimit is the smallest untilted finder module, in camera pixels, at which detection is
// asserted under the handheld preset's blur (DESIGN.md section 10, step 1). It is set from the
// sweep of TestDetectModuleSweep.
const geomModuleLimit = 4.0

// TestDetectModuleLimit: at the measured limit of geomModuleLimit camera pixels per untilted
// finder module (fill 0.5), with the handheld preset's unscaled blur of 1.5 pixels, the finders
// are found in every one of 24 poses (seeds 6000 to 6023) and never wrongly; and at fill 0.6
// (4.8 pixels per cell, the case of the PR #30 review) likewise.
func TestDetectModuleLimit(t *testing.T) {
	c := geomModuleCode(t)
	n := 24
	if geomRace {
		n = 6
	}
	for _, fill := range []float64{geomModuleLimit / 8, 0.6} {
		found, wrong, minMod := geomDetectRate(t, c, geomModuleConfig(t, fill), n, 6000)
		t.Logf("fill %.2f: found %d of %d, wrong %d; smallest true module %.2f px", fill, found, n, wrong, minMod)
		if found != n || wrong != 0 {
			t.Errorf("fill %.2f: found %d of %d, %d wrong", fill, found, n, wrong)
		}
	}
}

// TestDetectModuleSweep measures the detection rate against the finder module size under the
// handheld preset's blur: 200 poses (seeds 7000 to 7199) at each fill from 0.35 to 0.75, untilted
// modules of 2.8 to 6 camera pixels. It only logs; it runs with QRVT_LONG_TESTS=1.
func TestDetectModuleSweep(t *testing.T) {
	if !geomLong() {
		t.Skip("set QRVT_LONG_TESTS=1")
	}
	c := geomModuleCode(t)
	for _, fill := range []float64{0.35, 0.4, 0.45, 0.5, 0.55, 0.6, 0.75} {
		found, wrong, minMod := geomDetectRate(t, c, geomModuleConfig(t, fill), 200, 7000)
		t.Logf("fill %.2f (module %.1f px untilted, smallest seen %.2f): found %d of 200, wrong %d", fill, 8*fill, minMod, found, wrong)
	}
}
