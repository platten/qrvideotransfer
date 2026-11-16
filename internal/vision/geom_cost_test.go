package vision

import (
	"math"
	"runtime"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/video"
)

// geomDetectOnce times one Detect of f with brightness plane y, and fails unless the code is not
// located (hostile) or located (otherwise).
func geomDetectOnce(t *testing.T, d *FinderDetector, f *video.Frame, y []uint16, hostile bool) time.Duration {
	t.Helper()
	f.Y = y
	start := time.Now()
	_, ok := d.Detect(f, nil)
	el := time.Since(start)
	if ok == hostile {
		t.Fatalf("located %v, want %v", ok, !hostile)
	}
	return el
}

// TestGeomPairRatios: each hostile run is set against the mean of the real-code runs either side
// of it, and the median of those ratios is taken. A burst of load on a few hostile runs or on a few
// real-code runs moves the extremes, not the median; a steady ratio comes back exactly.
func TestGeomPairRatios(t *testing.T) {
	ms := func(v ...float64) []time.Duration {
		d := make([]time.Duration, len(v))
		for i, x := range v {
			d[i] = time.Duration(x * float64(time.Millisecond))
		}
		return d
	}
	for _, k := range []struct {
		real, hostile []time.Duration
		lo, med, hi   float64
	}{
		// Steady: every ratio 12.
		{ms(25, 25, 25, 25, 25, 25), ms(300, 300, 300, 300, 300), 12, 12, 12},
		// Neighbours differ: 300 against the mean of 20 and 30 is 12.
		{ms(20, 30, 20, 30), ms(300, 300, 300), 12, 12, 12},
		// One hostile run slowed by load (600) and one real-code run sped up by a quiet moment (15,
		// so its two neighbours read 300/20 = 15): the median stays 12.
		{ms(25, 25, 25, 25, 25, 25, 15, 25), ms(300, 600, 300, 300, 300, 300, 300), 12, 12, 24},
		// One real-code run slowed (75): its neighbours read 300/50 = 6; the median stays 12.
		{ms(25, 25, 75, 25, 25, 25), ms(300, 300, 300, 300, 300), 6, 12, 12},
	} {
		lo, med, hi := geomPairRatios(k.real, k.hostile)
		if math.Abs(lo-k.lo) > 1e-9 || math.Abs(med-k.med) > 1e-9 || math.Abs(hi-k.hi) > 1e-9 {
			t.Errorf("real %v, hostile %v: ratios %.3f / %.3f / %.3f, want %.3f / %.3f / %.3f", k.real, k.hostile, lo, med, hi, k.lo, k.med, k.hi)
		}
	}
}

// geomCostRounds is how many interleaved reference and hostile runs geomCostRatio times.
const geomCostRounds = 7

// geomCostRatio asserts that a hostile frame's Detect takes at most most times a real code's of the
// same size, measured in the same process, and allocates at most limit bytes. The two are timed in
// turn, geomCostRounds times each, and the ratio is that of their fastest runs: interleaving lets
// both see the same load, and the minimum discards the runs a busy machine slowed. A ratio holds on
// slow and loaded runners alike, where a wall-clock bound does not.
func geomCostRatio(t *testing.T, name string, real, hostile *video.Frame, hostilePlanes [][]uint16, most float64, limit uint64) {
	t.Helper()
	var dr, dh FinderDetector
	// Allocation, hostile runs only.
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := range len(hostilePlanes) * 2 {
		geomDetectOnce(t, &dh, hostile, hostilePlanes[i%len(hostilePlanes)], true)
	}
	runtime.ReadMemStats(&after)
	alloc := (after.TotalAlloc - before.TotalAlloc) / uint64(len(hostilePlanes)*2)
	ref, el := time.Duration(1<<62), time.Duration(1<<62)
	realY := real.Y
	for i := range geomCostRounds {
		ref = min(ref, geomDetectOnce(t, &dr, real, realY, false))
		el = min(el, geomDetectOnce(t, &dh, hostile, hostilePlanes[i%len(hostilePlanes)], true))
	}
	ratio := float64(el) / float64(ref)
	t.Logf("%s: fastest %v per Detect against %v for a real code (%.1f times), %.1f MB allocated", name, el, ref, ratio, float64(alloc)/(1<<20))
	if alloc > limit {
		t.Errorf("%s: %.1f MB allocated per Detect, want at most %.0f", name, float64(alloc)/(1<<20), float64(limit)/(1<<20))
	}
	if ratio > most {
		t.Errorf("%s: %.1f times a real code's Detect, want at most %.0f", name, ratio, most)
	}
}
