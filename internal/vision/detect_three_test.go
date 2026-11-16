package vision

import (
	"math"
	"testing"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/video"
)

// geomPaintDisc paints a disc of radius r pixels at (cx, cy) on f's brightness with value v.
func geomPaintDisc(f *video.Frame, cx, cy, r float64, v uint16) {
	for y := max(0, int(cy-r)); y <= min(f.H-1, int(cy+r)); y++ {
		for x := max(0, int(cx-r)); x <= min(f.W-1, int(cx+r)); x++ {
			if math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy) <= r {
				f.Y[y*f.W+x] = v
			}
		}
	}
}

// TestDetectThreeFindersPlain: with any one finder of a rendered frame covered by a white disc of
// 12 cells' radius, the quad is located from the other three: the three visible centres within a
// tenth of a module, the hidden one within a module, and the header reads.
func TestDetectThreeFindersPlain(t *testing.T) {
	c := geomNewCode(t, 320, 180, 8, 2, 1, 28)
	want := geomPlainQuad(c)
	for k := range 4 {
		f := geomPlain(c, 0)
		geomPaintDisc(f, want.Centres[k].X, want.Centres[k].Y, 12*float64(c.pitch), 235)
		var d FinderDetector
		q, ok := d.Detect(f, nil)
		if !ok {
			t.Errorf("finder %d hidden: not located", k)
			continue
		}
		for j := range 4 {
			tol := want.Module / 10
			if j == k {
				tol = want.Module
			}
			if e := geomDist(q.Centres[j], want.Centres[j]); e > tol {
				t.Errorf("finder %d hidden: centre %d at %v, want %v", k, j, q.Centres[j], want.Centres[j])
			}
		}
		if hs, ok := (SlotReader{}).Read(f, q); !ok || hs[0] != c.headers[0] {
			t.Errorf("finder %d hidden: header %v %v", k, hs, ok)
		}
	}
}

// TestDetectTwoHidden: with any two finders covered, nothing is located.
func TestDetectTwoHidden(t *testing.T) {
	c := geomNewCode(t, 320, 180, 8, 2, 1, 29)
	want := geomPlainQuad(c)
	pairs := [][2]int{{0, 3}, {1, 2}, {0, 1}, {0, 2}, {1, 3}, {2, 3}}
	for _, p := range pairs[:raceSize(len(pairs), 2)] {
		f := geomPlain(c, 0)
		for _, k := range p {
			geomPaintDisc(f, want.Centres[k].X, want.Centres[k].Y, 12*float64(c.pitch), 235)
		}
		var d FinderDetector
		if q, ok := d.Detect(f, nil); ok {
			t.Errorf("finders %d and %d hidden: located %v", p[0], p[1], q.Centres)
		}
	}
}

// TestDetectThreeHostile: a 1920 x 1080 frame of random noise with three finder patterns drawn at
// plausible places (two normal, one inverted), and no header or anchors, is never located; each
// Detect allocates at most 32 MB and takes at most 16 times as long as locating a real code of the
// same size in the same process. The ratio is high by nature: a real 1920 x 1080 code is located on
// the first scan, at half resolution, while here the full-resolution rescan follows (four times the
// pixels), and noise yields many finder-like hits. Measured 11 times; the three-finder search is a
// quarter of it.
func TestDetectThreeHostile(t *testing.T) {
	raceSkipStatistics(t) // a cost ratio: race instrumentation distorts it unevenly
	const w, h = 1920, 1080
	f := &video.Frame{W: w, H: h, Bits: 8, Y: make([]uint16, w*h)}
	s := uint64(11)
	for i := range f.Y {
		s = s*6364136223846793005 + 1442695040888963407
		f.Y[i] = uint16(16 + (s>>33)%220)
	}
	const m = 8 // pixels per module
	draw := func(cx, cy int, c frame.Corner) {
		for my := -1; my < 8; my++ {
			for mx := -1; mx < 8; mx++ {
				dark := frame.SeparatorDark(c)
				if mx >= 0 && my >= 0 && mx < 7 && my < 7 {
					dark = frame.FinderModuleDark(c, mx, my)
				}
				v := uint16(235)
				if dark {
					v = 16
				}
				for y := range m {
					for x := range m {
						f.Y[(cy+(my-3)*m-m/2+y)*w+cx+(mx-3)*m-m/2+x] = v
					}
				}
			}
		}
	}
	draw(200, 150, frame.TopLeft)
	draw(1700, 150, frame.TopRight)
	draw(1700, 930, frame.BottomRight)
	c := geomNewCode(t, 240, 135, 8, 2, 1, 39) // a real code of the same size, for the reference
	geomCostRatio(t, "three finder patterns on noise", geomPlain(c, 0), f, [][]uint16{f.Y}, 16, 32<<20)
}

