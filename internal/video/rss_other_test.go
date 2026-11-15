//go:build !linux && !darwin

package video

// sampleRSS is not measured here: on Windows the peak working set needs a process handle query
// (GetProcessMemoryInfo, through a struct address), which this test does not add.
func sampleRSS(int) (uint64, bool) { return 0, false }

// rssIsPeak says whether sampleRSS gives the true peak (true) or a sampled current size.
const rssIsPeak = false
