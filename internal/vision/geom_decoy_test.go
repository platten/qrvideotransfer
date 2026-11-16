package vision

import (
	"image"
	"strconv"
	"testing"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/video"
)

// The decoy studies of the PR #38 review. Both paint a copy of one finder of a 480 x 270 code at
// 4 pixels per cell near the real one, log every case not located and the totals, and fail on a
// wrong quad by the designer's line: a visible finder more than half a module from the truth, or a
// covered one more than a module. They run with QRVT_LONG_TESTS=1.

// geomDecoyCorners lists the corners in the studies' order.
var geomDecoyCorners = []frame.Corner{frame.TopLeft, frame.TopRight, frame.BottomLeft, frame.BottomRight}

// geomDecoyName names a corner in the studies' logs.
func geomDecoyName(c frame.Corner) string {
	return [...]string{"top-left", "top-right", "bottom-left", "bottom-right"}[c]
}

// geomDecoyStudy runs the cases of a decoy study and counts them.
type geomDecoyStudy struct {
	t             *testing.T
	c             *geomCode
	plain         *video.Frame
	want          Quad
	cases, missed int
}

func newGeomDecoyStudy(t *testing.T) *geomDecoyStudy {
	t.Helper()
	raceSkipStatistics(t)
	if !geomLong() {
		t.Skip("set QRVT_LONG_TESTS=1")
	}
	c := geomNewCode(t, 480, 270, 4, 2, 1, 45)
	return &geomDecoyStudy{t: t, c: c, plain: geomPlain(c, 0), want: geomPlainQuad(c)}
}

// run paints rectangle r of the rendered frame, inverted or not, moved by (dx, dy) pixels and
// clipped to the frame, over a copy of the frame whose finder at corner k is covered by a white
// disc of 12 cells' radius when covered, and judges what Detect finds.
func (s *geomDecoyStudy) run(name string, k int, covered bool, r image.Rectangle, dx, dy int, inverted bool) {
	s.t.Helper()
	s.cases++
	f := geomPlain(s.c, 0)
	if covered {
		geomPaintDisc(f, s.want.Centres[k].X, s.want.Centres[k].Y, 12*float64(s.c.pitch), 235)
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			tx, ty := x+dx, y+dy
			if tx < 0 || ty < 0 || tx >= f.W || ty >= f.H {
				continue
			}
			v := s.plain.Y[y*s.plain.W+x]
			if inverted {
				v = 16 + 235 - v
			}
			f.Y[ty*f.W+tx] = v
		}
	}
	var d FinderDetector
	q, ok := d.Detect(f, nil)
	if !ok {
		s.missed++
		s.t.Logf("not located: %s", name)
		return
	}
	for j := range 4 {
		tol := s.want.Module / 2
		if covered && j == k {
			tol = s.want.Module
		}
		if e := geomDist(q.Centres[j], s.want.Centres[j]); e > tol {
			s.t.Errorf("wrong quad: %s: centre %d at %v, want %v", name, j, q.Centres[j], s.want.Centres[j])
		}
	}
}

// TestDecoyMatrix is the author's study, round 3: 816 cases, every finder visible. A copy of one
// finder, alone or with its separator, of either polarity, is painted 0 to 16 cells inward from it
// horizontally, vertically or along the diagonal, overlapping the real finder when near.
func TestDecoyMatrix(t *testing.T) {
	s := newGeomDecoyStudy(t)
	u := 2 * s.c.pitch // one finder module: Uy is two cells on a 270-row grid
	for _, finderOnly := range []bool{false, true} {
		for _, dir := range [][2]int{{1, 0}, {0, 1}, {1, 1}} {
			for off := 0; off <= 16; off++ {
				for k, corner := range geomDecoyCorners {
					right := corner == frame.TopRight || corner == frame.BottomRight
					bottom := corner == frame.BottomLeft || corner == frame.BottomRight
					r := s.c.layout.Separator(corner).Pixels(s.c.pitch)
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
					dx, dy := dir[0]*off*s.c.pitch, dir[1]*off*s.c.pitch
					if right {
						dx = -dx
					}
					if bottom {
						dy = -dy
					}
					for _, inverted := range []bool{false, true} {
						name := geomDecoyName(corner) + map[bool]string{false: ", with separator", true: ", finder only"}[finderOnly] +
							map[bool]string{false: ", normal", true: ", inverted"}[inverted]
						s.run(name+", offset "+strconv.Itoa(off)+" cells inward along "+map[[2]int]string{{1, 0}: "x", {0, 1}: "y", {1, 1}: "the diagonal"}[dir],
							k, false, r, dx, dy, inverted)
					}
				}
			}
		}
	}
	t.Logf("%d cases, %d not located", s.cases, s.missed)
}

// TestDecoyMatrixReview is the reviewer's study, round 4: 512 cases. For each corner, a copy of
// its finder with its separator, of either polarity, is painted 1 to 8 cells off to the left,
// right, up or down, with the real finder covered by a white disc of 12 cells' radius or visible
// (overlapped by the decoy at small offsets).
func TestDecoyMatrixReview(t *testing.T) {
	s := newGeomDecoyStudy(t)
	dirs := []struct {
		name   string
		dx, dy int
	}{{"left", -1, 0}, {"right", 1, 0}, {"up", 0, -1}, {"down", 0, 1}}
	for k, corner := range geomDecoyCorners {
		r := s.c.layout.Separator(corner).Pixels(s.c.pitch)
		for _, covered := range []bool{false, true} {
			for _, inverted := range []bool{false, true} {
				for _, dir := range dirs {
					for off := 1; off <= 8; off++ {
						name := geomDecoyName(corner) + map[bool]string{false: ", visible", true: ", covered"}[covered] +
							map[bool]string{false: ", normal", true: ", inverted"}[inverted] + ", " + strconv.Itoa(off) + " cells " + dir.name
						s.run(name, k, covered, r, dir.dx*off*s.c.pitch, dir.dy*off*s.c.pitch, inverted)
					}
				}
			}
		}
	}
	t.Logf("%d cases, %d not located", s.cases, s.missed)
}
