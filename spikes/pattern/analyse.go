package main

// The S2 analysis (docs/IMPLEMENTATION_PROMPT.md section 5, after M2): a recording of the pattern
// is decoded and every capture is run through the M2 vision chain (Detect, the header reader, the
// mesh fitter, the sampler, the phase field and the equaliser). The pattern's data cells carry only
// their scramble bits, so every expected bit is recomputed from the header, and the soft bits are
// judged against them per code frame and per segment.

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
	"github.com/platten/qrvideotransfer/internal/video"
	"github.com/platten/qrvideotransfer/internal/vision"
)

// segGeom is what the analysis needs of one segment's code: its layout, its scrambling and its
// constellation.
type segGeom struct {
	layout  *frame.Layout
	mapping *frame.Mapping
	mod     *modem.Constellation
	b       int
}

func newSegGeom(h frame.Header) *segGeom {
	l, err := frame.NewLayout(int(h.GridX), int(h.GridY))
	if err != nil {
		panic(err) // the plans hold valid grids only
	}
	m, err := frame.NewMapping(h)
	if err != nil {
		panic(err)
	}
	c, err := modem.ByID(h.Modulation)
	if err != nil {
		panic(err)
	}
	return &segGeom{layout: l, mapping: m, mod: c, b: m.BitsPerCell()}
}

// expectedBit returns the bit painted at data-bit position q of code frame idx: its scramble bit
// (FORMAT.md section 9 with every codeword bit zero). Position q is bit q mod b of data cell q div b,
// most significant first, the order of the soft bits.
func (g *segGeom) expectedBit(idx uint32, q int) uint8 { return g.mapping.Scrambler(idx).Bit(q) }

// loadPlans reads every pattern-*.json segment table in dir.
func loadPlans(dir string) ([]plan, error) {
	names, err := filepath.Glob(filepath.Join(dir, "pattern-*.json"))
	if err != nil {
		return nil, err
	}
	var out []plan
	for _, n := range names {
		b, err := os.ReadFile(n)
		if err != nil {
			return nil, err
		}
		var pl plan
		if err := json.Unmarshal(b, &pl); err != nil {
			return nil, err
		}
		out = append(out, pl)
	}
	return out, nil
}

// locate returns the plan and segment a header belongs to: the plan with its frame count whose
// segment holding the frame index has the header's grid and modulation. The pattern's session and
// inner code are required.
func locate(plans []plan, h frame.Header) (int, int, bool) {
	if h.Session != session || h.InnerCode != innerCode {
		return 0, 0, false
	}
	for pi, pl := range plans {
		if pl.TotalFrames != h.TotalFrames {
			continue
		}
		for si, s := range pl.Segments {
			if h.FrameIndex < s.FirstIndex || h.FrameIndex >= s.FirstIndex+uint32(s.CodeFrames) { //nolint:gosec // small
				continue
			}
			if int(h.GridX) == s.GridX && int(h.GridY) == s.GridY && h.Modulation == s.Modulation {
				return pi, si, true
			}
		}
	}
	return 0, 0, false
}

// phaseFrame returns the code frame that phase p (frame index mod 4) shows in a capture whose
// headers read the frame indices hs: a header's own index when its phase is p, else the index one
// step after or before the first header's. Two steps away is ambiguous and gives nothing.
func phaseFrame(hs []uint32, p int) (uint32, bool) {
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
		return h + 1, true
	case 3:
		if h > 0 {
			return h - 1, true
		}
	}
	return 0, false
}

// calBuckets and calTable are the confidence buckets of the honesty tests (DESIGN.md section 15).
const calBuckets = 10

type calTable struct {
	N      [calBuckets]int     `json:"n"`
	Errs   [calBuckets]int     `json:"errors"`
	Stated [calBuckets]float64 `json:"stated"` // sum of stated error probabilities
}

// add counts soft bit s for a sent bit: positive means 0, and a zero soft bit counts as an error
// half the time.
func (c *calTable) add(s int8, bit uint8) {
	a := int(s)
	if a < 0 {
		a = -a
	}
	j := a * calBuckets / 128
	c.N[j]++
	c.Stated[j] += 1 / (1 + math.Exp(float64(a)/modem.LLRScale))
	switch {
	case s == 0:
		if c.N[j]%2 == 0 {
			c.Errs[j]++
		}
	case (s > 0) != (bit == 0):
		c.Errs[j]++
	}
}