// TestDetectThreeHostile4K: the costly three-finder path at 4K. A real 480 x 270 code at 8 pixels
// per cell, its bottom-right finder covered, whose four header copies claim 2047 x 1393 (inside the
// 1.5-pixel gate, so a layout is built and the anchors are searched), alternating with 2046 x 1393,
// is never located; each Detect allocates at most 32 MB and takes at most 6 times as long as
// locating the real code in the same process.
func TestDetectThreeHostile4K(t *testing.T) {
	raceSkipStatistics(t) // a cost ratio: race instrumentation distorts it unevenly
	c := geomNewCode(t, 480, 270, 8, 2, 1, 40)
	q := geomPlainQuad(c)
	var planes [][]uint16
	for _, gx := range []int{2047, 2046} {
		g := geomPlain(c, 0)
		g.Y = geomFakeCopies(t, c, gx, 1393, 0, 1, 2, 3)
		geomPaintDisc(g, q.Centres[3].X, q.Centres[3].Y, 12*float64(c.pitch), 235)
		planes = append(planes, g.Y)
	}
	geomCostRatio(t, "4K three-finder hostile header", geomPlain(c, 0), geomPlain(c, 0), planes, 6, 32<<20)
}

// TestThreeFindersConflictingCopy: with finder k covered and header copy j repainted to claim a
// 300 x 180 grid, for every k and j, the code is still located from three finders with its true
// grid: the conflicting grid takes one layout of the two allowed, which are cached by grid.
func TestThreeFindersConflictingCopy(t *testing.T) {
	c := geomNewCode(t, 320, 180, 8, 2, 1, 41)
	want := geomPlainQuad(c)
	n := 0
	for step := range 4 {
		for k := range 4 {
			if n == raceSize(16, 2) {
				return
			}
			n++
			j := (k + step + 1) % 4 // every (k, j) pair once, with distinct k and j in the first cases
			f := geomPlain(c, 0)
			f.Y = geomFakeCopies(t, c, 300, 180, j)
			geomPaintDisc(f, want.Centres[k].X, want.Centres[k].Y, 12*float64(c.pitch), 235)
			var d FinderDetector
			q, ok := d.Detect(f, nil)
			if !ok {
				t.Errorf("finder %d covered, copy %d conflicting: not located", k, j)
				continue
			}
			for i := range 4 {
				if e := geomDist(q.Centres[i], want.Centres[i]); e > want.Module {
					t.Errorf("finder %d covered, copy %d conflicting: centre %d at %v, want %v", k, j, i, q.Centres[i], want.Centres[i])
				}
			}
		}
	}
}

