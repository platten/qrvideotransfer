package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The platforms task (DESIGN.md section 17) checks, on a developer's machine before every push,
// what the CI runners would otherwise find first:
//
//   - compile: go vet and go test -c of every package for all six targets, with and without the
//     release build tags, CGO_ENABLED=0;
//   - lint: golangci-lint for linux, darwin and windows, with and without the release tags;
//   - race: race-enabled test binaries for linux/arm64, windows/amd64, darwin/amd64 and
//     darwin/arm64, with zig as the C cross-compiler (windows/arm64 has no race detector; the
//     host's own race run is the test task's -race);
//   - windows: the windows/amd64 test binaries, plain and race, run through WSL interop;
//   - arm64: the linux/arm64 test binaries run under user-mode emulation (qemu-aarch64), with
//     QRVT_EMULATED=1 so that tests asserting wall-clock bounds can relax them.
//
// macOS binaries and the arm64 Windows binaries are built but not run: those stay CI-only.

// zigVersion is the pinned zig, installed by scripts/setup.sh --cross.
const zigVersion = "0.17.1"

// emulatedEnv is set for test binaries run under emulation; tests that assert wall-clock bounds
// read it and log those bounds instead of asserting them.
const emulatedEnv = "QRVT_EMULATED=1"

type platformJob struct {
	name string
	env  []string
	tags string
}

// compileJobs returns one job per target without build tags and, when tags is set, one more per
// target with them; a job is named "goos/goarch", followed by its tags.
func compileJobs(tags string) []platformJob {
	var jobs []platformJob
	for _, set := range slices.Compact([]string{"", tags}) {
		for _, t := range targets {
			name := strings.TrimSpace(t[0] + "/" + t[1] + " " + set)
			jobs = append(jobs, platformJob{name, []string{"CGO_ENABLED=0", "GOOS=" + t[0], "GOARCH=" + t[1]}, set})
		}
	}
	return jobs
}

// zigTarget returns zig's target triple for a race-enabled build, or false where Go has no race
// detector (windows/arm64).
func zigTarget(goos, goarch string) (string, bool) {
	t, ok := map[string]string{
		"linux/amd64":   "x86_64-linux-gnu",
		"linux/arm64":   "aarch64-linux-gnu",
		"windows/amd64": "x86_64-windows-gnu",
		"darwin/amd64":  "x86_64-macos",
		"darwin/arm64":  "aarch64-macos",
	}[goos+"/"+goarch]
	return t, ok
}

// raceEnv returns the environment and external linker flags of a race-enabled cross build.
// Windows needs the import library of WaitOnAddress, which the race runtime calls, and a low
// image base: at lld's default the race runtime cannot map its shadow memory (error 87).
func raceEnv(zig, goos, goarch, target string) (env []string, ldflags string) {
	env = []string{"CGO_ENABLED=1", "GOOS=" + goos, "GOARCH=" + goarch,
		"CC=" + zig + " cc -target " + target, "CXX=" + zig + " c++ -target " + target}
	if goos == "windows" {
		ldflags = "-extldflags '-lapi-ms-win-core-synch-l1-2-0 -Wl,--image-base=0x400000'"
	}
	return env, ldflags
}

// findZig returns zig from the ZIG environment variable, else from PATH, else "".
func findZig(getenv func(string) string, lookPath func(string) (string, error)) string {
	if z := getenv("ZIG"); z != "" {
		return z
	}
	if p, err := lookPath("zig"); err == nil {
		return p
	}
	return ""
}

// arm64Runner returns how to run a linux/arm64 binary: directly when binfmt has an aarch64 entry
// (""), else through qemu-aarch64 on PATH; false when neither exists.
func arm64Runner(lookPath func(string) (string, error), binfmt func(string) bool) (string, bool) {
	if binfmt("/proc/sys/fs/binfmt_misc/qemu-aarch64") {
		return "", true
	}
	for _, q := range []string{"qemu-aarch64-static", "qemu-aarch64"} {
		if p, err := lookPath(q); err == nil {
			return p, true
		}
	}
	return "", false
}

type testResult struct{ passed, failed, skipped []string }

