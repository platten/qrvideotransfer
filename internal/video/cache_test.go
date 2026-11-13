package video

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestUnwritableCache: a compilation cache that cannot be written (here read-only; on Windows also a
// file another worker is renaming into place) costs a compilation, not the job.
func TestUnwritableCache(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions do not stop writes here")
	}
	// Learn the name of wazero's versioned subdirectory from an empty cache with a tiny module.
	probe := t.TempDir()
	if _, err := newEngineFrom(t.Context(), probe, false, spinWasm); err != nil {
		t.Fatal(err)
	}
	es, _ := os.ReadDir(probe)
	if len(es) != 1 {
		t.Fatalf("cache layout %v", es)
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, es[0].Name())
	if err := os.Mkdir(sub, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(sub, 0o700) }() //nolint:gosec // let TempDir clean up
	if _, err := newEngineFrom(t.Context(), dir, false, spinWasm); err != nil {
		t.Fatal(err)
	}
}
