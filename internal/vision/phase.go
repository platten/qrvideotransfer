package vision

import (
	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/modem"
)

// CleanWeight is the phase weight a cell needs to count for a code frame (DESIGN.md section 7,
// rule 3).
const CleanWeight = 0.92

// AnchorPhase implements PhaseEstimator (DESIGN.md section 7, rule 2). It is a value and safe for
// concurrent use.
type AnchorPhase struct {
	// Gamma is the exponent that turns code values above black (16 on the 8-bit scale) into linear
	// light; 0 means 2.4. PaletteGamma measures it.
	Gamma float64
}

// Estimate returns each data cell's weights for the four values of frame index mod 4.
func (e AnchorPhase) Estimate(a []AnchorObs, l *frame.Layout) PhaseField {
	return PhaseField{}
}

// PaletteGamma measures the linearising exponent from the palette (patch k of strip c at 8c + k).
// It needs a grey modulation with more than two levels; otherwise it reports false.
func PaletteGamma(palette []modem.Obs, c *modem.Constellation) (float64, bool) {
	return 0, false
}

// Phase returns the phase (frame index mod 4) that data cell i counts for, and false when no
// weight reaches CleanWeight.
func (p *PhaseField) Phase(i int) (int, bool) {
	return 0, false
}

// Clean reports whether any cell counts for some phase. A capture where none does is unusable and
// raises the ExposureTooLong warning (DESIGN.md section 7, rule 10).
func (p *PhaseField) Clean() bool {
	return false
}
