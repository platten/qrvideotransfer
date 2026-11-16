package vision

import (
	"testing"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/video"
)

// TestThreeFindersBesideGateFailure: a 2400 x 900 frame holding a real 320 x 180 code (4 pixels
// per cell) with one finder covered, and beside it a small 212 x 120 code whose header claims
// 2047 x 1393, which fails the 1.5-pixel gate. A gate failure counts against the quad that met it
// only, so the real code is still located from three finders, for each covered corner.
func TestThreeFindersBesideGateFailure(t *testing.T) {
	real := geomNewCode(t, 320, 180, 4, 2, 1, 42)
	small := geomNewCode(t, 212, 120, 4, 2, 1, 43)
	smallY := geomFakeCopies(t, small, 2047, 1393, 0, 1, 2, 3)
	const w, h = 2400, 900
	const rx, ry, sx, sy = 0, 90, 1500, 200
	want := geomPlainQuad(real)
	for k := range raceSize(4, 2) {
		f := &video.Frame{W: w, H: h, Bits: 8, Y: make([]uint16, w*h)}
		for i := range f.Y {
			f.Y[i] = 126
		}
		r := geomPlain(real, 0)
		geomPaintDisc(r, want.Centres[k].X, want.Centres[k].Y, 12*float64(real.pitch), 235)
		for y := range r.H {
			copy(f.Y[(ry+y)*w+rx:], r.Y[y*r.W:(y+1)*r.W])
		}
		sw := small.input.Frames[0].W
		for y := range small.input.Frames[0].H {
			copy(f.Y[(sy+y)*w+sx:], smallY[y*sw:(y+1)*sw])
		}
		var d FinderDetector
		q, ok := d.Detect(f, nil)
		if !ok {
			t.Errorf("finder %d covered: not located", k)
			continue
		}
		for j := range 4 {
			c := Point{want.Centres[j].X + rx, want.Centres[j].Y + ry}
			if e := geomDist(q.Centres[j], c); e > want.Module {
				t.Errorf("finder %d covered: centre %d at %v, want %v", k, j, q.Centres[j], c)
			}
		}
	}
}

// TestThreeFindersBesideDecoy: a 480 x 270 code at 4 pixels per cell whose top-right finder is
// covered, with a copy of that finder (normal polarity) painted 1, 1.5, 2 and 3 cells to its left:
// the code is located, every centre within a module of the truth (a decoy within half a module may
// stand in for the finder).
func TestThreeFindersBesideDecoy(t *testing.T) {
	c := geomNewCode(t, 480, 270, 4, 2, 1, 44)
	want := geomPlainQuad(c)
	plain := geomPlain(c, 0)
	sep := c.layout.Separator(frame.TopRight).Pixels(c.pitch)
	offs := []float64{1, 1.5, 2, 3}
	for _, off := range offs[:raceSize(len(offs), 2)] {
		f := geomPlain(c, 0)
		geomPaintDisc(f, want.Centres[1].X, want.Centres[1].Y, 12*float64(c.pitch), 235)
		dx := int(off * float64(c.pitch))
		for y := sep.Min.Y; y < sep.Max.Y; y++ {
			for x := sep.Min.X; x < sep.Max.X; x++ {
				f.Y[y*f.W+x-dx] = plain.Y[y*plain.W+x]
			}
		}
		var d FinderDetector
		q, ok := d.Detect(f, nil)
		if !ok {
			t.Errorf("decoy %.1f cells off: not located", off)
			continue
		}
		for j := range 4 {
			if e := geomDist(q.Centres[j], want.Centres[j]); e > want.Module {
				t.Errorf("decoy %.1f cells off: centre %d at %v, want %v", off, j, q.Centres[j], want.Centres[j])
			}
		}
	}
}

