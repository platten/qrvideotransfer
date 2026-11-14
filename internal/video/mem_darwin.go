package video

import "golang.org/x/sys/unix"

// physicalMemory is the machine's physical memory in bytes, or 0 when it cannot be read.
func physicalMemory() uint64 {
	n, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0
	}
	return n
}
