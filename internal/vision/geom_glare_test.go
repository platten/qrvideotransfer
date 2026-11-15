package vision

import (
	"math"
	"slices"
	"sync"
	"testing"

	"github.com/platten/qrvideotransfer/internal/channel"
	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
	"github.com/platten/qrvideotransfer/internal/prng"
	"github.com/platten/qrvideotransfer/internal/video"
)

// geomDisc is a glare patch in display (video) pixels.
type geomDisc struct{ u, v, r float64 }

// geomGlare paints glare discs onto a copy of capture f: inside the disc the sensor's clipped white
// (limited-range white 235 with neutral colour), blending linearly to the capture over the next
// quarter radius, as the simulator's specular patch does. It is painted in code values after the
// fact, an approximation of the simulator's patch, which adds light before the tone curve.
func geomGlare(f *video.Frame, tr *channel.Truth, discs []geomDisc) *video.Frame {
	g := *f
	g.Y = slices.Clone(f.Y)
	g.Cb, g.Cr = slices.Clone(f.Cb), slices.Clone(f.Cr)
	white := float64(int(235) << (f.Bits - 8))
	grey := float64(int(128) << (f.Bits - 8))
	for _, d := range discs {
		cx, cy := tr.Point(d.u, d.v)
		ex, ey := tr.Point(d.u+d.r, d.v)
		r := math.Hypot(ex-cx, ey-cy)
		x0, x1 := max(0, int(cx-1.25*r)), min(f.W-1, int(cx+1.25*r))
		y0, y1 := max(0, int(cy-1.25*r)), min(f.H-1, int(cy+1.25*r))
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				dist := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
				a := math.Min(1, math.Max(0, (1.25*r-dist)/(0.25*r)))
				if a == 0 {
					continue
				}
				i := y*f.W + x
				g.Y[i] = uint16(math.Round(a*white + (1-a)*float64(g.Y[i])))
				if g.Cb != nil {
					j := (y/2)*((f.W+1)/2) + x/2
					g.Cb[j] = uint16(math.Round(a*grey + (1-a)*float64(g.Cb[j])))
					g.Cr[j] = uint16(math.Round(a*grey + (1-a)*float64(g.Cr[j])))
				}
			}
		}
	}
	return &g
}

// geomGlareStats are the counts of the glare study for one case.
type geomGlareStats struct {
	poses, found, wrong, header, wrongHeader, meshFailed int
	sumSq                                                float64
	cells                                                int
	inside, insideSaturated, insideWhite                 int // data cells under a patch
}

// geomGlareCase is one placement of glare patches, given the code and a pose number.
type geomGlareCase struct {
	name  string
	discs func(c *geomCode, i int) []geomDisc
}

// geomGlareCases returns the targeted cases of the PR #30 review: a patch of share 2% of the code
// area (enough to cover a finder and its surroundings) over each finder, over one and then two
// header copies, over an anchor cluster near the centre and near the left edge; and patches of 5%
// and 20% at random places on the code.
func geomGlareCases(c *geomCode) []geomGlareCase {
	p := float64(c.pitch)
	gx, gy := float64(c.layout.GridX()), float64(c.layout.GridY())
	area := gx * gy * p * p
	radius := func(share float64) float64 { return math.Sqrt(share * area / math.Pi) }
	q := geomPlainQuad(c)
	at := func(f frame.PointF) (float64, float64) {
		return q.Centres[0].X + f.X*(q.Centres[1].X-q.Centres[0].X), q.Centres[0].Y + f.Y*(q.Centres[2].Y-q.Centres[0].Y)
	}
	var cases []geomGlareCase
	for k := range 4 {
		cases = append(cases, geomGlareCase{[]string{"finder TL", "finder TR", "finder BL", "finder BR"}[k], func(*geomCode, int) []geomDisc {
			return []geomDisc{{q.Centres[k].X, q.Centres[k].Y, radius(0.02)}}
		}})
	}
	copyDisc := func(cp int) geomDisc {
		u, v := at(frame.HeaderSlot(cp, 29, 1))
		return geomDisc{u, v, radius(0.02)}
	}
	cases = append(cases,
		geomGlareCase{"header copy 0", func(*geomCode, int) []geomDisc { return []geomDisc{copyDisc(0)} }},
		geomGlareCase{"header copies 0 and 1", func(*geomCode, int) []geomDisc { return []geomDisc{copyDisc(0), copyDisc(1)} }},
		geomGlareCase{"anchors at the centre", func(*geomCode, int) []geomDisc {
			return []geomDisc{{gx / 2 * p, gy / 2 * p, radius(0.02)}}
		}},
		geomGlareCase{"anchors at the left edge", func(*geomCode, int) []geomDisc {
			return []geomDisc{{30 * p, gy / 2 * p, radius(0.02)}}
		}},
	)
	for _, share := range []float64{0.05, 0.20} {
		name := map[float64]string{0.05: "random 5%", 0.20: "random 20%"}[share]
		cases = append(cases, geomGlareCase{name, func(_ *geomCode, i int) []geomDisc {
			g := prng.SplitMix64{State: prng.Seed("qrvt/vision/glare", []byte{byte(i), byte(i >> 8), byte(share * 100)})}
			r := radius(share)
			w, h := gx*p, gy*p
			uni := func() float64 { return float64(g.Next()>>11) / (1 << 53) }
			rw, rh := math.Min(r, w/2), math.Min(r, h/2)
			return []geomDisc{{rw + uni()*(w-2*rw), rh + uni()*(h-2*rh), r}}
		}})
	}
	return cases
}

