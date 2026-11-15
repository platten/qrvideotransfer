package main

var docsOnlyAllowList = []string{"docs/FORMAT.md"}

func codeChanged(event string, files []string) bool { return false }

func parseDiffNames(out string) []string { return nil }

func ciJobs(b []byte) (jobs []string, needs map[string][]string, gated map[string]bool) {
	return nil, nil, nil
}
