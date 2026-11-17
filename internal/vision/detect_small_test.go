package vision

import "testing"

// TestDetectSmallGrids characterises the open issue of docs/DESIGN.md section 19 on small grids:
// the confirmation needs confirmMinAnchors located anchors, and a grid whose layout holds fewer
// (the anchor lattice is every 32 columns and 30 rows) is never located, even on a perfect capture
// where its header reads and every anchor is found. 53 x 30 (480p at 16-pixel cells) holds 2
// anchors and 106 x 60 (480p at 8, 720p at 12) holds 6; 120 x 90 holds 12 and is located. When a
// denser lattice for small grids (a FORMAT change) lands, the first two must be located too.
func TestDetectSmallGrids(t *testing.T) {
	for _, g := range []struct {
		gx, gy, pitch, anchors int
		located                bool
	}{{53, 30, 16, 2, false}, {106, 60, 8, 6, false}, {120, 90, 8, 12, true}} {
		c := geomNewCode(t, g.gx, g.gy, g.pitch, 2, 1, 1)
		f := geomPlain(c, 0)
		if n := len(c.layout.Anchors()); n != g.anchors {
			t.Errorf("%d x %d: %d anchors, want %d", g.gx, g.gy, n, g.anchors)
		}
		if hs, ok := (SlotReader{}).Read(f, geomPlainQuad(c)); !ok || hs[0] != c.headers[0] {
			t.Errorf("%d x %d: header not read on the true quad", g.gx, g.gy)
		}
		var d FinderDetector
		if _, ok := d.Detect(f, nil); ok != g.located {
			t.Errorf("%d x %d: located %v, want %v", g.gx, g.gy, ok, g.located)
		}
	}
}