// parseTestOutput collects the top-level test results of a -test.v run.
func parseTestOutput(out string) testResult {
	var r testResult
	for _, l := range lines([]byte(out)) {
		for prefix, list := range map[string]*[]string{"--- PASS: ": &r.passed, "--- FAIL: ": &r.failed, "--- SKIP: ": &r.skipped} {
			if rest, ok := strings.CutPrefix(l, prefix); ok {
				*list = append(*list, strings.Fields(rest)[0])
			}
		}
	}
	return r
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// platformReport collects one line per step and the errors.
type platformReport struct {
	lines []string
	errs  []error
}

func (r *platformReport) add(format string, a ...any) {
	l := fmt.Sprintf(format, a...)
	r.lines = append(r.lines, l)
	fmt.Println("platforms:", l)
}

func (r *platformReport) fail(err error) {
	r.errs = append(r.errs, err)
	r.add("FAIL %v", err)
}

// timed runs f and returns its duration.
func timed(f func()) time.Duration {
	s := time.Now()
	f()
	return time.Since(s).Round(time.Second)
}

func goTagArgs(tags string) []string {
	if tags == "" {
		return nil
	}
	return []string{"-tags=" + tags}
}

// buildTests builds the test binary of every package into dir with the given environment, tags
// and ldflags, and returns the packages that have one.
func buildTests(env []string, tags, ldflags, dir string, race bool) error {
	args := append([]string{"test", "-c", "-o", dir + string(os.PathSeparator)}, goTagArgs(tags)...)
	if race {
		args = append(args, "-race")
	}
	if ldflags != "" {
		args = append(args, "-ldflags="+ldflags)
	}
	return run(env, "go", append(args, "./...")...)
}

// pkgDirs returns, for each package of the module, its directory relative to the module root.
func pkgDirs(tags string) (map[string]string, error) {
	pkgs, err := packages(tags)
	if err != nil {
		return nil, err
	}
	dirs := map[string]string{}
	for _, p := range pkgs {
		rel := strings.TrimPrefix(strings.TrimPrefix(p, module), "/")
		if rel == "" {
			rel = "."
		}
		dirs[p] = rel
	}
	return dirs, nil
}

// runBinaries runs the test binary of each package found in bindir (named <base>.test or
// <base>.test.exe), one at a time, from the package's directory under root, through runner when
// it is set, and reports the results.
func runBinaries(r *platformReport, label, runner, bindir, root, suffix string, env []string, dirs map[string]string, timeout time.Duration) {
	pkgs := make([]string, 0, len(dirs))
	for p := range dirs {
		pkgs = append(pkgs, p)
	}
	slices.Sort(pkgs)
	var npkg, npass, nskip int
	for _, p := range pkgs {
		bin := filepath.Join(bindir, path.Base(p)+".test"+suffix)
		if !fileExists(bin) {
			continue
		}
		npkg++
		args := []string{"-test.v", "-test.count=1", "-test.timeout=" + timeout.String()}
		name := bin
		if runner != "" {
			name, args = runner, append([]string{bin}, args...)
		}
		cmd := exec.Command(name, args...)
		cmd.Dir = filepath.Join(root, filepath.FromSlash(dirs[p]))
		cmd.Env = append(os.Environ(), env...)
		var out []byte
		var err error
		d := timed(func() { out, err = cmd.CombinedOutput() })
		res := parseTestOutput(string(out))
		npass += len(res.passed)
		nskip += len(res.skipped)
		status := "ok"
		if err != nil {
			status = "FAIL"
			r.fail(fmt.Errorf("%s %s: %v; failed: %s", label, p, err, strings.Join(res.failed, ", ")))
			fmt.Println(lastLines(string(out), 30))
		}
		line := fmt.Sprintf("%s %s %s: %s, %d passed", label, status, p, d, len(res.passed))
		if len(res.skipped) > 0 {
			line += ", skipped: " + strings.Join(res.skipped, " ")
		}
		r.add("%s", line)
	}
	r.add("%s: %d packages, %d tests passed, %d skipped", label, npkg, npass, nskip)
}

func lastLines(s string, n int) string {
	ls := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, "\n")
}

