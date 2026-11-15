package video

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// TestWorkerParentKilledReserved: on Linux a worker whose parent dies is killed by the kernel (its
// parent-death signal), even when its guest has reserved 512 MiB and spins, which keeps it from
// exiting through its pipe (docs/spikes/S1.md section 14.4). The parent is a helper process (the
// test binary as a fake parent), killed as soon as the worker has reported its reservation; the
// worker runs with GOMAXPROCS 2, at which, without the signal, it survived 8 runs in 8.
func TestWorkerParentKilledReserved(t *testing.T) {
	t.Parallel()
	parentKilled(t, "spinbig")
}

// TestStartProcJob: startProc honours its job flag as on Windows: with it (the default) the worker
// gets SIGKILL as its parent-death signal; without it (the "nojob" test of the pipe exit alone) it
// gets none.
func TestStartProcJob(t *testing.T) {
	t.Parallel()
	for _, job := range []bool{true, false} {
		cmd := exec.Command(os.Args[0], WorkerArg, "early") //nolint:gosec // the test binary
		cmd.Env = workerEnv()
		release, err := startProc(cmd, job)
		if err != nil {
			t.Fatal(err)
		}
		_ = cmd.Wait()
		release()
		want := syscall.Signal(0)
		if job {
			want = syscall.SIGKILL
		}
		if got := cmd.SysProcAttr.Pdeathsig; got != want {
			t.Errorf("job %v: parent-death signal %v, want %v", job, got, want)
		}
	}
}
