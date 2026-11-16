package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"syscall"
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
//   - arm64: the linux/arm64 test binaries, natively on an arm64 host or under user-mode
//     emulation through binfmt, with QRVT_EMULATED=1 there.
//
// The routine run before a push leaves out arm64 (about 12 minutes); -full runs every step and
// fails on any step that could not run, once per pull request, before the push that opens it.

// zigVersion is the pinned zig, installed by scripts/setup.sh --cross.
const zigVersion = "0.17.1"

// emulatedEnv is set for test binaries run under emulation; tests that assert wall-clock bounds
// read it and log those bounds instead of asserting them.
const emulatedEnv = "QRVT_EMULATED=1"

// emulationNotice names every assertion that is logged rather than asserted under emulation.
const emulationNotice = "under emulation (QRVT_EMULATED=1) these bounds are logged, not asserted: internal/video TestHostileFiles, 60 s per hostile file"

// emulationMarker is the phrase with which tests log a bound they did not assert under emulation.
const emulationMarker = "logged rather than asserted under emulation"

func windowsKillCommand(exe string) (string, []string) { return "", nil }

// loggedBound matches a test's log line ("    file_test.go:12: …") that reports a bound logged
// rather than asserted under emulation; output that merely quotes such a line does not match.
var loggedBound = regexp.MustCompile(`^\s+\w+_test\.go:\d+: .*logged rather than asserted under emulation`)

// notCoveredLocally is what only the CI runners check (DESIGN.md section 17).
var notCoveredLocally = []string{
	"running anything on macOS (amd64 or arm64)",
	"running the windows/arm64 binaries",
	"native arm64 timing on the arm runners",
	"race runs on linux/arm64 and darwin",
	"internal/video's race tests on Windows",
}

// platformSteps are the task's steps, in order.
var platformSteps = []string{"compile", "lint", "race", "windows", "arm64"}

type platformOpts struct {
	skip            map[string]string // step -> why it is skipped
	strict          bool              // a step that could not run fails the task
	timeout         time.Duration     // per test binary
	emulatedTimeout time.Duration     // per test binary under emulation
}

// parsePlatformArgs parses the task's flags. Without -full, arm64 is left out (the routine run).
func parsePlatformArgs(args []string) (platformOpts, error) {
	fs := newFlags("platforms")
	full := fs.Bool("full", false, "run every step, linux/arm64 included, and fail on any step that could not run (once per pull request, before the push that opens it)")
	strict := fs.Bool("strict", false, "fail on any step that could not run")
	skip := fs.String("skip", "", "comma-separated steps to leave out: "+strings.Join(platformSteps, ", "))
	timeout := fs.Duration("timeout", 30*time.Minute, "time limit per test binary")
	emulated := fs.Duration("emulated-timeout", 90*time.Minute, "time limit per test binary under emulation")
	_ = fs.Parse(args) // ExitOnError: never returns an error
	o := platformOpts{skip: map[string]string{}, strict: *strict || *full, timeout: *timeout, emulatedTimeout: *emulated}
	if !*full {
		o.skip["arm64"] = "routine run; -full runs it"
	}
	for _, s := range strings.Split(*skip, ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		if !slices.Contains(platformSteps, s) {
			return o, fmt.Errorf("-skip %q: the steps are %s", s, strings.Join(platformSteps, ", "))
		}
		o.skip[s] = "skipped by -skip"
	}
	return o, nil
}

// platformDeps are the task's contacts with the machine, replaced in tests.
type platformDeps struct {
	hostOS, hostArch string
	lookPath         func(string) (string, error)
	readFile         func(string) ([]byte, error)
	getenv           func(string) string
	exists           func(string) bool
	// run runs a command with extra environment, streaming its output.
	run func(ctx context.Context, env []string, name string, args ...string) error
	// output returns a command's standard output.
	output func(ctx context.Context, name string, args ...string) (string, error)
	// runTest runs a test binary in dir and returns its combined output.
	runTest  func(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error)
	lintTool func() (string, error)
	pkgDirs  func(tags string) (map[string]string, error)
	// listFiles lists the files to copy for the Windows run, relative to the module root.
	listFiles func() ([]string, error)
	report    io.Writer // where results are printed as they come (nil: not printed)
	tags      string    // the release build tags
	tempRoot  string    // where the task's own temporary directories go
}

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

