package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// TestPinnedToolsCoverTargets checks that every downloaded tool has a pinned checksum for each of
// the six development platforms, and that its URL names the version.
func TestPinnedToolsCoverTargets(t *testing.T) {
	for _, tl := range tools {
		if tl.asset == nil {
			if !strings.HasSuffix(tl.pkg, "@v"+tl.version) {
				t.Errorf("%s: go install path %q does not pin v%s", tl.name, tl.pkg, tl.version)
			}
			continue
		}
		for _, tg := range targets {
			key := tg[0] + "/" + tg[1]
			sum := tl.sha256[key]
			if len(sum) != 64 {
				t.Errorf("%s: no SHA-256 for %s", tl.name, key)
			}
			if u := tl.asset(tg[0], tg[1]); !strings.HasPrefix(u, "https://github.com/") || !strings.Contains(u, tl.version) {
				t.Errorf("%s: URL %q is not a versioned GitHub release asset", tl.name, u)
			}
		}
	}
}

func TestSelectTools(t *testing.T) {
	got, err := selectTools("golangci-lint, actionlint")
	if err != nil || len(got) != 2 || got[0].name != "golangci-lint" || got[1].name != "actionlint" {
		t.Fatalf("selectTools = %v, %v", got, err)
	}
	if all, err := selectTools(""); err != nil || len(all) != len(tools) {
		t.Fatalf("selectTools(\"\") = %d tools, %v; want all %d", len(all), err, len(tools))
	}
	if _, err := selectTools("nonesuch"); err == nil {
		t.Fatal("an unknown tool name gave no error")
	}
}

func archiveOf(t *testing.T, zipped bool, names ...string) []byte {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a")
	if zipped {
		writeZip(t, p, names)
	} else {
		writeTarGz(t, p, names)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestExtractBinary(t *testing.T) {
	for _, zipped := range []bool{false, true} {
		data := archiveOf(t, zipped, "dir/README.md", "dir/mytool", "dir/mytool-other")
		out := filepath.Join(t.TempDir(), "mytool")
		if err := extractBinary(data, "mytool", zipped, out); err != nil {
			t.Fatalf("zipped=%v: %v", zipped, err)
		}
		b, err := os.ReadFile(out)
		if err != nil || string(b) != "x" {
			t.Fatalf("zipped=%v: extracted %q, %v", zipped, b, err)
		}
		if runtime.GOOS != "windows" {
			if fi, _ := os.Stat(out); fi.Mode().Perm()&0o100 == 0 {
				t.Fatalf("zipped=%v: extracted binary is not executable: %v", zipped, fi.Mode())
			}
		}
		if err := extractBinary(data, "absent", zipped, out); err == nil {
			t.Fatalf("zipped=%v: a missing member gave no error", zipped)
		}
	}
	if err := extractBinary([]byte("not an archive"), "x", false, filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("garbage gave no error")
	}
	if err := extractBinary([]byte("not an archive"), "x", true, filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("garbage zip gave no error")
	}
}

func TestFetchVerified(t *testing.T) {
	body := []byte("release asset")
	h := sha256.Sum256(body)
	sum := hex.EncodeToString(h[:])
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The first request fails, to exercise the retry.
		if calls.Add(1) == 1 {
			http.Error(w, "busy", http.StatusBadGateway)
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	got, err := fetchVerified(srv.Client(), srv.URL, sum, 0)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("fetchVerified = %q, %v", got, err)
	}
	if _, err := fetchVerified(srv.Client(), srv.URL, strings.Repeat("0", 64), 0); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("a wrong checksum gave %v", err)
	}
	srv.Close()
	if _, err := fetchVerified(srv.Client(), srv.URL, sum, 0); err == nil {
		t.Fatal("an unreachable server gave no error")
	}
}

func TestInstallDownloaded(t *testing.T) {
	data := archiveOf(t, false, exe("fake-tool"))
	h := sha256.Sum256(data)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) }))
	defer srv.Close()
	tl := tool{
		name: "fake-tool", version: "1.0.0",
		asset:  func(goos, goarch string) string { return srv.URL + "/fake-tool.tar.gz" },
		sha256: map[string]string{runtime.GOOS + "/" + runtime.GOARCH: hex.EncodeToString(h[:])},
	}
	dir := t.TempDir()
	if err := installTool(srv.Client(), tl, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, exe("fake-tool"))); err != nil {
		t.Fatal(err)
	}
	tl.sha256 = map[string]string{}
	if err := installTool(srv.Client(), tl, dir); err == nil || !strings.Contains(err.Error(), "no pinned") {
		t.Fatalf("a platform without a checksum gave %v", err)
	}
}

func TestVersionToken(t *testing.T) {
	cases := []struct {
		out, version string
		want         bool
	}{
		{"golangci-lint has version 2.15.0 built with go1.27.4", "2.15.0", true},
		{"GitVersion:    v2.20.2\nGitCommit: abc", "2.20.2", true},
		{"Version:       1.55.0", "1.55.0", true},
		{"v1.7.12\ninstalled by downloading", "1.7.12", true},
		{"golangci-lint has version 2.15.01 built", "2.15.0", false},
		{"version 12.15.0", "2.15.0", false},
		{"version 2.15.0-rc.1", "2.15.0", false},
		{"version 2.15.0.1", "2.15.0", false},
		{"", "2.15.0", false},
	}
	for _, c := range cases {
		if got := hasVersionToken(c.out, c.version); got != c.want {
			t.Errorf("hasVersionToken(%q, %q) = %v, want %v", c.out, c.version, got, c.want)
		}
	}
}

func TestToolCandidatesPreferGOBIN(t *testing.T) {
	inGOBIN := filepath.Join("/gobin", exe("x"))
	got := toolCandidates("x", "/gobin", func(string) (string, error) { return "/path/on/PATH/x", nil })
	if len(got) != 2 || got[0] != inGOBIN || got[1] != "/path/on/PATH/x" {
		t.Fatalf("toolCandidates = %v, want the GOBIN copy first", got)
	}
	if got := toolCandidates("x", "/gobin", func(string) (string, error) { return inGOBIN, nil }); len(got) != 1 {
		t.Fatalf("toolCandidates = %v, want no duplicate", got)
	}
	if got := toolCandidates("x", "/gobin", func(string) (string, error) { return "", os.ErrNotExist }); len(got) != 1 {
		t.Fatalf("toolCandidates = %v, want only GOBIN", got)
	}
}
