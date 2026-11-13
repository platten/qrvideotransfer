package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseTagsFrom(t *testing.T) {
	cases := []struct {
		yaml, want string
		ok         bool
	}{
		{"env:\n  - RELEASE_TAGS=\n", "", true},
		{"env:\n  - RELEASE_TAGS=noasm\n", "noasm", true},
		{"env:\r\n  - RELEASE_TAGS=noasm,purego\r\n", "noasm,purego", true},
		{"env:\n  - OTHER=1\n", "", false},
		{"env:\n  - RELEASE_TAGS=a\n  - RELEASE_TAGS=b\n", "", false},
		{"env:\n  - RELEASE_TAGS=a b\n", "", false},
	}
	for _, c := range cases {
		got, err := releaseTagsFrom([]byte(c.yaml))
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("releaseTagsFrom(%q) = %q, %v; want %q, ok=%v", c.yaml, got, err, c.want, c.ok)
		}
	}
}

// TestRepositoryReleaseTags checks that the repository's .goreleaser.yaml defines the tag set once.
func TestRepositoryReleaseTags(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := releaseTagsFrom(b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "-tags={{ .Env.RELEASE_TAGS }}") {
		t.Fatal(".goreleaser.yaml does not pass RELEASE_TAGS to the build")
	}
}

// fakeArtifact is an entry of a fake dist/artifacts.json.
type fakeArtifact struct {
	Name   string         `json:"name"`
	Path   string         `json:"path"`
	Goos   string         `json:"goos,omitempty"`
	Goarch string         `json:"goarch,omitempty"`
	Type   string         `json:"type"`
	Extra  map[string]any `json:"extra,omitempty"`
}

