//go:build unix

package payload

import (
	"errors"
	"syscall"
)

// linkUnsupported reports whether a link error means the file system has no hard links: EPERM
// (Linux on FAT and exFAT), EOPNOTSUPP or ENOTSUP (macOS on exFAT and some network shares), or
// errors.ErrUnsupported. EXDEV and every other error are real failures.
func linkUnsupported(err error) bool {
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, errors.ErrUnsupported)
}
