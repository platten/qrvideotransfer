//go:build !windows

package video

import "os/exec"

// startWorker starts the worker. Outside Windows there is no Job Object: the worker exits by itself
// when its parent's pipe closes, and the parent kills it to stop it.
func startWorker(cmd *exec.Cmd, _ bool) (func(), error) { return func() {}, cmd.Start() }
