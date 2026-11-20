package main

import (
	"errors"
	"fmt"
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

// TestIsolatedRuns: the packages run in separate go test invocations with the same arguments:
// every package but the end-to-end ones and internal/video first, then each end-to-end package on
// its own, then internal/video on its own.
func TestIsolatedRuns(t *testing.T) {
	pkgs := []string{module, module + "/internal/fec", module + "/internal/pipeline", module + "/internal/video", module + "/internal/payload"}
	got := isolatedRuns([]string{"test", "-race"}, pkgs)
	// Every other package, then the end-to-end packages each on its own (the root package and
	// internal/pipeline, designer's ruling on PR #47), then internal/video alone.
	want := [][]string{
		{"test", "-race", module + "/internal/fec", module + "/internal/payload"},
		{"test", "-race", module},
		{"test", "-race", module + "/internal/pipeline"},
		{"test", "-race", module + "/internal/video"},
	}
	sameRuns(t, "isolatedRuns", got, want)
	// A package list without some of the groups gives no empty run.
	sameRuns(t, "isolatedRuns of one package", isolatedRuns([]string{"test"}, []string{module + "/internal/fec"}),
		[][]string{{"test", module + "/internal/fec"}})
	sameRuns(t, "isolatedRuns of internal/video", isolatedRuns([]string{"test"}, []string{module + "/internal/video"}),
		[][]string{{"test", module + "/internal/video"}})
}

// TestTestRunsPkg: -pkg goes through the same split as ./..., so the long tests of the root
// package, internal/pipeline and internal/video each run in a go test invocation of their own
// (DESIGN.md section 17), and -part, which names a half of the whole module's split, is refused
// with -pkg.
func TestTestRunsPkg(t *testing.T) {
	_, args, part, patterns := testCommand([]string{"-long", "-pkg", "./internal/vision,.,./internal/pipeline,./internal/video"}, "noasm,nounsafe")
	pkgs := []string{module + "/internal/vision", module, module + "/internal/pipeline", module + "/internal/video"}
	got, err := testRuns(args, patterns, pkgs, part)
	if err != nil {
		t.Fatal(err)
	}
	head := []string{"test", "-tags=noasm,nounsafe", "-timeout=120m"}
	run := func(p string) []string { return append(slices.Clone(head), p) }
	sameRuns(t, "testRuns", got, [][]string{run(module + "/internal/vision"), run(module), run(module + "/internal/pipeline"), run(module + "/internal/video")})
	// Two ordinary packages stay in one run.
	_, args, part, patterns = testCommand([]string{"-pkg", "./internal/vision,./internal/fec/ldpc"}, "")
	got, err = testRuns(args, patterns, []string{module + "/internal/vision", module + "/internal/fec/ldpc"}, part)
	if err != nil {
		t.Fatal(err)
	}
	sameRuns(t, "testRuns of two packages", got, [][]string{{"test", module + "/internal/vision", module + "/internal/fec/ldpc"}})
	// The whole module with -part, as CI's race steps run it.
	_, args, part, patterns = testCommand([]string{"-race", "-part", "isolated"}, "")
	got, err = testRuns(args, patterns, pkgs, part)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0][len(got[0])-1] != module {
		t.Errorf("testRuns -part isolated = %q", got)
	}
	_, args, part, patterns = testCommand([]string{"-race", "-part", "isolated", "-pkg", "./internal/video"}, "")
	if _, err := testRuns(args, patterns, pkgs[3:], part); err == nil {
		t.Error("-part with -pkg gave no error")
	}
}

// sameRuns reports a difference between two lists of go test runs.
func sameRuns(t *testing.T, what string, got, want [][]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s = %q, want %q", what, got, want)
		return
	}
	for i := range want {
		if !slices.Equal(got[i], want[i]) {
			t.Errorf("%s run %d = %q, want %q", what, i, got[i], want[i])
		}
	}
}

