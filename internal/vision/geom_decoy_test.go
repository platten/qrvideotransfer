package vision

import (
	"testing"

	"github.com/platten/qrvideotransfer/internal/frame"
)

// TestDecoyMatrix is the decoy study of the PR #38 review, round 3: 816 cases on a 480 x 270 code
// at 4 pixels per cell, every finder visible. A copy of one finder, alone or with its separator, of
// either polarity, is painted 0 to 16 cells inward from it horizontally, vertically or along the
// diagonal, overlapping the real finder when near. It logs every case not located and the totals,
// and fails on a wrong quad (a centre more than a module from the truth). It runs with
// QRVT_LONG_TESTS=1.
func TestDecoyMatrix(t *testing.T) {
	raceSkipStatistics(t)
	if !geomLong() {
		t.Skip("set QRVT_LONG_TESTS=1")
	}
	c := geomNewCode(t, 480, 270, 4, 2, 1, 45)
	want := geomPlainQuad(c)
	plain := geomPlain(c, 0)
	u := 2 * c.pitch // one finder module: Uy is two cells on a 270-row grid
	cases, missed := 0, 0
	for _, finderOnly := range []bool{false, true} {
		for _, dir := range [][2]int{{1, 0}, {0, 1}, {1, 1}} {
			for off := 0; off <= 16; off++ {
				for _, corner := range []frame.Corner{frame.TopLeft, frame.TopRight, frame.BottomLeft, frame.BottomRight} {
					right := corner == frame.TopRight || corner == frame.BottomRight
					bottom := corner == frame.BottomLeft || corner == frame.BottomRight
					r := c.layout.Separator(corner).Pixels(c.pitch)
					if finderOnly { // drop the separator's inner side
						if right {
							r.Min.X += u
						} else {
							r.Max.X -= u
						}
						if bottom {
							r.Min.Y += u
						} else {
							r.Max.Y -= u
						}
					}
					dx, dy := dir[0]*off*c.pitch, dir[1]*off*c.pitch
					if right {
						dx = -dx
					}
					if bottom {
						dy = -dy
					}
					for _, inverted := range []bool{false, true} {
						cases++
						f := geomPlain(c, 0)
						for y := r.Min.Y; y < r.Max.Y; y++ {
							for x := r.Min.X; x < r.Max.X; x++ {
								v := plain.Y[y*plain.W+x]
								if inverted {
									v = 16 + 235 - v
								}
								f.Y[(y+dy)*f.W+x+dx] = v
							}
						}
						name := func() string {
							return frameCornerName(corner) + map[bool]string{false: ", with separator", true: ", finder only"}[finderOnly] +
								map[bool]string{false: ", normal", true: ", inverted"}[inverted]
						}
						var d FinderDetector
						q, ok := d.Detect(f, nil)
						if !ok {
							missed++
							t.Logf("not located: %s, offset %d cells along %v", name(), off, dir)
							continue
						}
						for j := range 4 {
							if geomDist(q.Centres[j], want.Centres[j]) > want.Module {
								t.Errorf("wrong quad: %s, offset %d cells along %v: centre %d at %v, want %v", name(), off, dir, j, q.Centres[j], want.Centres[j])
							}
						}
					}
				}
			}
		}
	}
	t.Logf("%d cases, %d not located", cases, missed)
}

// frameCornerName names a corner in test logs.
func frameCornerName(c frame.Corner) string {
	return [...]string{"top-left", "top-right", "bottom-left", "bottom-right"}[c]
}
