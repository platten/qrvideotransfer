//go:build windows

package rs

import (
	"os"

	"golang.org/x/sys/windows"
)

// setSparse marks f as a sparse file before it is extended, so that NTFS does not allocate and zero
// the whole store up front. Failure (for example on FAT32, which has no sparse files) is not an
// error: the file then occupies its full size.
func setSparse(f *os.File) {
	var n uint32
	_ = windows.DeviceIoControl(windows.Handle(f.Fd()), windows.FSCTL_SET_SPARSE, nil, 0, nil, 0, &n, nil)
}
