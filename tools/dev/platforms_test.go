package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCompileJobs(t *testing.T) {
	jobs := compileJobs("noasm,nounsafe")
	if len(jobs) != 12 {
		t.Fatalf("got %d compile jobs, want 6 targets x 2 tag sets", len(jobs))
	}
	seen := map[string]bool{}
	for _, j := range jobs {
		if !slices.Contains(j.env, "CGO_ENABLED=0") {
			t.Errorf("%s: env %v lacks CGO_ENABLED=0", j.name, j.env)
		}
		seen[j.name] = true
	}
	for _, want := range []string{"linux/amd64", "windows/arm64 noasm,nounsafe", "darwin/arm64", "darwin/amd64 noasm,nounsafe"} {
		if !seen[want] {
			t.Errorf("no compile job %q (have %v)", want, seen)
		}
	}
	if j := compileJobs("")[0]; len(compileJobs("")) != 6 || !slices.Contains(j.env, "GOOS=linux") {
		t.Errorf("without release tags: %d jobs, first %+v", len(compileJobs("")), j)
	}
}

func TestZigTarget(t *testing.T) {
	cases := []struct {
		goos, goarch, want string
		ok                 bool
	}{
		{"linux", "amd64", "x86_64-linux-gnu", true},
		{"linux", "arm64", "aarch64-linux-gnu", true},
		{"windows", "amd64", "x86_64-windows-gnu", true},
		{"darwin", "amd64", "x86_64-macos", true},
		{"darwin", "arm64", "aarch64-macos", true},
		{"windows", "arm64", "", false}, // the race detector does not support windows/arm64
	}
	for _, c := range cases {
		got, ok := zigTarget(c.goos, c.goarch)
		if got != c.want || ok != c.ok {
			t.Errorf("zigTarget(%s, %s) = %q, %v; want %q, %v", c.goos, c.goarch, got, ok, c.want, c.ok)
		}
	}
}

func TestRaceEnv(t *testing.T) {
	env, ldflags := raceEnv("/opt/zig", "windows", "amd64", "x86_64-windows-gnu")
	for _, want := range []string{"CGO_ENABLED=1", "GOOS=windows", "GOARCH=amd64", "CC=/opt/zig cc -target x86_64-windows-gnu"} {
		if !slices.Contains(env, want) {
			t.Errorf("windows race env %v lacks %q", env, want)
		}
	}
	// Windows needs the WaitOnAddress library and a low image base for the race runtime's shadow.
	if !strings.Contains(ldflags, "-lapi-ms-win-core-synch-l1-2-0") || !strings.Contains(ldflags, "--image-base=0x400000") {
		t.Errorf("windows race ldflags = %q", ldflags)
	}
	if _, ldflags := raceEnv("zig", "linux", "arm64", "aarch64-linux-gnu"); ldflags != "" {
		t.Errorf("linux race ldflags = %q, want none", ldflags)
	}
}

func TestFindZig(t *testing.T) {
	look := func(name string) (string, error) {
		if name == "zig" {
			return "/path/zig", nil
		}
		return "", errors.New("not found")
	}
	none := func(string) (string, error) { return "", errors.New("not found") }
	if got := findZig(func(string) string { return "/env/zig" }, look); got != "/env/zig" {
		t.Errorf("findZig with ZIG set = %q", got)
	}
	if got := findZig(func(string) string { return "" }, look); got != "/path/zig" {
		t.Errorf("findZig from PATH = %q", got)
	}
	if got := findZig(func(string) string { return "" }, none); got != "" {
		t.Errorf("findZig with no zig = %q", got)
	}
}

func TestParseTestOutput(t *testing.T) {
	out := "=== RUN   TestA\n--- PASS: TestA (0.00s)\n=== RUN   TestB\n    b_test.go:9: windows only\n--- SKIP: TestB (0.00s)\n" +
		"=== RUN   TestC\n    --- FAIL: TestC/sub (0.01s)\n--- FAIL: TestC (0.01s)\n\r\nFAIL\n"
	r := parseTestOutput(out)
	if !slices.Equal(r.passed, []string{"TestA"}) || !slices.Equal(r.skipped, []string{"TestB"}) || !slices.Equal(r.failed, []string{"TestC"}) {
		t.Errorf("parseTestOutput = %+v", r)
	}
}

