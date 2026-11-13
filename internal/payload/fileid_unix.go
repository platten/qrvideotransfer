//go:build !windows

package payload

import (
	"errors"
	"os"
	"syscall"
)

// fileID identifies a file independently of its name: device and inode.
type fileID struct{ dev, ino uint64 }

// statID returns the identity of the file at name, without following a final link.
func statID(root *os.Root, name string) (fileID, error) {
	st, err := root.Lstat(name)
	if err != nil {
		return fileID{}, err
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return fileID{}, errors.New("payload: no file identity on this system")
	}
	return fileID{uint64(s.Dev), s.Ino}, nil //nolint:unconvert // Dev is int32 on darwin
}
