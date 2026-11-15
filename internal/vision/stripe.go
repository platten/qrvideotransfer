package vision

import (
	"math"
	"slices"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
)

// Constants of stripe cancellation.
const (
	stripeIters = 6  // estimates; each removes what the last missed
	peakFactor  = 20 // a bin is a peak when its power exceeds this many times the median
	nearFactor  = 4  // a peak's neighbours go with it when their power exceeds this many medians
	medianBins  = 1 << 14
)

// CancelStripes removes plane waves from the cells (DESIGN.md section 10, step 6): it subtracts
// each cell's decided level, takes the 2-D Fourier transform of the residual on the cell grid,
// keeps the peaks that stand above the noise and subtracts their inverse transform from obs, plane
// by plane for the planes c uses. It returns the number of spectral bins removed.
//
// The grid is padded with zeros to the next sizes with no prime factor above 5, and cells that are
// not data cells or have no usable observation are zero too. Those gaps, about a tenth of the grid,
// make one estimate fall short of the stripe by about as much, so the estimate is repeated on what
// is left, with the levels decided afresh each time, up to stripeIters times. A peak is a bin whose power exceeds
// peakFactor times the median power; its eight neighbours go with it when they exceed nearFactor
// times the median, which catches a wave whose frequency drifts across the capture (perspective).
// The constant bin is never removed: the levels' own offsets belong to the fit.
func CancelStripes(obs []modem.Obs, l *frame.Layout, f *Field, c *modem.Constellation) int {
	return cancelStripes(obs, l, f, c, nil)
}

// cancelStripes is CancelStripes leaving out the cells that skip marks.
func cancelStripes(obs []modem.Obs, l *frame.Layout, f *Field, c *modem.Constellation, skip []bool) int {
	if f == nil {
		return 0
	}
	cells := l.DataCells()
	n := min(len(obs), len(cells))
	w, h := smooth(l.GridX()), smooth(l.GridY())
	np := planes(c)
	stats := make([]modem.LocalStats, 0, n)
	idx := make([]int, 0, n) // usable cells, with their statistics and grid positions
	lat := f.lattice()
	for i := range n {
		if !usable(obs[i]) || skipped(skip, i) {
			continue
		}
		cl := cells[i]
		k, fx, fy := lat.locate(float64(cl.X)+0.5-float64(f.TileW)/2, float64(cl.Y)+0.5-float64(f.TileH)/2, true)
		stats = append(stats, f.interpolate(k, fx, fy))
		idx = append(idx, i)
	}
	if len(idx) == 0 {
		return 0
	}
	re, im := make([][]float64, np), make([][]float64, np)
	for p := range np {
		re[p], im[p] = make([]float64, w*h), make([]float64, w*h)
	}
	peak := make([]bool, w*h)
	stripe := make([][3]float64, len(idx))
	ft := newFFT2(w, h)
	removed := 0
	for range stripeIters {
		found := 0
		for p := range np {
			clear(re[p])
			clear(im[p])
		}
		for u, i := range idx {
			o := obs[i]
			for p := range np {
				setPlane(&o, p, plane(o, p)-float32(stripe[u][p]))
			}
			ce := stats[u].Centre[decide(o, &stats[u], np)]
			for p := range np {
				re[p][int(cells[i].Y)*w+int(cells[i].X)] = float64(plane(o, p) - ce[p])
			}
		}
		for p := range np {
			ft.transform(re[p], im[p], false)
			k := markPeaks(re[p], im[p], peak, w, h)
			if k == 0 {
				continue
			}
			found += k
			for j := range re[p] {
				if !peak[j] {
					re[p][j], im[p][j] = 0, 0
				}
			}
			ft.transform(re[p], im[p], true)
			for u, i := range idx {
				stripe[u][p] += re[p][int(cells[i].Y)*w+int(cells[i].X)]
			}
		}
		if found == 0 {
			break
		}
		removed += found
	}
	for u, i := range idx {
		for p := range np {
			setPlane(&obs[i], p, plane(obs[i], p)-float32(stripe[u][p]))
		}
	}
	return removed
}

// decide returns the level an observation most likely shows under st.
func decide(o modem.Obs, st *modem.LocalStats, np int) int {
	best, lv := math.Inf(1), 0
	for l := range st.Levels {
		d := 0.0
		for p := range np {
			x := float64(plane(o, p) - st.Centre[l][p])
			d += x * x
		}
		v := float64(max(st.Variance[l], modem.MinVariance))
		if m := d/(2*v) + float64(np)/2*math.Log(v); m < best {
			best, lv = m, l
		}
	}
	return lv
}

// plane returns plane p (0 Y, 1 Cb, 2 Cr) of an observation.
func plane(o modem.Obs, p int) float32 {
	switch p {
	case 1:
		return o.Cb
	case 2:
		return o.Cr
	}
	return o.Y
}

// setPlane sets plane p of an observation.
func setPlane(o *modem.Obs, p int, v float32) {
	switch p {
	case 1:
		o.Cb = v
	case 2:
		o.Cr = v
	default:
		o.Y = v
	}
}

