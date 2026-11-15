package video

import (
	"os/exec"
	"strconv"
	"strings"
)

// sampleRSS is a running process's current resident memory, in bytes, from ps; sampled
// repeatedly it approximates the peak. macOS offers no peak for another process without cgo or
// unsafe (proc_pid_rusage). It is false once the process has exited.
func sampleRSS(pid int) (uint64, bool) {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec // a process id
	if err != nil {
		return 0, false
	}
	kb, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	return kb << 10, err == nil && kb > 0
}

// rssIsPeak says whether sampleRSS gives the true peak (true) or a sampled current size.
const rssIsPeak = false
