package video

// TEMPORARY measurement for the review of PR #20, removed before review. On the GitHub macos-26-intel
// runner only, the package's run ends with a report of the largest probe active times (by runner and
// file) and the largest stop latency seen under the full test load, and fails so that go test prints
// it. Nowhere else does it change anything.

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
	probes      map[string]time.Duration // largest active time by runner kind and file
	stop        time.Duration
	stopBound   time.Duration
	stopWhere   string
	stopSamples int
}

func init() {
	if measuring() {
		measured.probes = map[string]time.Duration{}
		observeProbe = func(r runner, path string, active time.Duration) {
			k := runnerKind(r) + " " + filepath.Base(path)
			measured.Lock()
			measured.probes[k] = max(measured.probes[k], active)
			measured.Unlock()
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

// measureStop records a stop latency d of a stop whose bound is bound.
func measureStop(where string, d, bound time.Duration) {
	measured.Lock()
	defer measured.Unlock()
	measured.stopSamples++
	if d > measured.stop {
		measured.stop, measured.stopBound, measured.stopWhere = d, bound, where
	}
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
	fmt.Printf("MEASURE bounds: EngineProbeTime %v (half %v), probe active time counted in whole watchdog ticks of 1 s; workers %d\n",
		probeTime, probeTime/2, WorkerProcesses())
	for i, k := range keys {
		if i == 10 {
			break
		}
		fmt.Printf("MEASURE probe active %v: %s\n", measured.probes[k], k)
	}
	fmt.Printf("MEASURE stop latency max %v at %s (bound %v, %d samples)\n",
		measured.stop, measured.stopWhere, measured.stopBound, measured.stopSamples)
	fmt.Println("MEASURE this failure is the temporary measurement report, by design")
	return 1
}
