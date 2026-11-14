//go:build race

package video

// With the race detector, close-on-cancel's check at every loop back-edge of the engine runs as
// instrumented Go, and every guest read goes through instrumented host code: on windows-2025, with
// every package testing at once, probing a 4-frame 640 x 480 clip exceeded EngineProbeTime. Release
// builds never use the race detector (CGO_ENABLED=0), so only these tests get a longer probe bound.
func init() { probeTime = 4 * EngineProbeTime }

// TestSkipPictures' decode-time check: under the race detector the frame copies run several times
// slower (111 s for the 4K clip on macos-26-intel), so race builds decode skip1080.mkv, 100 frames
// at 1080p in 9 KB (the work before the stop is the limit times the file size, a quarter of the 4K
// clip's), with five times the 60 s bound. The non-race step on every runner keeps the 4K clip and
// 60 s.
func init() { skipDecodeClip, skipTimeFactor = "skip1080.mkv", 5 }
