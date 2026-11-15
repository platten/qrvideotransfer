package vision

import (
	"math"

	"github.com/platten/qrvideotransfer/internal/video"
)

// geomLuma is a validated view of a capture's planes. Image coordinates have pixel centres at
// half-integers: pixel (i, j) covers [i, i+1) x [j, j+1).
type geomLuma struct {
	w, h   int
	y      []uint16
	cb, cr []uint16 // nil when absent; (w+1)/2 x (h+1)/2
	cw, ch int
	scale  float32 // multiplies a sample to the 8-bit scale
	top    uint16  // largest code value
}

// geomView validates f and returns a view of it. A frame is usable when its sides are within the
// video engine's capture limits (video.MaxCaptureLongSide and video.MaxCaptureShortSide, in either
// orientation), its bit depth is 8 to 16, its brightness plane is complete, and its colour planes
// are either both complete or ignored.
func geomView(f *video.Frame) (geomLuma, bool) {
	if f == nil || f.W < 1 || f.H < 1 || f.Bits < 8 || f.Bits > 16 {
		return geomLuma{}, false
	}
	long, short := max(f.W, f.H), min(f.W, f.H)
	if long > video.MaxCaptureLongSide || short > video.MaxCaptureShortSide || len(f.Y) < f.W*f.H {
		return geomLuma{}, false
	}
	v := geomLuma{w: f.W, h: f.H, y: f.Y, scale: 1 / float32(int(1)<<(f.Bits-8)), top: uint16(int(1)<<f.Bits - 1)}
	cw, ch := (f.W+1)/2, (f.H+1)/2
	if len(f.Cb) >= cw*ch && len(f.Cr) >= cw*ch {
		v.cb, v.cr, v.cw, v.ch = f.Cb, f.Cr, cw, ch
	}
	return v, true
}

// at returns brightness at pixel (x, y) on the 8-bit scale, clamping to the frame.
func (v *geomLuma) at(x, y int) float32 {
	x = min(max(x, 0), v.w-1)
	y = min(max(y, 0), v.h-1)
	return float32(v.y[y*v.w+x]) * v.scale
}

// bilinear returns brightness at image position (x, y), interpolated between pixel centres and
// clamped at the edges. Non-finite positions give the corner pixel.
func (v *geomLuma) bilinear(x, y float64) float32 {
	x, y = x-0.5, y-0.5
	if !(x > -1) {
		x = -1
	}
	if !(y > -1) {
		y = -1
	}
	x, y = math.Min(x, float64(v.w)), math.Min(y, float64(v.h))
	x0, y0 := math.Floor(x), math.Floor(y)
	fx, fy := float32(x-x0), float32(y-y0)
	i, j := int(x0), int(y0)
	a, b := v.at(i, j), v.at(i+1, j)
	c, d := v.at(i, j+1), v.at(i+1, j+1)
	top := a + float32((b-a)*fx)
	bot := c + float32((d-c)*fx)
	return top + float32((bot-top)*fy)
}

// inside reports whether p lies within the frame.
func (v *geomLuma) inside(p gpt) bool {
	return p.X >= 0 && p.Y >= 0 && p.X < float64(v.w) && p.Y < float64(v.h)
}

// patch returns the mean brightness of an n x n grid of bilinear samples spread evenly over the
// parallelogram c + s ex + t ey, s and t in (-1, 1).
func (v *geomLuma) patch(c, ex, ey gpt, n int) float32 {
	var sum float32
	for j := range n {
		t := (2*float64(j)+1)/float64(n) - 1
		for i := range n {
			s := (2*float64(i)+1)/float64(n) - 1
			sum += v.bilinear(c.X+s*ex.X+t*ey.X, c.Y+s*ex.Y+t*ey.Y)
		}
	}
	return sum / float32(n*n)
}

// geomFinite reports whether every coordinate is finite.
func geomFinite(ps ...gpt) bool {
	for _, p := range ps {
		if math.IsNaN(p.X) || math.IsInf(p.X, 0) || math.IsNaN(p.Y) || math.IsInf(p.Y, 0) {
			return false
		}
	}
	return true
}

// gpt is a Point with vector arithmetic, for the geometry code.
type gpt Point

func (p gpt) add(q gpt) gpt       { return gpt{p.X + q.X, p.Y + q.Y} }
func (p gpt) sub(q gpt) gpt       { return gpt{p.X - q.X, p.Y - q.Y} }
func (p gpt) mul(k float64) gpt   { return gpt{p.X * k, p.Y * k} }
func (p gpt) norm() float64       { return math.Hypot(p.X, p.Y) }
func (p gpt) cross(q gpt) float64 { return p.X*q.Y - p.Y*q.X }
func (p gpt) dot(q gpt) float64   { return p.X*q.X + p.Y*q.Y }
