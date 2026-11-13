// Command qrvt turns files into a looping video of on-screen codes and a camera recording of that
// video back into the files. This file holds main only; the commands live in internal/cli.
package main

import (
	"fmt"
	"os"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if _, err := fmt.Fprintln(os.Stdout, "qrvt", version); err != nil {
		os.Exit(1)
	}
}
