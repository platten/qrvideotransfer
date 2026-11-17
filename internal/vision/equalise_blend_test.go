package vision

import (
	"math"
	"testing"

	"github.com/platten/qrvideotransfer/internal/modem"
)

// TestFitSkipsBlends: with anchors that see a boundary, the cells that blend two frames are marked
// to be left out of the fit; blend widens the variances by the share of another frame.
func TestFitSkipsBlends(t *testing.T) {
	s := newSynth(t, 480, 270, 2, 3, 0, 32)
	edge := func(_, y float64) [4]float64 {
		u := math.Min(1, math.Max(0, (y-135)/60))
		return [4]float64{1 - u, u, 0, 0}
	}
	pf := AnchorPhase{}.Estimate(synthAnchors(s.l, 2.4, edge), s.l)
	skip := mixedCells(pf)
	n := 0
	for _, k := range skip {
		if k {
			n++
		}
	}
	if n == 0 || n == len(skip) {
		t.Fatalf("%d of %d cells skipped", n, len(skip))
	}
	if mixedCells(PhaseField{Weights: make([][4]float32, 3)}) != nil {
		t.Fatal("a field with no clean cell should skip none")
	}
	st := modem.LocalStats{Levels: 2}
	st.Centre[1] = [3]float32{200, 128, 128}
	st.Centre[0] = [3]float32{0, 128, 128}
	st.Variance[0], st.Variance[1] = 4, 4
	blend(&st, 0, 1)
	if st.Variance[0] != 4 {
		t.Fatal("no blend changed the variance")
	}
	// Levels 0 and 200 in Y: the variance of the level centres is 100^2 (DESIGN.md section 10, step
	// 7), so a share of 0.1 of another frame adds 0.01 x 10,000.
	blend(&st, 0.1, 1)
	if want := float32(4 + 100); math.Abs(float64(st.Variance[0]-want)) > 1e-3 || st.Variance[1] != st.Variance[0] {
		t.Fatalf("blend variance %v, want %v", st.Variance[:2], want)
	}
	// With three planes it is the variance per plane, averaged: chroma is constant here.
	st.Variance[0], st.Variance[1] = 4, 4
	blend(&st, 0.1, 3)
	if want := float32(4 + 100.0/3); math.Abs(float64(st.Variance[0]-want)) > 1e-3 {
		t.Fatalf("three-plane blend variance %v, want %v", st.Variance[0], want)
	}
}

// TestGlareCountsClipped: cells the sampler already marked saturated because they clipped (a glare
// patch's core) count among the neighbours that read white, so the lifted cells around the core
// are made unknown too; clipped dark cells do not.
func TestGlareCountsClipped(t *testing.T) {
	s := newSynth(t, 212, 120, 2, 2, 0, 60)
	cx, cy := 100, 60
	for i, c := range s.l.DataCells() {
		dx, dy := int(c.X)-cx, int(c.Y)-cy
		switch r := max(iabs(dx), iabs(dy)); {
		case r <= 5: // the core: clipped white, already unknown
			s.obs[i] = modem.Obs{Y: 235, Cb: 128, Cr: 128, Saturated: true}
		case r <= 7: // the soft edge: lifted to the lightest level, not clipped
			s.obs[i].Y = s.centre(3, int(c.X), int(c.Y))[0]
		}
	}
	(&TileEqualiser{Mod: s.c}).Fit(s.obs, s.palette, s.l, s.anchors)
	edge, flagged := 0, 0
	for i, c := range s.l.DataCells() {
		if r := max(iabs(int(c.X)-cx), iabs(int(c.Y)-cy)); r == 6 || r == 7 {
			edge++
			if s.obs[i].Saturated {
				flagged++
			}
		}
	}
	if edge == 0 || flagged < edge*9/10 {
		t.Fatalf("%d of %d edge cells made unknown around a clipped core", flagged, edge)
	}
	// A clipped dark region is not a patch of white.
	d := newSynth(t, 212, 120, 2, 2, 0, 61)
	for i, c := range d.l.DataCells() {
		if max(iabs(int(c.X)-cx), iabs(int(c.Y)-cy)) <= 5 {
			d.obs[i] = modem.Obs{Y: 0, Cb: 128, Cr: 128, Saturated: true}
		}
	}
	(&TileEqualiser{Mod: d.c}).Fit(d.obs, d.palette, d.l, d.anchors)
	for i, c := range d.l.DataCells() {
		if r := max(iabs(int(c.X)-cx), iabs(int(c.Y)-cy)); r > 5 && r <= 9 && d.obs[i].Saturated {
			t.Fatalf("cell %d beside a clipped dark region made unknown", i)
		}
	}
}

// iabs returns |v|.
func iabs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// TestGlareSeveralLevelsClip: when an overexposed capture clips more than the lightest level, the
// cells that read as the lightest level are more common than one in L everywhere, which is no glare.
// The glare rule must not erase the cells that did not clip (it erased 3.9% of them with two Gray4
// levels clipped and 93% with three), yet still find a patch that lifts everything to white.
func TestGlareSeveralLevelsClip(t *testing.T) {
	for _, k := range []struct {
		clipped int     // Gray4 levels, from the top, at the clip
		maxLoss float64 // share of unclipped cells allowed to be made unknown
	}{{2, 0.002}, {3, 0.002}} {
		s := newSynth(t, 212, 120, 2, 2, 0, 70+uint64(k.clipped))
		for i, lv := range s.levels {
			if lv >= 4-k.clipped {
				// Clipped data cells are scattered, so the sampler does not mark them (it marks
				// clipped areas): they read 235 and stay usable.
				s.obs[i].Y = 235
			}
		}
		// A glare patch around (60, 60): every cell under it at the clip.
		for i, c := range s.l.DataCells() {
			if max(iabs(int(c.X)-60), iabs(int(c.Y)-60)) <= 6 {
				s.obs[i].Y = 235
			}
		}
		(&TileEqualiser{Mod: s.c}).Fit(s.obs, s.palette, s.l, s.anchors)
		unclipped, lost, patch, patchFlagged := 0, 0, 0, 0
		for i, c := range s.l.DataCells() {
			r := max(iabs(int(c.X)-60), iabs(int(c.Y)-60))
			switch {
			case r <= 4:
				patch++
				if s.obs[i].Saturated {
					patchFlagged++
				}
			case r > 10 && s.levels[i] < 4-k.clipped:
				unclipped++
				if s.obs[i].Saturated {
					lost++
				}
			}
		}
		t.Logf("%d levels clipped: %d of %d unclipped cells away from the patch made unknown; %d of %d patch cells unknown", k.clipped, lost, unclipped, patchFlagged, patch)
		if float64(lost) > k.maxLoss*float64(unclipped) {
			t.Errorf("%d levels clipped: %d of %d unclipped cells made unknown", k.clipped, lost, unclipped)
		}
		if k.clipped == 2 && patchFlagged < patch*9/10 {
			t.Errorf("%d levels clipped: the patch went unnoticed (%d of %d cells unknown)", k.clipped, patchFlagged, patch)
		}
	}
}