func (c *calTable) merge(o *calTable) {
	for j := range calBuckets {
		c.N[j] += o.N[j]
		c.Errs[j] += o.Errs[j]
		c.Stated[j] += o.Stated[j]
	}
}

// h2 is the binary entropy in bits.
func h2(p float64) float64 {
	if p <= 0 || p >= 1 {
		return 0
	}
	return -p*math.Log2(p) - (1-p)*math.Log2(1-p)
}

// frameAcc sums, per bit, the soft bits of every capture that credits a cell of one code frame, as a
// decoder combining its captures would.
type frameAcc struct {
	cells, b          int
	sum               []int16
	credited          []bool
	firstCap, lastCap int
	captures          int
}

func newFrameAcc(cells, b, capIndex int) *frameAcc {
	return &frameAcc{cells: cells, b: b, sum: make([]int16, cells*b), credited: make([]bool, cells), firstCap: capIndex, lastCap: capIndex}
}

func (a *frameAcc) add(soft []int8, credited []bool, capIndex int) {
	for i, ok := range credited {
		if !ok {
			continue
		}
		a.credited[i] = true
		for k := range a.b {
			a.sum[i*a.b+k] += int16(soft[i*a.b+k])
		}
	}
	a.firstCap, a.lastCap = min(a.firstCap, capIndex), max(a.lastCap, capIndex)
	a.captures++
}

// frameRec is one code frame's result: of its cells, how many some capture credited (clean); of its
// bits, how many the summed soft bits state and how many of those are wrong.
type frameRec struct {
	idx      uint32
	firstCap int
	cells    int
	clean    int
	stated   int
	wrong    int
	captures int
}

func (a *frameAcc) finish(exp func(q int) uint8) frameRec {
	r := frameRec{cells: a.cells, captures: a.captures, firstCap: a.firstCap}
	for i, ok := range a.credited {
		if !ok {
			continue
		}
		r.clean++
		for k := range a.b {
			q := i*a.b + k
			s := a.sum[q]
			if s == 0 {
				continue
			}
			r.stated++
			if (s > 0) != (exp(q) == 0) {
				r.wrong++
			}
		}
	}
	return r
}

// delivered is the frame's delivered bits per cell: b x the share of bits stated x (1 - H2(error
// rate among the stated bits)), the capacity of a binary channel with erasures and errors. A cell no
// capture credited delivers nothing.
func (r frameRec) delivered(b int) float64 {
	if r.cells == 0 || r.stated == 0 {
		return 0
	}
	return float64(b) * float64(r.stated) / float64(r.cells*b) * (1 - h2(float64(r.wrong)/float64(r.stated)))
}

// segSum is a segment's figures over every pass the recording made through it.
type segSum struct {
	Expected   int     `json:"expected_frames"` // code frames shown within the passes
	Seen       int     `json:"seen_frames"`     // of those, credited by some capture
	Passes     int     `json:"passes"`
	Delivered  float64 `json:"delivered_bits_per_cell"`
	CleanFrame float64 `json:"clean_share_per_code_frame"`
	Stated     int     `json:"stated_bits"`
	Wrong      int     `json:"wrong_bits"`
	Total      float64 `json:"delivered_bits_per_cell_summed"` // over the seen frames
}

// summarise groups a segment's frame records into passes (a new pass when the first capture is more
// than gap captures after the previous record's, or the index goes back), counts every index
// between a pass's first and last as shown, and averages delivered bits per cell and the clean share
// over the shown frames: a frame no capture credited delivers nothing.
func summarise(recs []frameRec, b, gap int) segSum {
	recs = slices.Clone(recs)
	sort.Slice(recs, func(i, j int) bool { return recs[i].firstCap < recs[j].firstCap })
	var s segSum
	var lo, hi uint32
	prev := -1 << 30
	closePass := func() {
		if s.Passes > 0 {
			s.Expected += int(hi-lo) + 1
		}
	}
	var del, clean float64
	for _, r := range recs {
		if r.firstCap-prev > gap || s.Passes > 0 && r.idx+8 < hi {
			closePass()
			s.Passes++
			lo, hi = r.idx, r.idx
		}
		lo, hi = min(lo, r.idx), max(hi, r.idx)
		prev = r.firstCap
		s.Seen++
		del += r.delivered(b)
		if r.cells > 0 {
			clean += float64(r.clean) / float64(r.cells)
		}
		s.Stated += r.stated
		s.Wrong += r.wrong
	}
	closePass()
	s.Total = del
	s.Expected = max(s.Expected, s.Seen)
	if s.Expected > 0 {
		s.Delivered, s.CleanFrame = del/float64(s.Expected), clean/float64(s.Expected)
	}
	return s
}

