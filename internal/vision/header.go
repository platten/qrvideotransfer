package vision

import (
	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/video"
)

// SlotReader is the HeaderReader of docs/DESIGN.md section 10, step 2. The zero value is ready to
// use and safe for concurrent use.
type SlotReader struct{}

var _ HeaderReader = SlotReader{}

// Read reads the four header copies of the code whose finders are q.
func (SlotReader) Read(f *video.Frame, q Quad) ([]frame.Header, bool) {
	return nil, false
}

// copySoft returns the soft bits of the four header copies, indexed by copy and slot number.
func copySoft(f *video.Frame, q Quad) ([4][frame.HeaderSlots]int8, bool) {
	return [4][frame.HeaderSlots]int8{}, false
}
