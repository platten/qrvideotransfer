package channel

import (
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// presetTables returns the rows of the preset tables of DESIGN.md section 15 ("Simulated camera"),
// by label, and the preset names of their header.
func presetTables(t *testing.T) (rows map[string][]string, names []string) {
	t.Helper()
	b, err := os.ReadFile("../../docs/DESIGN.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	i := strings.Index(doc, "### Simulated camera")
	j := strings.Index(doc[i:], "### Test videos")
	if i < 0 || j < 0 {
		t.Fatal("section 15 not found")
	}
	rows = map[string][]string{}
	for _, line := range strings.Split(doc[i:i+j], "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for k := range cells {
			cells[k] = strings.TrimSpace(cells[k])
		}
		if len(cells) != 7 || strings.HasPrefix(cells[0], "---") || cells[1] == "Video" {
			continue
		}
		if cells[0] == "" {
			names = nil
			for _, c := range cells[1:] {
				names = append(names, strings.Trim(c, "`"))
			}
			continue
		}
		rows[cells[0]] = cells[1:]
	}
	return rows, names
}

// num parses a table cell: a number, a percentage, or a number followed by a unit word.
func num(t *testing.T, s string) float64 {
	t.Helper()
	s = strings.TrimSpace(s)
	if s == "lossless" {
		return 0
	}
	scale := 1.0
	if strings.HasSuffix(s, "%") {
		s, scale = strings.TrimSuffix(s, "%"), 0.01
	}
	s = strings.TrimSuffix(s, " codes")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("cell %q: %v", s, err)
	}
	return v * scale
}

