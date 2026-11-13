package channel

import (
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
	var z0 *screen
	return z0
}

// toPanel maps video coordinates to panel coordinates.
func (s *screen) toPanel(u, v float64) (x, y float64) {
	var z0 float64
	var z1 float64
	return z0, z1
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
	var z0 *panel
	return z0
}

// resample scales a video-sized plane to the panel bilinearly, as a player does, with black
// letterbox bars.
func resample(src []float32, s *screen) []float32 {
	var z0 []float32
	return z0
}

// checkFrame validates one displayed frame against the first one's size.
func checkFrame(f *video.Frame, w, h int) error {
	var z0 error
	return z0
}
