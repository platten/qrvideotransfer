package video

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCgroupMemory: on Linux the worker cap counts the smaller of the machine's memory and the
// cgroup's limit, cgroup v2 (memory.max, at the process's own cgroup or the root) or v1
// (memory/memory.limit_in_bytes); "max", v1's "unlimited" value and a missing file mean no limit.
func TestCgroupMemory(t *testing.T) {
	t.Parallel()
	const sys = 64 << 30
	write := func(root, rel, s string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name, rel, value, self string
		want                   uint64
	}{
		{"none", "", "", "", sys},
		{"v2 root", "memory.max", "4294967296\n", "0::/\n", 4 << 30},
		{"v2 own cgroup", "user.slice/app/memory.max", "2147483648\n", "0::/user.slice/app\n", 2 << 30},
		{"v2 max", "memory.max", "max\n", "0::/\n", sys},
		{"v2 above the machine", "memory.max", "137438953472\n", "0::/\n", sys},
		{"v1", "memory/memory.limit_in_bytes", "3221225472\n", "4:memory:/\n", 3 << 30},
		{"v1 unlimited", "memory/memory.limit_in_bytes", "9223372036854771712\n", "4:memory:/\n", sys},
		{"garbage", "memory.max", "lots\n", "0::/\n", sys},
	} {
		root := t.TempDir()
		if c.rel != "" {
			write(root, c.rel, c.value)
		}
		self := filepath.Join(t.TempDir(), "cgroup")
		if err := os.WriteFile(self, []byte(c.self), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := limitMemory(sys, root, self); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
	if physicalMemory() == 0 {
		t.Error("no physical memory size on this machine")
	}
}
