package video

// TEMPORARY measurement, removed before review. On the GitHub macos-26-intel runner only, the
// package's run ends, after every test has run, with a list of every probe that
// saw at least one watchdog tick (its largest active time, by runner kind and file) and the
// breakdown of the slowest, and TestWatchdogUnderLoad's figures. The tools/dev task runs
// internal/video verbosely there, so that the lines reach the job's log. Nowhere else does it change
// anything.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"
)

var measured struct {
	sync.Mutex
	probes  map[string]time.Duration // largest active time by runner kind and file
	slowest time.Duration
	detail  string // the slowest probe's key and breakdown when it was seen
}

func init() {
	if measuring() {
		measured.probes = map[string]time.Duration{}
		observeProbe = func(r runner, path string, m *meter, active time.Duration) {
			k := runnerKind(r) + " " + filepath.Base(path)
			measured.Lock()
			defer measured.Unlock()
			if active < 0 { // the probe has ended: its phases
				notePhases(k, m)
				return
			}
			measured.probes[k] = max(measured.probes[k], active)
			if active > measured.slowest {
				measured.slowest, measured.detail = active, k+": "+m.breakdown(time.Now())
			}
		}
	}
}

// phaseMax is the largest time seen for each phase of a probe, and the probe it was seen in.
var phaseMax = map[string]struct {
	d time.Duration
	k string
}{}

// phaseNames are the phases, in order, as pairs of meter times.
var phaseNames = []string{"start to ready", "ready to memory reserved", "memory reserved to input opened", "input opened to first read"}

// notePhases records an ended probe's phases.
func notePhases(k string, m *meter) {
	ts := []int64{m.start.Load(), m.readyAt.Load(), m.memAt.Load(), m.openAt.Load(), m.firstIn.Load()}
	for i, name := range phaseNames {
		a, b := ts[i], ts[i+1]
		if i == 2 && a == 0 { // in process: no reservation time; from readiness
			a = ts[1]
		}
		if a == 0 || b == 0 {
			continue
		}
		if d := time.Duration(b - a); d > phaseMax[name].d {
			phaseMax[name] = struct {
				d time.Duration
				k string
			}{d, k}
		}
	}
}

// runnerKind names where a probe ran.
func runnerKind(r runner) string {
	switch r := r.(type) {
	case workerRunner:
		return "worker"
	case lazyEngine:
		if w, _ := currentWorkers(); w == nil {
			return "embedded/no-workers(in-process,guarded)"
		}
		if degraded.Load() != nil {
			return "embedded/after-failed-start(in-process,guarded)"
		}
		return "embedded/worker"
	case *engine:
		if r.guarded {
			return "in-process,guarded"
		}
		return "in-process,unguarded"
	}
	return fmt.Sprintf("%T", r)
}

// measuring is true on the GitHub macos-26-intel runner (macOS on amd64 under GitHub Actions).
func measuring() bool {
	return os.Getenv("GITHUB_ACTIONS") == "true" && runtime.GOOS == "darwin" && runtime.GOARCH == "amd64"
}

var watchdogLines []string // TestWatchdogUnderLoad's figures, in order

// measureWatchdog records one TestWatchdogUnderLoad round: its total time, and the worst lateness of
// an instance's cancellation and of its return after that cancellation.
func measureWatchdog(capped bool, el, lateCancel, lateStop time.Duration) {
	measured.Lock()
	defer measured.Unlock()
	watchdogLines = append(watchdogLines, fmt.Sprintf("capped %v: total %v; worst cancellation %v after turn + limit; worst return %v after its own cancellation (bounds 5 s each)",
		capped, el, lateCancel, lateStop))
}

// measureReport prints the report when measuring; the package run's result is unchanged (the
// isolated run is verbose there, so the lines appear in the job's log).
func measureReport(code int) int {
	if !measuring() {
		return code
	}
	measured.Lock()
	defer measured.Unlock()
	for _, l := range watchdogLines {
		fmt.Println("MEASURE watchdog", l)
	}
	keys := make([]string, 0, len(measured.probes))
	for k := range measured.probes {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int { return int(measured.probes[b] - measured.probes[a]) })
	fmt.Printf("MEASURE largest probe active %v against EngineProbeTime %v (whole 1 s watchdog ticks); workers %d; %d probes saw a tick\n",
		measured.slowest, probeTime, WorkerProcesses(), len(keys))
	for _, k := range keys {
		fmt.Printf("MEASURE probe active %v: %s\n", measured.probes[k], k)
	}
	fmt.Printf("MEASURE slowest: %s\n", measured.detail)
	for _, name := range phaseNames {
		fmt.Printf("MEASURE phase max %s: %v (%s)\n", name, phaseMax[name].d, phaseMax[name].k)
	}
	return code
}
