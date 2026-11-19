// Command dev is the task runner behind scripts/build.*, scripts/test.* and scripts/setup.*, so that
// the logic exists once and runs the same on Linux, macOS and Windows:
//
//	go run ./tools/dev <task> [flags] [-- extra go test arguments]
//
// Tasks: install-tools, check-tools, build, test, cover, lint, fuzz, licenses, vulncheck, changes, changelog,
// check-workflows, release-snapshot, sign-windows and format-vectors. Run a task with -h for its flags.
// Builds and ordinary tests run with CGO_ENABLED=0; the race detector needs cgo.
package main

import (
	"debug/buildinfo"
	"debug/elf"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Pinned Go version (IMPLEMENTATION_PROMPT.md section 3). The other tools are pinned in tools.go.
const goVersion = "go1.27.4"

// longTestsVar turns the long tests on when it is exactly "1" (test -long sets it; DESIGN.md
// section 17). Every test reads it as os.Getenv(...) == "1" or != "1" (TestLongTestGuards).
const longTestsVar = "QRVT_LONG_TESTS"

// targets are the six release targets.
var targets = [][2]string{
	{"linux", "amd64"}, {"linux", "arm64"},
	{"darwin", "amd64"}, {"darwin", "arm64"},
	{"windows", "amd64"}, {"windows", "arm64"},
}

var tasks = map[string]func(args []string) error{
	"install-tools":    installTools,
	"check-tools":      checkTools,
	"build":            build,
	"test":             test,
	"cover":            cover,
	"lint":             lint,
	"fuzz":             fuzz,
	"licenses":         licenses,
	"changes":          changes,
	"changelog":        changelog,
	"platforms":        platforms,
	"vulncheck":        vulncheck,
	"check-workflows":  checkWorkflows,
	"release-snapshot": releaseSnapshot,
	"sign-windows":     signWindows,
	"format-vectors":   formatVectors,
}

func main() {
	if len(os.Args) < 2 || tasks[os.Args[1]] == nil {
		names := make([]string, 0, len(tasks))
		for n := range tasks {
			names = append(names, n)
		}
		slices.Sort(names)
		fmt.Fprintf(os.Stderr, "usage: go run ./tools/dev <%s> [flags]\n", strings.Join(names, "|"))
		os.Exit(2)
	}
	if err := tasks[os.Args[1]](os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "dev:", err)
		os.Exit(1)
	}
}

// run executes a command with extra environment variables, streaming its output.
func run(env []string, name string, args ...string) error {
	fmt.Fprintf(os.Stderr, "+ %s %s %s\n", strings.Join(env, " "), name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr, cmd.Env = os.Stdout, os.Stderr, append(os.Environ(), env...)
	return cmd.Run()
}

// output executes a command and returns its combined output.
func output(name string, args ...string) (string, error) {
	b, err := exec.Command(name, args...).CombinedOutput()
	return string(b), err
}

func newFlags(name string) *flag.FlagSet {
	return flag.NewFlagSet(name, flag.ExitOnError)
}

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// gobin returns where go install puts binaries.
func gobin() string {
	if out, err := output("go", "env", "GOBIN"); err == nil && strings.TrimSpace(out) != "" {
		return strings.TrimSpace(out)
	}
	out, _ := output("go", "env", "GOPATH")
	return filepath.Join(strings.TrimSpace(strings.Split(out, string(os.PathListSeparator))[0]), "bin")
}

func checkGo() error {
	if runtime.Version() != goVersion {
		return fmt.Errorf("go is %s, want %s", runtime.Version(), goVersion)
	}
	return nil
}

func build(args []string) error {
	fs := newFlags("build")
	all := fs.Bool("all", false, "build all six targets instead of the host only")
	out := fs.String("o", "dist", "output directory")
	version := fs.String("version", "dev", "version string embedded in the binary")
	_ = fs.Parse(args) // ExitOnError: never returns an error
	tags, err := releaseTags()
	if err != nil {
		return err
	}
	todo := [][2]string{{runtime.GOOS, runtime.GOARCH}}
	if *all {
		todo = targets
	}
	for _, t := range todo {
		bin := filepath.Join(*out, "qrvt_"+t[0]+"_"+t[1], "qrvt")
		if t[0] == "windows" {
			bin += ".exe"
		}
		env := []string{"CGO_ENABLED=0", "GOOS=" + t[0], "GOARCH=" + t[1]}
		if err := run(env, "go", "build", "-trimpath", "-tags="+tags, "-ldflags", "-s -w -X main.version="+*version, "-o", bin, "./cmd/qrvt"); err != nil {
			return err
		}
		if err := checkStatic(bin, t[0]); err != nil {
			return err
		}
		fmt.Println("static", bin)
	}
	return nil
}

// checkStatic verifies that a binary was built without cgo and, for Linux, that it has no dynamic
// loader and no shared-library dependencies. Darwin and Windows binaries always link the system
// libraries, which is not a runtime dependency.
func checkStatic(bin, goos string) error {
	info, err := buildinfo.ReadFile(bin)
	if err != nil {
		return err
	}
	for _, s := range info.Settings {
		if s.Key == "CGO_ENABLED" && s.Value != "0" {
			return fmt.Errorf("%s was built with CGO_ENABLED=%s", bin, s.Value)
		}
	}
	if goos != "linux" {
		return nil
	}
	f, err := elf.Open(bin)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return fmt.Errorf("%s requests a dynamic loader", bin)
		}
	}
	if libs, _ := f.ImportedLibraries(); len(libs) > 0 {
		return fmt.Errorf("%s needs shared libraries %v", bin, libs)
	}
	return nil
}

