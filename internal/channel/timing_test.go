package channel

import (
	"math"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/video"
)

func testTimeline(t *testing.T, p DisplayParams, freeze time.Duration, starts ...time.Duration) *timeline {
	t.Helper()
	var fs []*video.Frame
	for i, s := range starts {
		fs = append(fs, &video.Frame{PTS: s, Index: 10 + i})
	}
	tl, err := newTimeline(fs, 100*time.Millisecond, p, TimingParams{SeamFreeze: freeze})
	if err != nil {
		t.Fatal(err)
	}
	return tl
}

func TestOccurrences(t *testing.T) {
	tl := testTimeline(t, DisplayParams{}, 20*time.Millisecond, 0, 50*time.Millisecond)
	got := tl.occurrences(0.110, 0.180)
	want := []occ{{1, 0.050, 0.120}, {0, 0.120, 0.170}, {1, 0.170, 0.240}}
	if len(got) != len(want) {
		t.Fatalf("occurrences = %v, want %v", got, want)
	}
	for i := range want {
		if got[i].j != want[i].j || !near(got[i].s, want[i].s, 1e-12) || !near(got[i].e, want[i].e, 1e-12) {
			t.Errorf("occurrence %d = %v, want %v", i, got[i], want[i])
		}
	}
	// Scan-out and response keep a showing visible after it ends at the top.
	tl = testTimeline(t, DisplayParams{ScanOut: 10 * time.Millisecond, Response: 5 * time.Millisecond}, 0, 0, 50*time.Millisecond)
	if got := tl.occurrences(0.060, 0.061); len(got) != 2 || got[0].j != 0 {
		t.Errorf("occurrences near a switch = %v", got)
	}
}

func TestNewTimelineRejects(t *testing.T) {
	for _, s := range [][]time.Duration{{0, 0}, {-1}, {0, 100 * time.Millisecond}} {
		var fs []*video.Frame
		for _, d := range s {
			fs = append(fs, &video.Frame{PTS: d})
		}
		if _, err := newTimeline(fs, 100*time.Millisecond, DisplayParams{}, TimingParams{}); err == nil {
			t.Errorf("times %v accepted", s)
		}
	}
}

// The weights of a mixed capture at known times: a switch at 50 ms reaches row fraction yd at
// 50 + 16 yd ms, and a 4 ms exposure from 52 ms sees the new frame for (56 - 50 - 16 yd) / 4 of it.
func TestWeightsKnownTimes(t *testing.T) {
	tl := testTimeline(t, DisplayParams{ScanOut: 16 * time.Millisecond}, 0, 0, 50*time.Millisecond)
	a, b := 0.052, 0.056
	occs := tl.occurrences(a, b)
	for _, yd := range []float64{0, 0.1, 0.25, 0.3, 0.5, 0.9} {
		wantNew := clamp((0.006-0.016*yd)/0.004, 0, 1)
		var gotNew, sum float64
		for _, o := range occs {
			w := tl.weight(o, a, b, yd)
			sum += w
			if o.j == 1 {
				gotNew += w
			}
		}
		if !near(gotNew, wantNew, 1e-12) || !near(sum, 1, 1e-12) {
			t.Errorf("yd %v: new frame weight %v, want %v; sum %v", yd, gotNew, wantNew, sum)
		}
	}
}

// The response ramp's closed form agrees with numerical integration of the visibility.
func TestWeightResponse(t *testing.T) {
	tl := testTimeline(t, DisplayParams{Response: 6 * time.Millisecond}, 0, 0, 50*time.Millisecond, 53*time.Millisecond)
	vis := func(o occ, t float64) float64 {
		r := func(u float64) float64 { return clamp(u/tl.resp, 0, 1) }
		return r(t-o.s) - r(t-o.e)
	}
	a, b := 0.048, 0.062
	occs := tl.occurrences(a, b)
	if len(occs) != 3 {
		t.Fatalf("occurrences %v", occs)
	}
	sum := 0.0
	for _, o := range occs {
		const n = 200000
		num := 0.0
		for k := range n {
			num += vis(o, a+(b-a)*(float64(k)+0.5)/n)
		}
		num /= n
		got := tl.weight(o, a, b, 0)
		sum += got
		if math.Abs(got-num) > 1e-6 {
			t.Errorf("frame %d weight %v, numerical %v", o.j, got, num)
		}
	}
	if !near(sum, 1, 1e-12) {
		t.Errorf("weights sum to %v", sum)
	}
}

// Flicker: the closed-form mean of 1 + A sin(wt) over the exposure.
func TestFlicker(t *testing.T) {
	tl := testTimeline(t, DisplayParams{FlickerAmp: 0.1, FlickerHz: 240}, 0, 0)
	for _, a := range []float64{0, 0.001, 0.0023, 0.5} {
		b := a + 0.0013
		const n = 100000
		num := 0.0
		for k := range n {
			num += 1 + 0.1*math.Sin(2*math.Pi*240*(a+(b-a)*(float64(k)+0.5)/n))
		}
		if got := tl.flicker(a, b); math.Abs(got-num/n) > 1e-9 {
			t.Errorf("flicker(%v) = %v, numerical %v", a, got, num/n)
		}
	}
	if testTimeline(t, DisplayParams{}, 0, 0).flicker(0, 1) != 1 {
		t.Error("no flicker should give 1")
	}
}

func TestSchedule(t *testing.T) {
	p := TimingParams{FPS: 30, Jitter: time.Millisecond, Drop: 0.05, DropBurst: 3}
	const n = 30000
	s := schedule(p, time.Second, n, 9)
	dropped, runs := 0, 0
	for i, sh := range s {
		nominal := 1 + float64(i)/30
		if sh.slot != i || math.Abs(sh.start-nominal) > 0.0040001 {
			t.Fatalf("shot %d = %+v", i, sh)
		}
		if sh.dropped {
			dropped++
			if i == 0 || !s[i-1].dropped {
				runs++
			}
		}
	}
	// Runs of 3 begin with probability 0.05 per capture not already in a run.
	if r := float64(dropped) / n; math.Abs(r-0.15/1.10) > 0.01 {
		t.Errorf("dropped share %v", r)
	}
	if float64(dropped)/float64(runs) < 2.9 {
		t.Errorf("mean run %v", float64(dropped)/float64(runs))
	}
	again := schedule(p, time.Second, n, 9)
	for i := range s {
		if s[i] != again[i] {
			t.Fatal("schedule is not deterministic")
		}
	}
	if s := schedule(TimingParams{FPS: 60}, 0, 3, 1); s[2].start != 2.0/60 || s[1].dropped {
		t.Errorf("plain schedule %v", s)
	}
}

func TestWindow(t *testing.T) {
	c := still(4, 100)
	c.Timing.Readout = 20 * time.Millisecond
	c.Timing.Exposure = 2 * time.Millisecond
	sh := shot{start: 1}
	if a, b := sh.window(&c, 50); !near(a, 1.010, 1e-12) || !near(b, 1.012, 1e-12) {
		t.Errorf("window = %v, %v", a, b)
	}
}
