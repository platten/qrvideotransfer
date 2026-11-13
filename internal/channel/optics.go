package channel

// optics is stage 4 for one capture, in linear light: the specular glare patch and veiling glare,
// then defocus and motion blur, then vignetting.
func (rc *recording) optics(img *rgb, sh *shot) {
}

// blurSigma returns the blur along x and y: the defocus sigma widened by the motion during the
// exposure, a box whose variance is its length squared over 12.
func (rc *recording) blurSigma(sh *shot) (sx, sy float64) {
	var z0 float64
	var z1 float64
	return z0, z1
}

// kernel returns a normalised Gaussian of the given sigma with radius ceil(3 sigma), or nil for a
// sigma too small to matter.
func kernel(sigma float64) []float32 {
	var z0 []float32
	return z0
}

// blur convolves a w x h plane with separable Gaussians, repeating the edge samples.
func blur(p []float32, w, h int, sx, sy float64) {
}

// separable filters the w x h image p (rows stride apart) in place with kernel kx along rows and
// ky along columns, either of which may be nil, repeating the edge samples. Each output sample
// sums its products in kernel order.
func separable(p []float32, stride, w, h int, kx, ky []float32) {
}

// accumulate sets dst to the sum over j, in order, of k[j] times the samples in(j), two terms
// per sweep.
func accumulate(dst []float32, k []float32, in func(j int) []float32) {
}

// screenBox returns the capture rectangle [x0, x1) x [y0, y1) holding the screen and the glare
// patch, widened by margin pixels and clipped to the frame.
func (rc *recording) screenBox(sh *shot, margin int) (x0, y0, x1, y1 int) {
	var z0 int
	var z1 int
	var z2 int
	var z3 int
	return z0, z1, z2, z3
}

// vignette darkens the image by v times the squared distance from the centre, relative to the corner.
func vignette(img *rgb, v float64) {
}

// glare adds the specular patch: a disc covering p.Glare of the code area at a random place on
// the screen, at glarePeak times white, with a smooth edge out to 1.25 times its radius.
func (rc *recording) glare(img *rgb, sh *shot) {
}
