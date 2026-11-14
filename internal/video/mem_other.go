//go:build !linux && !darwin && !windows

package video

// physicalMemory is 0 (unknown) where qrvt has no way to read it; the worker cap is then the CPU
// count.
func physicalMemory() uint64 { return 0 }