// duration is the pattern's length in seconds.
func (pl *plan) duration() float64 { return float64(pl.VideoFrames) / fps }

// segmentAt returns the segment showing at pattern time t (seconds, taken modulo the pattern's
// length: a player looping it starts again at 0).
func (pl *plan) segmentAt(t float64) int {
	t = math.Mod(t, pl.duration())
	if t < 0 {
		t += pl.duration()
	}
	for i := len(pl.Segments) - 1; i > 0; i-- {
		if t >= pl.Segments[i].Start {
			return i
		}
	}
	return 0
}

// shownDuring returns the code frames of segment s whose display interval meets the window
// [t, t+period) of pattern time: those a capture exposed then may show.
func shownDuring(s segment, t, period float64) []uint32 {
	const eps = 1e-9
	r := float64(s.Rate)
	j0 := max(0, int(math.Floor((t-s.Start)*r+eps)))
	j1 := min(s.CodeFrames-1, int(math.Ceil((t+period-s.Start)*r-eps))-1)
	var out []uint32
	for j := j0; j <= j1; j++ {
		out = append(out, s.FirstIndex+uint32(j)) //nolint:gosec // small
	}
	return out
}

// patternOffset returns the recording's offset into the pattern, in seconds: the median over the
// placed captures of their pattern time less their capture time, taken on the circle of the
// pattern's length so that captures of a second loop agree with those of the first.
func patternOffset(pts, pat []float64, dur float64) float64 {
	if len(pts) == 0 {
		return 0
	}
	wrap := func(v float64) float64 { return v - dur*math.Round(v/dur) } // to (-dur/2, dur/2]
	ref := pat[0] - pts[0]
	d := make([]float64, len(pts))
	for i := range pts {
		d[i] = ref + wrap(pat[i]-pts[i]-ref)
	}
	slices.Sort(d)
	m := math.Mod(d[len(d)/2], dur)
	if m < 0 {
		m += dur
	}
	return m
}

// analyser runs the vision chain on captures of the pattern plans.
type analyser struct {
	plans []plan
	geoms [][]*segGeom
}

func newAnalyser(plans []plan) *analyser {
	a := &analyser{plans: plans, geoms: make([][]*segGeom, len(plans))}
	for pi, pl := range plans {
		for _, s := range pl.Segments {
			a.geoms[pi] = append(a.geoms[pi], newSegGeom(frame.Header{Version: frame.Version, Session: session,
				TotalFrames: pl.TotalFrames, GridX: uint16(s.GridX), GridY: uint16(s.GridY), //nolint:gosec // grid <= 2047
				Modulation: s.Modulation, InnerCode: innerCode}))
		}
	}
	return a
}

// phaseSoft is one phase's soft bits from one capture, for the code frame it shows.
type phaseSoft struct {
	idx      uint32
	soft     []int8
	credited []bool
}

// capResult is what one capture gives.
type capResult struct {
	index                       int
	pts, patTime                float64 // capture time, and for a placed capture the pattern time it shows
	located, headerRead, placed bool
	boundary                    bool // headers of two segments
	fitted                      bool
	plan, seg                   int
	cells, clean, clipped       int
	glare                       int
	pxPerCell                   float64
	phases                      []phaseSoft
	cal                         [2]calTable // credited cells wholly one frame (estimated weight >= 0.999) and partly mixed
	credited, wrongCells        int         // credited cells, and those whose hard symbol is wrong
	neighbour                   int         // wrong cells whose hard symbol is exactly the neighbouring frame's
}

// wholeWeight is the estimated phase weight from which a credited cell counts as wholly one frame,
// as the honesty tests split clean from partly mixed cells by their true weight.
const wholeWeight = 0.999

