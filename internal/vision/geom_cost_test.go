package vision

import (
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/video"
)

// geomDetectCost runs Detect n times on f, its brightness plane taken from planes in turn, and
// returns the median time and the mean allocation per call. Every call must leave the code not
// located when hostile is true, or located otherwise.
func geomDetectCost(t *testing.T, f *video.Frame, planes [][]uint16, n int, hostile bool) (time.Duration, uint64) {
	t.Helper()
	var d FinderDetector
	times := make([]time.Duration, n)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := range n {
		f.Y = planes[i%len(planes)]
		start := time.Now()
		_, ok := d.Detect(f, nil)
		times[i] = time.Since(start)
		if ok == hostile {
			t.Fatalf("call %d: located %v, want %v", i, ok, !hostile)
		}
	}
	runtime.ReadMemStats(&after)
	slices.Sort(times)
	return times[n/2], (after.TotalAlloc - before.TotalAlloc) / uint64(n)
}

// geomCostRatio asserts that a hostile frame's Detect takes at most most times a real code's of the
// same size, measured in the same process, and allocates at most limit bytes. A ratio holds on slow
// and loaded runners alike, where a wall-clock bound does not.
func geomCostRatio(t *testing.T, name string, real, hostile *video.Frame, hostilePlanes [][]uint16, most float64, limit uint64) {
	t.Helper()
	ref, _ := geomDetectCost(t, real, [][]uint16{real.Y}, 5, false)
	el, alloc := geomDetectCost(t, hostile, hostilePlanes, 6, true)
	ratio := float64(el) / float64(ref)
	t.Logf("%s: %v per Detect against %v for a real code (%.1f times), %.1f MB allocated", name, el, ref, ratio, float64(alloc)/(1<<20))
	if alloc > limit {
		t.Errorf("%s: %.1f MB allocated per Detect, want at most %.0f", name, float64(alloc)/(1<<20), float64(limit)/(1<<20))
	}
	if ratio > most {
		t.Errorf("%s: %.1f times a real code's Detect, want at most %.0f", name, ratio, most)
	}
}
