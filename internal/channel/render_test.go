package channel

import (
	"math"
	"testing"
	"time"
)

// renderShot renders one shot of a recording in linear light.
func renderShot(rc *recording, sh shot) *rgb {
	occs := rc.tl.occurrences(rc.span(&sh))
	panels := make([]*panel, len(occs))
	for k, o := range occs {
		panels[k] = display(rc.frames[o.j], rc.scr)
	}
	return rc.render(&sh, occs, panels)
}

// A mixed capture: black until 50 ms, then white, with a 16 ms scan-out, seen by a 4 ms exposure
// from 52 ms. Capture row 16 sees display row 8.25 (row fraction 0.2578125), which turned white at
// 54.125 ms, so its white weight is (56 - 54.125) / 4 = 0.46875, in the truth and in the pixels.
func TestMixedCapture(t *testing.T) {
	c := still(128, 64)
	c.Timing.Exposure = 4 * time.Millisecond
	c.Display.ScanOut = 16 * time.Millisecond
	in := loop(50*time.Millisecond, 4, flat(64, 32, 16), flat(64, 32, 235))
	rc := mustRecording(t, in, c, Run{Captures: 1})
	sh := shot{start: 0.052}
	tr := &Truth{rc: rc, sh: sh, GridX: 16, GridY: 8}
	m := tr.Mix(20, 8.25)
	if len(m) != 2 || m[0].Frame != 0 || m[1].Frame != 1 || !near(m[1].W, 0.46875, 1e-12) || !near(m[0].W+m[1].W, 1, 1e-12) {
		t.Fatalf("Mix = %v", m)
	}
	img := renderShot(rc, sh)
	if got := img.p[0][16*128+40]; math.Abs(float64(got)-0.46875) > 1e-6 {
		t.Errorf("pixel = %v, want 0.46875", got)
	}
	// Above row fraction 0.125 the new frame has won outright, below 0.375 not at all.
	if m := tr.Mix(20, 2); len(m) != 1 || m[0].Frame != 1 || !near(m[0].W, 1, 1e-12) {
		t.Errorf("top Mix = %v", m)
	}
	if m := tr.CellMix(5, 6); len(m) != 1 || m[0].Frame != 0 {
		t.Errorf("bottom CellMix = %v", m)
	}
	if f := CleanFraction(tr, CleanThreshold); f <= 0.5 || f >= 1 {
		t.Errorf("clean fraction %v", f)
	}
}

// Rolled by 90 degrees, the same switch runs across the capture instead of down it.
func TestMixedCaptureRolled(t *testing.T) {
	c := still(64, 64)
	c.Timing.Exposure = 4 * time.Millisecond
	c.Display.ScanOut = 16 * time.Millisecond
	c.Geometry.Roll = 90
	c.Geometry.Fill = 0.5
	rc := mustRecording(t, loop(50*time.Millisecond, 4, flat(32, 32, 16), flat(32, 32, 235)), c, Run{Captures: 1})
	img := renderShot(rc, shot{start: 0.052})
	row := img.p[1][32*64 : 33*64]
	col := func(x int) float32 { return img.p[1][x*64+32] }
	if !(row[46] > row[17]+0.5) || math.Abs(float64(col(20)-col(44))) > 1e-6 {
		t.Errorf("row %v; column %v %v", row, col(20), col(44))
	}
	if row[2] != surround {
		t.Errorf("outside the screen = %v", row[2])
	}
}

// Three frames in one exposure: 10 ms frames, a 20 ms exposure starting mid-frame.
func TestThreeFrameMix(t *testing.T) {
	c := still(64, 32)
	c.Timing.Exposure = 20 * time.Millisecond
	in := loop(10*time.Millisecond, 4, flat(64, 32, 16), flat(64, 32, 126), flat(64, 32, 235), flat(64, 32, 16))
	rc := mustRecording(t, in, c, Run{Captures: 1})
	tr := &Truth{rc: rc, sh: shot{start: 0.005}}
	m := tr.Mix(10, 10)
	if len(m) != 3 || !near(m[0].W, 0.25, 1e-12) || !near(m[1].W, 0.5, 1e-12) || !near(m[2].W, 0.25, 1e-12) {
		t.Errorf("Mix = %v", m)
	}
	// Two displayed frames with one code frame index merge.
	in.Frames[1].Index = 0
	rc = mustRecording(t, in, c, Run{Captures: 1})
	if m := (&Truth{rc: rc, sh: shot{start: 0.005}}).Mix(10, 10); len(m) != 2 || !near(m[0].W, 0.75, 1e-12) {
		t.Errorf("merged Mix = %v", m)
	}
}

// Sharpened interpolation: magnified panel pixels keep hard edges; at one camera pixel per panel
// pixel it is plain bilinear interpolation.
func TestSharpSampling(t *testing.T) {
	i0, i1, f := sharp(3.2, 4, 10)
	if i0 != 2 || i1 != 3 || f != 1 {
		t.Errorf("sharp(3.2, 4) = %d %d %v", i0, i1, f)
	}
	if _, _, f := sharp(3.0, 4, 10); f != 0.5 {
		t.Errorf("sharp at an edge = %v", f)
	}
	if _, _, f := sharp(3.2, 1, 10); math.Abs(float64(f)-0.7) > 1e-6 {
		t.Errorf("bilinear weight %v", f)
	}
	if i0, i1, _ := sharp(-3, 1, 10); i0 != 0 || i1 != 0 {
		t.Error("edge clamp")
	}
	c := still(400, 200)
	rc := mustRecording(t, loop(time.Second, 4, flat(100, 50, 16)), c, Run{Captures: 1})
	if kx, ky := rc.sharpness(&shot{}); !near(kx, 4, 1e-9) || !near(ky, 4, 1e-9) {
		t.Errorf("sharpness %v %v", kx, ky)
	}
}
