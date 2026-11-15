package video

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Slow samples (docs/spikes/S1.md section 14): when an instance is slow in one of the two ways the
// M1 runs left unexplained, the test logs, at that moment, a line starting "slow-sample:" with the
// instances running by kind and the machine's load, to test whether the package's own spinning
// tests oversubscribe the runner. Logging only; nothing here asserts.

// Thresholds for a slow sample: a worker's or in-process instance's time from readiness to its first
// read of the input, and an in-process instance's return after the watchdog's cancellation.
const slowFirstRead, slowReturn = 3 * time.Second, 2 * time.Second

// spinning counts the package's deliberately spinning instances started and not yet returned (some
// may be waiting for a slot), by kind: in qrvt's process (close-on-cancel on), in a worker process
// of this test process, and in a worker of a separate fake parent (which this process's slots do not
// count).
var spinning struct{ inProcess, worker, otherProcess atomic.Int32 }

// spin1 counts one spinning instance of the kind c for the life of the call; use as
// defer spin1(&spinning.worker)().
func spin1(c *atomic.Int32) func() { c.Add(1); return func() { c.Add(-1) } }

// machineLoad says how busy this process and the machine are: the slots taken, the spinning
// instances by kind, the load average, the engine workers on the machine and its busiest processes.
func machineLoad() string {
	return fmt.Sprintf("worker slots %d/%d, in-process slots %d/%d, spinning (started): %d in process, %d in workers, %d in other processes; load average %s; %s",
		len(workerSlots), cap(workerSlots), len(slots), cap(slots),
		spinning.inProcess.Load(), spinning.worker.Load(), spinning.otherProcess.Load(), loadAverage(), busiest())
}

// loadAverage is the 1, 5 and 15 minute load average, or "n/a".
func loadAverage() string {
	switch runtime.GOOS {
	case "linux":
		if b, err := os.ReadFile("/proc/loadavg"); err == nil {
			if f := strings.Fields(string(b)); len(f) >= 3 {
				return strings.Join(f[:3], " ")
			}
		}
	case "darwin":
		if b, err := exec.Command("sysctl", "-n", "vm.loadavg").Output(); err == nil {
			return strings.Trim(strings.TrimSpace(string(b)), "{} ")
		}
	}
	return "n/a"
}

// busiest counts the engine workers running on the machine and names its five busiest processes
// (Unix only).
func busiest() string {
	if runtime.GOOS == "windows" {
		return "processes n/a"
	}
	out, err := exec.Command("ps", "-A", "-o", "%cpu=,args=").Output()
	if err != nil {
		return "processes n/a"
	}
	type proc struct {
		cpu  float64
		name string
	}
	var ps []proc
	workers := 0
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		var p proc
		if _, err := fmt.Sscanf(f[0], "%g", &p.cpu); err != nil {
			continue
		}
		name := f[1][strings.LastIndexByte(f[1], '/')+1:]
		if len(f) > 2 && f[2] == WorkerArg {
			workers++
			name += " (worker)"
		}
		p.name = name
		ps = append(ps, p)
	}
	for i := 0; i < len(ps) && i < 5; i++ { // partial selection sort: the five busiest
		for j := i + 1; j < len(ps); j++ {
			if ps[j].cpu > ps[i].cpu {
				ps[i], ps[j] = ps[j], ps[i]
			}
		}
	}
	top := make([]string, 0, 5)
	for _, p := range ps[:min(5, len(ps))] {
		top = append(top, fmt.Sprintf("%s %.0f%%", p.name, p.cpu))
	}
	return fmt.Sprintf("engine workers on the machine %d; busiest: %s", workers, strings.Join(top, ", "))
}

// watchSlow logs a slow sample for m, at most one of each kind, while the instance runs: when it has
// an input and has been ready for slowFirstRead without a first read, and when it has not returned
// slowReturn after the watchdog's cancellation. It stops when done is closed or the test ends (an
// instance a test left running must not log after it).
func watchSlow(t *testing.T, what string, input bool, m *meter, done <-chan struct{}) {
	ended := t.Context().Done()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var read, ret bool
	for {
		select {
		case <-done:
			return
		case <-ended:
			return
		case now := <-tick.C:
			if r := m.readyAt.Load(); input && !read && r != 0 && m.firstIn.Load() == 0 && now.Sub(time.Unix(0, r)) > slowFirstRead {
				read = true
				t.Logf("slow-sample: %s: ready %v without a first read; %s; %s", what, slowFirstRead, m.breakdown(now), machineLoad())
			}
			if c := m.cancelledAt.Load(); !ret && c != 0 && now.Sub(time.Unix(0, c)) > slowReturn {
				ret = true
				t.Logf("slow-sample: %s: not returned %v after its cancellation; %s; %s", what, slowReturn, m.breakdown(now), machineLoad())
			}
		}
	}
}

// observed is a runner that watches each of its instances for slow samples.
type observed struct {
	r runner
	t *testing.T
}

func (o observed) exec(ctx context.Context, j *job, m *meter) (int, error) {
	done := make(chan struct{})
	defer close(done)
	go watchSlow(o.t, o.t.Name()+" "+j.prog, j.input != "", m, done)
	return o.r.exec(ctx, j, m)
}
