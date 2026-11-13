//go:build windows

package video

import (
	"os"
)

// killOnClose puts the worker in a Job Object that kills it when the job's last handle closes,
// which happens when this process exits for any reason. The worker's own exit on a closed pipe is
// the baseline; this adds the operating system's guarantee. Failures are ignored.
func killOnClose(p *os.Process) func() { panic("not implemented") }