// TestSelectRuns: -part runs one half of the split, so that CI can give internal/video under the
// race detector a step (and a time limit) of its own.
func TestSelectRuns(t *testing.T) {
	runs := [][]string{{"test", "a", "b"}, {"test", module}, {"test", module + "/internal/pipeline"}, {"test", "./internal/video/..."}}
	for _, c := range []struct {
		part string
		want [][]string
	}{
		{"", runs},
		// The "others" race step keeps its contents from before PR #47; the end-to-end packages run
		// in the isolated part beside internal/video (designer's ruling on PR #47).
		{"others", runs[:1]},
		{"isolated", runs[1:]},
	} {
		got, err := selectRuns(runs, c.part)
		if err != nil || len(got) != len(c.want) {
			t.Errorf("selectRuns(%q) = %q, %v; want %q", c.part, got, err, c.want)
			continue
		}
		for i := range got {
			if !slices.Equal(got[i], c.want[i]) {
				t.Errorf("selectRuns(%q) run %d = %q, want %q", c.part, i, got[i], c.want[i])
			}
		}
	}
	if _, err := selectRuns(runs, "video"); err == nil {
		t.Error("an unknown part gave no error")
	}
	// A command that is not split has no parts.
	if _, err := selectRuns(runs[:1], "isolated"); err == nil {
		t.Error("a part of an unsplit command gave no error")
	}
}

// splitSteps are the check job's steps that run a part of the test split, by their run line, with
// their limits in minutes: the larger of twice the median on the slowest runner and 1.3 times the
// largest passing time on any runner, rounded up, at least 5 (designer's ruling on the CI budget,
// 2026-11-19; the figures are in DESIGN.md section 17).
var splitSteps = []struct {
	run   string
	limit int
}{
	{"run: scripts/test.sh cover -part rest\n", 22},
	{"run: scripts/test.sh cover -part video\n", 7},
	{"run: scripts/test.ps1 cover -part rest\n", 22},
	{"run: scripts/test.ps1 cover -part video\n", 7},
	{"run: scripts/test.sh -race -part others\n", 15},
	{"run: scripts/test.sh -race -part isolated\n", 21},
	{"run: scripts/test.ps1 -race -part others\n", 15},
	{"run: scripts/test.ps1 -race -part isolated\n", 21},
}

// TestSplitSteps checks that ci.yml runs the coverage and race tests in parts, each in a step with
// its own limit: the coverage run of internal/video after every other package's, and the race runs
// of every package but the isolated ones before those.
func TestSplitSteps(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, c := range splitSteps {
		i := strings.Index(s, c.run)
		if i < 0 {
			t.Errorf("ci.yml lacks %q", strings.TrimSpace(c.run))
			continue
		}
		step := s[strings.LastIndex(s[:i], "      - name:"):i]
		if want := fmt.Sprintf("timeout-minutes: %d\n", c.limit); !strings.Contains(step, want) {
			t.Errorf("the step running %q has no %d-minute limit", strings.TrimSpace(c.run), c.limit)
		}
	}
	for _, old := range []string{"run: scripts/test.sh -race\n", "run: scripts/test.ps1 -race\n", "run: scripts/test.sh cover\n", "run: scripts/test.ps1 cover\n"} {
		if strings.Contains(s, old) {
			t.Errorf("ci.yml still runs %q in one step", strings.TrimSpace(old))
		}
	}
	// A failure in the first part must not skip the second part's result.
	for _, want := range []string{
		"if: ${{ !cancelled() && runner.os != 'Windows' && steps.cover-rest.outcome != 'skipped' }}\n        timeout-minutes: 7\n        shell: bash\n        run: scripts/test.sh cover -part video\n",
		"if: ${{ !cancelled() && runner.os == 'Windows' && steps.cover-rest-windows.outcome != 'skipped' }}\n        timeout-minutes: 7\n        shell: pwsh\n        run: scripts/test.ps1 cover -part video\n",
		"&& steps.race-others.outcome != 'skipped' }}\n        timeout-minutes: 21\n        shell: bash\n        run: scripts/test.sh -race -part isolated\n",
		"&& steps.race-others-windows.outcome != 'skipped' }}\n        timeout-minutes: 21\n        shell: pwsh\n        run: scripts/test.ps1 -race -part isolated\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("a second-part step lacks the !cancelled() condition: want %q", want)
		}
	}
	for _, want := range []string{
		"        id: cover-rest\n        if: runner.os != 'Windows'\n",
		"        id: cover-rest-windows\n        if: runner.os == 'Windows'\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("ci.yml lacks %q", want)
		}
	}
}

