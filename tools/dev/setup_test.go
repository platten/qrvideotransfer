package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The setup scripts bound every package-manager command, so that a stalled mirror fails within
// minutes with a message instead of hanging until the CI step's limit (PR #52's run 44474815418
// waited 35 minutes in apt-get update; DESIGN.md section 19).

func readScript(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// scriptFunc returns the definition of a function in a script: from the line that starts with
// start to the first line after it that is "}".
func scriptFunc(t *testing.T, script, start string) string {
	t.Helper()
	i := strings.Index(script, "\n"+start)
	if i < 0 {
		t.Fatalf("no %q in the script", start)
	}
	j := strings.Index(script[i:], "\n}\n")
	if j < 0 {
		t.Fatalf("%q does not end", start)
	}
	return script[i+1 : i+j+3]
}

var (
	// A package-manager command, in setup.sh's code with its quoted strings removed.
	pmCommand = regexp.MustCompile(`\b(apt-get|dnf|pacman|brew)\s+(\$apt_opts\s+)?(update|install|-Syu)\b`)
	// The same command run through bounded; apt-get with the Acquire timeouts and retries.
	pmBounded  = regexp.MustCompile(`\bbounded\s+""\s+(\$sudo\s+)?(apt-get\s+\$apt_opts|dnf|pacman|brew)\s+(update|install|-Syu)\b`)
	shQuoted   = regexp.MustCompile(`"(\\.|[^"\\])*"|'[^']*'`)
	aptOptions = `apt_opts="-o Acquire::http::Timeout=30 -o Acquire::https::Timeout=30 -o Acquire::Retries=3"`
)

// TestSetupShBounded: every apt-get, dnf, pacman and brew command of setup.sh runs through
// bounded, apt-get with Acquire timeouts and retries; the downloads have curl's own.
func TestSetupShBounded(t *testing.T) {
	s := readScript(t, "setup.sh")
	var code strings.Builder
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		code.WriteString(shQuoted.ReplaceAllString(l, `""`) + "\n")
	}
	all, bounded := pmCommand.FindAllString(code.String(), -1), pmBounded.FindAllString(code.String(), -1)
	// apt update and install, dnf, pacman and brew for the packages; apt update and install, dnf
	// and pacman for qemu-user-static.
	if len(all) != 9 || len(bounded) != len(all) {
		t.Errorf("%d package-manager commands, %d of them bounded, want 9 and 9: %q", len(all), len(bounded), all)
	}
	for _, want := range []string{aptOptions, "--connect-timeout 30", "--speed-limit 1024 --speed-time 60", "--retry 3"} {
		if !strings.Contains(s, want) {
			t.Errorf("setup.sh lacks %q", want)
		}
	}
	if out, err := exec.Command("sh", "-n", filepath.Join("..", "..", "scripts", "setup.sh")).CombinedOutput(); err != nil && !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("sh -n setup.sh: %v: %s", err, out)
	}
}

// TestSetupShBoundedRuns runs setup.sh's bounded function: a command past its limit is stopped
// and reported with status 124, in seconds; any other command keeps its status and is not held.
func TestSetupShBoundedRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("setup.sh is for Linux and macOS")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	fn := scriptFunc(t, readScript(t, "setup.sh"), "bounded() {")
	for _, c := range []struct {
		call, wantOut, wantErr string
		max                    time.Duration
	}{
		{`bounded 1 sleep 60 || echo "status $?"`, "status 124\n", "sleep 60 did not finish within 1 s", 15 * time.Second},
		// SIGTERM ignored: SIGKILL follows.
		{`bounded 1 sh -c 'trap "" TERM; sleep 60' || echo "status $?"`, "status 124\n", "did not finish within 1 s", 25 * time.Second},
		{`bounded 30 sh -c 'exit 3' || echo "status $?"`, "status 3\n", "", 5 * time.Second},
		{`bounded 30 true && echo done`, "done\n", "", 5 * time.Second},
		{`bounded 0 sh -c 'exit 4' || echo "status $?"`, "status 4\n", "", 5 * time.Second},
	} {
		var out, errOut bytes.Buffer
		cmd := exec.Command("sh", "-c", "set -eu\n"+fn+c.call)
		cmd.Stdout, cmd.Stderr = &out, &errOut
		start := time.Now()
		err := cmd.Run()
		took := time.Since(start)
		if err != nil || out.String() != c.wantOut || !strings.Contains(errOut.String(), c.wantErr) || (c.wantErr == "" && errOut.Len() > 0) {
			t.Errorf("%s: %v, stdout %q, stderr %q; want %q, %q", c.call, err, out.String(), errOut.String(), c.wantOut, c.wantErr)
		}
		if took > c.max {
			t.Errorf("%s took %v, want at most %v", c.call, took, c.max)
		}
	}
}

// TestSetupPs1Bounded: setup.ps1 installs through winget only by way of Invoke-Bounded, and gives
// its downloads a timeout.
func TestSetupPs1Bounded(t *testing.T) {
	s := readScript(t, "setup.ps1")
	if strings.Contains(s, "& winget") || !strings.Contains(s, "Invoke-Bounded $PkgTimeout \"winget install $id\" (Get-Command winget).Source @('install'") {
		t.Error("setup.ps1 runs winget other than through Invoke-Bounded")
	}
	if !strings.Contains(s, "Invoke-WebRequest -Uri $url -OutFile $file -UseBasicParsing -TimeoutSec 300") {
		t.Error("setup.ps1 downloads without a timeout")
	}
}

// TestSetupPs1BoundedRuns runs setup.ps1's Invoke-Bounded with PowerShell, where there is one.
func TestSetupPs1BoundedRuns(t *testing.T) {
	ps := ""
	for _, name := range []string{"pwsh", "powershell.exe"} {
		if p, err := exec.LookPath(name); err == nil {
			ps = p
			break
		}
	}
	if ps == "" {
		t.Skip("no PowerShell")
	}
	fn := scriptFunc(t, readScript(t, "setup.ps1"), "function Invoke-Bounded")
	self := "(Get-Process -Id $PID).Path"
	for _, c := range []struct {
		call, want string
		max        time.Duration
	}{
		{"try { Invoke-Bounded 2 'a sleep' " + self + " @('-NoProfile', '-Command', 'Start-Sleep 60'); 'no error' } catch { $_.Exception.Message }",
			"setup: a sleep did not finish within 2 s", 30 * time.Second},
		{"'status ' + (Invoke-Bounded 60 'an exit' " + self + " @('-NoProfile', '-Command', 'exit 3'))", "status 3", 30 * time.Second},
		{"'status ' + (Invoke-Bounded 0 'an exit' " + self + " @('-NoProfile', '-Command', 'exit 4'))", "status 4", 30 * time.Second},
	} {
		start := time.Now()
		out, err := exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", "$ErrorActionPreference = 'Stop'\n"+fn+c.call).CombinedOutput()
		took := time.Since(start)
		if err != nil || !strings.Contains(string(out), c.want) {
			t.Errorf("%s: %v, %q; want %q", c.call, err, out, c.want)
		}
		if took > c.max {
			t.Errorf("%s took %v, want at most %v", c.call, took, c.max)
		}
	}
}
