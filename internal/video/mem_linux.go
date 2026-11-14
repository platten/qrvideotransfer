package video

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// physicalMemory is the memory this process may use, in bytes: the machine's (sysinfo), or its
// cgroup's limit when that is smaller; 0 when it cannot be read.
func physicalMemory() uint64 {
	var si unix.Sysinfo_t
	if unix.Sysinfo(&si) != nil {
		return 0
	}
	sys := uint64(si.Totalram) * uint64(si.Unit) //nolint:unconvert // the field types differ by architecture
	return limitMemory(sys, "/sys/fs/cgroup", "/proc/self/cgroup")
}

// limitMemory is sys, or the cgroup memory limit when one is set and smaller. root is the cgroup
// file system and self the process's cgroup list (/proc/self/cgroup). It reads cgroup v2's
// memory.max, at the process's own cgroup and at the root, and cgroup v1's
// memory/memory.limit_in_bytes; "max", v1's page-rounded maximum and anything unreadable mean no
// limit.
func limitMemory(sys uint64, root, self string) uint64 {
	files := []string{filepath.Join(root, "memory.max"), filepath.Join(root, "memory", "memory.limit_in_bytes")}
	if b, err := os.ReadFile(self); err == nil { //nolint:gosec // a fixed system path, or a test's
		for line := range strings.Lines(string(b)) {
			if p, ok := strings.CutPrefix(strings.TrimSpace(line), "0::"); ok && p != "/" {
				files = append(files, filepath.Join(root, filepath.Clean("/"+p), "memory.max"))
			}
		}
	}
	for _, f := range files {
		b, err := os.ReadFile(f) //nolint:gosec // a cgroup file
		if err != nil {
			continue
		}
		if n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64); err == nil && n > 0 && n < sys {
			sys = n
		}
	}
	return sys
}
