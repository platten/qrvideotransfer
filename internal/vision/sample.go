package vision

import (
	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
	"github.com/platten/qrvideotransfer/internal/video"
)

// FootprintSampler is the Sampler of docs/DESIGN.md section 10, step 4. The zero value is ready to
// use and safe for concurrent use.
type FootprintSampler struct{}

var _ Sampler = FootprintSampler{}

// Sample writes the observation of cells[i] to dst[i].
func (FootprintSampler) Sample(dst []modem.Obs, f *video.Frame, m *Mesh, cells []frame.Cell) {
}
