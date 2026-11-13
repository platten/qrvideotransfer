package channel

import (
	"math"
	"time"

	"github.com/platten/qrvideotransfer/internal/prng"
)

// pose is the hand-shake offset at one moment: a translation in camera pixels and an in-plane
// rotation in radians.
type pose struct{ dx, dy, roll float64 }

// track is the hand-shake over time: knots every dt seconds from t0, linear in between and held
// beyond the ends.
type track struct {
	t0, dt float64
	knots  []pose
}

// shake is stage 3's hand-shake: a random walk pulled back to the mean, one step per camera frame,
// with steps of p.Shake pixels RMS in all and a rotation that moves the frame's edge by about half
// as much.
func shake(p GeometryParams, fps int, start time.Duration, n int, seed uint64) track {
	g := prng.SplitMix64{State: prng.Seed("qrvt/channel/shake", u64(seed))}
	tr := track{t0: seconds(start), dt: 1 / float64(fps), knots: make([]pose, n+2)}
	sd := float64(p.Shake / math.Sqrt2)
	sr := float64(p.Shake / float64(p.Width))
	if p.Shake == 0 {
		return tr
	}
	for k := 1; k < len(tr.knots); k++ {
		q := tr.knots[k-1]
		tr.knots[k] = pose{
			dx:   float64(shakeRevert*q.dx) + float64(sd*gauss(&g)),
			dy:   float64(shakeRevert*q.dy) + float64(sd*gauss(&g)),
			roll: float64(shakeRevert*q.roll) + float64(sr*gauss(&g)),
		}
	}
	return tr
}

// at returns the pose at time t and its rate of change per second.
func (tr *track) at(t float64) (p, v pose) {
	u := float64((t - tr.t0) / tr.dt)
	last := len(tr.knots) - 1
	if !(u > 0) || last == 0 {
		return tr.knots[0], pose{}
	}
	if u >= float64(last) {
		return tr.knots[last], pose{}
	}
	k := int(u)
	f := u - float64(k)
	a, b := tr.knots[k], tr.knots[k+1]
	p = pose{lerp(a.dx, b.dx, f), lerp(a.dy, b.dy, f), lerp(a.roll, b.roll, f)}
	v = pose{float64((b.dx - a.dx) / tr.dt), float64((b.dy - a.dy) / tr.dt), float64((b.roll - a.roll) / tr.dt)}
	return p, v
}

// camera is the static geometry of a recording: a pinhole camera with radial distortion looking at
// the panel from a distance at which the untilted screen fills p.Fill of the frame width, the screen
// tilted by a random angle up to p.Tilt about a random axis in its plane and rolled by p.Roll.
type camera struct {
	w, h     int
	f        float64 // focal length, pixels
	cx, cy   float64 // principal point
	norm     float64 // distortion radius unit: half the frame width
	k1       float64
	mirror   bool
	z0       float64    // screen distance, in panel pixels
	tilt     [9]float64 // tilt rotation, row-major
	roll     float64    // radians
	pw2, ph2 float64    // half the panel size
}

func newCamera(p GeometryParams, s *screen, seed uint64) *camera {
	g := prng.SplitMix64{State: prng.Seed("qrvt/channel/pose", u64(seed))}
	c := &camera{
		w: p.Width, h: p.Height,
		f:  float64(float64(p.Width) / focalScale),
		cx: float64(float64(p.Width) / 2), cy: float64(float64(p.Height) / 2), norm: float64(float64(p.Width) / 2),
		k1: p.K1, mirror: p.Mirror,
		roll: float64(p.Roll * (math.Pi / 180)),
		pw2:  float64(float64(s.pw) / 2), ph2: float64(float64(s.ph) / 2),
	}
	c.z0 = float64(c.f*float64(s.pw)) / float64(p.Fill*float64(p.Width))
	theta := float64(float64(p.Tilt*(math.Pi/180)) * uniform(&g))
	sa, ca := dsincos(float64(2*math.Pi) * uniform(&g))
	st, ct := dsincos(theta)
	k := 1 - ct
	c.tilt = [9]float64{
		ct + float64(k*float64(ca*ca)), float64(k * float64(ca*sa)), float64(st * sa),
		float64(k * float64(ca*sa)), ct + float64(k*float64(sa*sa)), -float64(st * ca),
		-float64(st * sa), float64(st * ca), ct,
	}
	return c
}

