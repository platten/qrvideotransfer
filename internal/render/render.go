package render

import (
	"errors"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
	"github.com/platten/qrvideotransfer/internal/video"
)

// Renderer paints the code frames of one grid, cell pitch and modulation. It is immutable after New
// and safe for concurrent use; each call to Render paints into its own destination.
type Renderer struct {
	layout *frame.Layout
	mod    *modem.Constellation
	pitch  int
	w, h   int
}

// New returns a renderer for a gridX x gridY grid at a cell pitch of pitch pixels (4, 8, 12 or 16) and
// a modulation id. The frame is gridX*pitch x gridY*pitch pixels, at least 480 pixels tall.
func New(gridX, gridY, pitch int, modulation uint8) (*Renderer, error) {
	return nil, errors.New("render: not implemented")
}

// Layout returns the layout of the renderer's grid.
func (r *Renderer) Layout() *frame.Layout { return r.layout }

// Size returns the frame size in pixels.
func (r *Renderer) Size() (w, h int) { return r.w, r.h }

// Bits returns the bit depth of the planes: 8, or 10 for Gray8.
func (r *Renderer) Bits() uint8 { return 0 }

// Render paints the code frame with header h and one symbol per data cell, in data-cell order, into
// dst (FORMAT.md section 4.5). dst's planes must hold at least W*H luma and (W/2)*(H/2) samples per
// chroma plane; Render sets dst.W, dst.H and dst.Bits and allocates nothing frame-sized. Symbol bits
// above b are ignored.
func (r *Renderer) Render(dst *video.Frame, h frame.Header, symbols []uint8) error {
	return errors.New("render: not implemented")
}
