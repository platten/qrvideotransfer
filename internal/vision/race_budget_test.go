package vision

import (
	"testing"

	"github.com/platten/qrvideotransfer/internal/channel"
)

// The race budget of internal/vision.
//
// Under the race detector the simulator runs about ten times slower, and on the slowest CI runner
// (macos-26-intel) the whole package took 959 s, over the race step's 20 minutes. The vision code
// itself starts no goroutine; what the race step must exercise is every code path and the
// goroutine boundaries of the test harness (geomParallel) and of the simulator. So, under -race:
//
//   - statistical tests (rates over many poses, tables, long figures) are skipped with
//     raceSkipStatistics; they run, unchanged, in the non-race test step on every runner;
//   - tests that walk a code path on simulated captures take raceSize's small size: one or two
//     poses or captures instead of many;
//   - the concurrency tests (geom_concurrent_test.go) run in full: they are what the race step is
//     for.
//
// Budget: the package must finish under -race in about 3 minutes on a developer machine. Measured
// on a 16-thread laptop: 134 s before this budget, 40 s after; without -race, 14 s.

// raceSize returns full, or small under the race detector.
func raceSize(full, small int) int {
	if raceEnabled {
		return small
	}
	return full
}

// raceSkipStatistics skips a statistical test under the race detector: it asserts rates over many
// simulated captures, which the non-race step asserts on every runner.
func raceSkipStatistics(t *testing.T) {
	t.Helper()
	if raceEnabled {
		t.Skip("statistical: asserted in the non-race test step")
	}
}

// geomRaceGlareCode returns the code and handheld configuration of a glare test: a 480 x 270 grid
// filmed at 4K, or under the race detector a 212 x 120 grid filmed at 1280 x 720, since a 4K
// simulated capture costs too much there. n frames, seeded with seed.
func geomRaceGlareCode(t *testing.T, n int, seed uint64) (*geomCode, channel.Config) {
	t.Helper()
	c := geomNewCode(t, raceSize(480, 212), raceSize(270, 120), 4, 2, n, seed)
	return c, geomConfig(t, "handheld", raceSize(0, 1280), raceSize(0, 720))
}
