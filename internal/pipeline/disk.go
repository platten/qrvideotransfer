package pipeline

import "errors"

// ReserveBytes is the free disk space a decode leaves untouched (SECURITY.md section 5).
const ReserveBytes = 1 << 30

// diskFree returns the bytes available to this process in the folder; tests replace it.
var diskFree = platformDiskFree

func platformDiskFree(dir string) (int64, error) { return 0, errors.New("pipeline: not implemented") }
