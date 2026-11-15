//go:build race

package video

// TestSkipPictures' decode-time check under the race detector. The frame copies run several times
// slower there (111 s for the 4K clip on macos-26-intel), so race builds decode skip1080.mkv, 100
// frames at 1080p in 9 KB: the work before the stop is the limit times the file size, a quarter of
// the 4K clip's. They also allow five times the 60 s bound. The non-race step on every runner keeps
// the 4K clip and 60 s (SECURITY.md 9.5).
func init() { skipDecodeClip, skipTimeFactor = "skip1080.mkv", 5 }
