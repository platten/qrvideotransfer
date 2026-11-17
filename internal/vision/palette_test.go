package vision

import (
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/modem"
)

// TestSamplePalette: on a `tripod` capture of a Gray4 code located by the geometry pipeline, every
// palette strip reads its eight patches as levels k mod 4, so each run of four rises from black to
// white, and nothing is saturated. A nil mesh or a capture of the wrong size gives saturated
// observations.
func TestSamplePalette(t *testing.T) {
	s := newRadSession(t, 212, 120, 4, 2, 8, 0x9a1e, false)
	caps, ps := s.shootReal(t, preset(t, "tripod", 1600, 900), time.Second/30, 1, 0x9a1e)
	if len(caps) == 0 {
		t.Fatalf("no capture went through the pipeline (%+v)", ps)
	}
	rc := caps[0]
	for cp := range 4 {
		for k := range 8 {
			o := rc.palette[8*cp+k]
			if o.Saturated {
				t.Fatalf("strip %d patch %d saturated", cp, k)
			}
			if k%4 > 0 && o.Y <= rc.palette[8*cp+k-1].Y+20 {
				t.Errorf("strip %d: patch %d reads %.1f after %.1f, want a rise", cp, k, o.Y, rc.palette[8*cp+k-1].Y)
			}
		}
	}
	dst := make([]modem.Obs, 32)
	SamplePalette(dst, rc.f, nil, s.layout)
	for k, o := range dst {
		if !o.Saturated {
			t.Fatalf("nil mesh: patch %d not saturated", k)
		}
	}
}