// TestGlareOnFinder: on 4 handheld 4K poses (480 x 270 grid, seeds 21000 to 21003), a glare patch
// of 2% of the code area centred on each finder in turn: every pose located with its header read,
// no wrong quad or header, and a cell-centre error outside the patch of at most 0.15 cell RMS.
// Under -race, one 720p pose of a 212 x 120 grid (geomRaceGlareCode); 6 poses in the long version.
func TestGlareOnFinder(t *testing.T) {
	t.Parallel() // no time bound; it runs with the other parallel statistics
	n := raceSize(4, 1)
	if geomLong() {
		n = 6
	}
	c, cfg := geomRaceGlareCode(t, 4, 30)
	cases := geomGlareCases(c)[:4]
	poses := geomPoses(t, c, cfg, n, 21000)
	for k, gc := range cases {
		var s geomGlareStats
		for i, p := range poses {
			discs := gc.discs(c, i)
			geomGlareMeasure(c, geomGlare(p.f, p.tr, discs), p.tr, discs, &s)
		}
		rms := math.Sqrt(s.sumSq / float64(max(1, s.cells)))
		t.Logf("%s: found %d of %d, wrong %d, header %d, wrong %d, RMS outside %.4f", gc.name, s.found, s.poses, s.wrong, s.header, s.wrongHeader, rms)
		if s.found != n || s.header != n || s.wrong != 0 || s.wrongHeader != 0 || !(rms <= 0.15) ||
			float64(s.imprecise) > geomMaxImprecise*float64(n) {
			t.Errorf("finder %d under glare: found %d of %d, header %d, wrong %d and %d, RMS %.4f", k, s.found, n, s.header, s.wrong, s.wrongHeader, rms)
		}
	}
}

// TestGeomJudge: the line between exact, imprecise and wrong quads. A visible finder more than half
// a module off makes the quad wrong; only the hidden finder may be up to one module off, and only
// when the header reads, the mesh fits and the centre error is at most 0.15 cell.
func TestGeomJudge(t *testing.T) {
	c := geomNewCode(t, 212, 120, 4, 2, 1, 33)
	p, err := geomPose(c, geomConfig(t, "ideal", 1280, 720), 1, 0, 24000)
	if err != nil {
		t.Fatal(err)
	}
	want, mod := geomFinderTruth(c, p.tr)
	moved := func(k int, by float64) Quad {
		var q Quad
		for j := range 4 {
			q.Centres[j] = want[j]
		}
		q.Centres[k].X += by * mod[k]
		return q
	}
	for _, tc := range []struct {
		name         string
		q            Quad
		hidden       int
		header, mesh bool
		rms          float64
		want         geomVerdict
	}{
		{"exact", moved(0, 0.4), -1, true, true, 0.05, geomExact},
		{"visible 0.6 off", moved(1, 0.6), -1, true, true, 0.05, geomWrong},
		{"visible 0.6 off, another hidden", moved(1, 0.6), 2, true, true, 0.05, geomWrong},
		{"hidden 0.4 off", moved(2, 0.4), 2, true, true, 0.05, geomExact},
		{"hidden 0.6 off", moved(2, 0.6), 2, true, true, 0.05, geomImprecise},
		{"hidden 0.6 off, no header", moved(2, 0.6), 2, false, true, 0.05, geomWrong},
		{"hidden 0.6 off, no mesh", moved(2, 0.6), 2, true, false, 0.05, geomWrong},
		{"hidden 0.6 off, error 0.2", moved(2, 0.6), 2, true, true, 0.2, geomWrong},
		{"hidden 1.1 off", moved(3, 1.1), 3, true, true, 0.05, geomWrong},
	} {
		if got := geomJudge(c, p.tr, tc.q, tc.hidden, tc.header, tc.mesh, tc.rms); got != tc.want {
			t.Errorf("%s: verdict %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestDetectHidden: the detector reports which finder it predicted: none with four in view, the
// covered one with three.
func TestDetectHidden(t *testing.T) {
	c := geomNewCode(t, 320, 180, 8, 2, 1, 34)
	want := geomPlainQuad(c)
	var d FinderDetector
	if _, ok := d.Detect(geomPlain(c, 0), nil); !ok || d.hidden != -1 {
		t.Errorf("four finders: located %v, hidden %d", ok, d.hidden)
	}
	f := geomPlain(c, 0)
	geomPaintDisc(f, want.Centres[2].X, want.Centres[2].Y, 12*float64(c.pitch), 235)
	if _, ok := d.Detect(f, nil); !ok || d.hidden != 2 {
		t.Errorf("finder 2 covered: located %v, hidden %d", ok, d.hidden)
	}
}
