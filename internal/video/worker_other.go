//go:build !windows

package video

import "os"

// killOnClose is a no-op outside Windows: the worker exits by itself when its parent's pipe closes.
func killOnClose(*os.Process) func() { panic("not implemented") }
