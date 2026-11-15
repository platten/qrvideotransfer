//go:build race

package vision

// geomRace reports a race-detector build, in which the simulator runs about ten times slower; the
// simulated sweeps then film a quarter of their poses.
const geomRace = true
