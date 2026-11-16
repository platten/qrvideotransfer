//go:build !windows && !linux

package video

import "os/exec"

// startProc starts the worker. Outside Windows and Linux there is neither a Job Object nor a
// parent-death signal: the worker exits by itself when its parent's pipe closes, which a spinning
// guest can prevent (docs/spikes/S1.md section 14.4), and the parent kills it to stop it.
func startProc(cmd *exec.Cmd, _ bool) (func(), error) { return func() {}, cmd.Start() }
