//go:build realdevice

package video

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestRealRecordings probes and decodes the phone recordings in $QRVT_RECORDINGS/s2 with the
// embedded engine (all files at once, one instance each, so the speeds are under that load), compares the decoded frame count with the
// probe and reports decode speed. The recordings are never committed; everything reported comes from
// the probe, not the file names.
//
//	QRVT_RECORDINGS=dir go test -tags realdevice -run Real -v -timeout 3h ./internal/video/
func TestRealRecordings(t *testing.T) {
	root := os.Getenv("QRVT_RECORDINGS")
	if root == "" {
		t.Skip("QRVT_RECORDINGS not set")
	}
	files, _ := filepath.Glob(filepath.Join(root, "s2", "*"))
	if len(files) == 0 {
		t.Skip("no recordings in ", filepath.Join(root, "s2"))
	}
	b := embedded(t)
	// QRVT_CLOSE_ON_DONE=0 measures with close-on-cancel off (the speed the owner's worker processes
	// are to restore); with it on, the 4K files would take hours.
	if os.Getenv("QRVT_CLOSE_ON_DONE") == "0" {
		e, err := newEngine(t.Context(), defaultCacheDir(), false)
		if err != nil {
			t.Fatal(err)
		}
		b = &Embedded{ff{r: e}}
	}
	r := b.r
	rows := make([]string, len(files))
	var wg sync.WaitGroup
	for i, path := range files {
		wg.Go(func() {
			p, err := probe(t.Context(), r, path)
			if err != nil {
				t.Errorf("%s: %v", filepath.Base(path), err)
				return
			}
			d, err := b.NewDecoder(t.Context(), path, DecodeConfig{BrightnessOnly: true, BitDepth: p.BitDepth})
			if err != nil {
				t.Errorf("%s: %v", filepath.Base(path), err)
				return
			}
			start := time.Now()
			var f Frame
			n := 0
			for {
				err = d.Next(&f)
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Errorf("%s: frame %d: %v", filepath.Base(path), n, err)
					break
				}
				n++
			}
			el := time.Since(start)
			_ = d.Close()
			ratio := float64(p.Frames) * float64(p.Width) * float64(p.Height) / float64(p.Size)
			rows[i] = fmt.Sprintf("| %s | %dx%d | %d | %.2f (%d/%d) | %s %s | %d | %s | %.1f | %v | %.1f | %d | %d | %.2f | %s |",
				filepath.Base(path), p.Width, p.Height, p.Rotation, float64(p.FPSNum)/float64(p.FPSDen), p.FPSNum, p.FPSDen,
				p.Codec, p.Profile, p.BitDepth, p.Transfer, float64(p.BitRate)/1e6, p.DolbyVis, p.Duration.Seconds(),
				p.Frames, n, float64(n)/el.Seconds(), fmt.Sprintf("%.1f", ratio))
			if int64(n) != p.Frames {
				t.Errorf("%s: decoded %d frames, probe says %d", filepath.Base(path), n, p.Frames)
			}
		})
	}
	wg.Wait()
	t.Log("\n| File | Size | Rotation | Frame rate | Codec, profile | Bits | Transfer | Mbit/s | Dolby Vision | Seconds | Frames (probe) | Frames decoded | Decode fps | Pixels per byte |\n" +
		"| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n" + strings.Join(rows, "\n"))
}
