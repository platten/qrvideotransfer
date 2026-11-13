package render

import (
	"errors"
	"fmt"
	"image"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
	"github.com/platten/qrvideotransfer/internal/video"
)

// MinHeight is the smallest frame height in pixels (FORMAT.md section 3).
const MinHeight = 480

// area is a structure rectangle in pixels with its colour.
type area struct {
	r image.Rectangle
	s modem.Sample
}

// Renderer paints the code frames of one grid, cell pitch and modulation. It is immutable after New
// and safe for concurrent use; each call to Render paints into its own destination.
type Renderer struct {
	layout      *frame.Layout
	mod         *modem.Constellation
	pitch       int
	w, h        int
	grey        modem.Sample
	dark, light modem.Sample
	symbol      [modem.MaxLevels]modem.Sample // appearance by symbol, bits above b ignored
	fixed       []area                        // border, separators and finder modules, in painting order
	slots       [4][frame.HeaderSlots]image.Rectangle
	patches     []area
}

// New returns a renderer for a gridX x gridY grid at a cell pitch of pitch pixels (4, 8, 12 or 16) and
// a modulation id. The frame is gridX*pitch x gridY*pitch pixels, at least 480 pixels tall.
func New(gridX, gridY, pitch int, modulation uint8) (*Renderer, error) {
	mod, err := modem.ByID(modulation)
	if err != nil {
		return nil, err
	}
	if pitch != 4 && pitch != 8 && pitch != 12 && pitch != 16 {
		return nil, fmt.Errorf("render: cell pitch %d is not 4, 8, 12 or 16", pitch)
	}
	l, err := frame.NewLayout(gridX, gridY)
	if err != nil {
		return nil, err
	}
	if gridY*pitch < MinHeight {
		return nil, fmt.Errorf("render: frame height %d below %d", gridY*pitch, MinHeight)
	}
	r := &Renderer{
		layout: l, mod: mod, pitch: pitch, w: gridX * pitch, h: gridY * pitch,
		dark: mod.Level(0), light: mod.Level(mod.Levels() - 1),
	}
	// Mid-grey: luma 126 and neutral chroma, scaled to the bit depth (FORMAT.md section 4.5).
	sh := r.dark.Bits - 8
	r.grey = modem.Sample{Y: 126 << sh, Cb: 128 << sh, Cr: 128 << sh, Bits: r.dark.Bits}
	for s := range r.symbol {
		r.symbol[s] = mod.Map(uint8(s))
	}
	for _, b := range l.Border() {
		r.fixed = append(r.fixed, area{b.Pixels(pitch), r.grey})
	}
	for c := range frame.Corner(4) {
		r.fixed = append(r.fixed, area{l.Separator(c).Pixels(pitch), r.pick(!frame.SeparatorDark(c))})
		for my := range 7 {
			for mx := range 7 {
				r.fixed = append(r.fixed, area{l.FinderModule(c, mx, my).Pixels(pitch), r.pick(!frame.FinderModuleDark(c, mx, my))})
			}
		}
		for i := range 59 {
			for row := range 4 {
				r.slots[c][4*i+row] = l.Slot(c, i, row).Pixels(pitch)
			}
		}
		for k := range 8 {
			r.patches = append(r.patches, area{l.Patch(c, k).Pixels(pitch), mod.Level(k % mod.Levels())})
		}
	}
	return r, nil
}

// pick returns the light colour for true and the dark one for false.
func (r *Renderer) pick(light bool) modem.Sample {
	if light {
		return r.light
	}
	return r.dark
}

// Layout returns the layout of the renderer's grid.
func (r *Renderer) Layout() *frame.Layout { return r.layout }

// Size returns the frame size in pixels.
func (r *Renderer) Size() (w, h int) { return r.w, r.h }

// Bits returns the bit depth of the planes: 8, or 10 for Gray8.
func (r *Renderer) Bits() uint8 { return r.grey.Bits }

