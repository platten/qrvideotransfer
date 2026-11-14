//go:build unix

package payload

import (
	"os"
	"syscall"
	"testing"
)

// TestLinkUnsupportedUnix: only the errors a file system without hard links gives fall back to a
// rename. EPERM is what Linux returns on FAT; EOPNOTSUPP and ENOTSUP what macOS returns on exFAT and
// some network shares. EXDEV cannot happen inside one folder and is not one of them.
func TestLinkUnsupportedUnix(t *testing.T) {
	for _, c := range []struct {
		err  syscall.Errno
		want bool
	}{
		{syscall.EPERM, true}, {syscall.EOPNOTSUPP, true}, {syscall.ENOTSUP, true},
		{syscall.EXDEV, false}, {syscall.EACCES, false}, {syscall.EIO, false}, {syscall.ENOSPC, false},
		{syscall.EMLINK, false}, {syscall.EEXIST, false},
	} {
		if got := linkUnsupported(&os.LinkError{Op: "linkat", Err: c.err}); got != c.want {
			t.Errorf("%v: %v, want %v", c.err, got, c.want)
		}
	}
}
