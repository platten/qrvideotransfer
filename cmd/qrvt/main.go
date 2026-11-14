// Command qrvt turns files into a looping video of on-screen codes and a camera recording of that
// video back into the files. This file holds main only; the commands live in internal/cli.
package main

import (
	"context"
	"os"

	qrvt "github.com/platten/qrvideotransfer"
	"github.com/platten/qrvideotransfer/internal/cli"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	qrvt.EngineWorker() // first: a process started as an engine worker serves its job and exits here
	err := cli.Execute(context.Background(), cli.NewEnv(version), os.Args[1:])
	os.Exit(cli.ExitCode(err))
}
