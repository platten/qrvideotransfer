//go:build windows

package payload

import (
	"os"
	"syscall"
	"testing"
)

// TestLinkUnsupportedWindows: only the errors a file system without hard links gives (FAT and exFAT
// return ERROR_INVALID_FUNCTION, some shares ERROR_NOT_SUPPORTED) fall back to a rename.
func TestLinkUnsupportedWindows(t *testing.T) {
	for _, c := range []struct {
		err  syscall.Errno
		want bool
	}{
		{syscall.Errno(1), true},  // ERROR_INVALID_FUNCTION
		{syscall.Errno(50), true}, // ERROR_NOT_SUPPORTED
		{syscall.ERROR_ACCESS_DENIED, false}, {syscall.ERROR_ALREADY_EXISTS, false},
		{syscall.Errno(17), false},   // ERROR_NOT_SAME_DEVICE
		{syscall.Errno(1142), false}, // ERROR_TOO_MANY_LINKS
	} {
		if got := linkUnsupported(&os.LinkError{Op: "link", Err: c.err}); got != c.want {
			t.Errorf("%v: %v, want %v", c.err, got, c.want)
		}
	}
}