// capture runs the chain on one capture. det keeps no state between calls here (no previous quad
// is given), so captures may be analysed in any order.
func (a *analyser) capture(f *video.Frame, det *vision.FinderDetector, index int) capResult {
	r := capResult{index: index, pts: f.PTS.Seconds()}
	q, ok := det.Detect(f, nil)
	if !ok {
		return r
	}
	r.located = true
	hs, ok := vision.SlotReader{}.Read(f, q)
	if !ok {
		return r
	}
	r.headerRead = true
	var idxs []uint32
	pi, si := -1, -1
	for _, h := range hs {
		p, s, ok := locate(a.plans, h)
		if !ok {
			continue
		}
		if pi >= 0 && (p != pi || s != si) {
			r.boundary = true
			return r
		}
		pi, si = p, s
		idxs = append(idxs, h.FrameIndex)
	}
	if pi < 0 {
		return r
	}
	r.placed, r.plan, r.seg = true, pi, si
	g := a.geoms[pi][si]
	seg := a.plans[pi].Segments[si]
	r.patTime = seg.Start + float64(idxs[0]-seg.FirstIndex)/float64(seg.Rate)
	m, anchors, err := vision.MeshFitter{}.Fit(f, q, g.layout)
	if err != nil {
		return r
	}
	r.fitted = true
	cx, cy := float64(g.layout.GridX())/2, float64(g.layout.GridY())/2
	p0, px, py := m.At(cx, cy), m.At(cx+1, cy), m.At(cx, cy+1)
	r.pxPerCell = (math.Hypot(px.X-p0.X, px.Y-p0.Y) + math.Hypot(py.X-p0.X, py.Y-p0.Y)) / 2
	cells := g.layout.DataCells()
	r.cells = len(cells)
	obs := make([]modem.Obs, len(cells))
	vision.FootprintSampler{}.Sample(obs, f, m, cells)
	for _, o := range obs {
		if o.Saturated {
			r.clipped++
		}
	}
	pal := make([]modem.Obs, 32)
	vision.SamplePalette(pal, f, m, g.layout)
	gamma, ok := vision.PaletteGamma(pal, g.mod)
	if !ok {
		gamma = 0
	}
	pf := vision.AnchorPhase{Gamma: gamma}.Estimate(anchors, g.layout)
	fld := (&vision.TileEqualiser{Mod: g.mod, Stripes: true}).Fit(obs, pal, g.layout, anchors)
	sat := 0
	for _, o := range obs {
		if o.Saturated {
			sat++
		}
	}
	r.glare = sat - r.clipped
	for i := range cells {
		if _, ok := pf.Phase(i); ok {
			r.clean++
		}
	}
	b := g.b
	soft := make([]int8, len(cells)*b)
	for p := range 4 {
		if vision.SoftBits(soft, obs, g.layout, fld, g.mod, &pf, p) == 0 {
			continue
		}
		idx, ok := phaseFrame(idxs, p)
		if !ok || idx < seg.FirstIndex || idx >= seg.FirstIndex+uint32(seg.CodeFrames) { //nolint:gosec // small
			continue
		}
		credited := make([]bool, len(cells))
		scr := g.mapping.Scrambler(idx)
		var prev, next frame.Scrambler
		hasPrev, hasNext := idx > seg.FirstIndex, idx+1 < seg.FirstIndex+uint32(seg.CodeFrames) //nolint:gosec // small
		if hasPrev {
			prev = g.mapping.Scrambler(idx - 1)
		}
		if hasNext {
			next = g.mapping.Scrambler(idx + 1)
		}
		for i := range cells {
			w := pf.Weights[i][p]
			if w < vision.CleanWeight {
				continue
			}
			credited[i] = true
			t := 0
			if w < wholeWeight {
				t = 1
			}
			stated, wrong := true, false
			for k := range b {
				s := soft[i*b+k]
				bit := scr.Bit(i*b + k)
				r.cal[t].add(s, bit)
				if s == 0 {
					stated = false
				} else if (s > 0) != (bit == 0) {
					wrong = true
				}
			}
			if !stated {
				continue
			}
			r.credited++
			if !wrong {
				continue
			}
			r.wrongCells++
			if hasPrev && symbolIs(soft[i*b:i*b+b], prev, i*b) || hasNext && symbolIs(soft[i*b:i*b+b], next, i*b) {
				r.neighbour++
			}
		}
		r.phases = append(r.phases, phaseSoft{idx: idx, soft: slices.Clone(soft), credited: credited})
	}
	return r
}

// symbolIs reports whether the hard decisions of soft (all non-zero) are the bits of scrambler s
// at positions q0 onward.
func symbolIs(soft []int8, s frame.Scrambler, q0 int) bool {
	for k, v := range soft {
		if (v > 0) != (s.Bit(q0+k) == 0) {
			return false
		}
	}
	return true
}

