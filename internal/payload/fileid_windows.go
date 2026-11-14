//go:build windows

package payload

import (
	"os"

	"golang.org/x/sys/windows"
)

// fileID identifies a file independently of its name: volume serial number and file index.
type fileID struct{ vol, idx uint64 }

// statID returns the identity of the regular file at name.
func statID(root *os.Root, name string) (fileID, error) {
	f, err := root.Open(name)
	if err != nil {
		return fileID{}, err
	}
	defer func() { _ = f.Close() }()
	var d windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &d); err != nil {
		return fileID{}, err
	}
	return fileID{uint64(d.VolumeSerialNumber), uint64(d.FileIndexHigh)<<32 | uint64(d.FileIndexLow)}, nil
}
