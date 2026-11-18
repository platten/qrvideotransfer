package pipeline

import "runtime"

// decodeMemBudget is the memory the decode's vision workers may hold together, inside the 1 GiB a
// hostile input may make a process use (SECURITY.md sections 5 and 9.5).
const decodeMemBudget = 640 << 20

// WarnWorkersReduced: the worker count asked for was above what the decode's memory allows.
const WarnWorkersReduced = "WorkersReduced"

// workerBytes is the memory one vision worker holds for a w x h capture: its capture's planes, 16
// bits a sample (3 w h bytes), as much again for the reader's spare captures and the worker's
// scratch, and 4 MiB besides. Measured at 3840 x 2160: about 50 MiB a worker, against 51.8 here.
func workerBytes(w, h int) int64 { return 6*int64(w)*int64(h) + 4<<20 }

// decodeWorkers returns the vision workers for captures of w x h, asked for requested (0: the CPU
// count): at most as many as the memory budget holds, at least one. It reports whether an explicit
// request was reduced.
func decodeWorkers(requested, w, h int) (int, bool) {
	limit := int(max(1, decodeMemBudget/workerBytes(w, h)))
	if requested <= 0 {
		return min(runtime.GOMAXPROCS(0), limit), false
	}
	return min(requested, limit), requested > limit
}