const binfmtAarch64 = "/proc/sys/fs/binfmt_misc/qemu-aarch64"

// arm64Mode returns how linux/arm64 binaries run here: "native" on an arm64 Linux host, "binfmt"
// where an enabled aarch64 entry hands them to qemu, or "" and the reason they cannot. qemu without
// binfmt is not enough: tests that start their own binary again would exec an arm64 binary.
func arm64Mode(d platformDeps) (mode, reason string) {
	if d.hostOS == "linux" && d.hostArch == "arm64" {
		return "native", ""
	}
	b, err := d.readFile(binfmtAarch64)
	switch {
	case err == nil && strings.HasPrefix(string(b), "enabled"):
		return "binfmt", ""
	case err == nil:
		return "", "the binfmt entry for aarch64 is disabled (" + binfmtAarch64 + ")"
	}
	for _, q := range []string{"qemu-aarch64-static", "qemu-aarch64"} {
		if p, err := d.lookPath(q); err == nil {
			return "", p + " is installed but binfmt has no aarch64 entry, which tests that start their own binary again need"
		}
	}
	return "", "qemu-aarch64 not found (scripts/setup.sh --cross installs qemu-user-static)"
}

// wslUnavailable returns why the Windows binaries cannot run here, or "".
func wslUnavailable(d platformDeps) string {
	if _, err := d.lookPath("cmd.exe"); err != nil {
		return "no WSL interop: cmd.exe is not reachable"
	}
	if _, err := d.readFile("/proc/sys/fs/binfmt_misc/WSLInterop"); err != nil {
		return "no WSL interop: /proc/sys/fs/binfmt_misc/WSLInterop is not registered"
	}
	return ""
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

// copyable drops what must never leave the machine (recordings/) from a file list.
func copyable(files []string) []string {
	var out []string
	for _, f := range files {
		if f == "recordings" || strings.HasPrefix(f, "recordings/") {
			continue
		}
		out = append(out, f)
	}
	return out
}

type entryKind int

const (
	kindOK entryKind = iota
	kindInfo
	kindFail
	kindNotRun
	kindSkipped
	kindCIOnly // not run here, and accepted as CI-only (DESIGN.md section 17)
)

type reportEntry struct {
	kind entryKind
	text string
}

// platformReport collects the task's results, and prints each as it comes when w is set.
type platformReport struct {
	entries []reportEntry
	w       io.Writer
}

func (r *platformReport) add(k entryKind, format string, a ...any) {
	e := reportEntry{k, fmt.Sprintf(format, a...)}
	r.entries = append(r.entries, e)
	if r.w != nil {
		_, _ = fmt.Fprintln(r.w, "platforms:", e.line())
	}
}

func (e reportEntry) line() string {
	switch e.kind {
	case kindFail:
		return "FAIL " + e.text
	case kindNotRun:
		return "NOT RUN " + e.text
	case kindSkipped:
		return "SKIPPED " + e.text
	case kindCIOnly:
		return "CI-ONLY " + e.text
	}
	return e.text
}

// summary returns the report's lines, the counts and the list of what only CI checks.
func (r *platformReport) summary(strict bool) []string {
	var out []string
	count := map[entryKind]int{}
	for _, e := range r.entries {
		out = append(out, e.line())
		count[e.kind]++
	}
	notRun := fmt.Sprintf("%d not run", count[kindNotRun])
	if strict && count[kindNotRun] > 0 {
		notRun += " (failing: -strict or -full)"
	}
	out = append(out, fmt.Sprintf("%d failed, %s, %d CI-only, %d steps skipped", count[kindFail], notRun, count[kindCIOnly], count[kindSkipped]))
	out = append(out, "not covered locally (CI-only): "+strings.Join(notCoveredLocally, "; "))
	return out
}

// err returns the task's outcome: any failure, and with strict any step that could not run.
func (r *platformReport) err(strict bool) error {
	var errs []error
	for _, e := range r.entries {
		switch {
		case e.kind == kindFail:
			errs = append(errs, errors.New(e.line()))
		case strict && e.kind == kindNotRun:
			errs = append(errs, errors.New("not run: "+e.text))
		}
	}
	return errors.Join(errs...)
}

func goTagArgs(tags string) []string {
	if tags == "" {
		return nil
	}
	return []string{"-tags=" + tags}
}

// buildTests builds the test binary of every package into dir.
func buildTests(ctx context.Context, d platformDeps, env []string, tags, ldflags, dir string, race bool) error {
	args := append([]string{"test", "-c", "-o", dir + string(os.PathSeparator)}, goTagArgs(tags)...)
	if race {
		args = append(args, "-race")
	}
	if ldflags != "" {
		args = append(args, "-ldflags="+ldflags)
	}
	return d.run(ctx, env, "go", append(args, "./...")...)
}

// runPlatforms runs the steps that opts leaves in.
func runPlatforms(ctx context.Context, o platformOpts, d platformDeps) (*platformReport, error) {
	r := &platformReport{w: d.report}
	steps := map[string]func(){
		"compile": func() { platformsCompile(ctx, r, d) },
		"lint":    func() { platformsLint(ctx, r, d) },
		"race":    func() { platformsRace(ctx, r, d) },
		"windows": func() { platformsWindows(ctx, r, d, o, o.skip["race"] == "") },
		"arm64":   func() { platformsArm64(ctx, r, d, o) },
	}
	for _, s := range platformSteps {
		if why, ok := o.skip[s]; ok {
			r.add(kindSkipped, "%s: %s", s, why)
			continue
		}
		if ctx.Err() != nil {
			r.add(kindNotRun, "%s: interrupted", s)
			continue
		}
		start := time.Now()
		steps[s]()
		r.add(kindInfo, "%s: %s", s, time.Since(start).Round(time.Second))
	}
	return r, r.err(o.strict)
}

func platformsCompile(ctx context.Context, r *platformReport, d platformDeps) {
	for _, j := range compileJobs(d.tags) {
		if err := d.run(ctx, j.env, "go", append(append([]string{"vet"}, goTagArgs(j.tags)...), "./...")...); err != nil {
			r.add(kindFail, "go vet %s: %v", j.name, err)
			continue
		}
		dir, err := os.MkdirTemp(d.tempRoot, "compile-")
		if err != nil {
			r.add(kindFail, "compile: %v", err)
			return
		}
		if err := buildTests(ctx, d, j.env, j.tags, "", dir, false); err != nil {
			r.add(kindFail, "go test -c %s: %v", j.name, err)
		} else {
			r.add(kindOK, "compile ok %s", j.name)
		}
		_ = os.RemoveAll(dir)
	}
}

func platformsLint(ctx context.Context, r *platformReport, d platformDeps) {
	p, err := d.lintTool()
	if err != nil {
		r.add(kindFail, "lint: %v", err)
		return
	}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		for _, a := range lintRuns(d.tags, false) {
			if err := d.run(ctx, []string{"CGO_ENABLED=0", "GOOS=" + goos}, p, a...); err != nil {
				r.add(kindFail, "lint %s %s: %v", goos, strings.Join(a, " "), err)
			} else {
				r.add(kindOK, "lint ok %s %s", goos, strings.Join(a[1:len(a)-1], " "))
			}
		}
	}
}

