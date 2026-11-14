package video

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrEngine reports that ffmpeg (embedded or native) failed; the message carries the end of its log.
var ErrEngine = errors.New("video: the video engine failed")

// progress names what counts as progress for the watchdog.
type progress int

const (
	progOutput progress = iota // bytes on standard output (decoding)
	progReads                  // bytes read from the input file or written to standard output (probing)
	progInput                  // bytes taken from standard input (encoding)
	progFiles                  // growth of the files in the output directory (copying a stream)
)

// job is one run of ffmpeg or ffprobe.
type job struct {
	prog string // "ffmpeg" or "ffprobe"
	// args returns the arguments, given the input file's name and the output directory as the
	// program sees them.
	args     func(in, out string) []string
	input    string // host path of the input file, or ""
	outDir   string // host directory for output files, or ""
	stdin    io.Reader
	stdout   io.Writer
	stderr   io.Writer // nil: kept in a short tail for the error message
	progress progress
	timeout  time.Duration // watchdog limit; 0 means EngineStallTimeout
	check    func(m *meter, active time.Duration) error
}

var stallTimeout = EngineStallTimeout

const formatWhitelist = ""

func probeLimits(int64, time.Duration) func(*meter, time.Duration) error {
	return func(*meter, time.Duration) error { return nil }
}

// runner runs jobs: the embedded engine or a native ffmpeg.
type runner interface {
	// exec runs j, counting progress in m, and returns the exit code.
	exec(ctx context.Context, j *job, m *meter) (int, error)
}

// run executes j under the watchdog. A non-zero exit code becomes an [ErrEngine] error carrying the
// end of the program's log; a stall becomes the EngineStallTimeout [LimitError].
func run(ctx context.Context, r runner, j job) error {
	tail := &logTail{}
	var log fmt.Stringer = tail
	if j.stderr == nil {
		j.stderr = tail
	} else if s, ok := j.stderr.(fmt.Stringer); ok {
		log = s
	}
	m := &meter{}
	if j.stdout == nil {
		j.stdout = io.Discard
	}
	if j.progress != progInput {
		j.stdout = meterWriter{w: j.stdout, m: m}
	}
	if j.stdin != nil && j.progress == progInput {
		j.stdin = meterReader{r: j.stdin, m: m}
	}
	if j.progress == progFiles {
		m.extra = func() int64 { return dirSize(j.outDir) }
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	done := make(chan struct{})
	defer close(done)
	timeout := j.timeout
	if timeout == 0 {
		timeout = EngineStallTimeout
	}
	go watch(m, timeout, cancel, done)
	rc, err := r.exec(ctx, &j, m)
	if cause := context.Cause(ctx); cause != nil && ctx.Err() != nil {
		return cause
	}
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrEngine, j.prog, err)
	}
	if rc != 0 {
		return fmt.Errorf("%w: %s exit status %d: %s", ErrEngine, j.prog, rc, log.String())
	}
	return nil
}

// dirSize is the total size of the regular files directly in dir.
func dirSize(dir string) int64 {
	var n int64
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		if fi, err := e.Info(); err == nil && fi.Mode().IsRegular() {
			n += fi.Size()
		}
	}
	return n
}

// logTail keeps the last bytes written to it, for error messages.
type logTail struct {
	mu sync.Mutex
	b  []byte
}

const logTailLen = 600

func (t *logTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.b = append(t.b, p...)
	if len(t.b) > 4*logTailLen {
		t.b = append(t.b[:0], t.b[len(t.b)-logTailLen:]...)
	}
	t.mu.Unlock()
	return len(p), nil
}

func (t *logTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return sanitise(string(t.b), logTailLen)
}

// inputName is the name under which a decoding instance sees its input: "input" plus the file's
// extension when that is short and alphanumeric (ffmpeg uses it as a hint).
func inputName(path string) string {
	ext := filepath.Ext(path)
	if len(ext) < 2 || len(ext) > 6 {
		return "input"
	}
	for _, c := range ext[1:] {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return "input"
		}
	}
	return "input" + ext
}
