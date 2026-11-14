package video

// TEMPORARY measurement, removed before review. On the GitHub macos-26-intel runner only, the
// package's run ends, after every test has run under the full load, with a list of every probe that
// saw at least one watchdog tick (its largest active time, by runner kind and file) and the
// breakdown of the slowest, and fails so that go test prints it. Nowhere else does it change
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
			measured.probes[k] = max(measured.probes[k], active)
			if active > measured.slowest {
				measured.slowest, measured.detail = active, k+": "+m.breakdown(time.Now())
			}
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

// measureReport prints the report and fails the package run when measuring.
func measureReport(code int) int {
	if !measuring() {
		return code
	}
	measured.Lock()
	defer measured.Unlock()
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
	fmt.Println("MEASURE this failure is the temporary measurement report, by design")
	return 1
}
