package vision

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/modem"
)

// Limits of the glare assertion. Without fringes the miss rate measured up to 0.046 (Gray2,
// tripod) and the loss away from the patch up to 0.00007. With fringes of amplitude 24 and stripe
// cancellation on, the rule misses far more under the patch (0.063 tripod, 0.26 handheld) and loses
// up to 0.5% of the cells away from it, because the cancellation answers the patch's broad bump with
// waves across the capture; that combination is a recorded residual (DESIGN.md section 10, step 5),
// bounded here at its measured value with a margin so that it cannot grow unnoticed.
const (
	maxGlareMiss       = 0.05  // share of cells under the patch that still give soft bits
	maxGlareFalse      = 0.002 // share of cells away from the patch made unknown
	maxGlareMissFringe = 0.30
	// Through the geometry pipeline the miss rate measured 0.073 on tripod (0.036 handheld), an
	// accepted residual revisited with S2: the cells missed are wrong data stated with confidence.
	// Since the sampler marks clipped cells unknown (PR #38) it measures 0.037 to 0.038 (0.012 to
	// 0.013); the bound stays until a review tightens it.
	maxGlareMissPipeline = 0.10
	maxGlareFalseFringe  = 0.008
	maxFringeLoss        = 0.0005 // credited cells lost to fringes without a patch: measured 0.00019
)

