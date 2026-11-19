package main

// formatVectorsRuns returns the `go test` invocations that regenerate the format's test vectors
// that come from the encoder (FORMAT.md section 13): the frame vectors, testdata/golden/frames.json,
// through the raw-frame backend, and the reference video, testdata/golden/reference-v1.mp4 and its
// description, through the public API with the embedded engine and one worker. Each test then
// checks what it wrote.
func formatVectorsRuns(tags string) [][]string {
	return [][]string{
		goTest(tags, "-count=1", "-run", "^TestFrameVectors$", "./internal/pipeline", "-args", "-update"),
		goTest(tags, "-count=1", "-run", "^TestReferenceVideo$", ".", "-args", "-update-reference"),
	}
}

// formatVectors regenerates the frame vectors and the reference video. They are regenerated when
// FORMAT.md changes before v1 is frozen and never after (FORMAT.md section 13).
func formatVectors(args []string) error {
	_ = newFlags("format-vectors").Parse(args) // ExitOnError: never returns an error
	tags, err := releaseTags()
	if err != nil {
		return err
	}
	for _, a := range formatVectorsRuns(tags) {
		if err := run([]string{"CGO_ENABLED=0"}, "go", a...); err != nil {
			return err
		}
	}
	return nil
}
