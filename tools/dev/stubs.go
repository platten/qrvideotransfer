package main

import "net/http"

type actionRef struct{ repo, sha, version, where string }

func workflowProblems(name string, data []byte, workflow bool) []string { return nil }
func pinnedRefs(name string, data []byte) []actionRef                   { return nil }
func verifyRefs(refs []actionRef, lsRemote func(string) (string, error)) []string {
	return nil
}
func checkWorkflowDir(dir string) ([]string, int, error)                       { return nil, 0, nil }
func releaseTagsFrom(b []byte) (string, error)                                 { return "", nil }
func checkDist(dir string, checkBin func(path, goos string) error) error       { return nil }
func signWindows(args []string) error                                          { return nil }
func osslsigncodeArgs(pfx, pass, ts, in, out string) []string                  { return nil }
func selectTools(s string) ([]tool, error)                                     { return nil, nil }
func extractBinary(data []byte, name string, zipped bool, out string) error    { return nil }
func fetchVerified(c *http.Client, url, sum string, delay int) ([]byte, error) { return nil, nil }
func installTool(c *http.Client, t tool, dir string) error                     { return nil }
