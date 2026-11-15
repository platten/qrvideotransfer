//go:build !unix

package video

import "os"

// maxRSS is not available outside Unix: on Windows os.ProcessState's rusage holds times only.
func maxRSS(*os.ProcessState) (uint64, bool) { return 0, false }
