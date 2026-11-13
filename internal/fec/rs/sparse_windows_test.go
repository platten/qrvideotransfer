//go:build windows

package rs

import (
	"testing"

	"golang.org/x/sys/windows"
)

// On NTFS the store file carries the sparse attribute.
func TestStoreSparseOnWindows(t *testing.T) {
	s, path := newStore(t, smallPlan)
	defer func() { _ = s.Close() }()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	attr, err := windows.GetFileAttributes(p)
	if err != nil {
		t.Fatal(err)
	}
	if attr&windows.FILE_ATTRIBUTE_SPARSE_FILE == 0 {
		t.Errorf("store file attributes %#x lack FILE_ATTRIBUTE_SPARSE_FILE", attr)
	}
}
