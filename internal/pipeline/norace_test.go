//go:build !race

package pipeline

// raceEnabled reports a race-detector build: the tests then run a smoke subset.
const raceEnabled = false