// trackedFiles lists the files git tracks, relative to the module root.
func trackedFiles() ([]string, error) {
	out, err := exec.Command("git", "ls-files", "-z").Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	return strings.FieldsFunc(string(out), func(r rune) bool { return r == 0 }), nil
}

// copyTree copies the given files from the current directory to dst, keeping their paths.
func copyTree(files []string, dst string) error {
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // deleted in the working tree
			}
			return err
		}
		p := filepath.Join(dst, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// windowsTemp returns a new directory under the Windows user's temporary directory, as a Linux
// path, through WSL interop.
func windowsTemp() (string, error) {
	out, err := exec.Command("cmd.exe", "/d", "/c", "echo %TEMP%").Output()
	if err != nil {
		return "", fmt.Errorf("cmd.exe: %w", err)
	}
	win := strings.TrimSpace(strings.ReplaceAll(string(out), "\r", ""))
	lin, err := exec.Command("wslpath", "-u", win).Output()
	if err != nil {
		return "", fmt.Errorf("wslpath %s: %w", win, err)
	}
	return os.MkdirTemp(strings.TrimSpace(string(lin)), "qrvt-platforms-")
}

func platforms(args []string) error {
	fs := newFlags("platforms")
	skip := fs.String("skip", "", "comma-separated steps to skip: compile, lint, race, windows, arm64")
	timeout := fs.Duration("timeout", 30*time.Minute, "test timeout per package where tests run")
	_ = fs.Parse(args) // ExitOnError: never returns an error
	skipped := map[string]bool{}
	for _, s := range strings.Split(*skip, ",") {
		skipped[strings.TrimSpace(s)] = true
	}
	tags, err := releaseTags()
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "qrvt-platforms-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	r := &platformReport{}
	total := timed(func() {
		if !skipped["compile"] {
			r.add("compile: %s", timed(func() { platformsCompile(r, tags, tmp) }))
		}
		if !skipped["lint"] {
			r.add("lint: %s", timed(func() { platformsLint(r, tags) }))
		}
		zig := ""
		if !skipped["race"] {
			r.add("race: %s", timed(func() { zig = platformsRace(r, tags, tmp) }))
		}
		if !skipped["windows"] {
			r.add("windows: %s", timed(func() { platformsWindows(r, tags, tmp, zig, *timeout) }))
		}
		if !skipped["arm64"] {
			r.add("arm64: %s", timed(func() { platformsArm64(r, tags, tmp, *timeout) }))
		}
	})
	fmt.Println()
	fmt.Println("platforms summary:")
	for _, l := range r.lines {
		fmt.Println("  " + l)
	}
	fmt.Printf("  total: %s\n", total)
	fmt.Println("  not covered here: running the darwin binaries (macOS runners) and the windows/arm64 binaries (windows-11-arm runner) stays CI-only")
	return errors.Join(r.errs...)
}

func platformsCompile(r *platformReport, tags, tmp string) {
	for _, j := range compileJobs(tags) {
		if err := run(j.env, "go", append(append([]string{"vet"}, goTagArgs(j.tags)...), "./...")...); err != nil {
			r.fail(fmt.Errorf("go vet %s: %w", j.name, err))
			continue
		}
		dir, err := os.MkdirTemp(tmp, "compile-")
		if err != nil {
			r.fail(err)
			return
		}
		if err := buildTests(j.env, j.tags, "", dir, false); err != nil {
			r.fail(fmt.Errorf("go test -c %s: %w", j.name, err))
		} else {
			r.add("compile ok %s", j.name)
		}
		_ = os.RemoveAll(dir)
	}
}

func platformsLint(r *platformReport, tags string) {
	p, err := find(toolNamed("golangci-lint"))
	if err != nil {
		r.fail(err)
		return
	}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		for _, a := range lintRuns(tags, false) {
			if err := run([]string{"CGO_ENABLED=0", "GOOS=" + goos}, p, a...); err != nil {
				r.fail(fmt.Errorf("lint %s %s: %w", goos, strings.Join(a, " "), err))
			} else {
				r.add("lint ok %s %s", goos, strings.Join(a[1:len(a)-1], " "))
			}
		}
	}
}

