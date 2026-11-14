//go:build race

package video

// With the race detector, close-on-cancel's check at every loop back-edge of the engine runs as
// instrumented Go, and every guest read goes through instrumented host code: on windows-2025, with
// every package testing at once, probing a 4-frame 640 x 480 clip exceeded EngineProbeTime. Release
// builds never use the race detector (CGO_ENABLED=0), so only these tests get a longer probe bound.
func init() { probeTime = 4 * EngineProbeTime }