func writeTarGz(t *testing.T, path string, names []string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, n := range names {
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeZip(t *testing.T, path string, names []string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeDist builds a dist directory that satisfies checkDist; mutate may edit the artefact list
// before it is written.
func fakeDist(t *testing.T, mutate func([]fakeArtifact) []fakeArtifact) string {
	t.Helper()
	root := t.TempDir()
	dist := filepath.Join(root, "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	var arts []fakeArtifact
	var sums strings.Builder
	add := func(a fakeArtifact, content []byte) {
		a.Path = "dist/" + a.Name
		full := filepath.Join(root, filepath.FromSlash(a.Path))
		if content != nil {
			if err := os.WriteFile(full, content, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		b, err := os.ReadFile(full)
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(b)
		if a.Type != "Binary" {
			fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), a.Name)
		}
		arts = append(arts, a)
	}
	for _, tg := range targets {
		goos, goarch := tg[0], tg[1]
		bin := "qrvt"
		if goos == "windows" {
			bin += ".exe"
		}
		add(fakeArtifact{Name: "bin_" + goos + "_" + goarch, Goos: goos, Goarch: goarch, Type: "Binary"}, []byte("binary"))
		files := []string{"LICENSE", "NOTICE", "README.md", bin}
		name := "qrvt_1.0.0_" + goos + "_" + goarch
		if goos == "windows" {
			name += ".zip"
			writeZip(t, filepath.Join(dist, name), files)
			add(fakeArtifact{Name: name, Goos: goos, Goarch: goarch, Type: "Archive", Extra: map[string]any{"Format": "zip"}}, nil)
		} else {
			name += ".tar.gz"
			writeTarGz(t, filepath.Join(dist, name), files)
			add(fakeArtifact{Name: name, Goos: goos, Goarch: goarch, Type: "Archive", Extra: map[string]any{"Format": "tar.gz"}}, nil)
		}
		add(fakeArtifact{Name: name + ".sbom.json", Type: "SBOM"}, []byte("{}"))
		if goos == "linux" {
			for _, f := range []string{"deb", "rpm", "apk"} {
				add(fakeArtifact{Name: "qrvt_" + goarch + "." + f, Goos: goos, Goarch: goarch, Type: "Linux Package", Extra: map[string]any{"Format": f}}, []byte(f))
			}
		}
	}
	add(fakeArtifact{Name: "qrvt.rb", Type: "Homebrew Cask"}, []byte("cask"))
	add(fakeArtifact{Name: "qrvt.json", Type: "Scoop Manifest"}, []byte("{}"))
	add(fakeArtifact{Name: "platten.qrvt.yaml", Type: "Winget Manifest"}, []byte("w"))
	if err := os.WriteFile(filepath.Join(dist, "checksums.txt"), []byte(sums.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	arts = append(arts, fakeArtifact{Name: "checksums.txt", Path: "dist/checksums.txt", Type: "Checksum"})
	if mutate != nil {
		arts = mutate(arts)
	}
	b, err := json.Marshal(arts)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "artifacts.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dist
}

func noBinCheck(string, string) error { return nil }

func TestCheckDistComplete(t *testing.T) {
	dist := fakeDist(t, nil)
	if err := checkDist(dist, noBinCheck); err != nil {
		t.Fatal(err)
	}
	// The binary check is applied to every binary.
	var seen []string
	_ = checkDist(dist, func(p, goos string) error { seen = append(seen, goos); return nil })
	if len(seen) != 6 {
		t.Fatalf("binary check ran %d times, want 6", len(seen))
	}
	if err := checkDist(dist, func(string, string) error { return errors.New("dynamic") }); err == nil || !strings.Contains(err.Error(), "dynamic") {
		t.Fatalf("a failing binary check gave %v", err)
	}
}

func without(pred func(a fakeArtifact) bool) func([]fakeArtifact) []fakeArtifact {
	return func(as []fakeArtifact) []fakeArtifact {
		var out []fakeArtifact
		for _, a := range as {
			if !pred(a) {
				out = append(out, a)
			}
		}
		return out
	}
}

func TestCheckDistMissing(t *testing.T) {
	cases := []struct {
		name   string
		mutate func([]fakeArtifact) []fakeArtifact
		want   string
	}{
		{"archive", without(func(a fakeArtifact) bool { return a.Type == "Archive" && a.Goos == "darwin" && a.Goarch == "arm64" }), "no archive for darwin/arm64"},
		{"binary", without(func(a fakeArtifact) bool { return a.Type == "Binary" && a.Goos == "windows" && a.Goarch == "arm64" }), "no binary for windows/arm64"},
		{"rpm", without(func(a fakeArtifact) bool {
			return a.Type == "Linux Package" && a.Goarch == "arm64" && a.Extra["Format"] == "rpm"
		}), "no rpm package for linux/arm64"},
		{"sbom", without(func(a fakeArtifact) bool { return a.Type == "SBOM" && strings.Contains(a.Name, "linux_amd64") }), "5 SBOMs for 6 archives"},
		{"cask", without(func(a fakeArtifact) bool { return a.Type == "Homebrew Cask" }), "no Homebrew Cask"},
		{"scoop", without(func(a fakeArtifact) bool { return a.Type == "Scoop Manifest" }), "no Scoop Manifest"},
		{"winget", without(func(a fakeArtifact) bool { return a.Type == "Winget Manifest" }), "no Winget Manifest"},
		{"checksum", without(func(a fakeArtifact) bool { return a.Type == "Checksum" }), "no Checksum"},
		{"wrong format", func(as []fakeArtifact) []fakeArtifact {
			for i := range as {
				if as[i].Type == "Archive" && as[i].Goos == "windows" {
					as[i].Extra["Format"] = "tar.gz"
				}
			}
			return as
		}, "windows/amd64 archive is tar.gz, want zip"},
		{"missing file", func(as []fakeArtifact) []fakeArtifact {
			as[0].Path = "dist/absent"
			return as
		}, "absent"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkDist(fakeDist(t, c.mutate), noBinCheck)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("checkDist = %v, want an error containing %q", err, c.want)
			}
		})
	}
}

func TestCheckDistArchiveContents(t *testing.T) {
	dist := fakeDist(t, nil)
	writeTarGz(t, filepath.Join(dist, "qrvt_1.0.0_linux_amd64.tar.gz"), []string{"LICENSE", "qrvt"})
	err := checkDist(dist, noBinCheck)
	if err == nil || !strings.Contains(err.Error(), "lacks NOTICE") {
		t.Fatalf("checkDist = %v, want an archive lacking NOTICE (and a checksum mismatch)", err)
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("checkDist = %v, want a checksum mismatch too", err)
	}
	dist = fakeDist(t, nil)
	writeZip(t, filepath.Join(dist, "qrvt_1.0.0_windows_arm64.zip"), []string{"LICENSE", "NOTICE", "README.md", "qrvt"})
	if err := checkDist(dist, noBinCheck); err == nil || !strings.Contains(err.Error(), "lacks qrvt.exe") {
		t.Fatalf("checkDist = %v, want an archive lacking qrvt.exe", err)
	}
	if err := checkDist(t.TempDir(), noBinCheck); err == nil {
		t.Fatal("an empty directory passed")
	}
}

func TestSignWindowsNoop(t *testing.T) {
	t.Setenv("WINDOWS_SIGN_PFX_BASE64", "")
	if err := signWindows([]string{"some.exe"}); err != nil {
		t.Fatalf("without a certificate signWindows = %v, want a no-op", err)
	}
	t.Setenv("WINDOWS_SIGN_PFX_BASE64", "not base64!")
	if err := signWindows([]string{filepath.Join(t.TempDir(), "x.exe")}); err == nil {
		t.Fatal("a malformed certificate gave no error")
	}
	if err := signWindows(nil); err == nil {
		t.Fatal("no binary named gave no error")
	}
}

func TestOsslsigncodeArgs(t *testing.T) {
	got := strings.Join(osslsigncodeArgs("c.pfx", "p.txt", "http://ts", "in.exe", "out.exe"), " ")
	want := "sign -pkcs12 c.pfx -readpass p.txt -n qrvt -i https://github.com/platten/qrvideotransfer -h sha256 -ts http://ts -in in.exe -out out.exe"
	if got != want {
		t.Fatalf("args = %q\nwant %q", got, want)
	}
}