// Render paints the code frame with header h and one symbol per data cell, in data-cell order, into
// dst (FORMAT.md section 4.5). dst's planes must hold at least W*H luma and (W/2)*(H/2) samples per
// chroma plane; Render sets dst.W, dst.H and dst.Bits and allocates nothing frame-sized. Symbol bits
// above b are ignored.
func (r *Renderer) Render(dst *video.Frame, h frame.Header, symbols []uint8) error {
	if err := h.Validate(); err != nil {
		return err
	}
	if int(h.GridX) != r.layout.GridX() || int(h.GridY) != r.layout.GridY() || h.Modulation != r.mod.ID() {
		return errors.New("render: header grid or modulation differs from the renderer's")
	}
	cells := r.layout.DataCells()
	if len(symbols) != len(cells) {
		return fmt.Errorf("render: %d symbols for %d data cells", len(symbols), len(cells))
	}
	cw, ch := r.w/2, r.h/2
	if len(dst.Y) < r.w*r.h || len(dst.Cb) < cw*ch || len(dst.Cr) < cw*ch {
		return errors.New("render: destination planes too small")
	}
	dst.W, dst.H, dst.Bits = r.w, r.h, r.grey.Bits
	r.paintCells(dst, cells, symbols)
	p := r.pitch
	for _, a := range r.layout.Anchors() {
		// Each anchor is eight 2 x 2-cell quadrants of one colour.
		for qy := 0; qy < frame.AnchorH; qy += 2 {
			for qx := 0; qx < frame.AnchorW; qx += 2 {
				x, y := (a.X+qx)*p, (a.Y+qy)*p
				r.fill(dst, image.Rect(x, y, x+2*p, y+2*p), r.pick(frame.AnchorCellLight(h.FrameIndex, qx, qy)))
			}
		}
	}
	for _, a := range r.fixed {
		r.fill(dst, a.r, a.s)
	}
	bits := h.Encode()
	for c := range r.slots {
		for s, rect := range r.slots[c] {
			r.fill(dst, rect, r.pick(bits[s]))
		}
	}
	for _, a := range r.patches {
		r.fill(dst, a.r, a.s)
	}
	return nil
}

// paintCells paints mid-grey and the data cells, one row of cells at a time: the first pixel row of a
// cell row is painted and then copied to the others.
func (r *Renderer) paintCells(dst *video.Frame, cells []frame.Cell, symbols []uint8) {
	w, p, cw := r.w, r.pitch, r.w/2
	i := 0
	for cy := range r.layout.GridY() {
		y0 := cy * p
		ry := dst.Y[y0*w : y0*w+w]
		rb := dst.Cb[y0/2*cw : y0/2*cw+cw]
		rr := dst.Cr[y0/2*cw : y0/2*cw+cw]
		setAll(ry, r.grey.Y)
		setAll(rb, r.grey.Cb)
		setAll(rr, r.grey.Cr)
		for ; i < len(cells) && int(cells[i].Y) == cy; i++ {
			s := &r.symbol[symbols[i]&(modem.MaxLevels-1)]
			x := int(cells[i].X) * p
			setAll(ry[x:x+p], s.Y)
			setAll(rb[x/2:x/2+p/2], s.Cb)
			setAll(rr[x/2:x/2+p/2], s.Cr)
		}
		for k := 1; k < p; k++ {
			copy(dst.Y[(y0+k)*w:(y0+k+1)*w], ry)
		}
		for k := 1; k < p/2; k++ {
			o := (y0/2 + k) * cw
			copy(dst.Cb[o:o+cw], rb)
			copy(dst.Cr[o:o+cw], rr)
		}
	}
}

// fill paints rectangle rect, clipped to the frame. A chroma sample takes the colour when the top-left
// of the four luma pixels it covers lies in rect.
func (r *Renderer) fill(dst *video.Frame, rect image.Rectangle, s modem.Sample) {
	rect = rect.Intersect(image.Rect(0, 0, r.w, r.h))
	if rect.Empty() {
		return
	}
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		setAll(dst.Y[y*r.w+rect.Min.X:y*r.w+rect.Max.X], s.Y)
	}
	cw := r.w / 2
	x0, x1 := (rect.Min.X+1)/2, (rect.Max.X+1)/2
	for y := (rect.Min.Y + 1) / 2; y < (rect.Max.Y+1)/2; y++ {
		setAll(dst.Cb[y*cw+x0:y*cw+x1], s.Cb)
		setAll(dst.Cr[y*cw+x0:y*cw+x1], s.Cr)
	}
}

func setAll(dst []uint16, v uint16) {
	for i := range dst {
		dst[i] = v
	}
}
