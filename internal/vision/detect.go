package vision

import (
	"math"
	"slices"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/video"
)

// Detection parameters (docs/DESIGN.md section 10, step 1).
const (
	detectWidth    = 1280 // the scan runs on brightness downscaled to at most this width
	detectMinSide  = 16   // smallest downscaled side scanned
	detectClusters = 1024 // most candidate clusters kept per scan
	detectNormal   = 24   // most normal finders considered for the quad
	detectInverted = 12   // most inverted finders considered for the quad
	detectMinSides = 20.0 // shortest distance between finder centres, in modules
	detectQuads    = 6    // best quads confirmed by their header
	detectContrast = 24   // smallest local brightness range, 8-bit code values, for a dark pixel
	// The dark/light threshold lies at detectDarkNum/detectDarkDen of the way from the local
	// minimum to the maximum in code values: about half the light, in linear terms, for a tone curve
	// near gamma 1/2.2. Blur acts on linear light, so a threshold at the code-value midrange (a
	// fifth of the light) widens light rings and thins the dark ones; at 4-5 pixels per module and
	// the presets' blur it erased the finders' outer dark ring.
	detectDarkNum = 7
	detectDarkDen = 10
)

// FinderDetector is the Detector of docs/DESIGN.md section 10, step 1. The zero value is ready to
// use. It keeps scratch buffers between calls, so it is not safe for concurrent use: give each
// worker its own.
type FinderDetector struct {
	small    []uint16 // downscaled brightness, code values
	lo, hi   []uint16 // local minimum and maximum of small
	tmp      []uint16
	queue    []int
	dark     []bool // small below its local midrange
	sw, sh   int
	clusters []detectCluster
	layout   *frame.Layout // of the last confirmed header's grid
}

var _ Detector = (*FinderDetector)(nil)

// detectCluster is a finder candidate: the mean of the scan hits near one place.
type detectCluster struct {
	sum    gpt     // sum of hit centres, downscaled pixels
	module float64 // sum of hit module sizes
	hits   int
	inv    bool    // light centre: the bottom-right finder
	at     gpt     // refined centre, full-resolution pixels
	chord  float64 // refined module size along rows and columns, full-resolution pixels
	fm     float64 // mod() in full-resolution pixels
}

func (c *detectCluster) centre() gpt  { return c.sum.mul(1 / float64(c.hits)) }
func (c *detectCluster) mod() float64 { return c.module / float64(c.hits) }

// Detect finds the four finders of a code in f. It scans brightness downscaled to at most 1280
// pixels wide for the finder signature along rows, confirms each hit along its column, groups the
// hits, chooses the four groups (three normal, one inverted) that form a plausible quad, and
// refines each centre at full resolution. A quad is reported only when its header decodes: data
// cells can mimic a finder at the finder's own scale, and a wrong quad is worse than none. Given
// the previous capture's quad it first scans only around its finders, and falls back to the whole
// capture. When the whole capture yields no quad and it was downscaled, it scans once more one
// step finer.
func (d *FinderDetector) Detect(f *video.Frame, prev *Quad) (Quad, bool) {
	v, ok := geomView(f)
	if !ok {
		return Quad{}, false
	}
	s := (v.w + detectWidth - 1) / detectWidth
	if v.w/s < detectMinSide || v.h/s < detectMinSide {
		return Quad{}, false
	}
	d.downscale(&v, s)
	if prev != nil && prev.Module > 0 && geomFinite(gpt(prev.Centres[0]), gpt(prev.Centres[1]), gpt(prev.Centres[2]), gpt(prev.Centres[3])) {
		r := max(16, int(math.Min(12*prev.Module/float64(s), float64(d.sw))))
		d.clusters = d.clusters[:0]
		for _, c := range prev.Centres {
			x, y := int(c.X/float64(s)), int(c.Y/float64(s))
			d.scan(x-r, y-r, x+r, y+r)
		}
		if q, ok := d.choose(f, &v, s); ok {
			return q, true
		}
	}
	d.clusters = d.clusters[:0]
	d.scan(0, 0, d.sw, d.sh)
	if q, ok := d.choose(f, &v, s); ok || s == 1 {
		return q, ok
	}
	// A far finder at 25 degrees of tilt can measure under 3 pixels per module after downscaling;
	// one finer scan catches it.
	s--
	d.downscale(&v, s)
	d.clusters = d.clusters[:0]
	d.scan(0, 0, d.sw, d.sh)
	return d.choose(f, &v, s)
}

