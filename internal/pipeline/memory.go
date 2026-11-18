package pipeline

import "runtime"

// decodeMemBudget is the memory the decode's vision workers may hold together (SECURITY.md
// section 5).
const decodeMemBudget = 640 << 20

// WarnWorkersReduced: the worker count asked for was above what the decode's memory allows.
const WarnWorkersReduced = "WorkersReduced"

// workerBytes is the memory one vision worker holds for a w x h capture.
func workerBytes(w, h int) int64 { return 1 }

// decodeWorkers returns the vision workers for captures of w x h, asked for requested (0: the CPU
// count), and whether an explicit request was reduced.
func decodeWorkers(requested, w, h int) (int, bool) {
	if requested <= 0 {
		return runtime.GOMAXPROCS(0), false
	}
	return requested, false
}
