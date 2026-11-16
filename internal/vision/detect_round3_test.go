package vision

import (
	"testing"

	"github.com/platten/qrvideotransfer/internal/frame"
)

// geomFakeCopies returns a rendered frame of c whose listed header copies are repainted to claim a
// gridX x gridY grid, the rest of the header unchanged.
func geomFakeCopies(t *testing.T, c *geomCode, gridX, gridY int, copies ...int) []uint16 {
	t.Helper()
	f := geomPlain(c, 0)
	h := c.headers[0]
	h.GridX, h.GridY = uint16(gridX), uint16(gridY)
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
	bits := h.Encode()
	for _, cp := range copies {
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

// TestConfirmsTolerance: on a 240 x 135 grid, whose finder module is one cell, a visible finder
// 0.6 module off is rejected and one 0.3 module off is confirmed: the tolerance is the smaller of
// one cell and half a module.
func TestConfirmsTolerance(t *testing.T) {
	c := geomNewCode(t, 240, 135, 8, 2, 1, 35)
	f := geomPlain(c, 0)
	v, _ := geomView(f)
	for _, tc := range []struct {
		by float64
		ok bool
	}{{0.3, true}, {0.6, false}} {
		q := geomPlainQuad(c)
		q.Centres[frame.TopRight].X -= tc.by * q.Module
		var d FinderDetector
		if got := d.confirms(f, &v, q); got != tc.ok {
			t.Errorf("finder %.1f module off: confirmed %v, want %v", tc.by, got, tc.ok)
		}
	}
}

// TestFitFinderOff: with a finder 0.8 cell off on a 240 x 135 grid, Fit no longer pulls the mesh
// corner after it: once eight or more anchors are located the finders leave the refit, and the
// mesh error stays within 0.15 cell (it was up to 0.43).
func TestFitFinderOff(t *testing.T) {
	c := geomNewCode(t, 240, 135, 8, 2, 1, 36)
	f := geomPlain(c, 0)
	q := geomPlainQuad(c)
	q.Centres[frame.TopLeft].X += 0.8 * q.Module
	q.Centres[frame.TopLeft].Y += 0.3 * q.Module
	m, _, err := MeshFitter{}.Fit(f, q, c.layout)
	if err != nil {
		t.Fatal(err)
	}
	if e := geomMeshError(c, m); e > 0.15 {
		t.Errorf("mesh error %.3f cell with a finder 0.8 cell off, want at most 0.15", e)
	}
}

// TestDetectHostileGrid4K: the worst case inside the 1.5-pixel gate at 4K. A real 480 x 270 code at
// 8 pixels per cell whose four header copies claim 2047 x 1393, alternating with 2046 x 1393, so
// that a layout is built and the anchors are searched on every call. It is never located; each
// Detect allocates at most 32 MB and takes at most 6 times as long as locating the real code in the
// same process.
func TestDetectHostileGrid4K(t *testing.T) {
	raceSkipStatistics(t) // a cost ratio: race instrumentation distorts it unevenly
	c := geomNewCode(t, 480, 270, 8, 2, 1, 37)
	a := geomFakeCopies(t, c, 2047, 1393, 0, 1, 2, 3)
	b := geomFakeCopies(t, c, 2046, 1393, 0, 1, 2, 3)
	geomCostRatio(t, "4K header at the gate", geomPlain(c, 0), geomPlain(c, 0), [][]uint16{a, b}, 6, 32<<20)
}

// TestHeaderConflictingGrid: when copies 0 and 1 decode a header with the same frame index but
// another grid, Read returns both headers, and Detect still locates the code with its true grid
// from copies 2 and 3.
func TestHeaderConflictingGrid(t *testing.T) {
	c := geomNewCode(t, 240, 135, 8, 2, 1, 38)
	f := geomPlain(c, 0)
	f.Y = geomFakeCopies(t, c, 300, 135, 0, 1)
	hs, ok := SlotReader{}.Read(f, geomPlainQuad(c))
	if !ok || len(hs) != 2 {
		t.Fatalf("got %v %v, want the true header and the conflicting one", hs, ok)
	}
	var d FinderDetector
	q, ok := d.Detect(f, nil)
	if !ok {
		t.Fatal("not located")
	}
	want := geomPlainQuad(c)
	for k := range 4 {
		if geomDist(q.Centres[k], want.Centres[k]) > want.Module/2 {
			t.Errorf("centre %d at %v, want %v", k, q.Centres[k], want.Centres[k])
		}
	}
}