// geomGlareMeasure runs the geometry on one glared capture and adds the outcome to s.
func geomGlareMeasure(c *geomCode, f *video.Frame, tr *channel.Truth, discs []geomDisc, s *geomGlareStats) {
	s.poses++
	var d FinderDetector
	q, ok := d.Detect(f, nil)
	if !ok {
		return
	}
	want, mod := geomFinderTruth(c, tr)
	for k := range 4 {
		if geomDist(q.Centres[k], want[k]) > mod[k]/2 {
			s.wrong++
			return
		}
	}
	s.found++
	hs, _ := SlotReader{}.Read(f, q)
	shown := geomShown(c, tr)
	bad := len(hs) == 0
	for _, h := range hs {
		if int(h.FrameIndex) >= len(c.headers) || h != c.headers[h.FrameIndex] || !slices.Contains(shown, int(h.FrameIndex)) {
			bad = true
			s.wrongHeader++
			break
		}
	}
	if !bad {
		s.header++
	}
	m, _, err := MeshFitter{}.Fit(f, q, c.layout)
	if err != nil {
		s.meshFailed++
		return
	}
	p := float64(c.pitch)
	under := func(gx, gy int, k float64) bool {
		u, v := (float64(gx)+0.5)*p, (float64(gy)+0.5)*p
		for _, dc := range discs {
			if math.Hypot(u-dc.u, v-dc.v) <= k*dc.r {
				return true
			}
		}
		return false
	}
	rms, n := channel.CentreError(tr, func(gx, gy int) (float64, float64, bool) {
		if !geomMeasured(gx, gy) || under(gx, gy, 1.5) {
			return 0, 0, false
		}
		pt := m.At(float64(gx)+0.5, float64(gy)+0.5)
		return pt.X, pt.Y, true
	})
	s.sumSq += rms * rms * float64(n)
	s.cells += n
	var in []frame.Cell
	for _, cell := range c.layout.DataCells() {
		if under(int(cell.X), int(cell.Y), 1) {
			in = append(in, cell)
		}
	}
	obs := make([]modem.Obs, len(in))
	FootprintSampler{}.Sample(obs, f, m, in)
	for _, o := range obs {
		s.inside++
		if o.Saturated {
			s.insideSaturated++
		} else if o.Y > 225 {
			s.insideWhite++
		}
	}
}

// TestGlareStudy is the glare study of the PR #30 review, on the handheld preset at 4K with a
// 480 x 270 grid: 200 poses (seeds 20000 to 20199), each measured under every case of
// geomGlareCases. It reports, per case, the finders found, wrong quads, headers read, wrong
// headers, the cell-centre error outside 1.5 patch radii, and how the data cells under a patch
// come back (marked saturated, or as near-white values). It only logs; it runs with
// QRVT_LONG_TESTS=1.
func TestGlareStudy(t *testing.T) {
	if !geomLong() {
		t.Skip("set QRVT_LONG_TESTS=1")
	}
	const n = 200
	c := geomNewCode(t, 480, 270, 4, 2, 4, 17)
	cfg := geomConfig(t, "handheld", 0, 0)
	cases := geomGlareCases(c)
	stats := make([]geomGlareStats, len(cases))
	var mu sync.Mutex
	var filmErr error
	geomParallel(n, func(i int) {
		p, err := geomPose(c, cfg, n, i, 20000)
		if err != nil {
			mu.Lock()
			filmErr = err
			mu.Unlock()
			return
		}
		for k, gc := range cases {
			discs := gc.discs(c, i)
			var s geomGlareStats
			geomGlareMeasure(c, geomGlare(p.f, p.tr, discs), p.tr, discs, &s)
			mu.Lock()
			st := &stats[k]
			st.poses += s.poses
			st.found += s.found
			st.wrong += s.wrong
			st.header += s.header
			st.wrongHeader += s.wrongHeader
			st.meshFailed += s.meshFailed
			st.sumSq += s.sumSq
			st.cells += s.cells
			st.inside += s.inside
			st.insideSaturated += s.insideSaturated
			st.insideWhite += s.insideWhite
			mu.Unlock()
		}
	})
	if filmErr != nil {
		t.Fatal(filmErr)
	}
	for k, gc := range cases {
		s := stats[k]
		t.Logf("GLARE %-24s found %3d/%d wrong %d | header %3d wrong %d | mesh failed %d | centre RMS outside %.4f cell over %d | under patch %d cells: %.1f%% saturated, %.1f%% near-white unflagged",
			gc.name, s.found, s.poses, s.wrong, s.header, s.wrongHeader, s.meshFailed, math.Sqrt(s.sumSq/float64(max(1, s.cells))), s.cells,
			s.inside, 100*float64(s.insideSaturated)/float64(max(1, s.inside)), 100*float64(s.insideWhite)/float64(max(1, s.inside)))
	}
}

// TestGeometryHarshLong measures the harsh preset (shake 8 pixels, tilt to 25 degrees, blur 2,
// a specular glare patch of 2% and veiling glare of 3%) on 1,000 random 4K poses (seeds 30000 to
// 30999), 480 x 270 grid: finders, headers, wrong quads and headers counted separately, and the
// cell-centre error. It only logs; it runs with QRVT_LONG_TESTS=1.
func TestGeometryHarshLong(t *testing.T) {
	if !geomLong() {
		t.Skip("set QRVT_LONG_TESTS=1")
	}
	c := geomNewCode(t, 480, 270, 4, 2, 4, 18)
	geomSweep(t, c, geomConfig(t, "harsh", 0, 0), 1000, 30000)
}
