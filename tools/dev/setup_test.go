package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
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

// boundedShells returns the shells to run setup.sh's bounded function under: sh, and dash and bash
// where they are installed (dash ends a subshell at any failing command under set -e).
func boundedShells(t *testing.T) []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("setup.sh is for Linux and macOS")
	}
	var shells []string
	for _, s := range []string{"sh", "dash", "bash"} {
		if _, err := exec.LookPath(s); err == nil {
			shells = append(shells, s)
		}
	}
	if len(shells) == 0 {
		t.Skip("no sh")
	}
	return shells
}

// alive reports whether a process runs, waiting up to wait for it to end.
func alive(pid int, wait time.Duration) bool {
	for deadline := time.Now().Add(wait); ; time.Sleep(50 * time.Millisecond) {
		p, err := os.FindProcess(pid)
		if err != nil || p.Signal(syscall.Signal(0)) != nil {
			return false
		}
		if time.Now().After(deadline) {
			return true
		}
	}
}

// startBounded starts a shell running bounded over a command that writes its process ID to a file,
// and returns the running shell, the command's process ID and the shell's TMPDIR.
func startBounded(t *testing.T, shell, fn, call string) (*exec.Cmd, *bytes.Buffer, int, string) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "pid")
	marks := t.TempDir()
	cmd := exec.Command(shell, "-c", "set -eu\n"+fn+call)
	cmd.Env = append(os.Environ(), "PIDFILE="+pidFile, "TMPDIR="+marks)
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if b, err := os.ReadFile(pidFile); err == nil && strings.HasSuffix(string(b), "\n") {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			return cmd, &errOut, pid, marks
		}
	}
	_ = cmd.Process.Kill()
	t.Fatalf("%s: the command did not start", shell)
	return nil, nil, 0, ""
}

// TestSetupShBoundedRuns runs setup.sh's bounded function: a command past its limit is stopped
// and reported with status 124, in seconds; any other command keeps its status and is not held.
func TestSetupShBoundedRuns(t *testing.T) {
	fn := scriptFunc(t, readScript(t, "setup.sh"), "bounded() {")
	for _, shell := range boundedShells(t) {
		t.Run(shell, func(t *testing.T) {
			t.Parallel()
			for _, c := range []struct {
				call, wantOut, wantErr string
				max                    time.Duration
			}{
				{`bounded 1 sleep 60 || echo "status $?"`, "status 124\n", "sleep 60 did not finish within 1 s", 15 * time.Second},
				// SIGTERM ignored: SIGKILL follows, to the command and its child.
				{`bounded 1 sh -c 'trap "" TERM; sleep 60' || echo "status $?"`, "status 124\n", "did not finish within 1 s", 18 * time.Second},
				{`bounded 30 sh -c 'exit 3' || echo "status $?"`, "status 3\n", "", 5 * time.Second},
				{`bounded 30 true && echo done`, "done\n", "", 5 * time.Second},
				{`bounded 0 sh -c 'exit 4' || echo "status $?"`, "status 4\n", "", 5 * time.Second},
			} {
				var out, errOut bytes.Buffer
				cmd := exec.Command(shell, "-c", "set -eu\n"+fn+c.call)
				cmd.Stdout, cmd.Stderr = &out, &errOut
				start := time.Now()
				err := cmd.Run()
				took := time.Since(start)
				if err != nil || out.String() != c.wantOut || !strings.Contains(errOut.String(), c.wantErr) || (c.wantErr == "" && errOut.Len() > 0) {
					t.Errorf("%s: %s: %v, stdout %q, stderr %q; want %q, %q", shell, c.call, err, out.String(), errOut.String(), c.wantOut, c.wantErr)
				}
				if took > c.max {
					t.Errorf("%s: %s took %v, want at most %v", shell, c.call, took, c.max)
				}
			}
		})
	}
}

// TestSetupShBoundedKillsChildless: a command that ignores SIGTERM and has no child, so that pkill
// finds nothing to kill, is still killed within the limit and the 10 s grace (review of 3ba96bf:
// under dash, pkill's failure ended the watchdog before its kill).
func TestSetupShBoundedKillsChildless(t *testing.T) {
	fn := scriptFunc(t, readScript(t, "setup.sh"), "bounded() {")
	for _, shell := range boundedShells(t) {
		t.Run(shell, func(t *testing.T) {
			t.Parallel()
			start := time.Now()
			cmd, errOut, pid, _ := startBounded(t, shell, fn, `bounded 1 sh -c 'trap "" TERM; echo $$ > "$PIDFILE"; exec sleep 60'`)
			err := cmd.Wait()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 124 || !strings.Contains(errOut.String(), "did not finish within 1 s") {
				t.Errorf("%s: %v, stderr %q; want status 124 and the message", shell, err, errOut.String())
			}
			if took := time.Since(start); took > 15*time.Second {
				t.Errorf("%s: took %v, want the limit and the grace, 11 s", shell, took)
			}
			if alive(pid, time.Second) {
				t.Errorf("%s: the command (pid %d) outlived bounded", shell, pid)
				p, _ := os.FindProcess(pid)
				_ = p.Kill()
			}
		})
	}
}

// TestSetupShBoundedCleansUp: bounded leaves nothing behind, neither the watchdog's sleep when the
// command ends at once, nor, after an interrupt (Ctrl-C), the command or its marker folder; the
// interrupt ends the script with status 130.
func TestSetupShBoundedCleansUp(t *testing.T) {
	fn := scriptFunc(t, readScript(t, "setup.sh"), "bounded() {")
	for _, shell := range boundedShells(t) {
		t.Run(shell, func(t *testing.T) {
			t.Parallel()
			// A limit no other process uses, so that a stray watchdog sleep can be found by its arguments.
			limit := fmt.Sprintf("%d%d", 7000+os.Getpid()%1000, shell[0]) // one per shell (sh, dash, bash), which run in parallel
			cmd := exec.Command(shell, "-c", "set -eu\n"+fn+"i=0; while [ $i -lt 20 ]; do bounded "+limit+" true; i=$((i+1)); done")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s: %v: %s", shell, err, out)
			}
			time.Sleep(300 * time.Millisecond)
			if ps, err := exec.Command("ps", "-eo", "args").Output(); err == nil && strings.Contains(string(ps), "sleep "+limit+"\n") {
				t.Errorf("%s: a watchdog's sleep %s outlived bounded", shell, limit)
				_ = exec.Command("pkill", "-f", "^sleep "+limit+"$").Run()
			}

			cmd, _, pid, marks := startBounded(t, shell, fn, `bounded 300 sh -c 'echo $$ > "$PIDFILE"; exec sleep 60'`)
			_ = cmd.Process.Signal(os.Interrupt)
			err := cmd.Wait()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 130 {
				t.Errorf("%s: after an interrupt: %v; want exit status 130", shell, err)
			}
			if alive(pid, 5*time.Second) {
				t.Errorf("%s: the command (pid %d) outlived the interrupt", shell, pid)
				p, _ := os.FindProcess(pid)
				_ = p.Kill()
			}
			if left, _ := os.ReadDir(marks); len(left) > 0 {
				t.Errorf("%s: %d entries left in TMPDIR after the interrupt", shell, len(left))
			}
		})
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
