package channel

import (
	"fmt"
	"math"

	"github.com/platten/qrvideotransfer/internal/video"
)

// BT.709 Y'CbCr to R'G'B' coefficients (Kr = 0.2126, Kb = 0.0722).
const (
	kr709, kb709 = 0.2126, 0.0722
	kg709        = 1 - kr709 - kb709
	crToR        = 2 * (1 - kr709)
	cbToB        = 2 * (1 - kb709)
	cbToG        = 2 * kb709 * (1 - kb709) / kg709
	crToG        = 2 * kr709 * (1 - kr709) / kg709
)

// screen is the static relation between the video, the panel and its sample grid.
type screen struct {
	vw, vh   int     // video pixels
	pw, ph   int     // panel pixels
	scale    float64 // panel pixels per video pixel
	ox, oy   float64 // panel position of the video's top-left corner
	sub      int     // sample columns per panel pixel: 1, or 3 with sub-pixel stripes
	gammaLUT [256]float32
}

func newScreen(vw, vh int, p DisplayParams) *screen {
	s := &screen{vw: vw, vh: vh, pw: p.PanelW, ph: p.PanelH, scale: 1, sub: 1}
	if s.pw == 0 || s.ph == 0 {
		s.pw, s.ph = vw, vh
	}
	s.scale = math.Min(float64(s.pw)/float64(vw), float64(s.ph)/float64(vh))
	s.ox = float64((float64(s.pw) - float64(float64(vw)*s.scale)) / 2)
	s.oy = float64((float64(s.ph) - float64(float64(vh)*s.scale)) / 2)
	if p.SubPixel {
		s.sub = 3
	}
	g := panelGamma + p.GammaError
	for v := range s.gammaLUT {
		s.gammaLUT[v] = float32(p.Black + float64((1-p.Black)*dpow(float64(float64(v)/255), g)))
	}
	return s
}

// toPanel maps video coordinates to panel coordinates.
func (s *screen) toPanel(u, v float64) (x, y float64) {
	return float64(u*s.scale) + s.ox, float64(v*s.scale) + s.oy
}

// panel is one displayed frame as the panel emits it: linear light, white 1, on the sample grid
// (w = sub * panel width columns, h = panel height rows).
type panel struct {
	w, h    int
	r, g, b []float32
}

// display is stage 1 for one frame: Y'CbCr (BT.709, limited range, 4:2:0, any bit depth) to R'G'B',
// player scaling to the panel with letterboxing, 8-bit panel drive, the panel's gamma and black
// level, and optionally the sub-pixel stripes. Flicker, scan-out and response act in time and are
// applied when a capture integrates the panel (see timeline).
func display(f *video.Frame, s *screen) *panel {
	vw, vh := s.vw, s.vh
	rgb := [3][]float32{make([]float32, vw*vh), make([]float32, vw*vh), make([]float32, vw*vh)}
	div := float64(int(1) << (f.Bits - 8))
	cw := (vw + 1) / 2
	for y := range vh {
		for x := range vw {
			i := y*vw + x
			yy := float64((float64(float64(f.Y[i])/div) - 16) / 219)
			cb, cr := 0.0, 0.0
			if f.Cb != nil {
				j := (y/2)*cw + x/2
				cb = float64((float64(float64(f.Cb[j])/div) - 128) / 224)
				cr = float64((float64(float64(f.Cr[j])/div) - 128) / 224)
			}
			rgb[0][i] = float32(yy + float64(crToR*cr))
			rgb[1][i] = float32(yy - float64(cbToG*cb) - float64(crToG*cr))
			rgb[2][i] = float32(yy + float64(cbToB*cb))
		}
	}
	if s.pw != vw || s.ph != vh {
		for c := range rgb {
			rgb[c] = resample(rgb[c], s)
		}
	}
	p := &panel{w: s.pw * s.sub, h: s.ph}
	out := [3][]float32{}
	for c := range out {
		out[c] = make([]float32, p.w*p.h)
		for i, v := range rgb[c] {
			q := int(clamp(math.Floor(float64(float64(v)*255)+0.5), 0, 255))
			l := s.gammaLUT[q]
			if s.sub == 1 {
				out[c][i] = l
			} else {
				out[c][3*i+c] = 3 * l // each stripe is a third of the pixel, so three times as bright
			}
		}
	}
	p.r, p.g, p.b = out[0], out[1], out[2]
	return p
}

// resample scales a video-sized plane to the panel bilinearly, as a player does, with black
// letterbox bars.
func resample(src []float32, s *screen) []float32 {
	dst := make([]float32, s.pw*s.ph)
	for y := range s.ph {
		v := float64((float64(y)+0.5-s.oy)/s.scale) - 0.5
		if v < -0.5 || v > float64(s.vh)-0.5 {
			continue
		}
		y0 := math.Floor(v)
		fy := v - y0
		r0, r1 := clampi(int(y0), 0, s.vh-1)*s.vw, clampi(int(y0)+1, 0, s.vh-1)*s.vw
		for x := range s.pw {
			u := float64((float64(x)+0.5-s.ox)/s.scale) - 0.5
			if u < -0.5 || u > float64(s.vw)-0.5 {
				continue
			}
			x0 := math.Floor(u)
			fx := u - x0
			c0, c1 := clampi(int(x0), 0, s.vw-1), clampi(int(x0)+1, 0, s.vw-1)
			top := lerp(float64(src[r0+c0]), float64(src[r0+c1]), fx)
			bot := lerp(float64(src[r1+c0]), float64(src[r1+c1]), fx)
			dst[y*s.pw+x] = float32(lerp(top, bot, fy))
		}
	}
	return dst
}

// checkFrame validates one displayed frame against the first one's size.
func checkFrame(f *video.Frame, w, h int) error {
	switch {
	case f == nil:
		return fmt.Errorf("channel: nil frame")
	case f.W != w || f.H != h:
		return fmt.Errorf("channel: frame size %dx%d differs from %dx%d", f.W, f.H, w, h)
	case f.Bits < 8 || f.Bits > 16:
		return fmt.Errorf("channel: bit depth %d out of range", f.Bits)
	case len(f.Y) < w*h:
		return fmt.Errorf("channel: brightness plane too short")
	case (f.Cb == nil) != (f.Cr == nil):
		return fmt.Errorf("channel: one colour plane missing")
	case f.Cb != nil && (len(f.Cb) < (w+1)/2*((h+1)/2) || len(f.Cr) < (w+1)/2*((h+1)/2)):
		return fmt.Errorf("channel: colour plane too short")
	}
	return nil
}
