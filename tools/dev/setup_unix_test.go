//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestSetupShBoundedEarlyInterrupt: a Ctrl-C at any moment of bounded's start and end, including
// the moments between making the marker folder, starting the command and the watchdog, and setting
// the trap (review of PR #55), and those after the trap is cleared, leaves neither the folder, nor
// the command, nor the watchdog's sleep. The shell runs in a process group of its own, which gets
// SIGINT as a terminal's Ctrl-C would give it, 600 times: half of them over a command that runs on
// and half over one that ends at once, after a delay swept from 0 to 7.9 ms in steps of 0.1 ms (the
// scheduler adds its own jitter). The interrupt then ends the shell by its default action where
// bounded has no trap set and nothing of its own running, or bounded ends it with status 130, or
// the command was done before it came.
//
// Or the interrupt is ignored: bounded forks the watchdog with SIGINT ignored, so that the
// watchdog cannot catch one before it resets its signals (DESIGN.md section 19), and under bash an
// interrupt that a command run in the foreground survives is ignored (bash's wait-and-cooperative-
// exit rule). The test then gives a second one, as a user would, and asserts that it ends the shell
// and leaves nothing. At most 3% of the first interrupts may be ignored (measured: up to 4 of 600).
// The group matters: under bash, an interrupt sent to the shell alone while mktemp runs is ignored,
// as everywhere else in the script, since mktemp does not get it.
//
// Long only (about 5 to 17 s per shell, the shells in parallel): it runs with the long tests of
// any pull request that changes setup.sh (DESIGN.md section 17).
func TestSetupShBoundedEarlyInterrupt(t *testing.T) {
	if os.Getenv(longTestsVar) != "1" {
		t.Skip("long only: set " + longTestsVar + "=1 (go run ./tools/dev test -long -pkg ./tools/dev)")
	}
	fn := scriptFunc(t, readScript(t, "setup.sh"), "bounded() {")
	for _, shell := range boundedShells(t) {
		t.Run(shell, func(t *testing.T) {
			t.Parallel()
			// Arguments no other process uses: the watchdog's limit and the command's sleep.
			n := 7000 + os.Getpid()%1000
			limit, long := fmt.Sprintf("%d%d1", n, shell[0]), fmt.Sprintf("%d%d2", n, shell[0])
			marks := t.TempDir()
			var signalled, trapped, done, ignored int
			const runs = 600
			for i := range runs {
				command, delay := "sleep "+long, time.Duration(i/2%80)*100*time.Microsecond
				if i%2 == 1 {
					command = "true"
				}
				cmd := exec.Command(shell, "-c", "set -eu\n"+fn+"bounded "+limit+" "+command)
				cmd.Env = append(os.Environ(), "TMPDIR="+marks)
				cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				time.Sleep(delay)
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
				ended := make(chan error, 1)
				go func() { ended <- cmd.Wait() }()
				var err error
				select {
				case err = <-ended:
				case <-time.After(3 * time.Second):
					// Ignored (above): a second Ctrl-C, as a user would give.
					ignored++
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
					select {
					case err = <-ended:
					case <-time.After(10 * time.Second):
						_ = cmd.Process.Kill()
						err = <-ended
						t.Errorf("%s: run %d (%s, %v): two interrupts did not end the shell", shell, i, command, delay)
					}
				}
				var exit *exec.ExitError
				switch {
				case errors.As(err, &exit) && exit.ExitCode() == 130:
					trapped++
				case errors.As(err, &exit) && exit.ExitCode() == -1 && exit.Sys().(syscall.WaitStatus).Signal() == syscall.SIGINT:
					signalled++ // the signal's default action, with no trap of bounded's set
				case errors.As(err, &exit) && exit.ExitCode() == -1:
					// killed above, already reported
				case err == nil && command == "true":
					done++
				default:
					t.Errorf("%s: run %d (%s, %v): %v; want status 130 or the interrupt", shell, i, command, delay, err)
				}
				if left, _ := os.ReadDir(marks); len(left) > 0 {
					t.Errorf("%s: run %d (%s, %v): %d entries left in TMPDIR", shell, i, command, delay, len(left))
					for _, e := range left {
						_ = os.RemoveAll(filepath.Join(marks, e.Name()))
					}
				}
			}
			time.Sleep(300 * time.Millisecond)
			ps, err := exec.Command("ps", "-eo", "args").Output()
			if err != nil {
				t.Skipf("no ps: %v", err)
			}
			for _, a := range []string{"sleep " + limit, "sleep " + long} {
				if c := strings.Count(string(ps), a+"\n"); c > 0 {
					t.Errorf("%s: %d processes %q outlived an interrupt", shell, c, a)
					_ = exec.Command("pkill", "-f", "^"+a+"$").Run()
				}
			}
			t.Logf("%s, %d runs: %d ended by the interrupt's default action, %d by bounded with status 130, %d done first; "+
				"%d first interrupts ignored", shell, runs, signalled, trapped, done, ignored)
			if ignored > runs*3/100 {
				t.Errorf("%s ignored %d first interrupts of %d, more than 3%%", shell, ignored, runs)
			}
		})
	}
}