func TestParsePlatformArgs(t *testing.T) {
	o, err := parsePlatformArgs(nil)
	if err != nil || o.skip["arm64"] == "" || o.skip["windows"] != "" || o.strict {
		t.Errorf("default = %+v, %v; want arm64 skipped (routine run), not strict", o, err)
	}
	o, err = parsePlatformArgs([]string{"-full"})
	if err != nil || len(o.skip) != 0 || !o.strict {
		t.Errorf("-full = %+v, %v; want every step and strict", o, err)
	}
	o, err = parsePlatformArgs([]string{"-skip", "race, lint", "-strict"})
	if err != nil || o.skip["race"] == "" || o.skip["lint"] == "" || o.skip["arm64"] == "" || !o.strict {
		t.Errorf("-skip race,lint -strict = %+v, %v", o, err)
	}
	if _, err := parsePlatformArgs([]string{"-skip", "compile,bogus"}); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("an unknown -skip name gave %v", err)
	}
	if o.emulatedTimeout < 60*time.Minute {
		t.Errorf("emulated per-package timeout %v, want at least an hour (vision took 20-24 minutes)", o.emulatedTimeout)
	}
}

func TestArm64Mode(t *testing.T) {
	file := func(content string) func(string) ([]byte, error) {
		return func(p string) ([]byte, error) {
			if content == "" || p != "/proc/sys/fs/binfmt_misc/qemu-aarch64" {
				return nil, os.ErrNotExist
			}
			return []byte(content), nil
		}
	}
	none := func(string) (string, error) { return "", errors.New("not found") }
	qemu := func(name string) (string, error) {
		if name == "qemu-aarch64-static" {
			return "/usr/bin/qemu-aarch64-static", nil
		}
		return "", errors.New("not found")
	}
	cases := []struct {
		arch, binfmt string
		look         func(string) (string, error)
		mode, reason string
	}{
		{"arm64", "", none, "native", ""},
		{"amd64", "enabled\ninterpreter /usr/libexec/qemu-binfmt/aarch64-binfmt-P\n", none, "binfmt", ""},
		{"amd64", "disabled\ninterpreter x\n", qemu, "", "disabled"},
		{"amd64", "", qemu, "", "binfmt"},
		{"amd64", "", none, "", "qemu-aarch64 not found"},
	}
	for _, c := range cases {
		d := platformDeps{hostOS: "linux", hostArch: c.arch, lookPath: c.look, readFile: file(c.binfmt)}
		mode, reason := arm64Mode(d)
		if mode != c.mode || !strings.Contains(reason, c.reason) {
			t.Errorf("arm64Mode(%s, binfmt %q) = %q, %q; want %q and a reason containing %q", c.arch, c.binfmt, mode, reason, c.mode, c.reason)
		}
	}
}

func TestWSLAvailable(t *testing.T) {
	cmd := func(name string) (string, error) {
		if name == "cmd.exe" {
			return "/mnt/c/Windows/system32/cmd.exe", nil
		}
		return "", errors.New("not found")
	}
	none := func(string) (string, error) { return "", errors.New("not found") }
	interop := func(p string) ([]byte, error) {
		if p == "/proc/sys/fs/binfmt_misc/WSLInterop" {
			return []byte("enabled\n"), nil
		}
		return nil, os.ErrNotExist
	}
	missing := func(string) ([]byte, error) { return nil, os.ErrNotExist }
	if reason := wslUnavailable(platformDeps{lookPath: cmd, readFile: interop}); reason != "" {
		t.Errorf("with WSL interop: %q", reason)
	}
	if reason := wslUnavailable(platformDeps{lookPath: none, readFile: interop}); !strings.Contains(reason, "cmd.exe") {
		t.Errorf("without cmd.exe: %q", reason)
	}
	if reason := wslUnavailable(platformDeps{lookPath: cmd, readFile: missing}); !strings.Contains(reason, "WSLInterop") {
		t.Errorf("without WSLInterop: %q", reason)
	}
}

