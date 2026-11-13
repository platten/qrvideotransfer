package main

import (
	"fmt"
	"image"

	"github.com/platten/qrvideotransfer/internal/frame"
)

// Session and inner code of every frame of the pattern.
const (
	session   = 0x5332
	innerCode = 2
)

// ycc is a limited-range BT.709 Y'CbCr colour.
type ycc [3]uint8

var midGrey = ycc{126, 128, 128}

// levelColours returns the default appearance of each level of a
// modulation (FORMAT.md section 7).
func levelColours(mod uint8) []ycc {
	switch mod {
	case 1:
		return []ycc{{16, 128, 128}, {235, 128, 128}}
	case 2:
		return []ycc{{16, 128, 128}, {89, 128, 128}, {162, 128, 128}, {235, 128, 128}}
	case 3:
		return []ycc{{16, 128, 128}, {32, 240, 118}, {173, 42, 26}, {188, 154, 16}, {63, 102, 240}, {78, 214, 230}, {219, 16, 138}, {235, 128, 128}}
	}
	panic(fmt.Sprintf("modulation %d", mod))
}

// symbolLevel maps a b-bit symbol to its level number (FORMAT.md section 7).
func symbolLevel(mod uint8, v int) int {
	if mod == 2 { // level l with l ^ (l >> 1) == v
		return v ^ v>>1
	}
	return v // Gray2: level = bit; RGB8: level = value
}

// painter draws the code frames of one segment geometry: W x H pixels,
// cell pitch P, one modulation.
type painter struct {
	w, h, p int
	mod     uint8
	b       int // bits per cell
	levels  []ycc
	layout  *frame.Layout
	mapping *frame.Mapping
	total   uint32 // F
}

func newPainter(w, h, p int, mod uint8, total uint32) (*painter, error) {
	gx, gy := w/p, h/p
	l, err := frame.NewLayout(gx, gy)
	if err != nil {
		return nil, err
	}
	pt := &painter{w: w, h: h, p: p, mod: mod, levels: levelColours(mod), layout: l, total: total}
	m, err := frame.NewMapping(pt.header(0))
	if err != nil {
		return nil, err
	}
	pt.mapping, pt.b = m, m.BitsPerCell()
	return pt, nil
}

// header returns the header of code frame idx.
func (pt *painter) header(idx uint32) frame.Header {
	return frame.Header{
		Version: frame.Version, Session: session, FrameIndex: idx, TotalFrames: pt.total,
		GridX: uint16(pt.w / pt.p), GridY: uint16(pt.h / pt.p), //nolint:gosec // grid <= 2047
		Modulation: pt.mod, InnerCode: innerCode,
	}
}

func (pt *painter) frameSize() int { return pt.w*pt.h + 2*(pt.w/2)*(pt.h/2) }

// dataLevel returns the level of data cell e in a frame scrambled by s:
// every bit position holds its scramble bit alone (no codewords).
func (pt *painter) dataLevel(s frame.Scrambler, e int) int {
	v := 0
	for k := range pt.b {
		v = v<<1 | int(s.Bit(e*pt.b+k))
	}
	return symbolLevel(pt.mod, v)
}

// structure returns the structure rectangles of FORMAT.md section 4 in
// painting order with their colours, for code frame idx.
func (pt *painter) structure(idx uint32) []elem {
	l := pt.layout
	dark, light := pt.levels[0], pt.levels[len(pt.levels)-1]
	pick := func(isLight bool) ycc {
		if isLight {
			return light
		}
		return dark
	}
	var es []elem
	for _, r := range l.Border() {
		es = append(es, elem{kindBorder, r.Pixels(pt.p), midGrey})
	}
	slots := pt.header(idx).Encode()
	for c := range frame.Corner(4) {
		es = append(es, elem{kindSeparator, l.Separator(c).Pixels(pt.p), pick(!frame.SeparatorDark(c))})
		for my := range 7 {
			for mx := range 7 {
				es = append(es, elem{kindFinder, l.FinderModule(c, mx, my).Pixels(pt.p), pick(!frame.FinderModuleDark(c, mx, my))})
			}
		}
		for i := range 59 {
			for r := range 4 {
				es = append(es, elem{kindSlot, l.Slot(c, i, r).Pixels(pt.p), pick(slots[4*i+r])})
			}
		}
		for k := range 8 {
			es = append(es, elem{kindPatch, l.Patch(c, k).Pixels(pt.p), pt.levels[k%len(pt.levels)]})
		}
	}
	return es
}

type elemKind uint8

const (
	kindBorder elemKind = iota
	kindSeparator
	kindFinder
	kindSlot
	kindPatch
)

// elem is one painted structure rectangle.
type elem struct {
	kind elemKind
	r    image.Rectangle
	c    ycc
}

// render paints code frame idx into buf as planar yuv420p (FORMAT.md
// section 4.5): mid-grey, then data cells and anchors, then structure.
func (pt *painter) render(idx uint32, buf []byte) {
	cv := canvas{buf: buf, w: pt.w, h: pt.h}
	cv.fill(image.Rect(0, 0, pt.w, pt.h), midGrey)
	s := pt.mapping.Scrambler(idx)
	p := pt.p
	cell := func(x, y int) image.Rectangle { return image.Rect(x*p, y*p, (x+1)*p, (y+1)*p) }
	for e, c := range pt.layout.DataCells() {
		cv.fill(cell(int(c.X), int(c.Y)), pt.levels[pt.dataLevel(s, e)])
	}
	dark, light := pt.levels[0], pt.levels[len(pt.levels)-1]
	for _, a := range pt.layout.Anchors() {
		for dy := range frame.AnchorH {
			for dx := range frame.AnchorW {
				c := dark
				if frame.AnchorCellLight(idx, dx, dy) {
					c = light
				}
				cv.fill(cell(a.X+dx, a.Y+dy), c)
			}
		}
	}
	for _, el := range pt.structure(idx) {
		cv.fill(el.r, el.c)
	}
}

// canvas is a planar yuv420p frame. Each chroma sample takes the colour of
// the top-left of the four luma pixels it covers.
type canvas struct {
	buf  []byte
	w, h int
}

func (cv canvas) fill(r image.Rectangle, c ycc) {
	r = r.Intersect(image.Rect(0, 0, cv.w, cv.h))
	if r.Empty() {
		return
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := cv.buf[y*cv.w+r.Min.X : y*cv.w+r.Max.X]
		for i := range row {
			row[i] = c[0]
		}
	}
	cw, ch := cv.w/2, cv.h/2
	u := cv.buf[cv.w*cv.h:]
	v := u[cw*ch:]
	x0, x1 := (r.Min.X+1)/2, (r.Max.X+1)/2
	for y := (r.Min.Y + 1) / 2; y < (r.Max.Y+1)/2; y++ {
		for x := x0; x < x1; x++ {
			u[y*cw+x], v[y*cw+x] = c[1], c[2]
		}
	}
}
