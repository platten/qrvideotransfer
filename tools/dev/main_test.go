package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGoTest(t *testing.T) {
	if got := goTest("noasm,nounsafe", "-race", "./..."); !slices.Equal(got, []string{"test", "-tags=noasm,nounsafe", "-race", "./..."}) {
		t.Fatalf("goTest with tags = %v", got)
	}
	if got := goTest("", "./..."); !slices.Equal(got, []string{"test", "./..."}) {
		t.Fatalf("goTest without tags = %v", got)
	}
}

// TestFuzzTags: the fuzz task adds the HEVC fast path's tag, so that FuzzFastPathNAL is fuzzed too.
func TestFuzzTags(t *testing.T) {
	if got := fuzzTags("noasm,nounsafe"); got != "noasm,nounsafe,unsandboxedhevc" {
		t.Errorf("fuzzTags with release tags = %q", got)
	}
	if got := fuzzTags(""); got != "unsandboxedhevc" {
		t.Errorf("fuzzTags without = %q", got)
	}
}

func TestFuzzTargets(t *testing.T) {
	list := "FuzzA\nFuzzB\nok  \t" + module + "/internal/a\t0.01s\n" +
		"ok  \t" + module + "/internal/none\t0.01s\n" +
		"FuzzSpike\nok  \t" + module + "/spikes/s\t0.01s\n" +
		"FuzzC\nok  \t" + module + "/internal/c\t0.01s\n"
	got := fuzzTargets(list)
	want := []fuzzTarget{{module + "/internal/a", "FuzzA"}, {module + "/internal/a", "FuzzB"}, {module + "/internal/c", "FuzzC"}}
	if !slices.Equal(got, want) {
		t.Fatalf("fuzzTargets = %v, want %v", got, want)
	}
}

// TestFuzzAllRunsEveryTarget: one target's finding does not hide another's; every target runs and
// the error names each failure.
func TestFuzzAllRunsEveryTarget(t *testing.T) {
	ts := []fuzzTarget{{"p/a", "FuzzA"}, {"p/b", "FuzzB"}, {"p/c", "FuzzC"}, {"p/d", "FuzzD"}}
	var ran []string
	err := fuzzAll(ts, func(ft fuzzTarget) error {
		ran = append(ran, ft.name)
		if ft.name == "FuzzA" || ft.name == "FuzzC" {
			return errors.New("exit status 1")
		}
		return nil
	})
	if !slices.Equal(ran, []string{"FuzzA", "FuzzB", "FuzzC", "FuzzD"}) {
		t.Fatalf("ran %v, want every target", ran)
	}
	if err == nil {
		t.Fatal("fuzzAll = nil, want the failures")
	}
	msg := err.Error()
	for _, s := range []string{"2 of 4", "p/a FuzzA: exit status 1", "p/c FuzzC: exit status 1"} {
		if !strings.Contains(msg, s) {
			t.Errorf("error %q lacks %q", msg, s)
		}
	}
	if strings.Contains(msg, "FuzzB") || strings.Contains(msg, "FuzzD") {
		t.Errorf("error %q names a target that passed", msg)
	}
	if err := fuzzAll(ts, func(fuzzTarget) error { return nil }); err != nil {
		t.Fatalf("all pass: %v", err)
	}
}

func TestLintRuns(t *testing.T) {
	got := lintRuns("noasm,nounsafe", false)
	want := [][]string{{"run", "./..."}, {"run", "--build-tags=noasm,nounsafe", "./..."}}
	if len(got) != 2 || !slices.Equal(got[0], want[0]) || !slices.Equal(got[1], want[1]) {
		t.Fatalf("lintRuns with tags = %v, want %v", got, want)
	}
	if got := lintRuns("", true); len(got) != 1 || !slices.Equal(got[0], []string{"run", "--fix", "./..."}) {
		t.Fatalf("lintRuns without tags = %v", got)
	}
}

func TestVulncheckRuns(t *testing.T) {
	got := vulncheckRuns("noasm,nounsafe")
	if len(got) != 3 {
		t.Fatalf("vulncheckRuns = %v, want one per target system", got)
	}
	for i, goos := range []string{"linux", "darwin", "windows"} {
		if got[i].goos != goos || strings.Join(got[i].args, " ") != "-tags=noasm,nounsafe ./..." {
			t.Errorf("run %d = %+v", i, got[i])
		}
	}
	if got := vulncheckRuns(""); strings.Join(got[0].args, " ") != "./..." {
		t.Errorf("untagged run = %+v", got[0])
	}
	if toolNamed("govulncheck").pkg != "golang.org/x/vuln/cmd/govulncheck@v1.9.0" {
		t.Error("govulncheck is not pinned to v1.9.0")
	}
}

// TestIsolatedRuns: the packages run in two go test invocations, every package but internal/video
// first and internal/video on its own after them, with the same arguments.
func TestIsolatedRuns(t *testing.T) {
	pkgs := []string{module, module + "/internal/fec", module + "/internal/video", module + "/internal/payload"}
	got := isolatedRuns([]string{"test", "-race", "./..."}, pkgs)
	want := [][]string{
		{"test", "-race", module, module + "/internal/fec", module + "/internal/payload"},
		{"test", "-race", "./internal/video/..."},
	}
	if len(got) != 2 || !slices.Equal(got[0], want[0]) || !slices.Equal(got[1], want[1]) {
		t.Errorf("isolatedRuns = %q, want %q", got, want)
	}
	// Arguments that do not end in ./... are run as they are.
	if got := isolatedRuns([]string{"test", "./internal/fec"}, pkgs); len(got) != 1 || got[0][1] != "./internal/fec" {
		t.Errorf("isolatedRuns of one package = %q", got)
	}
}

// TestMergeProfiles: two coverage profiles become one, with a single mode line.
func TestMergeProfiles(t *testing.T) {
	dir := t.TempDir()
	a, b, out := filepath.Join(dir, "a"), filepath.Join(dir, "b"), filepath.Join(dir, "out")
	_ = os.WriteFile(a, []byte("mode: set\nx.go:1.1,2.2 1 1\n"), 0o600)
	_ = os.WriteFile(b, []byte("mode: set\ny.go:1.1,2.2 1 0\n"), 0o600)
	if err := mergeProfiles(out, a, b); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	if string(got) != "mode: set\nx.go:1.1,2.2 1 1\ny.go:1.1,2.2 1 0\n" {
		t.Errorf("merged %q", got)
	}
	if err := mergeProfiles(out, filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing profile was merged")
	}
}

func TestTestCommand(t *testing.T) {
	cases := []struct {
		args          []string
		wantEnv, want string
	}{
		{nil, "CGO_ENABLED=0", "test -tags=noasm,nounsafe ./..."},
		{[]string{"-race"}, "CGO_ENABLED=1", "test -tags=noasm,nounsafe -race -timeout=18m ./..."},
		{[]string{"-untagged"}, "CGO_ENABLED=0", "test ./..."},
		{[]string{"-long", "-short", "--", "-count=1"}, "CGO_ENABLED=0 QRVT_LONG_TESTS=1", "test -tags=noasm,nounsafe -short -count=1 ./..."},
	}
	for _, c := range cases {
		env, args := testCommand(c.args, "noasm,nounsafe")
		if strings.Join(env, " ") != c.wantEnv || strings.Join(args, " ") != c.want {
			t.Errorf("testCommand(%v) = %v, %v; want %s, %s", c.args, env, args, c.wantEnv, c.want)
		}
	}
}