// TestThreeFindersOverlappingDecoy: a 480 x 270 code at 4 pixels per cell with every finder
// visible, and a copy of one finder with its separator (16 cells square), of either polarity,
// painted 4 or 5 cells inward along the diagonal, so that it overlaps the real finder's rings. The
// real finder is damaged, not hidden, and disagrees with the anchors; it is not excluded from the
// three-finder path (only the finders of a quad whose header failed the 1.5-pixel gate are), so
// for each corner, offset and polarity the code is located, every centre within a module of the
// truth.
func TestThreeFindersOverlappingDecoy(t *testing.T) {
	c := geomNewCode(t, 480, 270, 4, 2, 1, 45)
	want := geomPlainQuad(c)
	plain := geomPlain(c, 0)
	corners := []frame.Corner{frame.TopLeft, frame.BottomRight, frame.TopRight, frame.BottomLeft}
	for _, corner := range corners[:raceSize(len(corners), 2)] {
		sep := c.layout.Separator(corner).Pixels(c.pitch)
		for _, off := range []int{4, 5}[:raceSize(2, 1)] {
			dx, dy := off*c.pitch, off*c.pitch
			if corner == frame.TopRight || corner == frame.BottomRight {
				dx = -dx
			}
			if corner == frame.BottomLeft || corner == frame.BottomRight {
				dy = -dy
			}
			for _, inverted := range []bool{false, true} {
				f := geomPlain(c, 0)
				for y := sep.Min.Y; y < sep.Max.Y; y++ {
					for x := sep.Min.X; x < sep.Max.X; x++ {
						v := plain.Y[y*plain.W+x]
						if inverted {
							v = 16 + 235 - v
						}
						f.Y[(y+dy)*f.W+x+dx] = v
					}
				}
				var d FinderDetector
				q, ok := d.Detect(f, nil)
				if !ok {
					t.Errorf("corner %d, %d cells, inverted %v: not located", corner, off, inverted)
					continue
				}
				for j := range 4 {
					if e := geomDist(q.Centres[j], want.Centres[j]); e > want.Module {
						t.Errorf("corner %d, %d cells, inverted %v: centre %d at %v, want %v", corner, off, inverted, j, q.Centres[j], want.Centres[j])
					}
				}
			}
		}
	}
}

// TestThreeFindersDecoyRightOfTopLeft: the weak spot of the round-4 decoy study. A copy of the
// top-left finder with its separator, of normal polarity, is painted 1 to 6 cells to its right, on
// a 480 x 270 code at 4 pixels per cell whose top-left finder is visible or covered. Quads built
// on the decoy must not crowd out the true candidate: the code is located in every case, each
// visible centre within half a module of the truth and a covered one within a module.
func TestThreeFindersDecoyRightOfTopLeft(t *testing.T) {
	c := geomNewCode(t, 480, 270, 4, 2, 1, 45)
	want := geomPlainQuad(c)
	plain := geomPlain(c, 0)
	sep := c.layout.Separator(frame.TopLeft).Pixels(c.pitch)
	offs := []int{1, 4, 2, 3, 5, 6}
	for _, covered := range []bool{false, true} {
		for _, off := range offs[:raceSize(len(offs), 2)] {
			f := geomPlain(c, 0)
			if covered {
				geomPaintDisc(f, want.Centres[0].X, want.Centres[0].Y, 12*float64(c.pitch), 235)
			}
			dx := off * c.pitch
			for y := sep.Min.Y; y < sep.Max.Y; y++ {
				for x := sep.Min.X; x < sep.Max.X; x++ {
					f.Y[y*f.W+x+dx] = plain.Y[y*plain.W+x]
				}
			}
			var d FinderDetector
			q, ok := d.Detect(f, nil)
			if !ok {
				t.Errorf("covered %v, decoy %d cells right: not located", covered, off)
				continue
			}
			for j := range 4 {
				tol := want.Module / 2
				if covered && j == 0 {
					tol = want.Module
				}
				if e := geomDist(q.Centres[j], want.Centres[j]); e > tol {
					t.Errorf("covered %v, decoy %d cells right: centre %d at %v, want %v", covered, off, j, q.Centres[j], want.Centres[j])
				}
			}
		}
	}
}
