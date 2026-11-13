package channel

import (
	"math"
	"testing"
	"time"
)

// sensorOf returns a recording for testing stage 5 with camera c.
func sensorOf(t *testing.T, c Config) *recording {
	t.Helper()
	return mustRecording(t, loop(time.Second, 1, flat(c.Geometry.Width/2, c.Geometry.Height/2, 16)), c, Run{Captures: 1, Seed: 5})
}

func uniformImage(w, h int, v [3]float32) *rgb {
	img := newRGB(w, h)
	for c := range img.p {
		for i := range img.p[c] {
			img.p[c][i] = v[c]
		}
	}
	return img
}

// With no impairment, the camera's gamma 1/2.4 undoes the panel's 2.4: codes come back unchanged.
func TestSensorIdentity(t *testing.T) {
	for _, code := range []uint16{16, 50, 126, 200, 235} {
		c := still(16, 8)
		f := flat(8, 4, code)
		f.Cb[0], f.Cr[0] = 100, 150
		in := loop(time.Second, 2, f)
		fs, _ := shoot(t, in, c, Run{Captures: 1})
		g := fs[0]
		if g.Y[3*16+5] != code || g.Bits != 8 || g.W != 16 || g.H != 8 || len(g.Cb) != 32 {
			t.Errorf("code %d came back as %d", code, g.Y[3*16+5])
		}
		if code == 126 && (absDiff(g.Cb[0], 100) > 1 || absDiff(g.Cr[0], 150) > 1) {
			t.Errorf("colour %d %d came back as %d %d", 100, 150, g.Cb[0], g.Cr[0])
		}
	}
}

func absDiff(a, b uint16) int { return int(math.Abs(float64(a) - float64(b))) }

// Noise statistics: per-channel noise of sigma code values gives brightness noise of
// sigma * 219/255 * |(Kr, Kg, Kb)|, and colour noise reduction lowers the colour planes' noise.
func TestSensorNoise(t *testing.T) {
	c := still(256, 256)
	c.Sensor.Noise = 6
	rc := sensorOf(t, c)
	measure := func(rc *recording) (sy, sc float64) {
		f := rc.sensor(uniformImage(256, 256, [3]float32{0.2, 0.2, 0.2}), &shot{}, &scratch{})
		return stddev(f.Y), stddev(f.Cb)
	}
	sy, sc := measure(rc)
	k := math.Sqrt(kr709*kr709 + kg709*kg709 + kb709*kb709)
	if want := 6 * 219.0 / 255 * k; math.Abs(sy-want) > 0.05*want {
		t.Errorf("brightness noise %v, want %v", sy, want)
	}
	c.Sensor.ChromaNR = 1
	_, sc2 := measure(sensorOf(t, c))
	if !(sc2 < 0.5*sc) {
		t.Errorf("colour noise %v with reduction, %v without", sc2, sc)
	}
}

func stddev(p []uint16) float64 {
	var s, s2 float64
	for _, v := range p {
		s += float64(v)
		s2 += float64(v) * float64(v)
	}
	n := float64(len(p))
	return math.Sqrt(s2/n - (s/n)*(s/n))
}

// 4:2:0: each colour sample is the mean of its 2 x 2 block.
func TestSensorSubsampling(t *testing.T) {
	c := still(4, 2)
	rc := sensorOf(t, c)
	img := newRGB(4, 2)
	// Left block: two blue pixels and two black; right block: all red.
	img.p[2][0], img.p[2][5] = 1, 1
	for _, i := range []int{2, 3, 6, 7} {
		img.p[0][i] = 1
	}
	f := rc.sensor(img, &shot{}, &scratch{})
	wantCb := 128 + 224*0.5*0.5 // half the blocks' pixels at full blue difference 0.5
	if math.Abs(float64(f.Cb[0])-wantCb) > 0.5 || math.Abs(float64(f.Cr[1])-(128+224*0.5)) > 0.5 || f.Cb[1] >= 128 {
		t.Errorf("Cb %v Cr %v", f.Cb, f.Cr)
	}
}