// TestGlareUnknown: credited cells under a glare patch come back unknown (zero soft bits) and
// credited cells away from it are unaffected. The same recording is filmed with and without a
// specular patch over 5% of the code (fixed seeds, so noise, fringes and geometry are identical). A
// cell is under the patch where the patch raised its brightness by more than half a level spacing,
// enough to change its level (lesser lifts mostly decode correctly: of the Gray2 cells lifted by a
// quarter to a half of a spacing that kept their soft bits, 3% of those bits were wrong). A cell is
// away from the patch where no cell within around + grow cells of it, the margin the rule gives up
// on purpose, was raised by a code value or more. Only cells the phase field credits are counted,
// each with the soft bits of its own phase. Gray4, Gray2, RGB8 and Gray8 on `tripod` and
// `handheld`, and Gray4 with fringes of amplitude 24 and stripe cancellation on, where fringes
// without a patch must cost no credited cell; 212 x 120 grid, 1600 x 900 camera, 4 captures each.
// Statistical: skipped under the race detector.
func TestGlareUnknown(t *testing.T) {
	raceSkipStatistics(t)
	type glareCase struct {
		mod    uint8
		fringe float64
	}
	cases := []glareCase{{2, 0}, {1, 0}, {3, 0}, {4, 0}, {2, 24}}
	for _, k := range cases {
		s := newRadSession(t, 212, 120, 4, k.mod, 8, 0x61+uint64(k.mod), false)
		for pi, name := range []string{"tripod", "handheld"} {
			t.Run(fmt.Sprintf("%s/fringe%.0f/%s", s.mod.Name(), k.fringe, name), func(t *testing.T) {
				c := preset(t, name, 1600, 900)
				c.Sensor.Fringe = k.fringe
				seed := 0x700 + uint64(10*pi) + uint64(k.mod)
				clean := s.shoot(t, c, time.Second/30, 4, seed)
				c.Optics.Glare = 0.05
				glare := s.shoot(t, c, time.Second/30, 4, seed)
				spacing := float64(219) / float64(s.mod.Levels()-1)
				var under, missed, away, falsely, missedBits, missedWrong, quarter, quarterMissed int
				b := s.mod.BitsPerCell()
				soft := make([]int8, len(s.layout.DataCells())*b)
				for n := range glare {
					obs := append([]modem.Obs(nil), glare[n].obs...)
					f := (&TileEqualiser{Mod: s.mod, Stripes: k.fringe > 0}).Fit(obs, glare[n].palette, s.layout, glare[n].anchors)
					pf := s.phaseOf(glare[n])
					// lit marks the grid cells the patch raised by a code value or more; a cell is
					// away from the patch only beyond the rings the rule deliberately gives up.
					gx, gy := s.layout.GridX(), s.layout.GridY()
					lit := make([]bool, gx*gy)
					for i, cl := range s.layout.DataCells() {
						lit[int(cl.Y)*gx+int(cl.X)] = glare[n].obs[i].Y-clean[n].obs[i].Y >= 1
					}
					near := func(x, y int) bool {
						const r = around + grow
						for v := max(y-r, 0); v <= min(y+r, gy-1); v++ {
							for u := max(x-r, 0); u <= min(x+r, gx-1); u++ {
								if lit[v*gx+u] {
									return true
								}
							}
						}
						return false
					}
					for p := range 4 {
						if SoftBits(soft, obs, s.layout, f, s.mod, &pf, p) == 0 {
							continue
						}
						for i := range obs {
							if q, ok := pf.Phase(i); !ok || q != p {
								continue
							}
							d := float64(glare[n].obs[i].Y - clean[n].obs[i].Y)
							if math.IsNaN(d) {
								continue
							}
							stated := false
							for _, v := range soft[i*b : i*b+b] {
								stated = stated || v != 0
							}
							if d > spacing/4 {
								quarter++
								if stated {
									quarterMissed++
								}
							}
							switch {
							case d > spacing/2:
								under++
								if stated {
									missed++
									fr, _ := credited(glare[n].tr.CellMix(int(s.layout.DataCells()[i].X), int(s.layout.DataCells()[i].Y)), p)
									for j, v := range soft[i*b : i*b+b] {
										missedBits++
										if fr >= 0 && (v > 0) != (symbolBit(s.symbols[fr][i], b, j) == 0) {
											missedWrong++
										}
									}
								}
							case !near(int(s.layout.DataCells()[i].X), int(s.layout.DataCells()[i].Y)):
								away++
								if !stated {
									falsely++
								}
							}
						}
					}
				}
				miss, fa := float64(missed)/float64(max(under, 1)), float64(falsely)/float64(max(away, 1))
				t.Logf("%d credited cells under the patch, %d still stated (miss rate %.4f; %d of their %d soft bits wrong); at a quarter of a spacing %d of %d still stated (%.4f, logged only); %d away from it, %d made unknown (%.5f)",
					under, missed, miss, missedWrong, missedBits, quarterMissed, quarter, float64(quarterMissed)/float64(max(quarter, 1)), away, falsely, fa)
				if under == 0 {
					t.Fatal("no cell under the patch")
				}
				lm, lf := maxGlareMiss, maxGlareFalse
				if k.fringe > 0 {
					lm, lf = maxGlareMissFringe, maxGlareFalseFringe
					// Without a patch, fringes alone must cost almost no credited cell.
					credit, lost := 0, 0
					for n := range clean {
						obs := append([]modem.Obs(nil), clean[n].obs...)
						(&TileEqualiser{Mod: s.mod, Stripes: true}).Fit(obs, clean[n].palette, s.layout, clean[n].anchors)
						pf := s.phaseOf(clean[n])
						for i := range obs {
							if _, ok := pf.Phase(i); ok && usable(clean[n].obs[i]) {
								credit++
								if obs[i].Saturated {
									lost++
								}
							}
						}
					}
					t.Logf("without a patch, %d of %d credited cells made unknown", lost, credit)
					if float64(lost) > maxFringeLoss*float64(credit) {
						t.Errorf("without a patch, %d of %d credited cells made unknown, above %.4f", lost, credit, maxFringeLoss)
					}
				}
				if miss > lm {
					t.Errorf("miss rate %.4f above %.3f", miss, lm)
				}
				if fa > lf {
					t.Errorf("%.5f of the cells away from the patch made unknown, above %.4f", fa, lf)
				}
			})
		}
	}
}
