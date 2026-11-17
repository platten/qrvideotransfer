package vision

import (
	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
	"github.com/platten/qrvideotransfer/internal/video"
)

// SamplePalette writes the observations of the 32 palette patches to dst (patch k of strip c at
// 8c + k, FORMAT.md section 4.3), each the mean over the central 60% of the patch read through the
// mesh, as FootprintSampler reads a cell. These are the palette the equaliser starts from (DESIGN.md
// section 10, step 5). dst must hold 32 observations. A nil mesh or an invalid capture gives
// saturated observations: no information.
func SamplePalette(dst []modem.Obs, f *video.Frame, m *Mesh, l *frame.Layout) {
	v, ok := geomView(f)
	gx, gy := float64(l.GridX()), float64(l.GridY())
	uy := gy / 135
	c0, dx, dy := 4.5*uy, gx-9*uy, 126*uy
	for cp := range 4 {
		for k := range 8 {
			if !ok || m == nil {
				dst[8*cp+k] = modem.Obs{Saturated: true}
				continue
			}
			r := frame.PalettePatch(cp, k)
			x0, x1 := c0+r.X0*dx, c0+r.X1*dx
			y0, y1 := c0+r.Y0*dy, c0+r.Y1*dy
			x, y := (x0+x1)/2, (y0+y1)/2
			hw, hh := 0.3*(x1-x0), 0.3*(y1-y0)
			c := gpt(m.At(x, y))
			hx := gpt(m.At(x+hw, y)).sub(gpt(m.At(x-hw, y))).mul(0.5)
			hy := gpt(m.At(x, y+hh)).sub(gpt(m.At(x, y-hh))).mul(0.5)
			dst[8*cp+k], _ = v.footprint(c, hx, hy)
		}
	}
}
