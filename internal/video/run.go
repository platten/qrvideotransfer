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
	progOutput progress = iota // bytes on standard output
	progReads                  // bytes read from the input file or written to standard output (probing)
	progInput                  // bytes taken from standard input (encoding)
	progFiles                  // growth of the files in the output directory (copying a stream)
	progFrames                 // complete, timestamped frames, counted by the reader in job.meter (decoding)
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
	timeout  time.Duration // watchdog limit; 0 means stallTimeout
	// meter, when set, is the meter run uses; the caller counts progress in it (progFrames).
	meter *meter
	// check, when set, is called at every watchdog tick with the time the instance has run, not
	// counting time waiting on qrvt or for a slot; an error cancels the instance with it as cause.
	check func(m *meter, active time.Duration) error
}

// stallTimeout is EngineStallTimeout; tests shorten it.
var stallTimeout = EngineStallTimeout

// formatWhitelist names the engine's demuxers for the containers qrvt reads (ffmpeg 5.1 names; a
// demuxer is allowed when any of its comma-separated names is listed): MP4 and MOV, Matroska and
// WebM, MPEG-TS, and H.264 and HEVC elementary streams. Anything else, an HLS playlist among them,
// is refused before it is opened, whatever the file's extension.
const formatWhitelist = "mov,mp4,m4a,3gp,3g2,mj2,matroska,webm,mpegts,h264,hevc"

// inputOptions precede the input of every job that reads a recording: local files only, and only
// the whitelisted containers.
var inputOptions = []string{"-protocol_whitelist", "file,pipe", "-format_whitelist", formatWhitelist}

// runner runs jobs: the embedded engine or a native ffmpeg.
type runner interface {
	// exec runs j, counting progress in m, and returns the exit code.
	exec(ctx context.Context, j *job, m *meter) (int, error)
}

// run executes j under the watchdog. A non-zero exit code becomes an [ErrEngine] error carrying the
// end of the program's log; a stall becomes the EngineStallTimeout [LimitError], and a failed check
// its own error.
func run(ctx context.Context, r runner, j job) error {
	tail := &logTail{}
	var log fmt.Stringer = tail
	if j.stderr == nil {
		j.stderr = tail
	} else if s, ok := j.stderr.(fmt.Stringer); ok {
		log = s
	}
	m := j.meter
	if m == nil {
		m = &meter{}
	}
	if j.stdout == nil {
		j.stdout = io.Discard
	}
	if j.progress != progInput {
		j.stdout = meterWriter{w: j.stdout, m: m, count: j.progress != progFrames}
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
		timeout = stallTimeout
	}
	go watch(m, timeout, j.check, cancel, done)
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
