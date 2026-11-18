package main

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestParseProfileGroups: the gate groups statements as CI's go-test-coverage step does (its
// package is a file's path up to the last slash), so that the two agree: a package directory's
// files together, and each file at the module's root on its own, since it has no slash.
func TestParseProfileGroups(t *testing.T) {
	prof := filepath.Join(t.TempDir(), "coverage.out")
	lines := []string{"mode: set",
		module + "/qrvt.go:1.1,2.1 3 1", module + "/qrvt.go:3.1,4.1 1 0",
		module + "/convert.go:1.1,2.1 2 1",
		module + "/internal/frame/a.go:1.1,2.1 4 1", module + "/internal/frame/b.go:1.1,2.1 1 0",
		module + "/tools/dev/main.go:1.1,2.1 5 0"}
	if err := os.WriteFile(prof, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := parseProfile(prof)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]int{
		module + "/qrvt.go":        {4, 3},
		module + "/convert.go":     {2, 2},
		module + "/internal/frame": {5, 4},
	}
	if !maps.Equal(got, want) {
		t.Errorf("groups %v, want %v", got, want)
	}
}

// TestCoverTimeout: the coverage step sets its own per-package limit of 15 minutes instead of go
// test's unchosen 10-minute default (designer's ruling on PR #47; DESIGN.md section 19); the job
// and step budgets stay the real guard.
func TestCoverTimeout(t *testing.T) {
	a := coverArgs("noasm,nounsafe")
	if !slices.Contains(a, "-timeout=15m") || !slices.Contains(a, "-coverprofile=PROFILE") || a[len(a)-1] != "./..." {
		t.Errorf("coverage arguments %v", a)
	}
}
