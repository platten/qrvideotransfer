package vision

import (
	"slices"
	"sync"
	"testing"

	"github.com/platten/qrvideotransfer/internal/modem"
)

// TestRadiometryConcurrent: TileEqualiser, AnchorPhase, SoftBits and CancelStripes are safe for
// concurrent use, sharing the layout, constellation, palette and anchors, as a decoder's workers do.
// Several goroutines run the whole radiometry chain on their own copies of one synthetic capture
// and must get what one goroutine gets alone. It runs in full under the race detector, which is
// what it is for.
func TestRadiometryConcurrent(t *testing.T) {
	s := newSynth(t, 212, 120, 2, 3, 0, 50)
	addWave(s, 15, 0.21, 0.13)
	ramp := func(x, y float64) [4]float64 {
		u := min(1, max(0, (y-40)/40))
		return [4]float64{1 - u, u, 0, 0}
	}
	anchors := synthAnchors(s.l, 2.4, ramp)
	for i := range anchors {
		anchors[i].Black, anchors[i].White = s.anchors[i].Black, s.anchors[i].White
	}
	run := func() []int8 {
		obs := append([]modem.Obs(nil), s.obs...)
		f := (&TileEqualiser{Mod: s.c, Stripes: true}).Fit(obs, s.palette, s.l, anchors)
		CancelStripes(obs, s.l, f, s.c)
		pf := AnchorPhase{Gamma: 2.4}.Estimate(anchors, s.l)
		out := make([]int8, 0, 4*2*len(obs))
		soft := make([]int8, 2*len(obs))
		for p := range 4 {
			SoftBits(soft, obs, s.l, f, s.c, &pf, p)
			out = append(out, soft...)
		}
		return out
	}
	want := run()
	const workers = 4
	got := make([][]int8, workers)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[w] = run()
		}()
	}
	wg.Wait()
	for w := range workers {
		if !slices.Equal(got[w], want) {
			t.Fatalf("worker %d got other soft bits than one goroutine alone", w)
		}
	}
}