func msOf(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// The presets exist with exactly the values of the DESIGN.md tables, read back from the document.
func TestPresetsMatchDesign(t *testing.T) {
	rows, names := presetTables(t)
	if !slices.Equal(names, Presets()) {
		t.Fatalf("presets %v, document has %v", Presets(), names)
	}
	type field struct {
		cells int // values per cell
		get   func(c *Config) []float64
	}
	fields := map[string]field{
		"Screen fills, of frame width":                  {1, func(c *Config) []float64 { return []float64{c.Geometry.Fill} }},
		"Tilt up to, degrees":                           {1, func(c *Config) []float64 { return []float64{c.Geometry.Tilt} }},
		"Shake, pixels RMS per capture":                 {1, func(c *Config) []float64 { return []float64{c.Geometry.Shake} }},
		"Lens distortion k1":                            {1, func(c *Config) []float64 { return []float64{c.Geometry.K1} }},
		"Blur sigma, camera pixels":                     {1, func(c *Config) []float64 { return []float64{c.Optics.Blur} }},
		"Noise sigma, 8-bit code values":                {1, func(c *Config) []float64 { return []float64{c.Sensor.Noise} }},
		"Exposure / readout, ms":                        {2, func(c *Config) []float64 { return []float64{msOf(c.Timing.Exposure), msOf(c.Timing.Readout)} }},
		"Gamma error":                                   {1, func(c *Config) []float64 { return []float64{c.Display.GammaError} }},
		"White-balance shift":                           {1, func(c *Config) []float64 { return []float64{c.Sensor.WhiteBalance} }},
		"Corner vignetting":                             {1, func(c *Config) []float64 { return []float64{c.Optics.Vignette} }},
		"Sharpening amount":                             {1, func(c *Config) []float64 { return []float64{c.Sensor.Sharpen} }},
		"Fringe / flicker amplitude":                    {2, func(c *Config) []float64 { return []float64{c.Sensor.Fringe, c.Display.FlickerAmp} }},
		"Glare patch, share of code area":               {1, func(c *Config) []float64 { return []float64{c.Optics.Glare} }},
		"Dropped captures":                              {1, func(c *Config) []float64 { return []float64{c.Timing.Drop} }},
		"Recorder bitrate, Mbit/s":                      {1, func(c *Config) []float64 { return []float64{c.Recorder.Bitrate} }},
		"Panel black, share of white":                   {1, func(c *Config) []float64 { return []float64{c.Display.Black} }},
		"Panel response, ms":                            {1, func(c *Config) []float64 { return []float64{msOf(c.Display.Response)} }},
		"Capture-time jitter, ms RMS":                   {1, func(c *Config) []float64 { return []float64{msOf(c.Timing.Jitter)} }},
		"Colour matrix saturation boost":                {1, func(c *Config) []float64 { return []float64{c.Sensor.Saturation} }},
		"Colour noise reduction, share":                 {1, func(c *Config) []float64 { return []float64{c.Sensor.ChromaNR} }},
		"Veiling glare, share of white":                 {1, func(c *Config) []float64 { return []float64{c.Optics.Veil} }},
		"Exposure gain, linear signal of display white": {1, func(c *Config) []float64 { return []float64{c.Sensor.Gain} }},
	}
	seen := 0
	for label, cells := range rows {
		if label == "Camera" {
			seen++
			for k, name := range names {
				c, _ := Preset(name)
				g := c.Geometry
				res := map[int]string{2160: "4K", 1080: "1080p"}[g.Height]
				want := res + strconv.Itoa(c.Timing.FPS)
				if c.Sensor.HLG {
					want += ", " + strconv.Itoa(c.Sensor.Bits) + "-bit HLG"
				}
				if cells[k] != want || g.Width*9 != g.Height*16 || (c.Sensor.Bits == 10) != c.Sensor.HLG {
					t.Errorf("%s camera %dx%d@%d, %d-bit, document %q", name, g.Width, g.Height, c.Timing.FPS, c.Sensor.Bits, cells[k])
				}
			}
			continue
		}
		f, ok := fields[label]
		if !ok {
			t.Errorf("document row %q is not checked", label)
			continue
		}
		seen++
		for k, name := range names {
			c, _ := Preset(name)
			parts := strings.Split(cells[k], "/")
			if len(parts) == 1 && f.cells == 2 {
				parts = append(parts, parts[0]) // "0" stands for "0 / 0"
			}
			got := f.get(&c)
			for p := range parts {
				if want := num(t, parts[p]); !near(got[p], want, 1e-12) {
					t.Errorf("%s %q part %d = %v, document %v", name, label, p, got[p], want)
				}
			}
		}
	}
	if seen != len(fields)+1 {
		t.Errorf("checked %d rows, want %d", seen, len(fields)+1)
	}
	// The values every preset shares.
	for _, name := range names {
		c, err := Preset(name)
		if err != nil {
			t.Fatal(err)
		}
		d, tm, g := c.Display, c.Timing, c.Geometry
		if d.ScanOut != time.Second/60 || d.FlickerHz != 240 || d.PanelW != 0 || d.PanelH != 0 || d.SubPixel ||
			tm.DropBurst != 0 || tm.SeamFreeze != 0 || g.Roll != 0 || g.Mirror || c.Name != name {
			t.Errorf("%s: shared values differ: %+v", name, c)
		}
		if err := c.validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestPresetUnknown(t *testing.T) {
	if _, err := Preset("nope"); err == nil {
		t.Error("unknown preset accepted")
	}
}

func TestResize(t *testing.T) {
	c, _ := Preset("tripod")
	r := c.Resize(384, 216)
	if r.Geometry.Width != 384 || r.Geometry.Height != 216 || !near(QuantiserStep(r), QuantiserStep(c), 1e-12) || r.Optics.Blur != c.Optics.Blur {
		t.Errorf("Resize: %+v", r)
	}
	if c.Geometry.Width != 3840 {
		t.Error("Resize changed its receiver")
	}
}

func TestValidate(t *testing.T) {
	bad := []func(c *Config){
		func(c *Config) { c.Geometry.Width = 101 },
		func(c *Config) { c.Geometry.Height = 0 },
		func(c *Config) { c.Geometry.Width = 1 << 15 },
		func(c *Config) { c.Display.PanelW = -1 },
		func(c *Config) { c.Geometry.Fill = 0 },
		func(c *Config) { c.Geometry.Tilt = 90 },
		func(c *Config) { c.Geometry.K1 = -0.1 },
		func(c *Config) { c.Timing.FPS = 0 },
		func(c *Config) { c.Timing.Exposure = 0 },
		func(c *Config) { c.Timing.Drop = 1 },
		func(c *Config) { c.Sensor.Bits = 12 },
		func(c *Config) { c.Sensor.Gain = 0 },
		func(c *Config) { c.Optics.Glare = 1 },
		func(c *Config) { c.Sensor.ChromaNR = 2 },
	}
	for i, f := range bad {
		c, _ := Preset("handheld")
		f(&c)
		if err := c.validate(); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}