// markPeaks marks the peaks of a spectrum and their strong neighbours and returns how many bins
// it marked.
func markPeaks(re, im []float64, peak []bool, w, h int) int {
	power := func(j int) float64 { return float64(re[j]*re[j]) + float64(im[j]*im[j]) }
	stride := max(1, len(re)/medianBins)
	sample := make([]float64, 0, len(re)/stride+1)
	for j := 1; j < len(re); j += stride {
		sample = append(sample, power(j))
	}
	slices.Sort(sample)
	med := sample[len(sample)/2]
	clear(peak)
	marked := 0
	mark := func(j int) {
		if !peak[j] && j != 0 {
			peak[j] = true
			marked++
		}
	}
	for j := 1; j < len(re); j++ {
		if power(j) <= peakFactor*med {
			continue
		}
		mark(j)
		x, y := j%w, j/w
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				k := (y+dy+h)%h*w + (x+dx+w)%w
				if power(k) > nearFactor*med {
					mark(k)
				}
			}
		}
	}
	return marked
}

// smooth returns the smallest integer at least n with no prime factor above 5.
func smooth(n int) int {
	for m := max(n, 1); ; m++ {
		r := m
		for _, p := range [3]int{2, 3, 5} {
			for r%p == 0 {
				r /= p
			}
		}
		if r == 1 {
			return m
		}
	}
}

// fft2 is a 2-D mixed-radix Fourier transform of a fixed size whose sides have no prime factor
// above 5.
type fft2 struct {
	w, h   int
	row    *fft1
	col    *fft1
	cr, ci []float64 // one column
}

func newFFT2(w, h int) *fft2 {
	return &fft2{w: w, h: h, row: newFFT1(w), col: newFFT1(h), cr: make([]float64, h), ci: make([]float64, h)}
}

// transform replaces the w x h array (row-major) by its discrete Fourier transform, or with inverse
// set by its inverse, divided by w h.
func (t *fft2) transform(re, im []float64, inverse bool) {
	for y := range t.h {
		t.row.transform(re[y*t.w:(y+1)*t.w], im[y*t.w:(y+1)*t.w], inverse)
	}
	for x := range t.w {
		for y := range t.h {
			t.cr[y], t.ci[y] = re[y*t.w+x], im[y*t.w+x]
		}
		t.col.transform(t.cr, t.ci, inverse)
		for y := range t.h {
			re[y*t.w+x], im[y*t.w+x] = t.cr[y], t.ci[y]
		}
	}
	if inverse {
		s := 1 / float64(t.w*t.h)
		for j := range re {
			re[j], im[j] = float64(re[j]*s), float64(im[j]*s)
		}
	}
}

// fft1 is a 1-D mixed-radix (2, 3, 5) Cooley-Tukey transform of length n. Its products are
// converted explicitly so that no platform fuses them into FMA instructions: the transform gives
// identical results everywhere. The rest of stripe cancellation and the EM fit do not promise that;
// their level decisions may differ in the last bit between platforms.
type fft1 struct {
	n       int
	factors []int
	cos     []float64 // cos(2 pi j / n)
	sin     []float64 // sin(2 pi j / n)
	br, bi  []float64 // scratch: the input
	tr, ti  [5]float64
}

func newFFT1(n int) *fft1 {
	t := &fft1{n: n, cos: make([]float64, n), sin: make([]float64, n), br: make([]float64, n), bi: make([]float64, n)}
	for r := n; r > 1; {
		for _, p := range [3]int{2, 3, 5} {
			for r%p == 0 {
				t.factors = append(t.factors, p)
				r /= p
			}
		}
	}
	for j := range n {
		t.sin[j], t.cos[j] = math.Sincos(2 * math.Pi * float64(j) / float64(n))
	}
	return t
}

// transform replaces re, im (length n) by their transform; the inverse is not scaled.
func (t *fft1) transform(re, im []float64, inverse bool) {
	copy(t.br, re)
	copy(t.bi, im)
	sign := -1.0
	if inverse {
		sign = 1
	}
	t.step(re, im, t.br, t.bi, t.n, 1, t.factors, sign)
}

// step writes the length-n transform of src[0], src[stride], ... to dst, recursively.
func (t *fft1) step(dr, di, sr, si []float64, n, stride int, factors []int, sign float64) {
	if n == 1 {
		dr[0], di[0] = sr[0], si[0]
		return
	}
	p, m := factors[0], n/factors[0]
	for q := range p {
		t.step(dr[q*m:], di[q*m:], sr[q*stride:], si[q*stride:], m, stride*p, factors[1:], sign)
	}
	// X[k + r m] = sum over q of W_n^(q (k + r m)) Y_q[k], with W_n = exp(sign 2 pi i / n).
	tw := t.n / n // twiddle index step for length n
	for k := range m {
		for q := range p {
			t.tr[q], t.ti[q] = dr[q*m+k], di[q*m+k]
		}
		for r := range p {
			e := k + r*m
			sumR, sumI := t.tr[0], t.ti[0]
			for q := 1; q < p; q++ {
				j := q * e % n * tw
				c, s := t.cos[j], float64(sign*t.sin[j])
				sumR += float64(c*t.tr[q]) - float64(s*t.ti[q])
				sumI += float64(c*t.ti[q]) + float64(s*t.tr[q])
			}
			dr[e], di[e] = sumR, sumI
		}
	}
}