// goTest returns the arguments of `go test` built with the given tags (none when tags is empty).
// The test, cover and fuzz tasks pass the release build's tags, so that CI tests what ships.
func goTest(tags string, args ...string) []string {
	out := []string{"test"}
	if tags != "" {
		out = append(out, "-tags="+tags)
	}
	return append(out, args...)
}

// testCommand returns the environment and `go test` arguments of the test task.
func testCommand(args []string, tags string) (env, goArgs []string, part string, patterns []string) {
	fs := newFlags("test")
	race := fs.Bool("race", false, "run with the race detector (enables cgo)")
	long := fs.Bool("long", false, "include long tests (QRVT_LONG_TESTS=1)")
	short := fs.Bool("short", false, "pass -short to go test")
	untagged := fs.Bool("untagged", false, "test the default build, without the release build tags")
	only := fs.String("pkg", "", "comma-separated packages to test instead of ./..., split as ./... is: the root package, internal/pipeline and internal/video each in a go test run of its own (for example ./internal/vision,.,./internal/pipeline)")
	p := fs.String("part", "", "others: every package but internal/video, the root package and internal/pipeline; isolated: each of those alone")
	_ = fs.Parse(args) // ExitOnError: never returns an error
	if *untagged {
		tags = ""
	}
	env = []string{"CGO_ENABLED=0"}
	var extra []string
	if *race {
		// internal/video's engine tests take minutes under the race detector on the slowest runner
		// (macos-26-intel), past go test's default of 10 minutes per package; 18 minutes stays inside
		// the isolated race step's 20.
		// -count=1: a cached race pass reuses one old run's luck for the nondeterministic tests
		// (internal/fec/conv's parallel test, internal/fec/rs's subprocess test), and setup-go
		// restores the test cache across runs (DESIGN.md section 17).
		env, extra = []string{"CGO_ENABLED=1"}, append(extra, "-race", "-timeout=18m", "-count=1")
	}
	if *long {
		// The long tests run for longer than go test's default of 10 minutes per package: at that
		// limit the root package, internal/fec/ldpc and internal/vision panicked, the rest of their
		// tests unrun; the slowest, internal/vision, took 54 minutes on 16 threads (DESIGN.md
		// section 17). 120 minutes leaves room and still fails a hang.
		env = append(env, longTestsVar+"=1")
		extra = append(extra, "-timeout=120m")
	}
	if *short {
		extra = append(extra, "-short")
	}
	target := []string{"./..."}
	if *only != "" {
		target = strings.Split(*only, ",")
	}
	extra = append(append(extra, fs.Args()...), target...)
	return env, goTest(tags, extra...), *p, target
}

// testRuns returns the go test invocations of the test task: goArgs, whose tail is the package
// patterns, split by isolatedRuns over pkgs, the packages the patterns name (go list), and then the
// part that -part selects. -part names a half of the whole module's split, so it needs ./...
func testRuns(goArgs, patterns, pkgs []string, part string) ([][]string, error) {
	if part != "" && !slices.Equal(patterns, []string{"./..."}) {
		return nil, fmt.Errorf("-part %s needs the whole module (./...), not -pkg", part)
	}
	return selectRuns(isolatedRuns(goArgs[:len(goArgs)-len(patterns)], pkgs), part)
}