// TestCheckJobLimit: the check job's limit covers the two race steps a runner runs, at their
// limits, plus the budget for every other step, so the split is not undone by the job limit
// (DESIGN.md section 17).
func TestCheckJobLimit(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	job, race := checkJobLimits(b)
	// Four race steps: others and isolated on Unix, then the same on windows-2025.
	if !slices.Equal(race, []int{15, 21, 15, 21}) {
		t.Fatalf("race-step limits %v, want [15 21 15 21]", race)
	}
	for i := 0; i < len(race); i += 2 {
		if job < race[i]+race[i+1]+fixedStepsBudget {
			t.Errorf("the check job's limit is %d minutes, want at least %d + %d (race steps) + %d (other steps)", job, race[i], race[i+1], fixedStepsBudget)
		}
	}
	// The designer's ruling: the budget twice the largest non-race median of any runner, rounded up
	// (2 x 23.6 minutes on ubuntu-24.04 = 47.2), and the job's limit the race pair plus it, 84, rounded up to 5.
	if fixedStepsBudget != 48 || job != 85 {
		t.Errorf("fixedStepsBudget %d and job limit %d, want 48 and 85", fixedStepsBudget, job)
	}
	wf := "jobs:\n  check:\n    timeout-minutes: 50\n    steps:\n      - name: Race tests, other packages (Unix)\n        timeout-minutes: 20\n" +
		"      - name: Race tests, isolated packages (Windows)\n        timeout-minutes: 15\n      - name: Lint\n        timeout-minutes: 9\n  other:\n    timeout-minutes: 5\n"
	if job, race := checkJobLimits([]byte(wf)); job != 50 || !slices.Equal(race, []int{20, 15}) {
		t.Errorf("checkJobLimits = %d, %v; want 50, [20 15]", job, race)
	}
}

// TestIsolatedRaceStepFollowsOthers: the isolated packages' race step runs after a failure of the
// other packages' race step, but not alone after a failure earlier in the job, which skipped it.
func TestIsolatedRaceStepFollowsOthers(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"      - name: Race tests, other packages (Unix)\n        id: race-others\n",
		"      - name: Race tests, other packages (Windows)\n        id: race-others-windows\n",
		// The isolated part runs internal/video, the root package and internal/pipeline.
		"      - name: Race tests, isolated packages (Unix)\n",
		"      - name: Race tests, isolated packages (Windows)\n",
		"if: ${{ !cancelled() && runner.os != 'Windows' && steps.race-others.outcome != 'skipped' }}\n",
		"if: ${{ !cancelled() && matrix.os == 'windows-2025' && steps.race-others-windows.outcome != 'skipped' }}\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("ci.yml lacks %q", want)
		}
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
		{[]string{"-race"}, "CGO_ENABLED=1", "test -tags=noasm,nounsafe -race -timeout=18m -count=1 ./..."},
		{[]string{"-untagged"}, "CGO_ENABLED=0", "test ./..."},
		{[]string{"-long", "-short", "--", "-count=1"}, "CGO_ENABLED=0 QRVT_LONG_TESTS=1", "test -tags=noasm,nounsafe -timeout=120m -short -count=1 ./..."},
		{[]string{"-long", "-pkg", "./internal/vision,.,./internal/pipeline"}, "CGO_ENABLED=0 QRVT_LONG_TESTS=1", "test -tags=noasm,nounsafe -timeout=120m ./internal/vision . ./internal/pipeline"},
	}
	for _, c := range cases {
		env, args, _, patterns := testCommand(c.args, "noasm,nounsafe")
		if strings.Join(env, " ") != c.wantEnv || strings.Join(args, " ") != c.want {
			t.Errorf("testCommand(%v) = %v, %v; want %s, %s", c.args, env, args, c.wantEnv, c.want)
		}
		// The package patterns are the arguments' tail.
		if !slices.Equal(args[len(args)-len(patterns):], patterns) {
			t.Errorf("testCommand(%v) patterns %q are not the tail of %q", c.args, patterns, args)
		}
	}
}

// TestPackagesWholeModule: packages without a pattern lists the whole module, as pkgDirs (the
// platforms task) relies on; go list without one would list only the current directory's package.
func TestPackagesWholeModule(t *testing.T) {
	got, err := packages("")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, module+"/internal/video") || !slices.Contains(got, module+"/internal/vision") {
		t.Errorf("packages() = %q, want the whole module", got)
	}
}

// TestPackagesNoMatch: a pattern that names no package is an error that says so, not go list's
// warning read as package names (review of 3ba96bf).
func TestPackagesNoMatch(t *testing.T) {
	got, err := packages("", module+"/docs/...")
	if err == nil || !strings.Contains(err.Error(), "matched no packages") {
		t.Errorf("packages(docs/...) = %q, %v; want an error naming the pattern", got, err)
	}
}
