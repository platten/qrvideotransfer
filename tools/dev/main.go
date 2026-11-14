// Command dev is the task runner behind scripts/build.*, scripts/test.* and scripts/setup.*, so that
// the logic exists once and runs the same on Linux, macOS and Windows:
//
//	go run ./tools/dev <task> [flags] [-- extra go test arguments]
//
// Tasks: install-tools, check-tools, build, test, cover, lint, fuzz, licenses, vulncheck,
// check-workflows, release-snapshot and sign-windows. Run a task with -h for its flags. Builds and
// ordinary tests run with CGO_ENABLED=0; the race detector needs cgo.
package main

import (
	"debug/buildinfo"
	"debug/elf"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// Pinned Go version (IMPLEMENTATION_PROMPT.md section 3). The other tools are pinned in tools.go.
const goVersion = "go1.27.4"

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
	"vulncheck":        vulncheck,
	"check-workflows":  checkWorkflows,
	"release-snapshot": releaseSnapshot,
	"sign-windows":     signWindows,
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
func testCommand(args []string, tags string) (env, goArgs []string) {
	fs := newFlags("test")
	race := fs.Bool("race", false, "run with the race detector (enables cgo)")
	long := fs.Bool("long", false, "include long tests (QRVT_LONG_TESTS=1)")
	short := fs.Bool("short", false, "pass -short to go test")
	untagged := fs.Bool("untagged", false, "test the default build, without the release build tags")
	_ = fs.Parse(args) // ExitOnError: never returns an error
	if *untagged {
		tags = ""
	}
	env = []string{"CGO_ENABLED=0"}
	var extra []string
	if *race {
		env, extra = []string{"CGO_ENABLED=1"}, append(extra, "-race")
	}
	if *long {
		env = append(env, "QRVT_LONG_TESTS=1")
	}
	if *short {
		extra = append(extra, "-short")
	}
	extra = append(append(extra, fs.Args()...), "./...")
	return env, goTest(tags, extra...)
}

func test(args []string) error {
	tags, err := releaseTags()
	if err != nil {
		return err
	}
	env, goArgs := testCommand(args, tags)
	return run(env, "go", goArgs...)
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

func fuzzTags(release string) string { return release }

// fuzz runs every Fuzz target of the module, one at a time.
func fuzz(args []string) error {
	fs := newFlags("fuzz")
	d := fs.Duration("time", 10*time.Second, "fuzzing time per target")
	_ = fs.Parse(args) // ExitOnError: never returns an error
	tags, err := releaseTags()
	if err != nil {
		return err
	}
	out, err := exec.Command("go", goTest(tags, "-list", "^Fuzz", "./...")...).Output()
	if err != nil {
		return fmt.Errorf("listing fuzz targets: %w", err)
	}
	var names []string
	n := 0
	for line := range strings.Lines(string(out)) {
		f := strings.Fields(line)
		switch {
		case len(f) == 1 && strings.HasPrefix(f[0], "Fuzz"):
			names = append(names, f[0])
		case len(f) >= 2 && f[0] == "ok":
			for _, name := range names {
				if strings.HasPrefix(f[1], "github.com/platten/qrvideotransfer/spikes/") {
					continue
				}
				n++
				env := []string{"CGO_ENABLED=0"}
				if err := run(env, "go", goTest(tags, "-run", "^$", "-fuzz", "^"+name+"$", "-fuzztime", d.String(), f[1])...); err != nil {
					return err
				}
			}
			names = nil
		}
	}
	fmt.Printf("fuzzed %d targets for %s each\n", n, d)
	return nil
}