// segCaps accumulates a segment's per-capture figures.
type segCaps struct {
	captures                   int
	cleanShare, clipped, glare float64
	px                         []float64
	cal                        [2]calTable
	credited, wrong, neighbour int
	recs                       []frameRec
	fittedPts                  []float64 // times of the captures fitted in the segment
}

// aggregator collects capture results, sums each code frame's captures and finishes a code frame
// once no capture near it is outstanding.
type aggregator struct {
	a          *analyser
	fps        float64
	accs       map[[3]int]*frameAcc
	segs       map[[2]int]*segCaps
	done       map[int]bool
	watermark  int // every capture below it has been added
	counts     struct{ captures, located, headerRead, placed, boundary, fitted int }
	window     int
	maxPending int
	pts        []float64         // every capture's time
	placedPts  map[int][]float64 // by plan: placed captures' times
	placedPat  map[int][]float64 // and the pattern times they show
}

func newAggregator(a *analyser, fps float64) *aggregator {
	return &aggregator{a: a, fps: fps, accs: map[[3]int]*frameAcc{}, segs: map[[2]int]*segCaps{}, done: map[int]bool{},
		window: int(fps) + 4, placedPts: map[int][]float64{}, placedPat: map[int][]float64{}}
}

func (g *aggregator) seg(pi, si int) *segCaps {
	k := [2]int{pi, si}
	s := g.segs[k]
	if s == nil {
		s = &segCaps{}
		g.segs[k] = s
	}
	return s
}

func (g *aggregator) add(r capResult) {
	c := &g.counts
	c.captures++
	g.pts = append(g.pts, r.pts)
	if r.placed {
		g.placedPts[r.plan] = append(g.placedPts[r.plan], r.pts)
		g.placedPat[r.plan] = append(g.placedPat[r.plan], r.patTime)
	}
	for _, x := range []struct {
		ok bool
		n  *int
	}{{r.located, &c.located}, {r.headerRead, &c.headerRead}, {r.placed, &c.placed}, {r.boundary, &c.boundary}, {r.fitted, &c.fitted}} {
		if x.ok {
			*x.n++
		}
	}
	if r.fitted {
		s := g.seg(r.plan, r.seg)
		s.captures++
		s.cleanShare += float64(r.clean) / float64(r.cells)
		s.clipped += float64(r.clipped) / float64(r.cells)
		s.glare += float64(r.glare) / float64(r.cells)
		s.px = append(s.px, r.pxPerCell)
		s.fittedPts = append(s.fittedPts, r.pts)
		s.cal[0].merge(&r.cal[0])
		s.cal[1].merge(&r.cal[1])
		s.credited += r.credited
		s.wrong += r.wrongCells
		s.neighbour += r.neighbour
		geom := g.a.geoms[r.plan][r.seg]
		for _, ph := range r.phases {
			k := [3]int{r.plan, r.seg, int(ph.idx)}
			acc := g.accs[k]
			if acc == nil {
				acc = newFrameAcc(len(geom.layout.DataCells()), geom.b, r.index)
				g.accs[k] = acc
			}
			acc.add(ph.soft, ph.credited, r.index)
		}
	}
	g.done[r.index] = true
	for g.done[g.watermark] {
		delete(g.done, g.watermark)
		g.watermark++
	}
	g.flush(false)
}

// flush finishes the code frames whose last capture lies more than window captures below the
// watermark, or all of them.
func (g *aggregator) flush(all bool) {
	for k, acc := range g.accs {
		if !all && acc.lastCap+g.window >= g.watermark {
			continue
		}
		geom := g.a.geoms[k[0]][k[1]]
		idx := uint32(k[2]) //nolint:gosec // small
		scr := geom.mapping.Scrambler(idx)
		r := acc.finish(scr.Bit)
		r.idx = idx
		s := g.seg(k[0], k[1])
		s.recs = append(s.recs, r)
		delete(g.accs, k)
	}
	g.maxPending = max(g.maxPending, len(g.accs))
}

