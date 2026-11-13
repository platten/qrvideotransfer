package video

import (
	"context"
	"errors"
	"io"
	"time"
)

// procRunner runs each job in a worker process: qrvt's own executable started with WorkerArg. The
// job travels over the worker's standard input, never through its arguments or environment; the
// results come back over its standard output, and every byte of them is treated as untrusted.
type procRunner struct {
	exe  string
	args []string

	env []string

	started func(pid int)
}

// errWorkerStart marks a worker that could not be started; the job then runs in process.
var errWorkerStart = errors.New("cannot start an engine worker")

// workerEnv is the environment a worker gets: nothing but what the operating system needs to run a
// Go program, and, in tests, where coverage goes.
func workerEnv() []string { panic("not implemented") }

// exec implements runner. The watchdog's cancellation kills the worker, then waits for it.
func (p *procRunner) exec(ctx context.Context, j *job, m *meter, cacheDir string) (int, error) {
	panic("not implemented")
}

// feedWorker sends the job and then the program's standard input, if any. It keeps the pipe open
// afterwards: closing it tells the worker to exit.
func feedWorker(w io.Writer, job []byte, stdin io.Reader) { panic("not implemented") }

// readWorker dispatches the worker's messages until its exit message. Anything malformed ends the
// job with an error, before any allocation larger than one bounded message.
func readWorker(r io.Reader, j *job, m *meter) (int, error) { panic("not implemented") }

// waitDelay bounds how long a killed worker may take to be reaped.
const waitDelay = 5 * time.Second