// downscale box-filters the brightness by s into d.small and marks the pixels darker than
// lo + detectDarkShare (hi - lo), where lo and hi are the minimum and maximum of a square window
// around them whose side is about a twentieth of the downscaled width. Where the window's range is
// under detectContrast the pixel counts as light.
func (d *FinderDetector) downscale(v *geomLuma, s int) {
	sw, sh := v.w/s, v.h/s
	d.sw, d.sh = sw, sh
	n := sw * sh
	d.small = slices.Grow(d.small[:0], n)[:n]
	d.lo = slices.Grow(d.lo[:0], n)[:n]
	d.hi = slices.Grow(d.hi[:0], n)[:n]
	d.dark = slices.Grow(d.dark[:0], n)[:n]
	for y := range sh {
		for x := range sw {
			sum := 0
			for j := range s {
				row := v.y[(y*s+j)*v.w+x*s:]
				for i := range s {
					sum += int(row[i])
				}
			}
			d.small[y*sw+x] = uint16(sum / (s * s)) //nolint:gosec // a mean of uint16 samples
		}
	}
	r := max(4, sw/40)
	d.slide(d.lo, d.small, r, false)
	d.slide(d.hi, d.small, r, true)
	minRange := int(float32(detectContrast) / v.scale)
	for i, p := range d.small {
		lo, hi := int(d.lo[i]), int(d.hi[i])
		d.dark[i] = hi-lo >= minRange && detectDarkDen*(int(p)-lo) < detectDarkNum*(hi-lo)
	}
}

// slide writes to dst the minimum (or maximum) of src over the square window of radius r around
// each pixel, clipped to the image, separably with monotonic queues.
func (d *FinderDetector) slide(dst, src []uint16, r int, isMax bool) {
	sw, sh := d.sw, d.sh
	d.tmp = slices.Grow(d.tmp[:0], sw*sh)[:sw*sh]
	d.queue = slices.Grow(d.queue[:0], max(sw, sh))[:max(sw, sh)]
	better := func(a, b uint16) bool { return a < b }
	if isMax {
		better = func(a, b uint16) bool { return a > b }
	}
	// pass filters n samples at stride st from in to out.
	pass := func(out, in []uint16, n, st int) {
		q := d.queue[:0]
		head := 0
		for i := 0; i < n+r; i++ {
			if i < n {
				for len(q) > head && !better(in[q[len(q)-1]*st], in[i*st]) {
					q = q[:len(q)-1]
				}
				q = append(q, i)
			}
			if o := i - r; o >= 0 {
				for q[head] < o-r {
					head++
				}
				out[o*st] = in[q[head]*st]
			}
		}
	}
	for y := range sh {
		pass(d.tmp[y*sw:], src[y*sw:], sw, 1)
	}
	for x := range sw {
		pass(dst[x:], d.tmp[x:], sh, sw)
	}
}

// detectPattern checks five consecutive run lengths for the finder signature: inner runs b, c, d in
// the ratio 1:3:1 and flanks a and e of at least a quarter module (DESIGN.md asks for half; blur
// in linear light thins the outer dark ring, see detectDarkNum). It returns the module size. A
// loose check, for the diagonals, widens the tolerances by a quarter and accepts flanks of 0.15
// module.
func detectPattern(a, b, c, d, e int, loose bool) (float64, bool) {
	m := float64(b+c+d) / 5
	if m < 0.8 {
		return 0, false
	}
	tol, flank := 1.0, 0.25
	if loose {
		tol, flank = 1.25, 0.15
	}
	fb, fc, fd := float64(b), float64(c), float64(d)
	ok := math.Abs(fb-m) <= 0.6*tol*m && math.Abs(fd-m) <= 0.6*tol*m && math.Abs(fc-3*m) <= 1.2*tol*m &&
		float64(a) >= flank*m && float64(e) >= flank*m && fb <= 2*tol*fd && fd <= 2*tol*fb
	return m, ok
}

