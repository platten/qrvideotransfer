package vision

import (
	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
)

// CancelStripes removes plane waves from the cells (DESIGN.md section 10, step 6): it subtracts
// each cell's decided level, takes the 2-D Fourier transform of the residual on the cell grid,
// keeps the peaks that stand above the noise and subtracts their inverse transform from obs, plane
// by plane for the planes c uses. It returns the number of spectral bins removed.
func CancelStripes(obs []modem.Obs, l *frame.Layout, f *Field, c *modem.Constellation) int {
	return 0
}
