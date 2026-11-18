//go:build windows

package pipeline

import "golang.org/x/sys/windows"

// platformDiskFree returns the bytes available to this process's user in dir.
func platformDiskFree(dir string) (int64, error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil {
		return 0, err
	}
	return int64(min(avail, 1<<62)), nil
}
