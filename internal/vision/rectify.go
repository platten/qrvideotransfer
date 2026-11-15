package vision

import (
	"errors"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/video"
)

// MeshFitter is the Rectifier of docs/DESIGN.md section 10, step 3. The zero value is ready to use
// and safe for concurrent use.
type MeshFitter struct{}

var _ Rectifier = MeshFitter{}

// Fit fits the cell-to-image mesh of the code with layout l whose finders are q.
func (MeshFitter) Fit(f *video.Frame, q Quad, l *frame.Layout) (*Mesh, []AnchorObs, error) {
	return nil, nil, errors.New("vision: not implemented")
}
