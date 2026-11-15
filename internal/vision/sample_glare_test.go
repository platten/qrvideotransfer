package vision

import (
	"math"
	"testing"

	"github.com/platten/qrvideotransfer/internal/modem"
)

// TestSampleGlareFlagged: the simulator's specular glare patch (5% of the code area) on handheld
// 4K captures (480 x 270 grid, seeds 22000 and 22001). Each pose is filmed twice, with and without
// the patch, which leaves everything else equal; both are sampled through the mesh fitted to the
// capture without it. Cells the patch turns near-white (changed by 20 code values or more, and at
// 225 or above) must come back saturated (at most 3% missed), and cells it leaves within 1 code
// value must not (at most 0.1% flagged). Cells on the patch's soft edge, brightened but not
// clipped, are counted apart: telling them from data is the equaliser's part.
func TestSampleGlareFlagged(t *testing.T) {
	n := 2
	if geomRace {
		n = 1
	}
	c := geomNewCode(t, 480, 270, 4, 2, 1, 31)
	clean := geomConfig(t, "handheld", 0, 0)
	glared := clean
	glared.Optics.Glare = 0.05
	cells := c.layout.DataCells()
	oc, og := make([]modem.Obs, len(cells)), make([]modem.Obs, len(cells))
	var under, partial, missed, outside, falseFlags int
	for i := range n {
		pc, err := geomPose(c, clean, n, i, 22000)
		if err != nil {
			t.Fatal(err)
		}
		pg, err := geomPose(c, glared, n, i, 22000)
		if err != nil {
			t.Fatal(err)
		}
		var d FinderDetector
		q, ok := d.Detect(pc.f, nil)
		if !ok {
			t.Fatal("not located")
		}
		m, _, err := MeshFitter{}.Fit(pc.f, q, c.layout)
		if err != nil {
			t.Fatal(err)
		}
		FootprintSampler{}.Sample(oc, pc.f, m, cells)
		FootprintSampler{}.Sample(og, pg.f, m, cells)
		for k := range cells {
			switch diff := math.Abs(float64(og[k].Y - oc[k].Y)); {
			case diff >= 20 && og[k].Y < sampleClipWhite-sampleClipMargin:
				partial++ // brightened by the patch but under the clip level: left to the equaliser
			case diff >= 20:
				under++
				if !og[k].Saturated {
					missed++
				}
			case diff < 1:
				outside++
				if og[k].Saturated {
					falseFlags++
				}
			}
		}
	}
	missRate, falseRate := float64(missed)/float64(max(1, under)), float64(falseFlags)/float64(max(1, outside))
	t.Logf("under the patch %d near-white cells, %.2f%% not flagged; %d brightened cells on its soft edge; outside %d cells, %.3f%% flagged",
		under, 100*missRate, partial, outside, 100*falseRate)
	if under == 0 || missRate > 0.03 {
		t.Errorf("%.2f%% of %d glared cells not flagged, want at most 3%%", 100*missRate, under)
	}
	if falseRate > 0.001 {
		t.Errorf("%.3f%% of %d unglared cells flagged, want at most 0.1%%", 100*falseRate, outside)
	}
}

// TestSampleWhiteAtClip: on the ideal preset, whose display white lands exactly on the sensor's
// clip, light data cells are not flagged as glare: at most 1% of the cells of a four-grey code, and
// at most 4% of a black-and-white one (where a light cell among mostly light neighbours is common),
// at 1920 x 1080 with a 240 x 135 grid at fill 0.75, seed 23000.
func TestSampleWhiteAtClip(t *testing.T) {
	for _, tc := range []struct {
		mod  uint8
		most float64
	}{{2, 0.01}, {1, 0.04}} {
		c := geomNewCode(t, 240, 135, 8, tc.mod, 1, 32)
		cfg := geomConfig(t, "ideal", 1920, 1080)
		cfg.Geometry.Fill = 0.75
		p, err := geomPose(c, cfg, 1, 0, 23000)
		if err != nil {
			t.Fatal(err)
		}
		var d FinderDetector
		q, ok := d.Detect(p.f, nil)
		if !ok {
			t.Fatal("not located")
		}
		m, _, err := MeshFitter{}.Fit(p.f, q, c.layout)
		if err != nil {
			t.Fatal(err)
		}
		cells := c.layout.DataCells()
		obs := make([]modem.Obs, len(cells))
		FootprintSampler{}.Sample(obs, p.f, m, cells)
		flagged := 0
		for _, o := range obs {
			if o.Saturated {
				flagged++
			}
		}
		share := float64(flagged) / float64(len(cells))
		t.Logf("modulation %d: %.2f%% flagged", tc.mod, 100*share)
		if share > tc.most {
			t.Errorf("modulation %d: %.2f%% of cells flagged, want at most %.0f%%", tc.mod, 100*share, 100*tc.most)
		}
	}
}
