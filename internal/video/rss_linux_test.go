package video

import (
	"os"
	"strconv"
	"strings"
)

// sampleRSS is a running process's peak resident memory so far, in bytes: VmHWM from
// /proc/<pid>/status, which counts the process's own address space since exec. (Its rusage cannot
// be used: Go starts children with CLONE_VM, so the parent's high-water mark is carried into the
// child's ru_maxrss at exec.) It is false once the process has exited.
func sampleRSS(pid int) (uint64, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0, false
	}
	for line := range strings.Lines(string(b)) {
		if v, ok := strings.CutPrefix(line, "VmHWM:"); ok {
			kb, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			return kb << 10, err == nil
		}
	}
	return 0, false // a zombie has no memory lines
}

// rssIsPeak says whether sampleRSS gives the true peak (true) or a sampled current size.
const rssIsPeak = true
