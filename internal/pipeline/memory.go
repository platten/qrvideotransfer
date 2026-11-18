package pipeline

import (
	"context"
	"runtime"
	"sync"
)

// decodeMemBudget is the memory the decode's vision workers may hold together, inside the 1 GiB a
// hostile input may make a process use (SECURITY.md sections 5 and 9.5). At most half of it goes
// to the workers' captures (decodeWorkers); the rest, and whatever the captures leave, to the
// grid stage (stageGate).
const decodeMemBudget = 640 << 20

// WarnWorkersReduced is the warning code for a worker count asked for above what the decode's
// memory allows.
const WarnWorkersReduced = "WorkersReduced"

// workerBytes is the memory one vision worker holds for a w x h capture: its capture's planes, 16
// bits a sample (3 w h bytes), as much again for the reader's spare captures and the worker's
// detection scratch, and 4 MiB besides. Measured at 3840 x 2160 on noise: about 50 MiB a worker,
// against 51.8 here.
func workerBytes(w, h int) int64 { return 6*int64(w)*int64(h) + 4<<20 }

// The grid stage's memory model (DESIGN.md section 19): sampling the data cells, the tile
// equaliser with stripe cancellation, and the soft bits of the four phases hold about
// unitCellBytes per data cell and unitBaseBytes besides, whatever the capture's size. Measured on
// the encoder's output in its heaviest mode (modulation 1), as the heap's growth over one capture's
// stage: 84 MiB for 117,112 cells (1920 x 1080, 4-pixel cells) and 283 MiB for 469,088 (3840 x
// 2160), which gives 593 bytes a cell and 18 MiB.
const (
	unitCellBytes = 600
	unitBaseBytes = 16 << 20
)

// unitBytes is the grid stage's memory for a grid of cells data cells.
func unitBytes(cells int) int64 { return unitCellBytes*int64(cells) + unitBaseBytes }

// decodeWorkers returns the vision workers for captures of w x h, asked for requested (0: the CPU
// count): at most as many as half the memory budget holds, at least one. It reports whether an
// explicit request was reduced.
func decodeWorkers(requested, w, h int) (int, bool) {
	limit := int(max(1, decodeMemBudget/2/workerBytes(w, h)))
	if requested <= 0 {
		return min(runtime.GOMAXPROCS(0), limit), false
	}
	return min(requested, limit), requested > limit
}

// stageBudget is the memory left to the grid stage by workers vision workers on w x h captures:
// the budget less their captures, at least half of it.
func stageBudget(workers, w, h int) int64 {
	return max(decodeMemBudget-int64(workers)*workerBytes(w, h), decodeMemBudget/2)
}

// stageGate admits captures to the grid stage while the memory model of the units inside fits its
// budget: a unit takes unitBytes of its grid, and one whose grid alone exceeds the budget takes
// all of it and runs alone. Units wait in order of arrival, so a large grid is not starved by
// small ones. The zero gate (nil) admits everything.
type stageGate struct {
	mu      sync.Mutex
	budget  int64
	used    int64
	waiters []*stageWaiter

	// For tests: the most units inside at once, and the most memory they held.
	inside, maxInside int
	maxUsed           int64
}

type stageWaiter struct {
	n     int64
	ready chan struct{}
}

func newStageGate(budget int64) *stageGate { return &stageGate{budget: max(budget, 1)} }

// weight is what a unit with cells data cells takes of the gate.
func (g *stageGate) weight(cells int) int64 { return min(unitBytes(cells), g.budget) }

// enter waits until a unit of cells data cells fits, or ctx ends; it returns the release.
func (g *stageGate) enter(ctx context.Context, cells int) (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	n := g.weight(cells)
	g.mu.Lock()
	if len(g.waiters) == 0 && g.used+n <= g.budget {
		g.admit(n)
		g.mu.Unlock()
		return func() { g.leave(n) }, nil
	}
	w := &stageWaiter{n: n, ready: make(chan struct{})}
	g.waiters = append(g.waiters, w)
	g.mu.Unlock()
	select {
	case <-w.ready:
		return func() { g.leave(n) }, nil
	case <-ctx.Done():
		g.mu.Lock()
		select {
		case <-w.ready: // admitted meanwhile
			g.mu.Unlock()
			g.leave(n)
		default:
			for i, x := range g.waiters {
				if x == w {
					g.waiters = append(g.waiters[:i], g.waiters[i+1:]...)
					break
				}
			}
			g.wake()
			g.mu.Unlock()
		}
		return nil, ctx.Err()
	}
}

// admit counts a unit of n in; g.mu is held.
func (g *stageGate) admit(n int64) {
	g.used += n
	g.inside++
	g.maxInside = max(g.maxInside, g.inside)
	g.maxUsed = max(g.maxUsed, g.used)
}

func (g *stageGate) leave(n int64) {
	g.mu.Lock()
	g.used -= n
	g.inside--
	g.wake()
	g.mu.Unlock()
}

// wake admits waiters in order while the first fits; g.mu is held.
func (g *stageGate) wake() {
	for len(g.waiters) > 0 && g.used+g.waiters[0].n <= g.budget {
		w := g.waiters[0]
		g.waiters = g.waiters[1:]
		g.admit(w.n)
		close(w.ready)
	}
}