// hom returns the homography from panel coordinates to undistorted capture coordinates at pose q.
func (c *camera) hom(q pose) [9]float64 {
	sr, cr := dsincos(c.roll + q.roll)
	t := &c.tilt
	var m [9]float64 // rotation: roll after tilt
	for j := range 3 {
		m[j] = float64(cr*t[j]) - float64(sr*t[3+j])
		m[3+j] = float64(sr*t[j]) + float64(cr*t[3+j])
		m[6+j] = t[6+j]
	}
	// Translation, with the rotation centred on the panel's centre.
	s := float64(c.z0 / c.f)
	t0 := float64(q.dx*s) - float64(m[0]*c.pw2) - float64(m[1]*c.ph2)
	t1 := float64(q.dy*s) - float64(m[3]*c.pw2) - float64(m[4]*c.ph2)
	t2 := c.z0 - float64(m[6]*c.pw2) - float64(m[7]*c.ph2)
	return [9]float64{
		float64(c.f*m[0]) + float64(c.cx*m[6]), float64(c.f*m[1]) + float64(c.cx*m[7]), float64(c.f*t0) + float64(c.cx*t2),
		float64(c.f*m[3]) + float64(c.cy*m[6]), float64(c.f*m[4]) + float64(c.cy*m[7]), float64(c.f*t1) + float64(c.cy*t2),
		m[6], m[7], t2,
	}
}

// apply maps (x, y) through homography h.
func apply(h *[9]float64, x, y float64) (float64, float64) {
	w := float64(h[6]*x) + float64(h[7]*y) + h[8]
	return (float64(h[0]*x) + float64(h[1]*y) + h[2]) / w, (float64(h[3]*x) + float64(h[4]*y) + h[5]) / w
}

// invert returns the adjugate of h, which is its inverse up to scale.
func invert(h *[9]float64) [9]float64 {
	d := func(a, b, c, e float64) float64 { return float64(a*b) - float64(c*e) }
	return [9]float64{
		d(h[4], h[8], h[5], h[7]), d(h[2], h[7], h[1], h[8]), d(h[1], h[5], h[2], h[4]),
		d(h[5], h[6], h[3], h[8]), d(h[0], h[8], h[2], h[6]), d(h[2], h[3], h[0], h[5]),
		d(h[3], h[7], h[4], h[6]), d(h[1], h[6], h[0], h[7]), d(h[0], h[4], h[1], h[3]),
	}
}

// undistort maps a capture position to where an ideal pinhole camera would have seen it.
func (c *camera) undistort(x, y float64) (float64, float64) {
	if c.mirror {
		x = float64(c.w) - x
	}
	dx, dy := x-c.cx, y-c.cy
	r2 := float64((float64(dx*dx) + float64(dy*dy)) / float64(c.norm*c.norm))
	fac := float64(c.k1*r2) + 1
	return float64(dx*fac) + c.cx, float64(dy*fac) + c.cy
}

// distort is the inverse of undistort.
func (c *camera) distort(x, y float64) (float64, float64) {
	dx, dy := x-c.cx, y-c.cy
	ru := float64(math.Sqrt(float64(dx*dx)+float64(dy*dy)) / c.norm)
	if c.k1 != 0 && ru > 0 {
		r := ru // Newton's method on r + k1 r^3 = ru, from the right of the root
		for range 12 {
			r2 := float64(r * r)
			r -= (float64(c.k1*float64(r2*r)) + r - ru) / (float64(3*float64(c.k1*r2)) + 1)
		}
		dx, dy = float64(float64(dx*r)/ru), float64(float64(dy*r)/ru)
	}
	x, y = dx+c.cx, dy+c.cy
	if c.mirror {
		x = float64(c.w) - x
	}
	return x, y
}
