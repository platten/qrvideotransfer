package qrvt

import (
	"slices"
	"testing"

	"github.com/platten/qrvideotransfer/internal/channel"
)

// TestFilmPrefix: a recording of k camera slots is the first captures of a recording of more slots
// with the same seed, on the presets with shake, drops and glare. That is what lets the short
// matrix film one pass first (filmPrefix): decoding applies captures in order and stops once every
// block is complete, so a prefix that decodes means the whole recording decodes the same way.
func TestFilmPrefix(t *testing.T) {
	in, _ := payloadDir(t, 40, 3000)
	path, _ := encodeTest(t, EncodeOptions{Inputs: []string{in}})
	frames := readRaw(t, path)
	for _, name := range []string{"handheld", "harsh"} {
		c, err := channel.Preset(name)
		if err != nil {
			t.Fatal(err)
		}
		c = c.Resize(960, 540)
		short := readRaw(t, film(t, frames, 12, c, channel.Run{Captures: 7, Seed: 3}))
		long := readRaw(t, film(t, frames, 12, c, channel.Run{Captures: 12, Seed: 3}))
		if len(short) == 0 || len(long) < len(short) {
			t.Fatalf("%s: %d and %d captures", name, len(short), len(long))
		}
		for i, f := range short {
			g := long[i]
			if f.PTS != g.PTS || !slices.Equal(f.Y, g.Y) || !slices.Equal(f.Cb, g.Cb) || !slices.Equal(f.Cr, g.Cr) {
				t.Errorf("%s: capture %d differs", name, i)
			}
		}
	}
}