// scan looks for the finder signature along the rows of the rectangle [x0, x1) x [y0, y1) of the
// downscaled image and adds confirmed hits to the clusters.
func (d *FinderDetector) scan(x0, y0, x1, y1 int) {
	x0, y0 = max(x0, 0), max(y0, 0)
	x1, y1 = min(x1, d.sw), min(y1, d.sh)
	var start, length [5]int
	for y := y0; y < y1; y++ {
		row := d.dark[y*d.sw : (y+1)*d.sw]
		n := 0 // runs seen in this row, up to 5 kept
		runStart := x0
		for x := x0 + 1; x <= x1; x++ {
			if x < x1 && row[x] == row[x-1] {
				continue
			}
			copy(start[:], start[1:])
			copy(length[:], length[1:])
			start[4], length[4] = runStart, x-runStart
			runStart = x
			n++
			if n < 5 {
				continue
			}
			m, ok := detectPattern(length[0], length[1], length[2], length[3], length[4], false)
			if !ok {
				continue
			}
			inv := !row[start[2]]
			cx := start[2] + length[2]/2
			d.confirm(cx, y, inv, m)
		}
	}
}

// confirm checks a row hit at (x, y) along its column and again along the row through the column's
// centre, and adds it to the clusters.
func (d *FinderDetector) confirm(x, y int, inv bool, m float64) {
	limit := int(6*m) + 2
	oy, mv, ok := d.cross(x, y, 0, 1, inv, false, limit)
	if !ok || mv > 2*m || m > 2*mv {
		return
	}
	cy := float64(y) + oy
	iy := int(cy)
	ox, mh, ok := d.cross(x, iy, 1, 0, inv, false, limit)
	if !ok {
		return
	}
	cx := float64(x) + ox
	// Every line through the centre of concentric squares crosses them in the same ratios, so
	// the signature must hold along both diagonals too, centred on the hit.
	ix := int(cx)
	// The diagonals are checked loosely: at a few pixels per module a steep perspective distorts
	// them most. The shortest of the four chords is within 8% of the module whatever the finder's
	// rotation.
	mod := math.Min(mh, mv)
	for _, dy := range []int{1, -1} {
		o, md, ok := d.cross(ix, iy, 1, dy, inv, true, limit)
		if !ok || math.Abs(o) > math.Max(1, 0.5*md) || md > 2.5*m || m > 2.5*md {
			return
		}
		mod = math.Min(mod, md*math.Sqrt2)
	}
	d.add(gpt{cx + 0.5, cy + 0.5}, mod, inv)
}

// cross walks from (x, y) both ways along (dx, dy) through the centre run, the light runs and the
// flanks, and returns the centre run's middle, in steps from (x, y), and the module size in steps.
func (d *FinderDetector) cross(x, y, dx, dy int, inv, loose bool, limit int) (float64, float64, bool) {
	if x < 0 || y < 0 || x >= d.sw || y >= d.sh || d.dark[y*d.sw+x] == inv {
		return 0, 0, false
	}
	var runs [2][3]int // per direction: centre part, light run, flank
	for k, sg := range [2]int{-1, 1} {
		px, py := x, y
		for part := range 3 {
			want := (part != 1) != inv // dark, light, dark (inverted: light, dark, light)
			for runs[k][part] < limit {
				nx, ny := px+sg*dx, py+sg*dy
				if nx < 0 || ny < 0 || nx >= d.sw || ny >= d.sh || d.dark[ny*d.sw+nx] != want {
					break
				}
				px, py = nx, ny
				runs[k][part]++
			}
		}
	}
	c := runs[0][0] + runs[1][0] + 1
	m, ok := detectPattern(runs[0][2], runs[0][1], c, runs[1][1], runs[1][2], loose)
	if !ok {
		return 0, 0, false
	}
	return float64(runs[1][0]-runs[0][0]) / 2, m, true
}

