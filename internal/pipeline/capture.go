package pipeline

import (
	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
	"github.com/platten/qrvideotransfer/internal/prng"
	"github.com/platten/qrvideotransfer/internal/video"
	"github.com/platten/qrvideotransfer/internal/vision"
)

// params are the header fields that every frame of one transfer shares.
type params struct {
	session      uint16
	gridX, gridY uint16
	mod, inner   uint8
	frames       uint32
}

func paramsOf(h frame.Header) params {
	return params{session: h.Session, gridX: h.GridX, gridY: h.GridY, mod: h.Modulation, inner: h.InnerCode, frames: h.TotalFrames}
}

// header returns a header with these parameters and frame index 0.
func (p params) header() frame.Header {
	return frame.Header{Version: frame.Version, Session: p.session, TotalFrames: p.frames, GridX: p.gridX, GridY: p.gridY,
		Modulation: p.mod, InnerCode: p.inner}
}

// geometry is what decoding needs of one transfer's grid. It is immutable.
type geometry struct {
	key     params
	layout  *frame.Layout
	mapping *frame.Mapping
	mod     *modem.Constellation
	cpf     int // C
	sent    int // sent bits per codeword
}

func newGeometry(key params) (*geometry, error) {
	m, err := frame.NewMapping(key.header())
	if err != nil {
		return nil, err
	}
	// NewMapping validated the grid and the modulation.
	l, _ := frame.NewLayout(int(key.gridX), int(key.gridY))
	c, _ := modem.ByID(key.mod)
	return &geometry{key: key, layout: l, mapping: m, mod: c, cpf: m.CodewordsPerFrame(), sent: m.Sent()}, nil
}

// phaseBits are the soft bits one capture gives one code frame: codeword c's sent bit t at
// c*sent + t, descrambled, so that positive means codeword bit 0.
type phaseBits struct {
	frame uint32
	soft  []int8
}

// captureResult is what a worker reports of one capture.
type captureResult struct {
	index        int  // order of the capture across the decode
	located      bool // a header was read and the mesh fitted
	key          params
	phases       []phaseBits
	cells, clean int // data cells, and those the phase field credits to one frame
}

// captureWorker runs the vision chain of DESIGN.md section 10 on captures. It owns its detector
// and buffers; give each worker its own.
type captureWorker struct {
	det   vision.FinderDetector
	geoms []*geometry // the last two grids seen, most recent first
	obs   []modem.Obs
	pal   []modem.Obs
	soft  []int8
}

// geometry returns the cached geometry of key, building it if needed.
func (w *captureWorker) geometry(key params) (*geometry, error) {
	for i, g := range w.geoms {
		if g.key == key {
			w.geoms[0], w.geoms[i] = w.geoms[i], w.geoms[0]
			return g, nil
		}
	}
	g, err := newGeometry(key)
	if err != nil {
		return nil, err
	}
	w.geoms = append([]*geometry{g}, w.geoms[:min(len(w.geoms), 1)]...)
	return g, nil
}

// process locates the code in f, reads its header and returns the soft bits of each code frame it
// shows, in codeword order. Only headers with the first header's parameters count.
func (w *captureWorker) process(f *video.Frame, index int) captureResult {
	r := captureResult{index: index}
	w.see(f, &r, func(g *geometry, idx uint32, soft []int8) {
		pb := phaseBits{frame: idx, soft: make([]int8, g.cpf*g.sent)}
		g.gather(pb.soft, soft, idx)
		r.phases = append(r.phases, pb)
	})
	return r
}

// see runs the vision chain on f, filling r's key, located, cells and clean, and calls each with
// the geometry of the header's grid and the descrambled soft bits, by bit position, of every code
// frame the capture shows.
func (w *captureWorker) see(f *video.Frame, r *captureResult, each func(g *geometry, frame uint32, soft []int8)) {
	q, ok := w.det.Detect(f, nil)
	if !ok {
		return
	}
	hs, ok := vision.SlotReader{}.Read(f, q)
	if !ok || len(hs) == 0 {
		return
	}
	r.key = paramsOf(hs[0])
	var idxs []uint32
	for _, h := range hs {
		if paramsOf(h) == r.key {
			idxs = append(idxs, h.FrameIndex)
		}
	}
	g, err := w.geometry(r.key)
	if err != nil {
		return
	}
	m, anchors, err := vision.MeshFitter{}.Fit(f, q, g.layout)
	if err != nil {
		return
	}
	r.located = true
	cells := g.layout.DataCells()
	r.cells = len(cells)
	w.obs = grow(w.obs, len(cells))
	w.pal = grow(w.pal, 32)
	vision.FootprintSampler{}.Sample(w.obs, f, m, cells)
	vision.SamplePalette(w.pal, f, m, g.layout)
	gamma, ok := vision.PaletteGamma(w.pal, g.mod)
	if !ok {
		gamma = 0
	}
	pf := vision.AnchorPhase{Gamma: gamma}.Estimate(anchors, g.layout)
	fld := (&vision.TileEqualiser{Mod: g.mod, Stripes: true}).Fit(w.obs, w.pal, g.layout, anchors)
	w.soft = grow(w.soft, g.mapping.Bits())
	for p := range 4 {
		n := vision.SoftBits(w.soft, w.obs, g.layout, fld, g.mod, &pf, p)
		if n == 0 {
			continue
		}
		r.clean += n
		if idx, ok := phaseFrame(idxs, p, r.key.frames); ok {
			g.descramble(w.soft, idx)
			each(g, idx, w.soft)
		}
	}
}

// grow returns s with length n, reusing its storage when it suffices.
func grow[T any](s []T, n int) []T {
	if cap(s) < n {
		return make([]T, n)
	}
	return s[:n]
}

// phaseFrame returns the code frame that phase p (frame index mod 4) shows in a capture whose
// headers read the frame indices hs, in a loop of frames code frames: a header's own index when its
// phase is p, else the frame one step after or before the first header's (wrapping at the seam,
// which keeps the phase sequence because frames is a multiple of 4). Two steps away is ambiguous and
// gives nothing (DESIGN.md section 7).
func phaseFrame(hs []uint32, p int, frames uint32) (uint32, bool) {
	for _, h := range hs {
		if int(h&3) == p {
			return h, true
		}
	}
	if len(hs) == 0 {
		return 0, false
	}
	h := hs[0]
	switch (p - int(h&3)) & 3 {
	case 1:
		return (h + 1) % frames, true
	case 3:
		return (h + frames - 1) % frames, true
	}
	return 0, false
}

// descramble removes frame idx's scrambling from soft, its soft bits by bit position, in place
// (FORMAT.md section 9): a soft bit then speaks of the codeword bit, positive meaning 0.
func (g *geometry) descramble(soft []int8, idx uint32) {
	s := uint64(g.mapping.Scrambler(idx))
	for i := 0; i*64 < len(soft); i++ {
		x := prng.At(s, uint64(i))
		for k, q := 0, i*64; k < 64 && q < len(soft); k, q = k+1, q+1 {
			if x>>(63-k)&1 == 1 {
				soft[q] = neg(soft[q])
			}
		}
	}
}

// gather writes soft, frame idx's descrambled soft bits by bit position, to dst in codeword order
// (FORMAT.md section 9).
func (g *geometry) gather(dst, soft []int8, idx uint32) {
	for c := range g.cpf {
		row := dst[c*g.sent : (c+1)*g.sent]
		for t := range row {
			row[t] = soft[g.mapping.Position(idx, c, t)]
		}
	}
}

// neg negates a soft bit, keeping it within -127..127.
func neg(v int8) int8 {
	if v == -128 {
		return 127
	}
	return -v
}
