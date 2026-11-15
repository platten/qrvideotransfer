//go:build !darwin

package video

// TEMPORARY measurement, removed before review. The report runs only on macOS.
func loadAverage() string { return "unknown" }
