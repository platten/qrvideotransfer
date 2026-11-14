//go:build unix

package video

import (
	"os"
	"runtime"
	"strconv"

	"golang.org/x/sys/unix"
)

// closeInherited closes every descriptor above 2 that this worker inherited: one that qrvt itself
// inherited without close-on-exec passes through exec to the worker. Go opens every descriptor of its
// own with close-on-exec, and exec closes those, so a descriptor without the flag was inherited; it
// is closed, and the runtime's own (its poller's, the directory read here) are left alone. That is
// why this does not use close_range(3, ~0), which would close the runtime's descriptors too.
func closeInherited() {
	dir := "/dev/fd"
	if runtime.GOOS == "linux" {
		dir = "/proc/self/fd"
	}
	for _, fd := range openFDs(dir) {
		if fd > 2 {
			if fl, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err == nil && fl&unix.FD_CLOEXEC == 0 {
				_ = unix.Close(fd)
			}
		}
	}
}

// openFDs lists this process's descriptors from dir, /proc/self/fd (Linux) or /dev/fd (macOS and
// the BSDs); when that cannot be read, it gives every number below the descriptor limit, up to
// 65,536.
func openFDs(dir string) []int {
	var fds []int
	if es, err := os.ReadDir(dir); err == nil {
		for _, e := range es {
			if fd, err := strconv.Atoi(e.Name()); err == nil {
				fds = append(fds, fd)
			}
		}
		return fds
	}
	var rl unix.Rlimit
	n := uint64(1024)
	if unix.Getrlimit(unix.RLIMIT_NOFILE, &rl) == nil {
		n = min(rl.Cur, 1<<16)
	}
	for fd := range int(n) {
		fds = append(fds, fd)
	}
	return fds
}