// fakeDeps returns dependencies for runPlatforms in which every command succeeds, the module has
// the given packages, and every package has a test binary.
func fakeDeps(t *testing.T, pkgs ...string) (platformDeps, *[]string) {
	t.Helper()
	var calls []string
	win := t.TempDir()
	dirs := map[string]string{}
	for _, p := range pkgs {
		dirs[p] = strings.TrimPrefix(strings.TrimPrefix(p, module), "/")
	}
	return platformDeps{
		hostOS: "linux", hostArch: "amd64",
		lookPath: func(name string) (string, error) {
			if name == "cmd.exe" || name == "zig" {
				return "/x/" + name, nil
			}
			return "", errors.New("not found")
		},
		readFile: func(p string) ([]byte, error) {
			if strings.HasSuffix(p, "WSLInterop") || strings.HasSuffix(p, "qemu-aarch64") {
				return []byte("enabled\n"), nil
			}
			return nil, os.ErrNotExist
		},
		getenv:   func(string) string { return "" },
		exists:   func(string) bool { return true },
		run:      func(context.Context, []string, string, ...string) error { return nil },
		lintTool: func() (string, error) { return "golangci-lint", nil },
		output: func(_ context.Context, name string, args ...string) (string, error) {
			switch {
			case strings.HasSuffix(name, "zig"):
				return zigVersion + "\n", nil
			case name == "cmd.exe":
				return `C:\Temp` + "\r\n", nil
			case name == "wslpath" && args[0] == "-w":
				return args[1] + "\n", nil // the Linux path stands in for the Windows one
			case name == "wslpath":
				return win + "\n", nil
			}
			return "", nil
		},
		runTest: func(_ context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
			calls = append(calls, strings.Join(append([]string{name}, args...), " ")+" @"+filepath.Base(dir)+" "+strings.Join(env, " "))
			return []byte("=== RUN   TestX\n--- PASS: TestX (0.00s)\nPASS\n"), nil
		},
		pkgDirs:   func(string) (map[string]string, error) { return dirs, nil },
		listFiles: func() ([]string, error) { return nil, nil },
		tags:      "noasm,nounsafe",
		tempRoot:  t.TempDir(),
	}, &calls
}

func TestRunPlatformsAllPass(t *testing.T) {
	d, _ := fakeDeps(t, module+"/internal/frame")
	o, _ := parsePlatformArgs([]string{"-full"})
	r, err := runPlatforms(t.Context(), o, d)
	if err != nil {
		t.Fatalf("runPlatforms = %v\n%s", err, strings.Join(r.summary(o.strict), "\n"))
	}
	s := strings.Join(r.summary(o.strict), "\n")
	for _, want := range []string{"compile ok linux/amd64", "lint ok windows", "race build ok windows/amd64", "windows/amd64 ok", "linux/arm64 (emulated) ok", "not covered locally"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary lacks %q:\n%s", want, s)
		}
	}
	for _, item := range notCoveredLocally {
		if !strings.Contains(s, item) {
			t.Errorf("summary lacks the not-covered item %q", item)
		}
	}
}

func TestRunPlatformsExitWithoutFailLines(t *testing.T) {
	d, _ := fakeDeps(t, module+"/internal/frame")
	d.runTest = func(context.Context, string, []string, string, ...string) ([]byte, error) {
		return []byte("=== RUN   TestX\n--- PASS: TestX (0.00s)\n"), errors.New("exit status 2")
	}
	o, _ := parsePlatformArgs([]string{"-skip", "compile,lint,race"})
	if _, err := runPlatforms(t.Context(), o, d); err == nil {
		t.Fatal("a test binary that exited non-zero without FAIL lines did not fail the task")
	}
}

