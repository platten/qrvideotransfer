package vision

import (
	"testing"

	"github.com/platten/qrvideotransfer/internal/video"
)

// TestDetectPlain: on a rendered frame the four centres are found within a tenth of a module, in
// corner order, with the right module size, at every pitch and for 16:9 and 16:10 grids.
func TestDetectPlain(t *testing.T) {
	for _, g := range []struct{ gx, gy, pitch int }{{212, 120, 4}, {480, 270, 4}, {320, 180, 8}, {432, 270, 4}, {240, 135, 8}} {
		c := geomNewCode(t, g.gx, g.gy, g.pitch, 2, 1, 1)
		want := geomPlainQuad(c)
		var d FinderDetector
		q, ok := d.Detect(geomPlain(c, 0), nil)
		if !ok {
			t.Errorf("%dx%d at %d: not found", g.gx, g.gy, g.pitch)
			continue
		}
		for k := range 4 {
			if e := geomDist(q.Centres[k], want.Centres[k]); e > want.Module/10 {
				t.Errorf("%dx%d at %d: centre %d at %v, want %v", g.gx, g.gy, g.pitch, k, q.Centres[k], want.Centres[k])
			}
		}
		if q.Module < 0.9*want.Module || q.Module > 1.1*want.Module {
			t.Errorf("%dx%d at %d: module %.2f, want %.2f", g.gx, g.gy, g.pitch, q.Module, want.Module)
		}
	}
}

// TestDetectRotated: with the screen rolled by 90, 180 and 270 degrees and mirrored-free, the inverted
// finder still names the corners.
func TestDetectRotated(t *testing.T) {
	c := geomNewCode(t, 212, 120, 4, 2, 1, 2)
	for _, roll := range []float64{90, 180, 270, 33} {
		cfg := geomConfig(t, "ideal", 1280, 1280) // square, so the rolled screen fits
		cfg.Geometry.Roll = roll
		cfg.Geometry.Fill = 0.8
		fs, ts := geomFilm(t, c, cfg, 0, 1, 5)
		var d FinderDetector
		q, ok := d.Detect(fs[0], nil)
		if !ok {
			t.Errorf("roll %v: not found", roll)
			continue
		}
		want, mod := geomFinderTruth(c, ts[0])
		for k := range 4 {
			if e := geomDist(q.Centres[k], want[k]); e > mod[k]/2 {
				t.Errorf("roll %v: centre %d at %v, want %v", roll, k, q.Centres[k], want[k])
			}
		}
	}
}

// TestDetectTracking: given the previous quad, the result is the same as without it, also when the
// previous quad is far off.
func TestDetectTracking(t *testing.T) {
	c := geomNewCode(t, 212, 120, 4, 2, 4, 3)
	cfg := geomConfig(t, "handheld", 1280, 720)
	fs, _ := geomFilm(t, c, cfg, 0, 6, 9)
	var d FinderDetector
	var prev *Quad
	for i, f := range fs {
		fresh, ok := d.Detect(f, nil)
		if !ok {
			t.Fatalf("capture %d: not found", i)
		}
		got, ok := d.Detect(f, prev)
		if !ok {
			t.Fatalf("capture %d: not found when tracking", i)
		}
		for k := range 4 {
			if geomDist(got.Centres[k], fresh.Centres[k]) > fresh.Module/4 {
				t.Errorf("capture %d: tracked centre %d at %v, fresh %v", i, k, got.Centres[k], fresh.Centres[k])
			}
		}
		prev = &got
	}
	far := Quad{Centres: [4]Point{{10, 10}, {20, 10}, {10, 20}, {20, 20}}, Module: 1}
	if _, ok := d.Detect(fs[0], &far); !ok {
		t.Error("not found with a wrong previous quad")
	}
}

// TestDetectNothing: frames without a code, and malformed frames, give no quad.
func TestDetectNothing(t *testing.T) {
	flat := &video.Frame{W: 640, H: 360, Bits: 8, Y: make([]uint16, 640*360)}
	for i := range flat.Y {
		flat.Y[i] = 120
	}
	noise := &video.Frame{W: 640, H: 360, Bits: 10, Y: make([]uint16, 640*360)}
	s := uint64(7)
	for i := range noise.Y {
		s = s*6364136223846793005 + 1442695040888963407
		noise.Y[i] = uint16(s >> 54)
	}
	for name, f := range map[string]*video.Frame{
		"flat":     flat,
		"noise":    noise,
		"nil":      nil,
		"short":    {W: 640, H: 360, Bits: 8, Y: make([]uint16, 100)},
		"tiny":     {W: 1, H: 1, Bits: 8, Y: make([]uint16, 1)},
		"bits":     {W: 4, H: 4, Bits: 3, Y: make([]uint16, 16)},
		"negative": {W: -4, H: 4, Bits: 8},
	} {
		var d FinderDetector
		if _, ok := d.Detect(f, nil); ok {
			t.Errorf("%s: found a quad", name)
		}
	}
}

// TestDetectTenBit: a 10-bit capture is detected as its 8-bit equivalent.
func TestDetectTenBit(t *testing.T) {
	c := geomNewCode(t, 212, 120, 4, 2, 1, 4)
	f := geomPlain(c, 0)
	for i := range f.Y {
		f.Y[i] *= 4
	}
	f.Bits = 10
	var d FinderDetector
	if _, ok := d.Detect(f, nil); !ok {
		t.Error("10-bit frame: not found")
	}
}

// TestDetectLarge: a capture wider than 1280 pixels is downscaled for the scan and the centres are
// still refined at full resolution.
func TestDetectLarge(t *testing.T) {
	c := geomNewCode(t, 480, 270, 8, 2, 1, 5)
	want := geomPlainQuad(c)
	var d FinderDetector
	q, ok := d.Detect(geomPlain(c, 0), nil)
	if !ok {
		t.Fatal("not found")
	}
	for k := range 4 {
		if e := geomDist(q.Centres[k], want.Centres[k]); e > want.Module/10 {
			t.Errorf("centre %d at %v, want %v", k, q.Centres[k], want.Centres[k])
		}
	}
}