// platformsRace builds the race-enabled test binaries; the windows/amd64 ones stay in
// tempRoot/race-windows-amd64 for the windows step.
func platformsRace(ctx context.Context, r *platformReport, d platformDeps) {
	zig := findZig(d.getenv, d.lookPath)
	if zig == "" {
		r.add(kindNotRun, "race: zig not found (set ZIG, or run scripts/setup.sh --cross)")
		return
	}
	if out, err := d.output(ctx, zig, "version"); err != nil || strings.TrimSpace(out) != zigVersion {
		r.add(kindNotRun, "race: %s is zig %q, want %s (scripts/setup.sh --cross)", zig, strings.TrimSpace(out), zigVersion)
		return
	}
	for _, t := range [][2]string{{"linux", "arm64"}, {"windows", "amd64"}, {"darwin", "amd64"}, {"darwin", "arm64"}} {
		target, _ := zigTarget(t[0], t[1])
		env, ldflags := raceEnv(zig, t[0], t[1], target)
		dir := filepath.Join(d.tempRoot, "race-"+t[0]+"-"+t[1])
		if err := os.MkdirAll(dir, 0o755); err != nil {
			r.add(kindFail, "race: %v", err)
			return
		}
		if err := buildTests(ctx, d, env, d.tags, ldflags, dir, true); err != nil {
			r.add(kindFail, "race build %s/%s: %v", t[0], t[1], err)
			continue
		}
		r.add(kindOK, "race build ok %s/%s (zig %s)", t[0], t[1], target)
	}
	r.add(kindInfo, "race: windows/arm64 has no race detector; linux/amd64 is the test task's -race")
}

