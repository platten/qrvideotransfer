//go:build !unix

package video

// closeInherited does nothing outside Unix: on Windows, Go passes a child only the handles it names
// (its three standard ones here), whatever this process inherited.
func closeInherited() {}
