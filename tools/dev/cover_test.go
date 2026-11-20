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

// TestCoverRuns: cover -part runs internal/video's coverage in a CI step of its own, after every
// other package's (designer's ruling on the CI budget, 2026-11-19).
func TestCoverRuns(t *testing.T) {
	pkgs := []string{module, module + "/internal/fec", module + "/internal/pipeline", module + "/internal/video"}
	runs := isolatedRuns([]string{"test"}, pkgs)
	for _, c := range []struct {
		part string
		want [][]string
	}{
		{"", runs},
		{"rest", runs[:3]},
		{"video", runs[3:]},
	} {
		got, err := coverRuns(runs, c.part)
		if err != nil {
			t.Errorf("coverRuns(%q): %v", c.part, err)
			continue
		}
		sameRuns(t, "coverRuns "+c.part, got, c.want)
	}
	if _, err := coverRuns(runs, "others"); err == nil {
		t.Error("an unknown part gave no error")
	}
	if _, err := coverRuns(runs[:3], "video"); err == nil {
		t.Error("a split without internal/video gave a video part")
	}
}

// TestCoverMerge: -part rest keeps its merged profile beside the final one, and -part video merges
// it with its own, so that the gate, here and in CI's go-test-coverage step, sees every package.
func TestCoverMerge(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	profile := filepath.Join(dir, "coverage.out")
	a, b := write("a", "mode: set\na.go:1.1,2.2 1 1\n"), write("b", "mode: set\nb.go:1.1,2.2 1 0\n")
	if gate, err := coverMerge(profile, "video", []string{b}); err == nil || gate {
		t.Errorf("-part video before -part rest: gate %v, err %v", gate, err)
	}
	if gate, err := coverMerge(profile, "rest", []string{a}); err != nil || gate {
		t.Fatalf("-part rest: gate %v, err %v", gate, err)
	}
	if _, err := os.Stat(profile); err == nil {
		t.Error("-part rest wrote the final profile")
	}
	v := write("v", "mode: set\nv.go:1.1,2.2 1 1\n")
	if gate, err := coverMerge(profile, "video", []string{v}); err != nil || !gate {
		t.Fatalf("-part video: gate %v, err %v", gate, err)
	}
	if got := read(profile); got != "mode: set\na.go:1.1,2.2 1 1\nv.go:1.1,2.2 1 1\n" {
		t.Errorf("merged profile %q", got)
	}
	if gate, err := coverMerge(profile, "", []string{a, b}); err != nil || !gate {
		t.Fatalf("whole run: gate %v, err %v", gate, err)
	}
	if got := read(profile); got != "mode: set\na.go:1.1,2.2 1 1\nb.go:1.1,2.2 1 0\n" {
		t.Errorf("whole profile %q", got)
	}
}
