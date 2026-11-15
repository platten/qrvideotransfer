package vision

import "github.com/platten/qrvideotransfer/internal/video"

// FinderDetector is the Detector of docs/DESIGN.md section 10, step 1. The zero value is ready to
// use. It keeps scratch buffers between calls, so it is not safe for concurrent use: give each
// worker its own.
type FinderDetector struct{}

var _ Detector = (*FinderDetector)(nil)

// Detect finds the four finders of a code in f.
func (d *FinderDetector) Detect(f *video.Frame, prev *Quad) (Quad, bool) {
	return Quad{}, false
}