// segReport is one segment's figures in one recording.
type segReport struct {
	Plan       int     `json:"plan"`
	Seg        int     `json:"segment"`
	Rate       int     `json:"display_rate"`
	Cell       int     `json:"cell"`
	Modulation uint8   `json:"modulation"`
	Cells      int     `json:"data_cells"`
	Captures   int     `json:"captures"`          // located, placed in the segment and fitted
	ByTime     int     `json:"captures_by_time"`  // captures taken while the segment showed
	Located    float64 `json:"located_share"`     // Captures / ByTime
	Shown      float64 `json:"code_frames_shown"` // ByTime x display rate / camera rate
	// LocatedFrames is how many code frames were shown while a located capture was exposed, and
	// DeliveredLocated the delivered bits per cell over them: the channel's figure given that the
	// code was located.
	LocatedFrames    int     `json:"code_frames_under_located_captures"`
	DeliveredLocated float64 `json:"delivered_bits_per_cell_located"`
	// DeliveredAll is the delivered bits per cell over every code frame shown while the camera
	// filmed the segment, through the whole chain: a frame never located delivers nothing.
	DeliveredAll float64     `json:"delivered_bits_per_cell_all_shown"`
	RateRatio    float64     `json:"camera_to_display_rate"`
	PxPerCell    float64     `json:"camera_pixels_per_cell"`
	CleanCapture float64     `json:"clean_share_per_capture"`
	Clipped      float64     `json:"clipped_share"`
	Glare        float64     `json:"glare_flagged_share"`
	Credited     int         `json:"credited_cells"`
	WrongCells   int         `json:"wrong_cells"`
	Neighbour    int         `json:"wrong_cells_matching_a_neighbour"`
	Sum          segSum      `json:"frames"`
	Cal          [2]calTable `json:"calibration"` // wholly one frame, partly mixed
}

// report is one recording's analysis.
type report struct {
	Plan     string         `json:"plan"`
	Counts   map[string]int `json:"captures"`
	Segments []segReport    `json:"segments"`
}

func (g *aggregator) finish() report {
	g.flush(true)
	c := g.counts
	rep := report{Counts: map[string]int{"total": c.captures, "located": c.located, "header_read": c.headerRead,
		"placed": c.placed, "segment_boundary": c.boundary, "mesh_fitted": c.fitted}}
	// The plan most captures were placed in; every capture is placed in its segment by time.
	pi, most := -1, 0
	for p, v := range g.placedPts {
		if len(v) > most || len(v) == most && p < pi {
			pi, most = p, len(v)
		}
	}
	if pi < 0 {
		return rep
	}
	pl := &g.a.plans[pi]
	rep.Plan = sizeName(pl.Width, pl.Height)
	off := patternOffset(g.placedPts[pi], g.placedPat[pi], pl.duration())
	byTime := make([]int, len(pl.Segments))
	for _, t := range g.pts {
		byTime[pl.segmentAt(t+off)]++
	}
	gap := int(2*g.fps) + 1
	for si, sg := range pl.Segments {
		s := g.segs[[2]int{pi, si}]
		if s == nil {
			s = &segCaps{}
		}
		if s.captures == 0 && byTime[si] == 0 {
			continue
		}
		geom := g.a.geoms[pi][si]
		sr := segReport{Plan: pi, Seg: si, Rate: sg.Rate, Cell: sg.Cell, Modulation: sg.Modulation,
			Cells: len(geom.layout.DataCells()), Captures: s.captures, ByTime: byTime[si], RateRatio: g.fps / float64(sg.Rate),
			Credited: s.credited, WrongCells: s.wrong, Neighbour: s.neighbour, Sum: summarise(s.recs, geom.b, gap), Cal: s.cal}
		sr.Shown = float64(byTime[si]) * float64(sg.Rate) / g.fps
		if byTime[si] > 0 {
			sr.Located = float64(s.captures) / float64(byTime[si])
		}
		under := map[uint32]bool{}
		for _, t := range s.fittedPts {
			for _, idx := range shownDuring(sg, math.Mod(t+off, pl.duration()), 1/g.fps) {
				under[idx] = true
			}
		}
		for _, r := range s.recs {
			under[r.idx] = true
		}
		sr.LocatedFrames = len(under)
		if sr.LocatedFrames > 0 {
			sr.DeliveredLocated = sr.Sum.Total / float64(sr.LocatedFrames)
		}
		if d := max(sr.Shown, float64(sr.Sum.Seen)); d > 0 {
			sr.DeliveredAll = sr.Sum.Total / d
		}
		if n := float64(s.captures); n > 0 {
			px := slices.Clone(s.px)
			slices.Sort(px)
			sr.PxPerCell, sr.CleanCapture, sr.Clipped, sr.Glare = px[len(px)/2], s.cleanShare/n, s.clipped/n, s.glare/n
		}
		rep.Segments = append(rep.Segments, sr)
	}
	return rep
}

func sizeName(w, h int) string { return strconv.Itoa(w) + "x" + strconv.Itoa(h) }