// runBinaries runs the test binary of each package in bindir (named <base>.test<suffix>), one at
// a time, from the package's directory under root, and reports the results. windows runs them
// through cmd.exe with their Windows path, so that tests that start their own binary again find
// it (WSL interop would pass the bare file name, which Windows looks up on PATH only).
func runBinaries(ctx context.Context, r *platformReport, d platformDeps, label, bindir, root string, windows bool, env []string, dirs map[string]string, timeout time.Duration) string {
	pkgs := slices.Sorted(maps.Keys(dirs))
	var npkg, npass, nskip int
	var logged []string
	for _, p := range pkgs {
		suffix := ""
		if windows {
			suffix = ".exe"
		}
		bin := filepath.Join(bindir, path.Base(p)+".test"+suffix)
		if !d.exists(bin) {
			continue
		}
		npkg++
		name, args := bin, []string{"-test.v", "-test.count=1", "-test.timeout=" + timeout.String()}
		if windows {
			win, err := d.output(ctx, "wslpath", "-w", bin)
			if err != nil {
				r.add(kindFail, "%s %s: wslpath: %v", label, p, err)
				continue
			}
			name, args = "cmd.exe", append([]string{"/d", "/c", strings.TrimSpace(win)}, args...)
		}
		start := time.Now()
		out, err := d.runTest(ctx, filepath.Join(root, filepath.FromSlash(dirs[p])), env, name, args...)
		res := parseTestOutput(string(out))
		npass += len(res.passed)
		nskip += len(res.skipped)
		for _, l := range lines(out) {
			if loggedBound.MatchString(l) {
				logged = append(logged, strings.TrimSpace(l))
			}
		}
		detail := fmt.Sprintf("%s: %s, %d passed", p, time.Since(start).Round(time.Second), len(res.passed))
		if len(res.skipped) > 0 {
			detail += ", skipped: " + strings.Join(res.skipped, " ")
		}
		if err != nil {
			r.add(kindFail, "%s %s; %v, failed: %s", label, detail, err, strings.Join(res.failed, ", "))
			if r.w != nil {
				_, _ = fmt.Fprintln(r.w, lastLines(string(out), 30))
			}
		} else {
			r.add(kindOK, "%s ok %s", label, detail)
		}
	}
	r.add(kindInfo, "%s: %d packages, %d tests passed, %d skipped", label, npkg, npass, nskip)
	for _, l := range logged {
		r.add(kindInfo, "%s logged: %s", label, l)
	}
	return strings.Join(logged, "\n")
}

func lastLines(s string, n int) string {
	ls := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, "\n")
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