// add puts a hit into the cluster of the same polarity within two modules, or starts one.
func (d *FinderDetector) add(p gpt, m float64, inv bool) {
	for i := range d.clusters {
		c := &d.clusters[i]
		if c.inv == inv && c.centre().sub(p).norm() <= math.Max(2*c.mod(), 2) {
			c.sum, c.module, c.hits = c.sum.add(p), c.module+m, c.hits+1
			return
		}
	}
	if len(d.clusters) < detectClusters {
		d.clusters = append(d.clusters, detectCluster{sum: p, module: m, hits: 1, inv: inv})
	}
}

// choose picks the best quad among the clusters and refines it at full resolution.
func (d *FinderDetector) choose(f *video.Frame, v *geomLuma, s int) (Quad, bool) {
	var normal, inverted []*detectCluster
	for i := range d.clusters {
		c := &d.clusters[i]
		if c.hits < 2 {
			continue
		}
		if c.inv {
			inverted = append(inverted, c)
		} else {
			normal = append(normal, c)
		}
	}
	byHits := func(a, b *detectCluster) int { return b.hits - a.hits }
	slices.SortStableFunc(normal, byHits)
	slices.SortStableFunc(inverted, byHits)
	normal = detectVerify(v, s, normal, detectNormal)
	inverted = detectVerify(v, s, inverted, detectInverted)
	type scored struct {
		q     [4]*detectCluster
		score float64
	}
	var top []scored // the best detectQuads quads, best first
	for _, br := range inverted {
		for _, tl := range normal {
			if !detectSizes(tl, br) {
				continue
			}
			for _, tr := range normal {
				if tr == tl || !detectSizes(tr, br) || !detectSizes(tr, tl) {
					continue
				}
				for _, bl := range normal {
					if bl == tl || bl == tr || !detectSizes(bl, br) || !detectSizes(bl, tl) || !detectSizes(bl, tr) {
						continue
					}
					q := [4]*detectCluster{tl, tr, bl, br}
					sc, ok := detectScore(q)
					if !ok || (len(top) == detectQuads && sc <= top[len(top)-1].score) {
						continue
					}
					i, _ := slices.BinarySearchFunc(top, sc, func(a scored, t float64) int {
						if a.score >= t {
							return -1
						}
						return 1
					})
					top = slices.Insert(top, i, scored{q, sc})
					top = top[:min(len(top), detectQuads)]
				}
			}
		}
	}
	if len(top) == 0 {
		return Quad{}, false
	}
	quad := func(best [4]*detectCluster) Quad {
		var q Quad
		for k, c := range best {
			q.Centres[k] = Point(c.at)
			q.Module += c.chord / 4
		}
		return q
	}
	// Data cells can mimic a finder at the finder's own scale (on a grid of 135 rows or fewer a finder
	// module is at most one cell), so the best quads are confirmed by their header, best first. A
	// quad whose header does not decode is not reported: a wrong quad would feed wrong cells on.
	for _, t := range top {
		if q := quad(t.q); d.confirms(f, v, q) {
			return q, true
		}
	}
	return Quad{}, false
}

// detectVerify refines the candidates at full resolution, most hits first, and keeps up to n that
// pass, with their refined centres in downscaled pixels.
func detectVerify(v *geomLuma, s int, cs []*detectCluster, n int) []*detectCluster {
	out := cs[:0]
	for _, c := range cs {
		if len(out) == n {
			break
		}
		p, m, ok := detectRefine(v, c.centre().mul(float64(s)), c.mod()*float64(s), c.inv)
		if ok {
			// The shortest chord underestimates a foreshortened module and the row and column
			// chords overestimate a rotated one (by up to sqrt 2); their geometric mean errs less.
			c.at, c.chord, c.fm = p, m, math.Sqrt(c.mod()*float64(s)*m)
			out = append(out, c)
		}
	}
	return out
}

// detectSizes reports whether two candidates' module sizes agree within a factor of 3: at 25
// degrees of tilt the near finders' modules measure about twice the far ones'.
func detectSizes(a, b *detectCluster) bool {
	return a.fm <= 3*b.fm && b.fm <= 3*a.fm
}

