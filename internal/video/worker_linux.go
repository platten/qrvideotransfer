package video

import (
	"os/exec"
	"runtime"
	"syscall"
)

// startProc starts the worker with SIGKILL as its parent-death signal, so the kernel kills it when
// qrvt dies, even while its guest spins and blocks the worker's own exit through its pipe
// (docs/spikes/S1.md section 14.4). The signal fires when the thread that started the worker exits,
// not only when the process does, and Go ends a thread when a goroutine locked to it exits. So the
// worker is started from a goroutine that locks its thread and keeps it until release, which the
// caller calls once the worker has been waited for; while locked, no other goroutine can run on that
// thread, lock it and end it. Without job (tests of the pipe exit alone, as for the Job Object on
// Windows) the worker is started plainly, with neither.
func startProc(cmd *exec.Cmd, job bool) (func(), error) {
	if !job {
		return func() {}, cmd.Start()
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
	started, done := make(chan error), make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		started <- cmd.Start()
		<-done
	}()
	if err := <-started; err != nil {
		close(done)
		return func() {}, err
	}
	return func() { close(done) }, nil
}
