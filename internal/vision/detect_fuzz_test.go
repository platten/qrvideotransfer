package vision

import (
	"encoding/binary"
	"math"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
	"github.com/platten/qrvideotransfer/internal/video"
)

// geomFuzzMax is the largest image side the fuzz target builds.
const geomFuzzMax = 400

// geomFuzzFrame builds an image from fuzz bytes: two bytes of size, then brightness samples,
// repeated to fill the frame.
func geomFuzzFrame(data []byte) *video.Frame {
	if len(data) < 3 {
		return nil
	}
	w, h := 2+int(data[0])%geomFuzzMax, 2+int(data[1])%geomFuzzMax
	px := data[2:]
	f := &video.Frame{W: w, H: h, Bits: 8, Y: make([]uint16, w*h)}
	for i := range f.Y {
		f.Y[i] = uint16(px[i%len(px)])
	}
	return f
}

// geomFuzzQuad reads a quad from 36 fuzz bytes: eight float32 coordinates and the module size.
func geomFuzzQuad(b []byte) Quad {
	var q Quad
	if len(b) < 36 {
		return q
	}
	f := func(i int) float64 { return float64(math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))) }
	for k := range 4 {
		q.Centres[k] = Point{f(2 * k), f(2*k + 1)}
	}
	q.Module = f(8)
	return q
}

// FuzzGeometry is the fuzz target of SECURITY.md section 9.1: finder detection, header reading,
// mesh fitting and sampling on arbitrary images and arbitrary quads never panic, and take time
// bounded by the image size (here at most 400 x 400 pixels, checked against a generous 5 s).
func FuzzGeometry(f *testing.F) {
	c := geomNewCode(f, 212, 120, 4, 2, 1, 40)
	plain := geomPlain(c, 0)
	// Seeds: a 400 x 240 crop of a rendered frame, a uniform image, and stripes.
	seed := []byte{byte(398 % 256), 238}
	for y := range 240 {
		for x := range 400 {
			seed = append(seed, byte(plain.Y[y*plain.W+x]))
		}
	}
	f.Add(seed)
	f.Add([]byte{100, 100, 128})
	f.Add([]byte{200, 50, 16, 16, 16, 235, 235, 16, 16, 16, 16, 16, 16, 235})
	q := geomPlainQuad(c)
	qb := make([]byte, 36)
	for k := range 4 {
		binary.LittleEndian.PutUint32(qb[8*k:], math.Float32bits(float32(q.Centres[k].X/4)))
		binary.LittleEndian.PutUint32(qb[8*k+4:], math.Float32bits(float32(q.Centres[k].Y/4)))
	}
	binary.LittleEndian.PutUint32(qb[32:], math.Float32bits(float32(q.Module/4)))
	f.Add(append(append([]byte{}, qb...), seed...))
	layout, err := frame.NewLayout(212, 120)
	if err != nil {
		f.Fatal(err)
	}
	cells := layout.DataCells()[:2000]
	f.Fuzz(func(t *testing.T, data []byte) {
		start := time.Now()
		img := geomFuzzFrame(data)
		var d FinderDetector
		quad, ok := d.Detect(img, nil)
		if !ok {
			quad = geomFuzzQuad(data)
		}
		_, _ = d.Detect(img, &quad)
		_, _ = SlotReader{}.Read(img, quad)
		m, _, err := MeshFitter{}.Fit(img, quad, layout)
		if err == nil {
			obs := make([]modem.Obs, len(cells))
			FootprintSampler{}.Sample(obs, img, m, cells)
		}
		if el := time.Since(start); el > 5*time.Second {
			t.Errorf("took %v", el)
		}
	})
}