// detectScore scores four candidates (top-left, top-right, bottom-left, bottom-right) as a quad:
// it must be convex, in clockwise order on the image (top-left, top-right, bottom-right,
// bottom-left), nearly a parallelogram, with sides of at least detectMinSides modules, and its
// left and right sides must each measure 126 modules (FORMAT.md section 4: Dy = 126 Uy) within a
// factor of 1/0.65, measured in the mean module of their ends. The score is the total number of hits.
func detectScore(q [4]*detectCluster) (float64, bool) {
	tl, tr, bl, br := q[0].at, q[1].at, q[2].at, q[3].at
	m := (q[0].fm + q[1].fm + q[2].fm + q[3].fm) / 4
	ring := [4]gpt{tl, tr, br, bl}
	for i := range 4 {
		a, b, c := ring[i], ring[(i+1)%4], ring[(i+2)%4]
		if b.sub(a).norm() < detectMinSides*m || b.sub(a).cross(c.sub(b)) <= 0 {
			return 0, false
		}
	}
	for _, s := range [2][2]int{{0, 2}, {1, 3}} {
		a, b := q[s[0]], q[s[1]]
		n := a.at.sub(b.at).norm() / ((a.fm + b.fm) / 2)
		if n < 126*0.65 || n > 126/0.65 {
			return 0, false
		}
	}
	if tr.add(bl).sub(tl).sub(br).norm() > 0.35*br.sub(tl).norm() {
		return 0, false
	}
	return float64(q[0].hits + q[1].hits + q[2].hits + q[3].hits), true
}

// detectRefine refines a finder centre at full resolution: the midpoints of the edges of the
// light ring (dark ring for the inverted finder) along seven rows and seven columns near the
// centre, averaged, twice. It returns the centre and the module size.
func detectRefine(v *geomLuma, c gpt, m float64, inv bool) (gpt, float64, bool) {
	mod := m
	for range 3 {
		centre := v.patch(c, gpt{0.6 * m, 0}, gpt{0, 0.6 * m}, 4)
		// The extremes of ring 2 (the centre's opposite) and ring 3 (the centre's colour), each one
		// module wide and so weakened alike by blur, over 16 directions.
		ring, outer := centre, float32(math.NaN())
		for k := range 16 {
			sn, cs := math.Sincos(float64(k) * math.Pi / 8)
			r := v.bilinear(c.X+2.2*m*cs, c.Y+2.2*m*sn)
			if (r > ring) != inv {
				ring = r
			}
			o := v.bilinear(c.X+3*m*cs, c.Y+3*m*sn)
			if math.IsNaN(float64(outer)) || (o < outer) != inv {
				outer = o
			}
		}
		if math.Abs(float64(ring-centre)) < 8 || math.Abs(float64(ring-outer)) < 4 {
			return c, 0, false
		}
		// Midway between the two rings' extremes: the thin rings' edges then sit where they are.
		t := (outer + ring) / 2
		var axis [2]struct {
			at, mod float64
			n       int
		}
		for a, u := range [2]gpt{{1, 0}, {0, 1}} {
			w := gpt{u.Y, u.X} // offset direction across the walk
			for k := -3; k <= 3; k++ {
				p := c.add(w.mul(0.2 * m * float64(k)))
				i0, o0, ok0 := detectEdges(v, p, u.mul(-1), t, !inv, int(4*m)+2)
				i1, o1, ok1 := detectEdges(v, p, u, t, !inv, int(4*m)+2)
				// The ring's inner and outer edges are 3 and 5 modules apart along any line through it.
				if r := (i0 + i1) / (o0 + o1); !ok0 || !ok1 || r < 0.4 || r > 0.85 {
					continue
				}
				mid := (i1 - i0 + o1 - o0) / 4 // along u, relative to p
				axis[a].at += p.dot(u) + mid
				axis[a].mod += ((i0+i1)/3 + (o0+o1)/5) / 2
				axis[a].n++
			}
		}
		if axis[0].n < 4 || axis[1].n < 4 {
			return c, 0, false
		}
		c = gpt{axis[0].at / float64(axis[0].n), axis[1].at / float64(axis[1].n)}
		mod = (axis[0].mod/float64(axis[0].n) + axis[1].mod/float64(axis[1].n)) / 2
		if !(mod > 0.5*m && mod < 2.5*m) {
			return c, 0, false
		}
	}
	return c, mod, geomFinite(c)
}

