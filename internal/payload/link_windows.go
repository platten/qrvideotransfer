//go:build windows

package payload

import (
	"errors"
	"syscall"
)

// Windows errors a file system without hard links returns from CreateHardLink.
const (
	errorInvalidFunction syscall.Errno = 1  // ERROR_INVALID_FUNCTION: FAT and exFAT
	errorNotSupported    syscall.Errno = 50 // ERROR_NOT_SUPPORTED: some network shares
)

// linkUnsupported reports whether a link error means the file system has no hard links:
// ERROR_INVALID_FUNCTION, ERROR_NOT_SUPPORTED, or errors.ErrUnsupported. Every other error is a real
// failure.
func linkUnsupported(err error) bool {
	return errors.Is(err, errorInvalidFunction) || errors.Is(err, errorNotSupported) || errors.Is(err, errors.ErrUnsupported)
}
