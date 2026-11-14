//go:build windows

package video

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

var isProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

// TestWorkerJobObject: a worker is started suspended, put in its kill-on-close Job Object and only
// then resumed, so it never runs outside the job. It runs (it answers), IsProcessInJob finds it in
// that job, and closing the job's last handle kills it while its pipe from the parent is still open.
func TestWorkerJobObject(t *testing.T) {
	t.Parallel()
	cmd := exec.Command(os.Args[0], WorkerArg, "readyhang") //nolint:gosec // the test binary
	cmd.Env = os.Environ()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	job, err := startWorkerJob(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	b, _ := json.Marshal(workerJob{Prog: "ffmpeg", Version: protocolVersion})
	if err := writeMsg(stdin, msgJob, b); err != nil {
		t.Fatal(err)
	}
	if typ, _, err := readMsg(bufio.NewReader(stdout), nil, parentLimit); typ != msgReady || err != nil {
		t.Fatalf("the worker did not run: %q %v", typ, err)
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(cmd.Process.Pid)) //nolint:gosec // a process id
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var in int32
	r, _, callErr := isProcessInJob.Call(uintptr(h), uintptr(job), addr(&in))
	runtime.KeepAlive(&in)
	if r == 0 {
		t.Fatalf("IsProcessInJob: %v", callErr)
	}
	if in == 0 {
		t.Fatal("the worker is not in its Job Object")
	}
	if err := windows.CloseHandle(job); err != nil {
		t.Fatal(err)
	}
	gone(t, []int{cmd.Process.Pid}, 5*time.Second)
}