// windowsTemp returns the Windows user's temporary directory as a Linux path.
func windowsTemp(ctx context.Context, d platformDeps) (string, error) {
	out, err := d.output(ctx, "cmd.exe", "/d", "/c", "echo %TEMP%")
	if err != nil {
		return "", fmt.Errorf("cmd.exe: %w", err)
	}
	win := strings.TrimSpace(strings.ReplaceAll(out, "\r", ""))
	lin, err := d.output(ctx, "wslpath", "-u", win)
	if err != nil {
		return "", fmt.Errorf("wslpath %s: %w", win, err)
	}
	return strings.TrimSpace(lin), nil
}

// removeStale removes the task's temporary directories under base left by an interrupted run
// more than six hours ago.
func removeStale(base string) {
	old, _ := filepath.Glob(filepath.Join(base, "qrvt-platforms-*"))
	for _, p := range old {
		if fi, err := os.Stat(p); err == nil && time.Since(fi.ModTime()) > 6*time.Hour {
			_ = os.RemoveAll(p)
		}
	}
}

// platformsWindows runs the windows/amd64 test binaries, plain and (when the race step ran) race,
// through WSL interop, in a copy of the module's files under the Windows user's temporary
// directory, removed afterwards.
func platformsWindows(ctx context.Context, r *platformReport, d platformDeps, o platformOpts, race bool) {
	if why := wslUnavailable(d); why != "" {
		r.add(kindNotRun, "windows: %s", why)
		return
	}
	base, err := windowsTemp(ctx, d)
	if err != nil {
		r.add(kindFail, "windows: %v", err)
		return
	}
	removeStale(base)
	root, err := os.MkdirTemp(base, "qrvt-platforms-")
	if err != nil {
		r.add(kindFail, "windows: %v", err)
		return
	}
	defer func() { _ = os.RemoveAll(root) }()
	files, err := d.listFiles()
	if err == nil {
		err = copyTree(copyable(files), root)
	}
	if err != nil {
		r.add(kindFail, "windows: copying the module: %v", err)
		return
	}
	dirs, err := d.pkgDirs(d.tags)
	if err != nil {
		r.add(kindFail, "windows: %v", err)
		return
	}
	plain := filepath.Join(root, ".bin-plain")
	if err := buildTests(ctx, d, []string{"CGO_ENABLED=0", "GOOS=windows", "GOARCH=amd64"}, d.tags, "", plain, false); err != nil {
		r.add(kindFail, "windows/amd64 test build: %v", err)
		return
	}
	runBinaries(ctx, r, d, "windows/amd64", plain, root, true, nil, dirs, o.timeout)
	raceBin := filepath.Join(d.tempRoot, "race-windows-amd64")
	if !race || !d.exists(raceBin) {
		r.add(kindNotRun, "windows/amd64 race: no race binaries (the race step builds them, with zig)")
		return
	}
	// internal/video's race binary starts engine workers, each a race-enabled copy of itself that
	// reserves the engine's 512 MiB; under the zig-linked race runtime their ThreadSanitizer cannot
	// allocate its own memory then ("failed to allocate 0x40000000"), so the package's race tests
	// stay with CI's windows-2025 race step.
	raceDirs := maps.Clone(dirs)
	if _, ok := raceDirs[isolated]; ok {
		delete(raceDirs, isolated)
		r.add(kindCIOnly, "windows/amd64 race %s: its engine workers' ThreadSanitizer cannot allocate memory under the zig-linked race runtime; CI's windows-2025 race step runs it", isolated)
	}
	runBinaries(ctx, r, d, "windows/amd64 race", raceBin, root, true, nil, raceDirs, o.timeout)
}

