package channel

import (
	"time"
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
	var z0 track
	return z0
}

// at returns the pose at time t and its rate of change per second.
func (tr *track) at(t float64) (p, v pose) {
	var z0 pose
	var z1 pose
	return z0, z1
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
	var z0 *camera
	return z0
}

// hom returns the homography from panel coordinates to undistorted capture coordinates at pose q.
func (c *camera) hom(q pose) [9]float64 {
	var z0 [9]float64
	return z0
}

// apply maps (x, y) through homography h.
func apply(h *[9]float64, x, y float64) (float64, float64) {
	var z0 float64
	var z1 float64
	return z0, z1
}

// invert returns the adjugate of h, which is its inverse up to scale.
func invert(h *[9]float64) [9]float64 {
	var z0 [9]float64
	return z0
}

// undistort maps a capture position to where an ideal pinhole camera would have seen it.
func (c *camera) undistort(x, y float64) (float64, float64) {
	var z0 float64
	var z1 float64
	return z0, z1
}

// distort is the inverse of undistort.
func (c *camera) distort(x, y float64) (float64, float64) {
	var z0 float64
	var z1 float64
	return z0, z1
}
