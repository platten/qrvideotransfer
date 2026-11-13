package qrvt

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/platten/qrvideotransfer/internal/video"
)

// TestMain calls the hook first, as a host's main must.
func TestMain(m *testing.M) {
	EngineWorker()
	os.Exit(m.Run())
}

// TestEngineWorker: in an ordinary process the hook returns and enables workers; started as a
// worker, it serves the job (here an empty one, refused) and exits with the worker's code.
func TestEngineWorker(t *testing.T) {
	if err := video.WorkerStatus(); err != nil {
		t.Fatalf("workers not enabled: %v", err)
	}
	code := -1
	engineWorker([]string{video.WorkerArg}, strings.NewReader(""), io.Discard, func(c int) { code = c })
	if code != 2 {
		t.Errorf("worker with no job exited %d", code)
	}
	code = -1
	engineWorker([]string{"decode"}, strings.NewReader(""), io.Discard, func(c int) { code = c })
	if code != -1 {
		t.Errorf("an ordinary process exited %d", code)
	}
}