func TestRunPlatformsNotRun(t *testing.T) {
	d, _ := fakeDeps(t, module+"/internal/frame")
	d.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	d.readFile = func(string) ([]byte, error) { return nil, os.ErrNotExist }
	o, _ := parsePlatformArgs([]string{"-skip", "compile,lint"})
	r, err := runPlatforms(t.Context(), o, d)
	if err != nil {
		t.Fatalf("without -strict, not run is not a failure: %v", err)
	}
	s := strings.Join(r.summary(false), "\n")
	for _, want := range []string{"NOT RUN race", "NOT RUN windows", "zig not found", "cmd.exe", "SKIPPED arm64"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary lacks %q:\n%s", want, s)
		}
	}
	o.strict = true
	if _, err := runPlatforms(t.Context(), o, d); err == nil || !strings.Contains(err.Error(), "not run") {
		t.Errorf("with -strict, not run gave %v", err)
	}
	o, _ = parsePlatformArgs([]string{"-full", "-skip", "compile,lint,race,windows"})
	r, err = runPlatforms(t.Context(), o, d)
	if err == nil || !strings.Contains(strings.Join(r.summary(true), "\n"), "qemu-aarch64 not found") {
		t.Errorf("-full without qemu: %v", err)
	}
}

func TestRunPlatformsSkipSaysSkipped(t *testing.T) {
	d, _ := fakeDeps(t, module+"/internal/frame")
	o, _ := parsePlatformArgs([]string{"-skip", "compile,lint,race,windows"})
	r, err := runPlatforms(t.Context(), o, d)
	s := strings.Join(r.summary(false), "\n")
	if err != nil || !strings.Contains(s, "SKIPPED race: skipped by -skip") || !strings.Contains(s, "SKIPPED arm64: routine run") {
		t.Errorf("summary:\n%s\nerr %v", s, err)
	}
}

func TestWindowsRaceExcludesVideo(t *testing.T) {
	d, calls := fakeDeps(t, module+"/internal/frame", module+"/internal/video")
	o, _ := parsePlatformArgs([]string{"-skip", "compile,lint"})
	r, err := runPlatforms(t.Context(), o, d)
	if err != nil {
		t.Fatal(err)
	}
	var plainVideo, raceVideo, raceFrame int
	for _, c := range *calls {
		race := strings.Contains(c, "race-windows-amd64")
		switch {
		case strings.Contains(c, "video.test.exe") && race:
			raceVideo++
		case strings.Contains(c, "video.test.exe"):
			plainVideo++
		case strings.Contains(c, "frame.test.exe") && race:
			raceFrame++
		}
	}
	if plainVideo != 1 || raceVideo != 0 || raceFrame != 1 {
		t.Errorf("windows runs: plain video %d, race video %d, race frame %d; want 1, 0, 1\n%s", plainVideo, raceVideo, raceFrame, strings.Join(*calls, "\n"))
	}
	if s := strings.Join(r.summary(false), "\n"); !strings.Contains(s, "NOT RUN windows/amd64 race "+isolated) {
		t.Errorf("summary does not report video's race tests as not run:\n%s", s)
	}
}

func TestArm64EmulationNotice(t *testing.T) {
	d, calls := fakeDeps(t, module+"/internal/video")
	d.runTest = func(_ context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, strings.Join(env, " "))
		return []byte("=== RUN   TestHostileFiles\n    sandbox_test.go:579: nostsd.mp4: 1m5s, over the 60 s bound, logged rather than asserted under emulation\n--- PASS: TestHostileFiles (82.00s)\nPASS\n"), nil
	}
	o, _ := parsePlatformArgs([]string{"-full", "-skip", "compile,lint,race,windows"})
	r, err := runPlatforms(t.Context(), o, d)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Join(r.summary(true), "\n")
	if !strings.Contains(s, emulationNotice) || !strings.Contains(s, "nostsd.mp4: 1m5s, over the 60 s bound") {
		t.Errorf("summary lacks the emulation notice or the logged line:\n%s", s)
	}
	if len(*calls) != 1 || !strings.Contains((*calls)[0], emulatedEnv) {
		t.Errorf("the emulated run's environment: %q", *calls)
	}
}

func TestCopyFiles(t *testing.T) {
	got := copyable([]string{"go.mod", "recordings/a.mp4", "internal/x.go", "recordings"})
	if !slices.Equal(got, []string{"go.mod", "internal/x.go"}) {
		t.Errorf("copyable = %q", got)
	}
}
