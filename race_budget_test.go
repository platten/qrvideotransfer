package qrvt

import "testing"

// Under the race detector the package runs a subset sized for the race step (as internal/vision
// does): the API's round trip, which crosses the pipeline's goroutines; the matrix, the typed
// errors and the simulator's prefix property run in the non-race step.

// raceSkipStatistics skips, under the race detector, a test asserted in the non-race test step.
func raceSkipStatistics(t *testing.T) {
	t.Helper()
	if raceEnabled {
		t.Skip("asserted in the non-race test step")
	}
}
