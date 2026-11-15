package vision

// At maps cell coordinates (x, y) to image coordinates. Cell (gx, gy) covers [gx, gx+1) x
// [gy, gy+1), so its centre is (gx+0.5, gy+0.5).
func (m *Mesh) At(x, y float64) Point {
	return Point{}
}
