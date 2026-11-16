package main

import (
	"errors"
	"slices"
	"strings"
	"testing"
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

func TestArm64Runner(t *testing.T) {
	none := func(string) (string, error) { return "", errors.New("not found") }
	if got, ok := arm64Runner(none, func(string) bool { return true }); !ok || got != "" {
		t.Errorf("with binfmt registered: %q, %v; want the binary run directly", got, ok)
	}
	look := func(name string) (string, error) {
		if name == "qemu-aarch64-static" {
			return "/usr/bin/qemu-aarch64-static", nil
		}
		return "", errors.New("not found")
	}
	if got, ok := arm64Runner(look, func(string) bool { return false }); !ok || got != "/usr/bin/qemu-aarch64-static" {
		t.Errorf("with qemu on PATH: %q, %v", got, ok)
	}
	if _, ok := arm64Runner(none, func(string) bool { return false }); ok {
		t.Error("with neither, arm64Runner reported a runner")
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
