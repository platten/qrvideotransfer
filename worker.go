package qrvt

import (
	"io"
	"os"

	"github.com/platten/qrvideotransfer/internal/video"
)

// EngineWorker must be the first call in the main function of every program that links qrvt,
// before any flag parsing. qrvt runs each video engine instance in a child process that re-executes
// the program with a reserved first argument; when this process is such a worker, EngineWorker runs
// its job and exits the process, which is the one place this package ever does so. Otherwise it
// returns at once, having enabled the worker processes. A program that never calls it still works,
// with the engine in its own process and about 4 to 11 times slower; decodes then report a warning.
func EngineWorker() { engineWorker(os.Args[1:], os.Stdin, os.Stdout, os.Exit) }

func engineWorker(args []string, stdin io.Reader, stdout io.Writer, exit func(int)) {
	if served, code := video.ServeWorker(args, stdin, stdout); served {
		exit(code)
	}
}