// test runs every run of testRuns, even after one fails.
func test(args []string) error {
	tags, err := releaseTags()
	if err != nil {
		return err
	}
	env, goArgs, part, patterns := testCommand(args, tags)
	pkgs, err := packages(tags, patterns...)
	if err != nil {
		return err
	}
	runs, err := testRuns(goArgs, patterns, pkgs, part)
	if err != nil {
		return err
	}
	var errs []error
	for _, a := range runs {
		errs = append(errs, run(env, "go", a...))
	}
	return errors.Join(errs...)
}

// lintRuns returns the golangci-lint invocations: the default build, then, when the release build
// has tags, the release build, whose files can differ by build constraint.
func lintRuns(tags string, fix bool) [][]string {
	base := []string{"run"}
	if fix {
		base = append(base, "--fix")
	}
	runs := [][]string{append(slices.Clone(base), "./...")}
	if tags != "" {
		runs = append(runs, append(slices.Clone(base), "--build-tags="+tags, "./..."))
	}
	return runs
}

func lint(args []string) error {
	fs := newFlags("lint")
	fix := fs.Bool("fix", false, "apply automatic fixes")
	_ = fs.Parse(args) // ExitOnError: never returns an error
	p, err := find(toolNamed("golangci-lint"))
	if err != nil {
		return err
	}
	tags, err := releaseTags()
	if err != nil {
		return err
	}
	for _, a := range lintRuns(tags, *fix) {
		if err := run([]string{"CGO_ENABLED=0"}, p, a...); err != nil {
			return err
		}
	}
	return nil
}

// vulnRun is one govulncheck invocation, for one target system.
type vulnRun struct {
	goos string
	args []string
}

// vulncheckRuns returns the govulncheck invocations for the release build: its tags, on each of
// the three target systems, since both decide which code is reachable.
func vulncheckRuns(tags string) []vulnRun {
	var args []string
	if tags != "" {
		args = append(args, "-tags="+tags)
	}
	args = append(args, "./...")
	var runs []vulnRun
	for _, goos := range []string{"linux", "darwin", "windows"} {
		runs = append(runs, vulnRun{goos, args})
	}
	return runs
}

// vulncheck runs the pinned govulncheck over the release build (RELEASE_TAGS). The CI action runs
// it over the default build.
func vulncheck(args []string) error {
	_ = newFlags("vulncheck").Parse(args) // ExitOnError: never returns an error
	p, err := find(toolNamed("govulncheck"))
	if err != nil {
		return err
	}
	tags, err := releaseTags()
	if err != nil {
		return err
	}
	for _, r := range vulncheckRuns(tags) {
		if err := run([]string{"CGO_ENABLED=0", "GOOS=" + r.goos}, p, r.args...); err != nil {
			return fmt.Errorf("govulncheck for %s: %w", r.goos, err)
		}
	}
	return nil
}

// allowedLicenses are the licence names (as go-licenses reports them) whose code may be combined
// with GPL-3.0-or-later code and distributed under the GPL-3.0. GPL-2.0 is absent on purpose:
// go-licenses cannot tell GPL-2.0-only, which is incompatible, from GPL-2.0-or-later; such a
// dependency fails the check and needs a human decision.
var allowedLicenses = []string{
	"0BSD", "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "BSL-1.0", "CC0-1.0", "GPL-3.0", "ISC",
	"LGPL-2.1", "LGPL-3.0", "MIT", "Unlicense", "Zlib",
	// MPL-2.0 is GPL-compatible only through its Secondary Licenses clause. Code whose notice says
	// "Incompatible With Secondary Licenses" (MPL-2.0 Exhibit B) must be refused, and go-licenses
	// cannot see that marking: check every new MPL-2.0 dependency's notices by hand before adding
	// it. No dependency was MPL-2.0 when this list was written (2026-11-13).
	"MPL-2.0",
}

