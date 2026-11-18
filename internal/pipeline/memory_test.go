package pipeline

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/frame"
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

// dataCells is the data cells of a gx x gy grid.
func dataCells(t *testing.T, gx, gy int) int {
	t.Helper()
	l, err := frame.NewLayout(gx, gy)
	if err != nil {
		t.Fatal(err)
	}
	return len(l.DataCells())
}

// TestStageConcurrencyByGrid: the grid stage admits fewer captures at once as the grid grows, never
// fewer than one, and a grid whose stage alone exceeds the budget takes all of it (DESIGN.md
// section 19). The grids: the 1080p and 4K presets' at 4-pixel cells, and the largest a header can
// claim under the vision's 1.5-pixel gate at 3840 x 2160.
func TestStageConcurrencyByGrid(t *testing.T) {
	grids := [][2]int{{212, 120}, {480, 270}, {960, 540}, {2047, 1393}}
	for _, size := range [][2]int{{848, 480}, {3840, 2160}, {7680, 4320}} {
		workers, _ := decodeWorkers(64, size[0], size[1])
		g := newStageGate(stageBudget(workers, size[0], size[1]))
		prev := 1 << 30
		for _, gr := range grids {
			cells := dataCells(t, gr[0], gr[1])
			n := int(g.budget / g.weight(cells))
			if n < 1 || n > prev {
				t.Errorf("%v captures, %v grid: %d at once after %d", size, gr, n, prev)
			}
			if unitBytes(cells) >= g.budget && g.weight(cells) != g.budget {
				t.Errorf("%v captures, %v grid: a unit over the budget takes %d of %d", size, gr, g.weight(cells), g.budget)
			}
			prev = n
			t.Logf("%dx%d captures, %d workers, stage budget %d MiB: %dx%d grid, %d MiB a unit, %d at once",
				size[0], size[1], workers, g.budget>>20, gr[0], gr[1], unitBytes(cells)>>20, n)
		}
		if prev != 1 {
			t.Errorf("%v captures: the largest grid runs %d at once", size, prev)
		}
		if p := int64(workers)*workerBytes(size[0], size[1]) + g.budget; p > decodeMemBudget && workers > 1 {
			t.Errorf("%v captures: %d workers and the stage project %d bytes, over %d", size, workers, p, decodeMemBudget)
		}
	}
}

// TestStageGateGridChange: captures whose grids change from one to the next, small and large in
// turn, never hold more of the stage than its budget at once (a grid change cannot raise the
// concurrency the gate allows), and a unit over the budget runs alone; a capture left waiting when
// the decode ends releases nothing it did not take.
func TestStageGateGridChange(t *testing.T) {
	small, mid, large := dataCells(t, 480, 270), dataCells(t, 960, 540), dataCells(t, 2047, 1393)
	g := newStageGate(stageBudget(5, 3840, 2160))
	if g.weight(large) != g.budget || g.weight(mid) > g.budget || 2*g.weight(small) > g.budget {
		t.Fatalf("weights %d %d %d, budget %d", g.weight(small), g.weight(mid), g.weight(large), g.budget)
	}
	var wg sync.WaitGroup
	var alone atomic.Int32
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := range 50 {
				cells := []int{small, mid, large, small}[(i+k)%4]
				release, err := g.enter(context.Background(), cells)
				if err != nil {
					t.Error(err)
					return
				}
				g.mu.Lock()
				if g.used > g.budget || cells == large && g.inside != 1 {
					alone.Add(1)
				}
				g.mu.Unlock()
				release()
			}
		}()
	}
	wg.Wait()
	if alone.Load() != 0 || g.maxUsed > g.budget || g.used != 0 || g.inside != 0 {
		t.Errorf("%d admissions broke the budget; most held %d of %d; left %d held by %d", alone.Load(), g.maxUsed, g.budget, g.used, g.inside)
	}
	if g.maxInside < 2 {
		t.Errorf("small grids never shared the stage (%d at once)", g.maxInside)
	}

	// A capture waiting behind a full gate when the decode ends.
	release, _ := g.enter(context.Background(), large)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() {
		_, err := g.enter(ctx, small)
		done <- err
	}()
	cancel()
	if err := <-done; err == nil {
		t.Error("a capture waiting when the decode ended was admitted")
	}
	release()
	if g.used != 0 || g.inside != 0 || len(g.waiters) != 0 {
		t.Errorf("after the end: %d held by %d, %d waiting", g.used, g.inside, len(g.waiters))
	}
	var none *stageGate
	if r, err := none.enter(ctx, large); err != nil || r == nil {
		t.Errorf("no gate: %v", err)
	}
}

// TestWorkersClampedWarning:an explicit worker count above the memory cap is reduced, and a
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
// eviction steps and a fixed time, whether the decode holds 40 frames or 1,600 (the eviction's
// ordered structures, DESIGN.md section 19); the order is the one of TestEvict and
// TestBookkeepingUnderCap. Steps alone would not tell a rescan of everything held from a heap, one
// step an eviction alike; the time does: a rescan costs about 40 times as much at 1,600 frames, the
// heaps about the same. The time is the least of five runs of 100 captures.
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
	steps, cost := map[int]float64{}, map[int]time.Duration{}
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
		cost[held] = time.Hour
		for range 5 {
			t0 := time.Now()
			feed(100)
			cost[held] = min(cost[held], time.Since(t0)/100)
		}
		steps[held] = float64(d.evictSteps-before) / 500
		d.close()
	}
	t.Logf("eviction steps per capture at the cap: %v; time per capture: %v", steps, cost)
	if steps[40] == 0 || steps[1600] > steps[40]+1 || steps[1600] > 8 {
		t.Errorf("eviction steps per capture grow with the frames held: %v", steps)
	}
	if cost[1600] > 8*cost[40] {
		t.Errorf("a capture at the cap costs %v holding 1,600 frames, %v holding 40", cost[1600], cost[40])
	}
}
