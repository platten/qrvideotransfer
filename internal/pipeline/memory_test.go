package pipeline

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/platten/qrvideotransfer/internal/payload"
	"github.com/platten/qrvideotransfer/internal/video"
)

// TestDecodeWorkersByMemory: the decode's vision workers are capped so that their frames and
// scratch fit the decode's memory budget (SECURITY.md sections 5 and 9.5): fewer as the captures
// grow, never fewer than one, never more than asked for; an explicit count above the cap is
// clamped and says so.
func TestDecodeWorkersByMemory(t *testing.T) {
	sizes := [][2]int{{848, 480}, {1920, 1080}, {3840, 2160}, {7680, 4320}}
	prev := 1 << 30
	for _, s := range sizes {
		n, clamped := decodeWorkers(1024, s[0], s[1])
		if n < 1 || n > prev || !clamped {
			t.Errorf("%dx%d: %d workers (clamped %v) after %d for a smaller size", s[0], s[1], n, clamped, prev)
		}
		if p := int64(n) * workerBytes(s[0], s[1]); p > decodeMemBudget {
			t.Errorf("%dx%d: %d workers project %d bytes, over the budget %d", s[0], s[1], n, p, decodeMemBudget)
		}
		prev = n
		t.Logf("%dx%d: %d workers, %.0f MiB each", s[0], s[1], n, float64(workerBytes(s[0], s[1]))/(1<<20))
	}
	if n, clamped := decodeWorkers(1, 7680, 4320); n != 1 || clamped {
		t.Errorf("one worker asked for at 8K: %d (clamped %v)", n, clamped)
	}
	if n, clamped := decodeWorkers(0, 848, 480); n < 1 || n > runtime.GOMAXPROCS(0) || clamped {
		t.Errorf("the default at 480p: %d (clamped %v)", n, clamped)
	}
	if n, _ := decodeWorkers(1<<20, 1<<15, 1<<15); n != 1 {
		t.Errorf("a frame beyond any budget: %d workers, want 1", n)
	}
}

// TestWorkersClampedWarning: an explicit worker count above the memory cap is reduced, and a
// warning says so.
func TestWorkersClampedWarning(t *testing.T) {
	rec := writeRaw(t, noiseFrames(2, 1920, 1080, 3), 30) // 40 workers fit at 1080p
	var warned []string
	_, err := Decode(context.Background(), DecodeConfig{Recordings: []string{rec}, Backend: video.NewRaw(), Workers: 1000,
		TempDir: t.TempDir(), Unpack: payload.UnpackOptions{OutputDir: filepath.Join(t.TempDir(), "out")},
		Progress: func(e Event) {
			if e.Warn != nil {
				warned = append(warned, e.Warn.Code)
			}
		}})
	var short *ShortError
	if !errors.As(err, &short) {
		t.Fatalf("got %v", err)
	}
	if len(warned) != 1 || warned[0] != WarnWorkersReduced {
		t.Errorf("warnings %v, want one %s", warned, WarnWorkersReduced)
	}
}

// TestEvictionCycle: frames cycled through a cap that holds only a few of them, each record dropped
// and its frame seen again, keep the bytes held under the cap, the work per capture fixed, and the
// list of frames waiting for an attempt within twice the records held (plus a constant).
func TestEvictionCycle(t *testing.T) {
	raceSkipStatistics(t)
	key, truth := fixture(t)
	key.frames = 1 << 16
	g, err := newGeometry(key)
	if err != nil {
		t.Fatal(err)
	}
	soft := append([]int8(nil), truth[0]...)
	clear(soft[:g.sent])
	d := &decoder{c: &DecodeConfig{}, tmp: t.TempDir(), frames: map[uint32]*frameAcc{},
		softCap: 10*frameRecordBytes(g.cpf) + int64((g.cpf-1)*g.sent)}
	defer d.close()
	for i := range 3000 {
		if err := d.apply(captureResult{index: 3 * i, located: true, key: key, cells: 1, clean: 1,
			phases: []phaseBits{{frame: uint32(i % 50), soft: soft}}}); err != nil {
			t.Fatal(err)
		}
		if d.usedBytes() > d.softCap || len(d.pending) > 2*len(d.frames)+64 {
			t.Fatalf("capture %d: %d bytes held (cap %d), %d pending for %d records", i, d.usedBytes(), d.softCap, len(d.pending), len(d.frames))
		}
	}
	if per := float64(d.evictSteps) / 3000; per > 3 {
		t.Errorf("%.2f eviction steps per capture", per)
	}
	d.sendStats()
	if d.stats.FramesSeen != 50 {
		t.Errorf("%d frames seen, want 50", d.stats.FramesSeen)
	}
}

// TestEvictionWorkBounded: holding the decode at its cap costs each capture a fixed number of
// eviction steps, whether the decode holds few frames or many (the eviction's ordered structures,
// DESIGN.md section 19); and the order is the one of TestEvict and TestBookkeepingUnderCap.
func TestEvictionWorkBounded(t *testing.T) {
	raceSkipStatistics(t)
	key, truth := fixture(t)
	key.frames = 1 << 16
	g, err := newGeometry(key)
	if err != nil {
		t.Fatal(err)
	}
	soft := append([]int8(nil), truth[0]...)
	clear(soft[:g.sent])
	perFrame := frameRecordBytes(g.cpf) + int64((g.cpf-1)*g.sent)
	steps := map[int]float64{}
	for _, held := range []int{40, 1600} {
		d := &decoder{c: &DecodeConfig{}, tmp: t.TempDir(), frames: map[uint32]*frameAcc{}, softCap: int64(held) * perFrame}
		f := 0
		feed := func(n int) {
			for range n {
				if err := d.apply(captureResult{index: 3 * f, located: true, key: key, cells: 1, clean: 1,
					phases: []phaseBits{{frame: uint32(f), soft: soft}}}); err != nil {
					t.Fatal(err)
				}
				f++
			}
		}
		feed(held + 10)
		before := d.evictSteps
		feed(100)
		steps[held] = float64(d.evictSteps-before) / 100
		d.close()
	}
	t.Logf("eviction steps per capture at the cap: %v", steps)
	if steps[40] == 0 || steps[1600] > steps[40]+1 || steps[1600] > 8 {
		t.Errorf("eviction steps per capture grow with the frames held: %v", steps)
	}
}