// platformsRace builds the race-enabled test binaries and returns zig's path, or "" when zig was
// not used. The windows/amd64 binaries stay in tmp/race-windows-amd64 for the windows step.
func platformsRace(r *platformReport, tags, tmp string) string {
	zig := findZig(os.Getenv, exec.LookPath)
	if zig == "" {
		r.add("race: not run: zig not found (set ZIG, or run scripts/setup.sh --cross)")
		return ""
	}
	if out, err := output(zig, "version"); err != nil || strings.TrimSpace(out) != zigVersion {
		r.add("race: not run: %s is zig %q, want %s (scripts/setup.sh --cross)", zig, strings.TrimSpace(out), zigVersion)
		return ""
	}
	for _, t := range [][2]string{{"linux", "arm64"}, {"windows", "amd64"}, {"darwin", "amd64"}, {"darwin", "arm64"}} {
		target, _ := zigTarget(t[0], t[1])
		env, ldflags := raceEnv(zig, t[0], t[1], target)
		dir := filepath.Join(tmp, "race-"+t[0]+"-"+t[1])
		if err := os.MkdirAll(dir, 0o755); err != nil {
			r.fail(err)
			return zig
		}
		if err := buildTests(env, tags, ldflags, dir, true); err != nil {
			r.fail(fmt.Errorf("race build %s/%s: %w", t[0], t[1], err))
			continue
		}
		r.add("race build ok %s/%s (zig %s)", t[0], t[1], target)
	}
	r.add("race: windows/arm64 has no race detector; linux/amd64 is the test task's -race")
	return zig
}

// platformsWindows runs the windows/amd64 test binaries, plain and race, through WSL interop, in
// a copy of the tracked files under the Windows user's temporary directory, removed afterwards.
func platformsWindows(r *platformReport, tags, tmp, zig string, timeout time.Duration) {
	if _, err := exec.LookPath("cmd.exe"); err != nil || !fileExists("/proc/sys/fs/binfmt_misc/WSLInterop") {
		r.add("windows: not run: no WSL interop (cmd.exe not reachable)")
		return
	}
	root, err := windowsTemp()
	if err != nil {
		r.fail(err)
		return
	}
	defer func() { _ = os.RemoveAll(root) }()
	files, err := trackedFiles()
	if err == nil {
		err = copyTree(files, root)
	}
	if err != nil {
		r.fail(err)
		return
	}
	dirs, err := pkgDirs(tags)
	if err != nil {
		r.fail(err)
		return
	}
	plain := filepath.Join(root, ".bin-plain")
	if err := buildTests([]string{"CGO_ENABLED=0", "GOOS=windows", "GOARCH=amd64"}, tags, "", plain, false); err != nil {
		r.fail(fmt.Errorf("windows/amd64 test build: %w", err))
		return
	}
	runBinaries(r, "windows/amd64", "", plain, root, ".exe", nil, dirs, timeout)
	race := filepath.Join(tmp, "race-windows-amd64")
	if zig == "" || !fileExists(race) {
		r.add("windows/amd64 race: not run: no race binaries (the race step needs zig)")
		return
	}
	runBinaries(r, "windows/amd64 race", "", race, root, ".exe", nil, dirs, timeout)
}

// platformsArm64 runs the linux/arm64 test binaries under user-mode emulation, from the
// packages' own directories, with QRVT_EMULATED=1.
func platformsArm64(r *platformReport, tags, tmp string, timeout time.Duration) {
	runner, ok := arm64Runner(exec.LookPath, fileExists)
	if !ok {
		r.add("arm64: not run: qemu-aarch64 not found (scripts/setup.sh --cross installs qemu-user-static)")
		return
	}
	dirs, err := pkgDirs(tags)
	if err != nil {
		r.fail(err)
		return
	}
	bin := filepath.Join(tmp, "arm64-plain")
	if err := buildTests([]string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=arm64"}, tags, "", bin, false); err != nil {
		r.fail(fmt.Errorf("linux/arm64 test build: %w", err))
		return
	}
	wd, err := os.Getwd()
	if err != nil {
		r.fail(err)
		return
	}
	runBinaries(r, "linux/arm64 (emulated)", runner, bin, wd, "", []string{emulatedEnv}, dirs, timeout)
}
