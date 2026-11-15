package vision

import (
	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
)

// Tile size of the level calibration, in cells (DESIGN.md section 10, step 5). Tile (i, j) covers
// cells [32i, 32i + 32) x [30j, 30j + 30); its centre, (32i + 16, 30j + 15), is the centre of the
// anchor at (12 + 32i, 13 + 30j).
const (
	TileW = 32
	TileH = 30
)

// TileEqualiser implements Equaliser: per-tile level statistics started from the palette and the
// anchors' black and white, refined by expectation-maximisation on the data cells, and optionally
// stripe cancellation (DESIGN.md section 10, steps 5 and 6). It holds no state between calls and is
// safe for concurrent use.
type TileEqualiser struct {
	Mod     *modem.Constellation // the modulation of the frames
	Rounds  int                  // EM rounds; 0 means 3
	Stripes bool                 // cancel stripes, correcting obs in place, and fit again
}

// Fit returns the level statistics of every tile. obs holds one observation per data cell in
// data-cell order; palette holds patch k of strip c (a frame.Corner) at 8c + k and may be empty.
// Saturated and non-finite observations are ignored. With Stripes set, Fit subtracts the stripes
// it finds from obs.
func (e *TileEqualiser) Fit(obs []modem.Obs, palette []modem.Obs, l *frame.Layout, a []AnchorObs) *Field {
	return nil
}

// At returns the statistics at cell (x, y), interpolated bilinearly between tile centres and
// extrapolated as constant beyond the outermost centres.
func (f *Field) At(x, y int) modem.LocalStats {
	return modem.LocalStats{}
}

// SoftBits writes b soft bits per data cell to dst, in data-cell order (DESIGN.md section 10,
// steps 7 and 8): m's LLR with the cell's interpolated statistics for cells whose phase weight for
// phase (the frame index mod 4) is at least CleanWeight, and zeros for the others. A nil p counts
// every cell. It returns the number of cells that count.
func SoftBits(dst []int8, obs []modem.Obs, l *frame.Layout, f *Field, m modem.Modem, p *PhaseField, phase int) int {
	return 0
}
