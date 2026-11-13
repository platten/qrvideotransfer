// Package vision turns a capture into soft bits: it detects the finders, reads the header, fits a
// mesh to the anchors, samples cells, equalises levels per tile and estimates the frame-phase field
// (docs/DESIGN.md sections 7 and 10).
package vision

import (
	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
	"github.com/platten/qrvideotransfer/internal/video"
)

// Point is a position in image coordinates, in pixels of the capture's Y plane.
type Point struct {
	X, Y float64
}

// Quad is the four finder centres of a code in a capture, indexed by frame.Corner, and the finder
// module size.
type Quad struct {
	Centres [4]Point
	Module  float64 // pixels per finder module
}

// Mesh maps cell coordinates to image coordinates: a lattice of control points, interpolated between
// nodes.
type Mesh struct {
	Cols, Rows   int     // nodes per row and per column
	X0, Y0       float64 // cell coordinates of node (0, 0)
	StepX, StepY float64 // cells between neighbouring nodes
	Nodes        []Point // row-major, Cols*Rows image positions
}

// AnchorObs is what was observed at one anchor.
type AnchorObs struct {
	Anchor       frame.Anchor
	Found        bool
	Corner       Point      // the saddle's centre, sub-pixel
	Black, White modem.Obs  // the anchor's own dark and light references
	Phase        [4]float32 // mean intensity of phase quadrants 0..3
}

// Field is the level statistics of every tile of the grid, interpolated between tiles.
type Field struct {
	TilesX, TilesY int
	TileW, TileH   int                // cells per tile
	Tiles          []modem.LocalStats // row-major
}

// PhaseField gives, for each data cell, a weight for each value of frame index mod 4.
type PhaseField struct {
	Weights [][4]float32 // indexed by data-cell number
}

// Detector finds the four finders in a capture, using the previous capture's quad when given.
type Detector interface {
	Detect(f *video.Frame, prev *Quad) (Quad, bool)
}

// HeaderReader reads the header copies; it returns one header per distinct frame index seen.
type HeaderReader interface {
	Read(f *video.Frame, q Quad) ([]frame.Header, bool)
}

// Rectifier fits the cell-to-image mesh from the anchors.
type Rectifier interface {
	Fit(f *video.Frame, q Quad, l *frame.Layout) (*Mesh, []AnchorObs, error)
}

// Sampler takes each listed cell's mean over the central part of its footprint.
type Sampler interface {
	Sample(dst []modem.Obs, f *video.Frame, m *Mesh, cells []frame.Cell)
}

// Equaliser fits per-tile level statistics from the cells, palettes and anchors.
type Equaliser interface {
	Fit(obs []modem.Obs, palette []modem.Obs, l *frame.Layout, a []AnchorObs) *Field
}

// PhaseEstimator estimates the frame-phase field from the anchors' phase markers.
type PhaseEstimator interface {
	Estimate(a []AnchorObs, l *frame.Layout) PhaseField
}
