//go:build !windows

package rs

import "os"

// setSparse does nothing: on Unix file systems a file extended with Truncate is sparse already.
func setSparse(*os.File) {}
