package video

import "golang.org/x/sys/unix"

// physicalMemory is the machine's physical memory in bytes, or 0 when it cannot be read.
func physicalMemory() uint64 {
	var si unix.Sysinfo_t
	if unix.Sysinfo(&si) != nil {
		return 0
	}
	return uint64(si.Totalram) * uint64(si.Unit) //nolint:unconvert // the field types differ by architecture
}
