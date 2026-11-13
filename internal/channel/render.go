package channel

// rgb is a linear-light image at camera resolution, white 1.
type rgb struct {
	w, h int
	p    [3][]float32
}

func newRGB(w, h int) *rgb {
	var z0 *rgb
	return z0
}

// render is stages 2 and 3 for one capture. Each capture pixel sees the panel point that the camera
// geometry maps it to at the time its row is exposed; its light is the exposure-weighted mix of the
// frames that point shows (scan-out and pixel response included), times the backlight's mean
// level. occs lists the showings that can be visible, with panels[k] the panel of occs[k].
func (rc *recording) render(sh *shot, occs []occ, panels []*panel) *rgb {
	var z0 *rgb
	return z0
}

// sharpness returns how much the panel's sample-grid interpolation is sharpened along x and y:
// the camera pixels per sample, at least 1. Bilinear interpolation sharpened this way averages the
// panel's square pixels over one camera pixel, as the sensor's pixel aperture does, instead of
// blurring their edges over a whole panel pixel.
func (rc *recording) sharpness(sh *shot) (kx, ky float64) {
	var z0 float64
	var z1 float64
	return z0, z1
}

// panelPerPixel returns the panel pixels per camera pixel at the frame's centre, mid-capture.
func (rc *recording) panelPerPixel(sh *shot) float64 {
	var z0 float64
	return z0
}

// sample interpolates the panel at sample-grid position (x, y), sharpened by kx and ky.
func (p *panel) sample(x, y, kx, ky float64) (r, g, b float32) {
	var z0 float32
	var z1 float32
	var z2 float32
	return z0, z1, z2
}

// sharp returns the two samples around position x of an axis with n samples (centres at
// half-integers) and the sharpened weight of the second.
func sharp(x, k float64, n int) (i0, i1 int, f float32) {
	var z0 int
	var z1 int
	var z2 float32
	return z0, z1, z2
}
