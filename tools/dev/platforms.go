package main

type platformJob struct {
	name string
	env  []string
}

func compileJobs(tags string) []platformJob { return nil }

func zigTarget(goos, goarch string) (string, bool) { return "", false }

func raceEnv(zig, goos, goarch, target string) (env []string, ldflags string) { return nil, "" }

func findZig(getenv func(string) string, lookPath func(string) (string, error)) string { return "" }

func arm64Runner(lookPath func(string) (string, error), binfmt func(string) bool) (string, bool) {
	return "", false
}

type testResult struct{ passed, failed, skipped []string }

func parseTestOutput(out string) testResult { return testResult{} }
