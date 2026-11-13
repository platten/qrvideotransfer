//go:build !windows

package video

import (
	"errors"
	"syscall"
)

// processAlive reports whether a process exists (a reaped one does not).
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
