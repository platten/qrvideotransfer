package vision

import (
	"math"
	"runtime"
	"slices"
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

// geomCostRounds is how many hostile runs geomCostRatio times, each between two real-code runs.
const geomCostRounds = 15

// geomPairRatios sets hostile run i against the mean of real-code runs i and i+1, the runs either
// side of it (real holds one more run than hostile), and returns the smallest, median and largest of
// those ratios.
func geomPairRatios(real, hostile []time.Duration) (lo, med, hi float64) {
	r := make([]float64, len(hostile))
	for i, h := range hostile {
		r[i] = float64(h) / (float64(real[i]+real[i+1]) / 2)
	}
	slices.Sort(r)
	return r[0], r[len(r)/2], r[len(r)-1]
}

// geomCostRatio asserts that a hostile frame's Detect takes at most most times a real code's of the
// same size, measured in the same process, and allocates at most limit bytes. The two are timed in
// turn, geomCostRounds hostile runs each between two real-code runs, and the ratio is the median of
// each hostile run's time over the mean of its two neighbours'. Neighbours see nearly the same load,
// and the median discards the pairs a burst of load upset. A ratio of the fastest runs did not
// hold: under load the short real-code run often finds a quiet moment the long hostile run does
// not, and in CI run 43967374482 that read 16.7 times where the machine's quiet ratio is 12.6. A
// ratio holds on slow and loaded runners alike, where a wall-clock bound does not. The callers run
// serially (no t.Parallel): the package's parallel tests would share the cores unevenly between
// the two runs of a pair.
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
	realY := real.Y
	refs := []time.Duration{geomDetectOnce(t, &dr, real, realY, false)}
	var els []time.Duration
	for i := range geomCostRounds {
		els = append(els, geomDetectOnce(t, &dh, hostile, hostilePlanes[i%len(hostilePlanes)], true))
		refs = append(refs, geomDetectOnce(t, &dr, real, realY, false))
	}
	lo, ratio, hi := geomPairRatios(refs, els)
	t.Logf("%s: hostile Detect over its real-code neighbours, %d pairs: %.2f / %.2f / %.2f times (smallest / median / largest; fastest %v against %v), %.1f MB allocated",
		name, len(els), lo, ratio, hi, slices.Min(els), slices.Min(refs), float64(alloc)/(1<<20))
	if alloc > limit {
		t.Errorf("%s: %.1f MB allocated per Detect, want at most %.0f", name, float64(alloc)/(1<<20), float64(limit)/(1<<20))
	}
	if ratio > most {
		t.Errorf("%s: median %.2f times a real code's Detect, want at most %.0f", name, ratio, most)
	}
}
