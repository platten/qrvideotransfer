//go:build unix

package video

import (
	"os"
	"runtime"
	"syscall"
)

// maxRSS is an exited process's peak resident memory in bytes, from its rusage: Linux reports
// kilobytes, macOS bytes.
func maxRSS(ps *os.ProcessState) (uint64, bool) {
	ru, ok := ps.SysUsage().(*syscall.Rusage)
	if !ok || ru.Maxrss <= 0 {
		return 0, false
	}
	if runtime.GOOS == "darwin" {
		return uint64(ru.Maxrss), true
	}
	return uint64(ru.Maxrss) << 10, true
}
