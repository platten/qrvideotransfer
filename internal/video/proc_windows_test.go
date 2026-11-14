//go:build windows

package video

import "golang.org/x/sys/windows"

// processAlive reports whether a process is still running.
func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid)) //nolint:misspell // the API name
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	ev, _ := windows.WaitForSingleObject(h, 0)
	return ev == uint32(windows.WAIT_TIMEOUT)
}