// detectEdges walks from p along unit direction u, one pixel per step for at most n steps, starting
// on the dark side of t (or the light side when dark is false), and returns the distance to the
// first crossing of t and to the crossing back.
func detectEdges(v *geomLuma, p, u gpt, t float32, dark bool, n int) (inner, outer float64, ok bool) {
	prev := v.bilinear(p.X, p.Y)
	if (prev < t) != dark {
		return 0, 0, false
	}
	side := dark
	var got [2]float64
	k := 0
	for i := 1; i <= n; i++ {
		q := p.add(u.mul(float64(i)))
		val := v.bilinear(q.X, q.Y)
		if (val < t) != side {
			got[k] = float64(i-1) + float64((t-prev)/(val-prev))
			k++
			if k == 2 {
				return got[0], got[1], true
			}
			side = !side
		}
		prev = val
	}
	return 0, 0, false
}

// confirms reports whether quad q is a code's: its header decodes, and with the header's grid, of
// the two anchors nearest each finder at least one is located where the finders predict it (or
// both lie outside the capture). One copy can decode with one finder wrong, since a copy is placed
// mostly by the finders nearest it; the anchors check every finder.
func (d *FinderDetector) confirms(f *video.Frame, v *geomLuma, q Quad) bool {
	hs, ok := SlotReader{}.Read(f, q)
	if !ok {
		return false
	}
	gx, gy := int(hs[0].GridX), int(hs[0].GridY)
	if d.layout == nil || d.layout.GridX() != gx || d.layout.GridY() != gy {
		l, err := frame.NewLayout(gx, gy)
		if err != nil {
			return false
		}
		d.layout = l
	}
	hm, ok := headerModel(v, q)
	if !ok {
		return false
	}
	c0 := 4.5 * float64(gy) / 135
	src := []gpt{{c0, c0}, {float64(gx) - c0, c0}, {c0, float64(gy) - c0}, {float64(gx) - c0, float64(gy) - c0}}
	dst := make([]gpt, 4)
	for k, c := range q.Centres {
		dst[k] = gpt(c)
	}
	model, _, ok := geomFitModel(src, dst, hm.lens)
	refs, ok2 := headerRefs(v, &hm)
	if !ok || !ok2 {
		return false
	}
	contrast := float32(0)
	for _, r := range refs {
		contrast = max(contrast, r[1]-r[0])
	}
	anchors := d.layout.Anchors()
	for _, fc := range src {
		near := [2]int{-1, -1}
		dist := [2]float64{math.Inf(1), math.Inf(1)}
		for i, a := range anchors {
			dd := gpt{float64(a.X + 2), float64(a.Y + 2)}.sub(fc).norm()
			switch {
			case dd < dist[0]:
				near[1], dist[1], near[0], dist[0] = near[0], dist[0], i, dd
			case dd < dist[1]:
				near[1], dist[1] = i, dd
			}
		}
		seen, located := 0, 0
		for _, i := range near {
			if i < 0 {
				continue
			}
			c := gpt{float64(anchors[i].X + 2), float64(anchors[i].Y + 2)}
			pred := model.at(c)
			if !v.inside(pred) {
				continue
			}
			seen++
			ex := model.at(c.add(gpt{0.5, 0})).sub(model.at(c.sub(gpt{0.5, 0})))
			ey := model.at(c.add(gpt{0, 0.5})).sub(model.at(c.sub(gpt{0, 0.5})))
			if _, ok := rectifySaddle(v, pred, ex, ey, rectifyMinSaddle*contrast); ok {
				located++
			}
		}
		if seen > 0 && located == 0 {
			return false
		}
	}
	return true
}
