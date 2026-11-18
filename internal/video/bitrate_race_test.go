//go:build race

package video

// bitrateRace reports a race-detector build: TestEncodeBitrateRoundTrip, 15 s there, then runs in
// the non-race step only. It crosses no goroutine boundary that the engine's other tests do not
// cross under the race detector, and the isolated race step must not grow (DESIGN.md section 19).
const bitrateRace = true
