package vision

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/modem"
)

// Limits of the glare assertion: measured up to 0.045 (Gray2) and 0.0009, with margin.
const (
	maxGlareMiss  = 0.06  // share of cells under the patch that still give soft bits
	maxGlareFalse = 0.002 // share of cells away from the patch made unknown
)

// TestGlareUnknown: cells under a glare patch come back unknown (zero soft bits) and cells away from
// it are unaffected. The same recording is filmed with and without a specular patch over 5% of the
// code (fixed seeds, so noise and geometry are identical); a cell is under the patch where the patch
// raised its brightness by more than a quarter of a level spacing, and away from it where it raised
// it by less than one code value. Gray4 and Gray2 on `tripod` and `handheld`, 212 x 120 grid,
// 1600 x 900 camera, 4 captures each.
func TestGlareUnknown(t *testing.T) {
	mods := []uint8{2, 1}
	if radRace {
		mods = mods[:1]
	}
	for _, mod := range mods {
		s := newRadSession(t, 212, 120, 4, mod, 8, 0x61+uint64(mod), false)
		for pi, name := range []string{"tripod", "handheld"} {
			t.Run(fmt.Sprintf("%s/%s", s.mod.Name(), name), func(t *testing.T) {
				c := preset(t, name, 1600, 900)
				seed := 0x700 + uint64(10*pi) + uint64(mod)
				clean := s.shoot(t, c, time.Second/30, 4, seed)
				c.Optics.Glare = 0.05
				glare := s.shoot(t, c, time.Second/30, 4, seed)
				spacing := float64(219) / float64(s.mod.Levels()-1)
				var under, missed, away, falsely int
				b := s.mod.BitsPerCell()
				soft := make([]int8, len(s.layout.DataCells())*b)
				for k := range glare {
					obs := append([]modem.Obs(nil), glare[k].obs...)
					f := (&TileEqualiser{Mod: s.mod}).Fit(obs, glare[k].palette, s.layout, glare[k].anchors)
					SoftBits(soft, obs, s.layout, f, s.mod, nil, 0)
					for i := range obs {
						d := float64(glare[k].obs[i].Y - clean[k].obs[i].Y)
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
				miss, fa := float64(missed)/float64(max(under, 1)), float64(falsely)/float64(max(away, 1))
				t.Logf("%d cells under the patch, %d still stated (miss rate %.4f); %d away from it, %d made unknown (%.5f)", under, missed, miss, away, falsely, fa)
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
