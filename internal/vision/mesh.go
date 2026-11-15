package vision

import "math"

// meshMaxSide bounds a mesh's nodes per row and per column before they are multiplied: far more
// than a 2047-cell grid needs at any step, and small enough that the product cannot overflow.
const meshMaxSide = 1 << 16

// At maps cell coordinates (x, y) to image coordinates. Cell (gx, gy) covers [gx, gx+1) x
// [gy, gy+1), so its centre is (gx+0.5, gy+0.5). Between nodes the mesh is bilinear; beyond the
// outermost nodes it extends the outermost lattice square linearly. A malformed mesh (fewer than
// 2 x 2 nodes, more than 65,536 per row or column, a node count other than Cols*Rows, a step that
// is not positive) or a non-finite
// position gives NaN.
func (m *Mesh) At(x, y float64) Point {
	nan := Point{math.NaN(), math.NaN()}
	if m == nil || m.Cols < 2 || m.Rows < 2 || m.Cols > meshMaxSide || m.Rows > meshMaxSide ||
		len(m.Nodes) != m.Cols*m.Rows || !(m.StepX > 0) || !(m.StepY > 0) {
		return nan
	}
	u, v := (x-m.X0)/m.StepX, (y-m.Y0)/m.StepY
	if !(math.Abs(u) < 1e9) || !(math.Abs(v) < 1e9) {
		return nan
	}
	i := min(max(int(math.Floor(u)), 0), m.Cols-2)
	j := min(max(int(math.Floor(v)), 0), m.Rows-2)
	fu, fv := u-float64(i), v-float64(j)
	k := j*m.Cols + i
	a, b, c, d := gpt(m.Nodes[k]), gpt(m.Nodes[k+1]), gpt(m.Nodes[k+m.Cols]), gpt(m.Nodes[k+m.Cols+1])
	top := a.add(b.sub(a).mul(fu))
	bot := c.add(d.sub(c).mul(fu))
	return Point(top.add(bot.sub(top).mul(fv)))
}
