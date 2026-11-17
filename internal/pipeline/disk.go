package pipeline

import (
	"fmt"

	"github.com/platten/qrvideotransfer/internal/payload"
)

// ReserveBytes is the free disk space a decode leaves untouched (SECURITY.md section 5).
const ReserveBytes = 1 << 30

// diskFree returns the bytes available to this process in the folder; tests replace it.
var diskFree = platformDiskFree

// checkDisk refuses to write need more bytes in dir unless they fit in its free space less
// ReserveBytes (SECURITY.md section 5, stream length; T3). It is checked before the shard store and
// the stream file are made, both sized by the validated descriptor.
func checkDisk(dir, what string, need int64) error {
	free, err := diskFree(dir)
	if err != nil {
		return fmt.Errorf("pipeline: free space for the %s: %w", what, err)
	}
	if avail := free - ReserveBytes; need > avail {
		return &payload.LimitError{Limit: "free disk space", Value: need, Max: max(avail, 0)}
	}
	return nil
}
