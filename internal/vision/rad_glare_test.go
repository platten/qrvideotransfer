package vision

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/modem"
)

// Limits of the glare assertion (see TestGlareUnknown for the measured figures).
const (
	maxGlareMiss  = 0.05  // share of cells under the patch that still give soft bits
	maxGlareFalse = 0.002 // share of cells away from the patch made unknown
)

// TestGlareUnknown: credited cells under a glare patch come back unknown (zero soft bits) and
// credited cells away from it are unaffected. The same recording is filmed with and without a
// specular patch over 5% of the code (fixed seeds, so noise, fringes and geometry are identical); a
// cell is under the patch where the patch raised its brightness by more than a quarter of a level
// spacing, and away from it where it raised it by less than one code value. Only cells the phase
// field credits are counted, and each gives its soft bits for its own phase. Gray4, Gray2, RGB8 and
// Gray8 on `tripod` and `handheld`, and Gray4 with fringes of amplitude 24 and stripe cancellation
// on; 212 x 120 grid, 1600 x 900 camera, 4 captures each.
func TestGlareUnknown(t *testing.T) {
	type glareCase struct {
		mod    uint8
		fringe float64
	}
	cases := []glareCase{{2, 0}, {1, 0}, {3, 0}, {4, 0}, {2, 24}}
	if radRace {
		cases = cases[:1]
	}
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
				var under, missed, away, falsely int
				b := s.mod.BitsPerCell()
				soft := make([]int8, len(s.layout.DataCells())*b)
				for n := range glare {
					obs := append([]modem.Obs(nil), glare[n].obs...)
					f := (&TileEqualiser{Mod: s.mod, Stripes: k.fringe > 0}).Fit(obs, glare[n].palette, s.layout, glare[n].anchors)
					pf := s.phaseOf(glare[n])
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
							switch {
							case d > spacing/4:
								under++
								if stated {
									missed++
								}
							case d < 1:
								away++
								if !stated {
									falsely++
								}
							}
						}
					}
				}
				miss, fa := float64(missed)/float64(max(under, 1)), float64(falsely)/float64(max(away, 1))
				t.Logf("%d credited cells under the patch, %d still stated (miss rate %.4f); %d away from it, %d made unknown (%.5f)", under, missed, miss, away, falsely, fa)
				if under == 0 {
					t.Fatal("no cell under the patch")
				}
				if miss > maxGlareMiss {
					t.Errorf("miss rate %.4f above %.3f", miss, maxGlareMiss)
				}
				if fa > maxGlareFalse {
					t.Errorf("%.5f of the cells away from the patch made unknown, above %.4f", fa, maxGlareFalse)
				}
			})
		}
	}
}
