package pipeline

import "testing"

// Under the race detector the package runs a subset sized for the race step (as internal/vision
// does): every test that crosses a goroutine boundary or guards a limit, at a smaller size where
// that keeps what it asserts; the statistical and simulator-filmed ones run in the non-race step.

// raceSize returns full, or small under the race detector.
func raceSize(full, small int) int {
	if raceEnabled {
		return small
	}
	return full
}

// raceSkipStatistics skips, under the race detector, a test asserted in the non-race test step.
func raceSkipStatistics(t *testing.T) {
	t.Helper()
	if raceEnabled {
		t.Skip("asserted in the non-race test step")
	}
}
