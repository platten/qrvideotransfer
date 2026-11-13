package video

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// procRunner runs each job in a worker process: qrvt's own executable started with WorkerArg. The
// job travels over the worker's standard input, never through its arguments or environment; the
// results come back over its standard output, and every byte of them is treated as untrusted.
type procRunner struct {
	exe  string
	args []string
	// env is the worker's whole environment (see workerEnv).
	env []string
	// started, when set, is told each worker's process id (tests).
	started func(pid int)
}

// errWorkerStart marks a worker that could not be started; the job then runs in process.
var errWorkerStart = errors.New("cannot start an engine worker")

// workerEnv is the environment a worker gets: nothing but what the operating system needs to run a
// Go program, and, in tests, where coverage goes.
func workerEnv() []string {
	var env []string
	keep := []string{"GOCOVERDIR"}
	if runtime.GOOS == "windows" {
		keep = append(keep, "SYSTEMROOT", "TEMP", "TMP")
	}
	for _, k := range keep {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// exec implements runner. The watchdog's cancellation kills the worker, then waits for it.
func (p *procRunner) exec(ctx context.Context, j *job, m *meter, cacheDir string) (int, error) {
	in := ""
	wj := workerJob{Prog: j.prog, CacheDir: cacheDir, MaxOutput: j.maxOutput, Reads: j.progress == progReads, Module: j.module}
	var err error
	if j.input != "" {
		in = "/in/" + inputName(j.input)
		if wj.Input, err = filepath.Abs(j.input); err != nil {
			return 0, err
		}
	}
	if j.outDir != "" {
		if wj.OutDir, err = filepath.Abs(j.outDir); err != nil {
			return 0, err
		}
	}
	wj.Args = j.args(in, "/out")
	msg, err := json.Marshal(wj)
	if err != nil || len(msg) > maxJobMsg {
		return 0, fmt.Errorf("%w: job too large", ErrEngine)
	}
	cmd := exec.Command(p.exe, p.args...) //nolint:gosec // qrvt's own executable
	cmd.Env, cmd.Dir, cmd.WaitDelay = p.env, os.TempDir(), waitDelay
	tail := &logTail{}
	cmd.Stderr = tail
	toWorker, err := cmd.StdinPipe()
	if err != nil {
		return 0, err
	}
	fromWorker, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("%w: %w", errWorkerStart, err)
	}
	release := killOnClose(cmd.Process)
	defer release()
	if p.started != nil {
		p.started(cmd.Process.Pid)
	}
	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
		case <-finished:
		}
	}()
	go feedWorker(toWorker, msg, j.stdin)
	rc, err := readWorker(bufio.NewReaderSize(fromWorker, 64<<10), j, m)
	close(finished)
	_ = toWorker.Close() // the worker's lifeline: it exits when this closes
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if err != nil {
		return 0, fmt.Errorf("%w; worker log: %s", err, tail.String())
	}
	return rc, nil
}

// feedWorker sends the job and then the program's standard input, if any. It keeps the pipe open
// afterwards: closing it tells the worker to exit.
func feedWorker(w io.Writer, job []byte, stdin io.Reader) {
	if writeMsg(w, msgJob, job) != nil {
		return
	}
	if stdin != nil {
		buf := make([]byte, maxChunk)
		for {
			n, err := stdin.Read(buf)
			if n > 0 && writeMsg(w, msgData, buf[:n]) != nil {
				return
			}
			if err != nil {
				break
			}
		}
	}
	_ = writeMsg(w, msgEnd, nil)
}

// readWorker dispatches the worker's messages until its exit message. Anything malformed ends the
// job with an error, before any allocation larger than one bounded message.
func readWorker(r io.Reader, j *job, m *meter) (int, error) {
	var buf []byte
	var reads int64
	for {
		var typ byte
		var err error
		typ, buf, err = readMsg(r, buf, parentLimit)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return 0, fmt.Errorf("%w: the engine worker ended without a result", ErrEngine)
			}
			return 0, fmt.Errorf("%w: %w", ErrEngine, err)
		}
		switch typ {
		case msgOut:
			if _, err := j.stdout.Write(buf); err != nil {
				return 0, err
			}
		case msgLog:
			_, _ = j.stderr.Write(buf)
		case msgProgress:
			if len(buf) != 8 {
				return 0, fmt.Errorf("%w: worker protocol: progress of %d bytes", ErrEngine, len(buf))
			}
			if v := int64(binary.BigEndian.Uint64(buf)); v > reads { //nolint:gosec // compared, not trusted
				m.n.Add(min(v-reads, 1<<40))
				reads = v
			}
		case msgExit:
			if len(buf) < 4 {
				return 0, fmt.Errorf("%w: worker protocol: short exit message", ErrEngine)
			}
			rc := int(int32(binary.BigEndian.Uint32(buf))) //nolint:gosec // a signed exit code
			if rc != 0 || len(buf) > 4 {
				_, _ = j.stderr.Write([]byte("\n" + sanitise(string(buf[4:]), logTailLen) + "\n"))
			}
			if rc == -1 {
				return 0, fmt.Errorf("%w: the worker refused the job: %s", ErrEngine, sanitise(string(buf[4:]), 200))
			}
			return rc, nil
		}
	}
}

// waitDelay bounds how long a killed worker may take to be reaped.
const waitDelay = 5 * time.Second