// licenses checks that every package linked into the module's programs, on each target system,
// carries a licence compatible with GPL-3.0-or-later.
func licenses(args []string) error {
	_ = newFlags("licenses").Parse(args) // ExitOnError: never returns an error
	p, err := find(toolNamed("go-licenses"))
	if err != nil {
		return err
	}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		env := []string{"CGO_ENABLED=0", "GOOS=" + goos}
		if err := run(env, p, "check", "./...", "--allowed_licenses="+strings.Join(allowedLicenses, ",")); err != nil {
			return fmt.Errorf("licence check for %s: %w", goos, err)
		}
	}
	fmt.Println("licences: every dependency is compatible with GPL-3.0-or-later")
	return nil
}

// fuzzTags adds unsandboxedhevc to the release build's tags, so that the fuzz task also fuzzes the
// HEVC fast path (FuzzFastPathNAL, SECURITY.md section 9.1), which only that tag compiles; the tag
// changes nothing else.
func fuzzTags(release string) string {
	if release == "" {
		return "unsandboxedhevc"
	}
	return release + ",unsandboxedhevc"
}

// fuzz runs every Fuzz target of the module, one at a time, built with fuzzTags. A target that fails
// does not stop the others; the task fails at the end, listing every failure.
func fuzz(args []string) error {
	fs := newFlags("fuzz")
	d := fs.Duration("time", 10*time.Second, "fuzzing time per target")
	_ = fs.Parse(args) // ExitOnError: never returns an error
	tags, err := releaseTags()
	if err != nil {
		return err
	}
	tags = fuzzTags(tags)
	out, err := exec.Command("go", goTest(tags, "-list", "^Fuzz", "./...")...).Output()
	if err != nil {
		return fmt.Errorf("listing fuzz targets: %w", err)
	}
	ts := fuzzTargets(string(out))
	err = fuzzAll(ts, func(t fuzzTarget) error {
		return run([]string{"CGO_ENABLED=0"}, "go", goTest(tags, "-run", "^$", "-fuzz", "^"+t.name+"$", "-fuzztime", d.String(), t.pkg)...)
	})
	fmt.Printf("fuzzed %d targets for %s each\n", len(ts), d)
	return err
}

// fuzzTarget is one Fuzz function and its package.
type fuzzTarget struct{ pkg, name string }

// fuzzTargets parses the output of `go test -list ^Fuzz ./...`: each package's Fuzz names, then its
// "ok" line. Packages under spikes/ are left out.
func fuzzTargets(list string) []fuzzTarget {
	var (
		ts    []fuzzTarget
		names []string
	)
	for line := range strings.Lines(list) {
		f := strings.Fields(line)
		switch {
		case len(f) == 1 && strings.HasPrefix(f[0], "Fuzz"):
			names = append(names, f[0])
		case len(f) >= 2 && f[0] == "ok":
			if !strings.HasPrefix(f[1], module+"/spikes/") {
				for _, name := range names {
					ts = append(ts, fuzzTarget{f[1], name})
				}
			}
			names = nil
		}
	}
	return ts
}

