package vision

import (
	"testing"

	"github.com/platten/qrvideotransfer/internal/frame"
)

// TestConfirmsDecoy: a decoy quad whose top-right finder is 20 cells off still decodes a header
// copy, but confirms rejects it; the true quad is confirmed. Detect on a frame whose real top-right
// finder is hidden and a copy of it painted 20 cells to the left does not return the decoy.
func TestConfirmsDecoy(t *testing.T) {
	c := geomNewCode(t, 320, 180, 8, 2, 1, 25)
	f := geomPlain(c, 0)
	v, _ := geomView(f)
	q := geomPlainQuad(c)
	var d FinderDetector
	if !d.confirms(f, &v, q) {
		t.Fatal("true quad not confirmed")
	}
	decoy := q
	decoy.Centres[frame.TopRight].X -= 20 * float64(c.pitch)
	if _, ok := (SlotReader{}).Read(f, decoy); !ok {
		t.Fatal("the decoy decodes no header; the test needs one copy to decode")
	}
	if d.confirms(f, &v, decoy) {
		t.Error("decoy quad with one finder 20 cells off confirmed")
	}

	// The same decoy in an image: the real finder greyed, a copy of it 20 cells to the left.
	g := geomPlain(c, 0)
	sep := c.layout.Separator(frame.TopRight).Pixels(c.pitch)
	dx := 20 * c.pitch
	for y := sep.Min.Y; y < sep.Max.Y; y++ {
		for x := sep.Min.X; x < sep.Max.X; x++ {
			g.Y[y*g.W+x-dx] = f.Y[y*f.W+x]
		}
	}
	for y := sep.Min.Y; y < sep.Max.Y; y++ {
		for x := sep.Max.X - dx; x < sep.Max.X; x++ {
			g.Y[y*g.W+x] = 126
		}
	}
	if got, ok := d.Detect(g, nil); ok && geomDist(got.Centres[frame.TopRight], q.Centres[frame.TopRight]) > q.Module/2 {
		t.Errorf("Detect returned the decoy: %v", got.Centres)
	}
}

// TestConfirmsOneLayout: within one Detect at most confirmMaxLayouts distinct grids are used; one
// more is refused, a grid already used is not, and layouts are cached by grid.
func TestConfirmsOneLayout(t *testing.T) {
	c := geomNewCode(t, 212, 120, 4, 2, 1, 27)
	f := geomPlain(c, 0)
	v, _ := geomView(f)
	d := FinderDetector{grids: [][2]int{{240, 135}, {300, 180}}}
	if d.confirms(f, &v, geomPlainQuad(c)) {
		t.Error("a grid beyond the cap was used in one Detect")
	}
	d.grids = [][2]int{{240, 135}, {212, 120}}
	if !d.confirms(f, &v, geomPlainQuad(c)) {
		t.Error("true quad not confirmed with its grid among those used")
	}
	cached := d.layouts[len(d.layouts)-1]
	d.grids = nil
	if !d.confirms(f, &v, geomPlainQuad(c)) || d.layouts[len(d.layouts)-1] != cached {
		t.Error("the layout was not reused from the cache")
	}
}

// TestConfirmsHarshPose: pose 2 of 200 from seed 9000 on the harsh preset at 4K (480 x 270 grid,
// code seed 19), whose header decodes and whose mesh fits but where both anchors next to one
// finder are unreadable, is confirmed and located.
func TestConfirmsHarshPose(t *testing.T) {
	raceSkipStatistics(t) // a pose of the harsh count, at 4K
	c := geomNewCode(t, 480, 270, 4, 2, 4, 19)
	p, err := geomPose(c, geomConfig(t, "harsh", 0, 0), 200, 2, 9000)
	if err != nil {
		t.Fatal(err)
	}
	want, mod := geomFinderTruth(c, p.tr)
	q := Quad{Module: mod[0]}
	for k := range 4 {
		q.Centres[k] = want[k]
	}
	v, _ := geomView(p.f)
	var d FinderDetector
	if !d.confirms(p.f, &v, q) {
		t.Error("true quad not confirmed")
	}
	got, ok := d.Detect(p.f, nil)
	if !ok {
		t.Fatal("not located")
	}
	for k := range 4 {
		if geomDist(got.Centres[k], want[k]) > mod[k]/2 {
			t.Errorf("centre %d at %v, want %v", k, got.Centres[k], want[k])
		}
	}
}

// TestGeometryHarshRegression: the harsh preset at 4K, 480 x 270 grid, 200 poses from seed 9000:
// at least 196 located with their header read (17 were lost to an anchor check that required the
// two anchors next to every finder), none wrong. It runs with QRVT_LONG_TESTS=1.
func TestGeometryHarshRegression(t *testing.T) {
	raceSkipStatistics(t)
	if !geomLong() {
		t.Skip("set QRVT_LONG_TESTS=1")
	}
	c := geomNewCode(t, 480, 270, 4, 2, 4, 19)
	s := geomSweep(t, c, geomConfig(t, "harsh", 0, 0), 200, 9000)
	if s.found < 196 || s.header < 196 || s.wrong != 0 || s.wrongHeader != 0 {
		t.Errorf("harsh: found %d, headers %d, wrong %d and %d; want at least 196, 196 and none wrong", s.found, s.header, s.wrong, s.wrongHeader)
	}
}

// geomFakeGrid returns a rendered 212 x 120 frame whose four header copies are repainted to claim
// a gridX x gridY grid.
func geomFakeGrid(t *testing.T, c *geomCode, gridX, gridY int) []uint16 {
	t.Helper()
	f := geomPlain(c, 0)
	h := c.headers[0]
	h.GridX, h.GridY = uint16(gridX), uint16(gridY)
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
	bits := h.Encode()
	for cp := range 4 {
		for s := range frame.HeaderSlots {
			v := uint16(16)
			if bits[s] {
				v = 235
			}
			geomPaintSlot(c, f, cp, s, v)
		}
	}
	return f.Y
}

// TestDetectHostileGrid: a frame whose header claims a 2047 x 2047 grid, alternating with 2047 x
// 2046 from call to call, is never located; each Detect allocates at most 16 MB for an 848 x 480
// frame (a 2047 x 2047 layout alone allocates about 22 MB) and takes at most twice as long as
// locating the real code in the same process. The grid fails the 1.5-pixel gate, so the
// three-finder path is not tried either.
func TestDetectHostileGrid(t *testing.T) {
	c := geomNewCode(t, 212, 120, 4, 2, 1, 26)
	a, b := geomFakeGrid(t, c, 2047, 2047), geomFakeGrid(t, c, 2047, 2046)
	geomCostRatio(t, "2047-cell header", geomPlain(c, 0), geomPlain(c, 0), [][]uint16{a, b}, 2, 16<<20)
}
