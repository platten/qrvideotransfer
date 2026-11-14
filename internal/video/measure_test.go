package video

// TEMPORARY measurement for the review of PR #20, removed before review. On the GitHub macos-26-intel
// runner only, the package's run ends with a report of the largest probe active time and the largest
// stop latency seen under the full test load, and fails so that go test prints it. Nowhere else
// does it change anything.

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"
)

var measured struct {
	sync.Mutex
	probe       time.Duration
	stop        time.Duration
	stopBound   time.Duration
	stopWhere   string
	stopSamples int
}

func init() {
	if measuring() {
		observeProbe = func(active time.Duration) {
			measured.Lock()
			if active > measured.probe {
				measured.probe = active
			}
			measured.Unlock()
		}
	}
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
	fmt.Printf("MEASURE probe active max %v (EngineProbeTime %v, half %v); stop latency max %v at %s (bound %v, %d samples); workers %d\n",
		measured.probe, probeTime, probeTime/2, measured.stop, measured.stopWhere, measured.stopBound, measured.stopSamples, WorkerProcesses())
	fmt.Println("MEASURE this failure is the temporary measurement report, by design")
	return 1
}