// fuzzAll calls fuzzOne for every target in turn, whatever the earlier ones returned, and returns
// an error listing every target that failed, so that one finding does not hide another.
func fuzzAll(ts []fuzzTarget, fuzzOne func(fuzzTarget) error) error {
	var failed []string
	for _, t := range ts {
		if err := fuzzOne(t); err != nil {
			failed = append(failed, fmt.Sprintf("\n\t%s %s: %v", t.pkg, t.name, err))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d of %d fuzz targets failed:%s", len(failed), len(ts), strings.Join(failed, ""))
	}
	return nil
}

// isolated is the package that runs on its own (DESIGN.md section 17). Its tests assert properties
// of one process: the worker cap and the watchdog's stop latency, and wall-clock bounds such as
// EngineProbeTime. Those hold on a machine that other test binaries are not oversubscribing, so CI
// tests them under that condition: run in parallel with every other package on a 4-vCPU runner,
// they failed on time bounds that the package alone meets.
const isolated = module + "/internal/video"

// endToEnd are the packages whose always-on end-to-end tests film and decode code videos: each
// runs on its own after the other packages. Run in parallel with them on the slowest runner
// (macos-26-intel), they pushed internal/vision past go test's 10-minute default (PR #47; DESIGN.md
// section 19).
var endToEnd = []string{module, module + "/internal/pipeline"}

// isolatedRuns splits a go test command (head, without its packages) over pkgs, import paths from
// go list, into runs: every package but the isolated and end-to-end ones, then each end-to-end
// package present on its own, then the isolated one alone. A group without a package gives no
// run, so the whole module (./...) gives all four runs and a -pkg list only those it needs.
func isolatedRuns(head, pkgs []string) [][]string {
	others, last := slices.Clone(head), slices.Clone(head)
	var alone [][]string
	for _, p := range pkgs {
		switch {
		case p == isolated || strings.HasPrefix(p, isolated+"/"):
			last = append(last, p)
		case slices.Contains(endToEnd, p):
			alone = append(alone, append(slices.Clone(head), p))
		default:
			others = append(others, p)
		}
	}
	slices.SortFunc(alone, func(a, b []string) int {
		return slices.Index(endToEnd, a[len(a)-1]) - slices.Index(endToEnd, b[len(b)-1])
	})
	var runs [][]string
	if len(others) > len(head) {
		runs = append(runs, others)
	}
	runs = append(runs, alone...)
	if len(last) > len(head) {
		runs = append(runs, last)
	}
	return runs
}

// selectRuns returns the part of an isolatedRuns split that -part names: "" for all, "others"
// for the first run, every package but the end-to-end ones and internal/video, and "isolated" for
// the runs after it, each package on its own. CI runs the two parts of the race tests as separate
// steps, each with its own time limit; the end-to-end packages run in the isolated part, which
// keeps the "others" step as it was before them (PR #47's second run cut it off at its limit).
func selectRuns(runs [][]string, part string) ([][]string, error) {
	switch {
	case part == "":
		return runs, nil
	case len(runs) < 2:
		return nil, fmt.Errorf("-part %s needs the whole module (./...)", part)
	case part == "others":
		return runs[:1], nil
	case part == "isolated":
		return runs[1:], nil
	}
	return nil, fmt.Errorf("-part %q: want others or isolated", part)
}

// fixedStepsBudget is the time, in minutes, that the check job's limit allows for every step but
// the two race steps a runner runs. With those at their limits (15 + 20) it gives the job's 70
// minutes (DESIGN.md section 17). Source: on macos-26-intel, the slowest runner, the non-race
// steps took 17.8 to 28.0 minutes, median 21.2, in the six passing runs before the steps' limits
// were set to about twice their normal times (runs 44474815418 to 44627182924); 35 is 1.65 times
// that median. Twice it, 42, would take the job to 77 minutes, which the designer decides.
const fixedStepsBudget = 35

// checkJobLimits returns, from ci.yml, the check job's timeout-minutes and the timeout-minutes of
// each of its race steps, in order.
func checkJobLimits(b []byte) (job int, race []int) {
	in, step := false, ""
	for _, l := range lines(b) {
		code := stripComment(l)
		switch {
		case code == "  check:":
			in = true
		case in && jobLine.MatchString(code):
			return job, race
		case !in:
		case strings.HasPrefix(code, "      - name: "):
			step = strings.TrimPrefix(code, "      - name: ")
		case strings.HasPrefix(code, "    timeout-minutes: "):
			job, _ = strconv.Atoi(strings.TrimPrefix(code, "    timeout-minutes: "))
		case strings.HasPrefix(code, "        timeout-minutes: ") && strings.HasPrefix(step, "Race tests"):
			n, _ := strconv.Atoi(strings.TrimPrefix(code, "        timeout-minutes: "))
			race = append(race, n)
		}
	}
	return job, race
}

// packages lists the packages that patterns name, with the given build tags.
func packages(tags string, patterns ...string) ([]string, error) {
	args := []string{"list"}
	if tags != "" {
		args = append(args, "-tags="+tags)
	}
	out, err := output("go", append(args, patterns...)...)
	if err != nil {
		return nil, fmt.Errorf("go list: %w: %s", err, out)
	}
	return strings.Fields(out), nil
}

// mergeProfiles writes the coverage profiles srcs, in order, to dst as one profile with the first's
// mode line.
func mergeProfiles(dst string, srcs ...string) error {
	var out strings.Builder
	for i, s := range srcs {
		b, err := os.ReadFile(s)
		if err != nil {
			return err
		}
		text := string(b)
		if i > 0 {
			if _, rest, ok := strings.Cut(text, "\n"); ok && strings.HasPrefix(text, "mode:") {
				text = rest
			}
		}
		out.WriteString(text)
	}
	return os.WriteFile(dst, []byte(out.String()), 0o600)
}
