package channel

import (
	"math"
	"testing"
	"time"
)

func TestKernel(t *testing.T) {
	k := kernel(1.5)
	if len(k) != 11 {
		t.Fatalf("kernel length %d", len(k))
	}
	sum := 0.0
	for _, v := range k {
		sum += float64(v)
	}
	norm := 0.0
	for i := -5; i <= 5; i++ {
		norm += math.Exp(-float64(i*i) / 4.5)
	}
	for i, v := range k {
		d := float64(i - 5)
		if want := math.Exp(-d*d/4.5) / norm; math.Abs(float64(v)-want) > 1e-7 {
			t.Errorf("k[%d] = %v, want %v", i, v, want)
		}
	}
	if math.Abs(sum-1) > 1e-6 || kernel(0.01) != nil {
		t.Errorf("sum %v", sum)
	}
}

// The blur of an impulse is the kernel: the outer product of the two axes' Gaussians.
func TestBlurImpulse(t *testing.T) {
	const w, h = 31, 25
	p := make([]float32, w*h)
	p[12*w+15] = 1
	blur(p, w, h, 2, 1)
	kx, ky := kernel(2), kernel(1)
	rx, ry := len(kx)/2, len(ky)/2
	for y := range h {
		for x := range w {
			want := float32(0)
			if dx, dy := x-15+rx, y-12+ry; dx >= 0 && dx < len(kx) && dy >= 0 && dy < len(ky) {
				want = kx[dx] * ky[dy]
			}
			if math.Abs(float64(p[y*w+x]-want)) > 1e-7 {
				t.Fatalf("(%d, %d) = %v, want %v", x, y, p[y*w+x], want)
			}
		}
	}
	// Edges repeat: a flat plane stays flat.
	q := make([]float32, w*h)
	for i := range q {
		q[i] = 0.5
	}
	blur(q, w, h, 2, 2)
	for i, v := range q {
		if math.Abs(float64(v)-0.5) > 1e-6 {
			t.Fatalf("flat plane %d = %v", i, v)
		}
	}
}

// Motion during the exposure widens the blur by the motion's length over the square root of 12.
func TestBlurSigma(t *testing.T) {
	c := still(100, 50)
	c.Optics.Blur = 1
	c.Timing.Exposure = 10 * time.Millisecond
	rc := mustRecording(t, loop(time.Second, 2, flat(50, 25, 16)), c, Run{Captures: 1})
	rc.track = track{t0: 0, dt: 1, knots: []pose{{}, {dx: 600, dy: 0}}}
	sx, sy := rc.blurSigma(&shot{start: 0.1})
	if !near(sx, math.Sqrt(1+36.0/12), 1e-12) || !near(sy, 1, 1e-12) {
		t.Errorf("sigma %v %v", sx, sy)
	}
}

func TestVignette(t *testing.T) {
	img := newRGB(40, 20)
	for c := range img.p {
		for i := range img.p[c] {
			img.p[c][i] = 1
		}
	}
	vignette(img, 0.2)
	corner := float64(img.p[0][0])
	d2 := 19.5*19.5 + 9.5*9.5
	if want := 1 - 0.2*d2/(400+100); math.Abs(corner-want) > 1e-6 {
		t.Errorf("corner %v, want %v", corner, want)
	}
	if mid := img.p[2][10*40+20]; math.Abs(float64(mid)-(1-0.2*0.5/500)) > 1e-6 {
		t.Errorf("centre %v", mid)
	}
}

// The glare patch saturates about its share of the code area, and veiling glare lifts everything.
func TestGlareAndVeil(t *testing.T) {
	c := still(400, 300)
	c.Optics.Glare = 0.05
	c.Optics.Veil = 0.1
	rc := mustRecording(t, loop(time.Second, 4, flat(200, 150, 16)), c, Run{Captures: 1, Seed: 3})
	img := newRGB(400, 300)
	rc.optics(img, &shot{})
	sat := 0
	for _, v := range img.p[1] {
		if v >= glarePeak+0.1-1e-6 {
			sat++
		}
	}
	// The video fills the frame at two camera pixels per video pixel.
	if share := float64(sat) / (400 * 300); math.Abs(share-0.05) > 0.003 {
		t.Errorf("saturated share %v", share)
	}
	if v := img.p[0][0]; math.Abs(float64(v)-0.1) > 1e-6 && v < glarePeak {
		t.Errorf("veil %v", v)
	}
}