// platformsArm64 runs the linux/arm64 test binaries from the packages' own directories: natively
// on an arm64 host, else under emulation with QRVT_EMULATED=1.
func platformsArm64(ctx context.Context, r *platformReport, d platformDeps, o platformOpts) {
	mode, why := arm64Mode(d)
	if mode == "" {
		r.add(kindNotRun, "arm64: %s", why)
		return
	}
	dirs, err := d.pkgDirs(d.tags)
	if err != nil {
		r.add(kindFail, "arm64: %v", err)
		return
	}
	bin := filepath.Join(d.tempRoot, "arm64-plain")
	if err := buildTests(ctx, d, []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=arm64"}, d.tags, "", bin, false); err != nil {
		r.add(kindFail, "linux/arm64 test build: %v", err)
		return
	}
	wd, err := os.Getwd()
	if err != nil {
		r.add(kindFail, "arm64: %v", err)
		return
	}
	if mode == "native" {
		runBinaries(ctx, r, d, "linux/arm64 (native)", bin, wd, false, nil, dirs, o.timeout)
		return
	}
	r.add(kindInfo, "arm64: %s", emulationNotice)
	runBinaries(ctx, r, d, "linux/arm64 (emulated)", bin, wd, false, []string{emulatedEnv}, dirs, o.emulatedTimeout)
}

// commandWithCancel returns a command bound to ctx. When ctx ends, a Windows test binary run
// through cmd.exe is killed with its process tree (taskkill), which killing the WSL side alone
// would leave running.
func commandWithCancel(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	if name == "cmd.exe" && len(args) >= 3 {
		image := path.Base(strings.ReplaceAll(args[2], `\`, "/"))
		cmd.Cancel = func() error {
			_ = exec.Command("taskkill.exe", "/F", "/T", "/IM", image).Run()
			return cmd.Process.Kill()
		}
	}
	return cmd
}

// realPlatformDeps returns the task's dependencies on this machine.
func realPlatformDeps(tags, tempRoot string) platformDeps {
	return platformDeps{
		hostOS: runtime.GOOS, hostArch: runtime.GOARCH,
		lookPath: exec.LookPath, readFile: os.ReadFile, getenv: os.Getenv,
		exists: func(p string) bool { _, err := os.Stat(p); return err == nil },
		run: func(ctx context.Context, env []string, name string, args ...string) error {
			fmt.Fprintf(os.Stderr, "+ %s %s %s\n", strings.Join(env, " "), name, strings.Join(args, " "))
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Stdout, cmd.Stderr, cmd.Env = os.Stdout, os.Stderr, append(os.Environ(), env...)
			return cmd.Run()
		},
		output: func(ctx context.Context, name string, args ...string) (string, error) {
			out, err := exec.CommandContext(ctx, name, args...).Output()
			return string(out), err
		},
		runTest: func(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
			cmd := commandWithCancel(ctx, name, args...)
			cmd.Dir, cmd.Env = dir, append(os.Environ(), env...)
			return cmd.CombinedOutput()
		},
		lintTool: func() (string, error) { return find(toolNamed("golangci-lint")) },
		pkgDirs:  pkgDirs,
		listFiles: func() ([]string, error) {
			out, err := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
			if err != nil {
				return nil, fmt.Errorf("git ls-files: %w", err)
			}
			return strings.FieldsFunc(string(out), func(r rune) bool { return r == 0 }), nil
		},
		report: os.Stdout, tags: tags, tempRoot: tempRoot,
	}
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

// platforms is the platforms task. An interrupt (SIGINT, SIGTERM) stops the running command and
// still removes the temporary directories.
func platforms(args []string) error {
	o, err := parsePlatformArgs(args)
	if err != nil {
		return err
	}
	tags, err := releaseTags()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	removeStale(os.TempDir())
	tmp, err := os.MkdirTemp("", "qrvt-platforms-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	start := time.Now()
	r, err := runPlatforms(ctx, o, realPlatformDeps(tags, tmp))
	fmt.Println()
	fmt.Println("platforms summary:")
	for _, l := range r.summary(o.strict) {
		fmt.Println("  " + l)
	}
	fmt.Printf("  total: %s\n", time.Since(start).Round(time.Second))
	return err
}
