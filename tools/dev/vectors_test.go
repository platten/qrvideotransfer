package main

import (
	"strings"
	"testing"
)

// TestFormatVectorsRuns: the format-vectors task regenerates the frame vectors and then the
// reference video, each through its own test with the release build's tags.
func TestFormatVectorsRuns(t *testing.T) {
	got := formatVectorsRuns("noasm,nounsafe")
	want := []string{
		"test -tags=noasm,nounsafe -count=1 -run ^TestFrameVectors$ ./internal/pipeline -args -update",
		"test -tags=noasm,nounsafe -count=1 -run ^TestReferenceVideo$ . -args -update-reference",
	}
	if len(got) != len(want) {
		t.Fatalf("formatVectorsRuns = %q, want %q", got, want)
	}
	for i := range want {
		if s := strings.Join(got[i], " "); s != want[i] {
			t.Errorf("run %d = %q, want %q", i, s, want[i])
		}
	}
	if tasks["format-vectors"] == nil {
		t.Error("no format-vectors task")
	}
}
