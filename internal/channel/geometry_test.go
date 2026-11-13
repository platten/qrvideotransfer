package channel

import (
	"math"
	"testing"
	"time"
)

// A pure rotation maps known points: with the screen rolled 90 degrees, a step right on the display
// is a step down in the capture, scaled by fill * camera width / panel width.
func TestPureRotation(t *testing.T) {
	c := still(400, 300)
	c.Geometry.Roll = 90
	c.Geometry.Fill = 0.5
	in := loop(time.Second, 4, flat(100, 60, 16))
	rc := mustRecording(t, in, c, Run{Captures: 1})
	tr := &Truth{rc: rc, sh: shot{}, GridX: 25, GridY: 15}
	s := 0.5 * 400 / 100.0
	for _, p := range [][4]float64{
		{50, 30, 200, 150},
		{60, 30, 200, 150 + 10*s},
		{50, 40, 200 - 10*s, 150},
		{0, 0, 200 + 30*s, 150 - 50*s},
	} {
		x, y := tr.Point(p[0], p[1])
		if !near(x, p[2], 1e-9) || !near(y, p[3], 1e-9) {
			t.Errorf("Point(%v, %v) = %v, %v; want %v, %v", p[0], p[1], x, y, p[2], p[3])
		}
	}
	if x, y := tr.Cell(12, 7); !near(x, 200-0*s, 1e-9) || !near(y, 150, 1e-9) {
		t.Errorf("Cell(12, 7) = %v, %v", x, y)
	}
	c.Geometry.Mirror = true
	rc = mustRecording(t, in, c, Run{Captures: 1})
	if x, _ := (&Truth{rc: rc}).Point(50, 40); !near(x, 200+10*s, 1e-9) {
		t.Errorf("mirrored x = %v", x)
	}
}

// Perspective: a tilted screen still maps lines to lines, and the tilt is at most the preset's.
func TestTiltHomography(t *testing.T) {
	g := GeometryParams{Width: 400, Height: 300, Fill: 0.8, Tilt: 25}
	scr := newScreen(100, 60, DisplayParams{})
	for seed := range uint64(50) {
		cam := newCamera(g, scr, seed)
		if cam.tilt[8] < math.Cos(25*math.Pi/180)-1e-12 {
			t.Fatalf("seed %d tilted by %v degrees", seed, math.Acos(cam.tilt[8])*180/math.Pi)
		}
		h := cam.hom(pose{})
		x0, y0 := apply(&h, 0, 10)
		x1, y1 := apply(&h, 50, 10)
		x2, y2 := apply(&h, 100, 10)
		if cross := (x1-x0)*(y2-y0) - (x2-x0)*(y1-y0); math.Abs(cross) > 1e-6 {
			t.Fatalf("seed %d: points of a line are not collinear (%v)", seed, cross)
		}
		inv := invert(&h)
		u, v := apply(&inv, x1, y1)
		if !near(u, 50, 1e-9) || !near(v, 10, 1e-9) {
			t.Fatalf("seed %d: inverse gives %v, %v", seed, u, v)
		}
	}
}

// Radial distortion: distort inverts undistort, and positive k1 pulls points towards the centre.
func TestDistortion(t *testing.T) {
	cam := &camera{w: 400, h: 300, cx: 200, cy: 150, norm: 200, k1: 0.08}
	for _, p := range [][2]float64{{0, 0}, {399, 299}, {200, 150}, {250, 100}} {
		xu, yu := cam.undistort(p[0], p[1])
		if r0, r1 := math.Hypot(p[0]-200, p[1]-150), math.Hypot(xu-200, yu-150); r1 < r0 {
			t.Errorf("undistort moved %v inwards", p)
		}
		x, y := cam.distort(xu, yu)
		if !near(x, p[0], 1e-9) || !near(y, p[1], 1e-9) {
			t.Errorf("round trip of %v gives %v, %v", p, x, y)
		}
	}
	// At the frame's edge (r = 1) a pinhole point moves by k1.
	if x, _ := cam.undistort(400, 150); !near(x, 200+200*1.08, 1e-12) {
		t.Errorf("edge maps to %v", x)
	}
}

func TestShakeTrack(t *testing.T) {
	g := GeometryParams{Width: 1000, Shake: 3}
	tr := shake(g, 50, time.Second, 20000, 4)
	if tr.t0 != 1 || tr.dt != 0.02 || len(tr.knots) != 20002 {
		t.Fatalf("track %v %v %d", tr.t0, tr.dt, len(tr.knots))
	}
	var s2 float64
	for k := 1; k < len(tr.knots); k++ {
		a, b := tr.knots[k-1], tr.knots[k]
		dx, dy := b.dx-shakeRevert*a.dx, b.dy-shakeRevert*a.dy
		s2 += dx*dx + dy*dy
	}
	if rms := math.Sqrt(s2 / float64(len(tr.knots)-1)); math.Abs(rms-3) > 0.05 {
		t.Errorf("step RMS %v, want 3", rms)
	}
	p, v := tr.at(1.03)
	a, b := tr.knots[1], tr.knots[2]
	if !near(p.dx, (a.dx+b.dx)/2, 1e-9) || !near(v.dy, (b.dy-a.dy)/0.02, 1e-9) {
		t.Errorf("at mid-step: %v %v", p, v)
	}
	if p, v := tr.at(0); p != tr.knots[0] || v != (pose{}) {
		t.Error("before the start")
	}
	if p, _ := tr.at(1e6); p != tr.knots[len(tr.knots)-1] {
		t.Error("after the end")
	}
	if z := shake(GeometryParams{Width: 10}, 30, 0, 5, 1); z.knots[5] != (pose{}) {
		t.Error("no shake moved")
	}
}

// Rolling-shutter shear: a camera moving sideways at v pixels per second with readout R sees a
// vertical line slanted by v R / H pixels per row.
func TestRollingShutterShear(t *testing.T) {
	c := still(200, 100)
	c.Timing.Readout = 20 * time.Millisecond
	f := grey(100, 50, func(x, _ int) uint16 {
		if x == 50 {
			return 235
		}
		return 16
	})
	rc := mustRecording(t, loop(time.Second, 2, f), c, Run{Captures: 1})
	const v = 1000.0
	rc.track = track{t0: 0, dt: 1, knots: []pose{{}, {dx: v}}}
	sh := shot{start: 0.01}
	tr := &Truth{rc: rc, sh: sh}
	x0, y0 := tr.Point(50.5, 0.5)
	for _, row := range []float64{10.5, 25.5, 49.5} {
		x, y := tr.Point(50.5, row)
		if want := x0 + v*0.020*(y-y0)/100; !near(x, want, 1e-6) {
			t.Errorf("row %v at x %v, want %v", row, x, want)
		}
	}
	// The rendered line follows the same slant: its centroid per row in linear light.
	occs := rc.tl.occurrences(rc.span(&sh))
	panels := make([]*panel, len(occs))
	for k, o := range occs {
		panels[k] = display(rc.frames[o.j], rc.scr)
	}
	img := newRGB(200, 100)
	rc.render(img, &sh, occs, panels)
	for _, y := range []int{5, 40, 90} {
		want, _ := tr.Point(50.5, (float64(y)+0.5)/2)
		var s, m float64
		for x := int(want) - 6; x <= int(want)+6; x++ {
			l := float64(img.p[1][y*200+x])
			s += l
			m += l * (float64(x) + 0.5)
		}
		if got := m / s; math.Abs(got-want) > 0.05 {
			t.Errorf("row %d centroid %v, want %v", y, got, want)
		}
	}
}