// White balance shifts red and blue; the colour matrix leaves grey grey.
func TestSensorColour(t *testing.T) {
	c := still(2, 2)
	c.Sensor.Saturation = 0.3
	f := sensorOf(t, c).sensor(uniformImage(2, 2, [3]float32{0.3, 0.3, 0.3}), &shot{}, &scratch{})
	if f.Cb[0] != 128 || f.Cr[0] != 128 {
		t.Errorf("grey became %d %d", f.Cb[0], f.Cr[0])
	}
	c.Sensor.WhiteBalance = 0.1
	f = sensorOf(t, c).sensor(uniformImage(2, 2, [3]float32{0.3, 0.3, 0.3}), &shot{}, &scratch{})
	if f.Cr[0] <= 130 || f.Cb[0] >= 126 {
		t.Errorf("white balance gave %d %d", f.Cb[0], f.Cr[0])
	}
	c.Sensor.Gain, c.Sensor.WhiteBalance, c.Sensor.Saturation = 2, 0, 0 // sensor saturation clips
	f = sensorOf(t, c).sensor(uniformImage(2, 2, [3]float32{0.9, 0.9, 0.9}), &shot{}, &scratch{})
	if f.Y[0] != 235 {
		t.Errorf("clipped white = %d", f.Y[0])
	}
}

// The HLG curve: sqrt(3E) up to 1/12, then logarithmic, reaching 1 at E = 1; 10-bit codes.
func TestToneCurves(t *testing.T) {
	h := toneCurve(true)
	if !near(float64(tone(h, 1.0/12)), 0.5, 1e-6) || !near(float64(tone(h, 1)), 1, 1e-4) || !near(float64(tone(h, 0.265)), 0.75, 2e-3) {
		t.Errorf("HLG %v %v %v", tone(h, 1.0/12), tone(h, 1), tone(h, 0.265))
	}
	s := toneCurve(false)
	for _, e := range []float32{0, 0.01, 0.18, 0.5, 1} {
		if got, want := float64(tone(s, e)), math.Pow(float64(e), 1/2.4); math.Abs(got-want) > 2e-4 {
			t.Errorf("gamma(%v) = %v, want %v", e, got, want)
		}
	}
	c := still(2, 2)
	c.Sensor.Bits, c.Sensor.HLG, c.Sensor.Gain = 10, true, 1
	f := sensorOf(t, c).sensor(uniformImage(2, 2, [3]float32{1, 1, 1}), &shot{}, &scratch{})
	if f.Bits != 10 || f.Y[0] != 940 || f.Cb[0] != 512 {
		t.Errorf("10-bit white = %d %d (%d bits)", f.Y[0], f.Cb[0], f.Bits)
	}
}

// Sharpening overshoots a step edge by amount * (step / 4) on each side; fringes have the stated
// amplitude.
func TestSharpenAndFringe(t *testing.T) {
	c := still(8, 2)
	c.Sensor.Sharpen = 1
	rc := sensorOf(t, c)
	img := newRGB(8, 2)
	for _, y := range []int{0, 1} {
		for x := 4; x < 8; x++ {
			for p := range img.p {
				img.p[p][y*8+x] = 1
			}
		}
	}
	f := rc.sensor(img, &shot{}, &scratch{})
	if f.Y[3] != 0 || f.Y[4] != 255 || f.Y[2] != 16 || f.Y[5] != 235 {
		t.Errorf("sharpened edge %v", f.Y[:8])
	}
	c = still(64, 64)
	c.Sensor.Fringe = 6
	rc = sensorOf(t, c)
	f = rc.sensor(uniformImage(64, 64, [3]float32{0.2, 0.2, 0.2}), &shot{}, &scratch{})
	lo, hi := f.Y[0], f.Y[0]
	for _, v := range f.Y {
		lo, hi = min(lo, v), max(hi, v)
	}
	if spread := float64(hi-lo) / 2; math.Abs(spread-6*219.0/255) > 1 {
		t.Errorf("fringe amplitude %v codes", spread)
	}
}

func TestQuantise(t *testing.T) {
	if quantise(-3, 255) != 0 || quantise(300, 255) != 255 || quantise(12.5, 255) != 13 || quantise(12.49, 255) != 12 {
		t.Error("quantise")
	}
}
