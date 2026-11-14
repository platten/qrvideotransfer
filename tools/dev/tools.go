package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// tool is a pinned developer tool. A tool with an asset is downloaded from its GitHub release and
// checked against the pinned SHA-256 of that archive (from the release's checksum file, recorded
// on 2026-11-13); this takes seconds, where building golangci-lint and GoReleaser from source took
// minutes on CI runners. A tool without an asset is built with go install.
type tool struct {
	name, version string
	// pkg is the go install path, used when no asset is pinned.
	pkg string
	// versionArgs prints a version containing version; nil reads the binary's Go build info.
	versionArgs []string
	asset       func(goos, goarch string) string
	sha256      map[string]string // by "goos/goarch"
}

func ghAsset(repo, tag, name string) string {
	return "https://github.com/" + repo + "/releases/download/" + tag + "/" + name
}

// archive returns the archive format of the release assets for goos.
func archive(goos string) string {
	if goos == "windows" {
		return "zip"
	}
	return "tar.gz"
}

// Pinned versions (IMPLEMENTATION_PROMPT.md section 3; syft, actionlint and go-licenses were
// looked up on 2026-11-13).
var tools = []tool{
	{
		name: "golangci-lint", version: "2.15.0", versionArgs: []string{"version"},
		asset: func(goos, goarch string) string {
			return ghAsset("golangci/golangci-lint", "v2.15.0", fmt.Sprintf("golangci-lint-2.15.0-%s-%s.%s", goos, goarch, archive(goos)))
		},
		sha256: map[string]string{
			"linux/amd64":   "9b7fe6a6bf6334b98decf8d86b11415ea7889afdabdd1959fdce23af2a9ebcea",
			"linux/arm64":   "b4d8f17068b35ee6060cc2f69a16fb48740d706fbba81661c318649c114a6998",
			"darwin/amd64":  "767b899a28285e88ded2823d5dcc4bc5ef681173eb2d80f029a7b17ee4ece3b8",
			"darwin/arm64":  "d1e409481a8ca846c4bc97e781eb5cb1bd5c23353a0b3977a1cbda1b1b6447d5",
			"windows/amd64": "cd7ea6063c95de245e17e5246e42bdc14bb221792c429dbd239ab3a6ade6ca6a",
			"windows/arm64": "4fb8988abf186158e30af1b8a0fb3671cf2cb02bc91d7e6ce6996f4c1e922e9a",
		},
	},
	{
		name: "goreleaser", version: "2.20.2", versionArgs: []string{"--version"},
		asset: func(goos, goarch string) string {
			o := map[string]string{"linux": "Linux", "darwin": "Darwin", "windows": "Windows"}[goos]
			a := map[string]string{"amd64": "x86_64", "arm64": "arm64"}[goarch]
			return ghAsset("goreleaser/goreleaser", "v2.20.2", fmt.Sprintf("goreleaser_%s_%s.%s", o, a, archive(goos)))
		},
		sha256: map[string]string{
			"linux/amd64":   "f3d24969a7389a46613875711d15a96d8f6ef1553d84c6ace09d69ac1e7d44c0",
			"linux/arm64":   "f0fa989c3531348e8f975aac647eb187a09046f8a34c54e0b79bed66c84b7f96",
			"darwin/amd64":  "58814536817f6ad787607ed7dfc9e774b3f1334dcffc87002ae2a54fd7b5c5c1",
			"darwin/arm64":  "4e68482f6bbdb2d3ce7f835be50a4eb7287ee999cefc563c49c5f42ad3778c78",
			"windows/amd64": "d230d6def8b6059b277d472a2790501516b429ffe83b91469daad8b7736b8a5d",
			"windows/arm64": "cf152a7d59e3d7d9cf278c7463e3f2b26cc28555e8fdea95c1dcfa773cb385f1",
		},
	},
	{
		// SBOM generator used by GoReleaser.
		name: "syft", version: "1.55.0", versionArgs: []string{"version"},
		asset: func(goos, goarch string) string {
			return ghAsset("anchore/syft", "v1.55.0", fmt.Sprintf("syft_1.55.0_%s_%s.%s", goos, goarch, archive(goos)))
		},
		sha256: map[string]string{
			"linux/amd64":   "bb65249542db5fedeeb4d3f9b018b75e7264dbfbbb10486f044581a92f90490d",
			"linux/arm64":   "929bd163db2d7cd2255d3bcb8967208079813d7b46b0c952f7b794d8e2c7d304",
			"darwin/amd64":  "e2e5831cbabaec9a36fe65c34755c1113982c842a685edce3ce0b54a0ded8003",
			"darwin/arm64":  "1217c258bd5eb91ef3189cf22a82e02f9cac07e4ce934350f428e93aff674625",
			"windows/amd64": "ada4c9b93af575f479ce80fce6629897272a2d41f300a14bc49bace020767ca2",
			"windows/arm64": "f52f7796e41e4d1ae511c228c99239fd09887077336dcb7a8c2aa7046ed7c816",
		},
	},
	{
		name: "actionlint", version: "1.7.12", versionArgs: []string{"-version"},
		asset: func(goos, goarch string) string {
			return ghAsset("rhysd/actionlint", "v1.7.12", fmt.Sprintf("actionlint_1.7.12_%s_%s.%s", goos, goarch, archive(goos)))
		},
		sha256: map[string]string{
			"linux/amd64":   "8aca8db96f1b94770f1b0d72b6dddcb1ebb8123cb3712530b08cc387b349a3d8",
			"linux/arm64":   "325e971b6ba9bfa504672e29be93c24981eeb1c07576d730e9f7c8805afff0c6",
			"darwin/amd64":  "5b44c3bc2255115c9b69e30efc0fecdf498fdb63c5d58e17084fd5f16324c644",
			"darwin/arm64":  "aba9ced2dee8d27fecca3dc7feb1a7f9a52caefa1eb46f3271ea66b6e0e6953f",
			"windows/amd64": "6e7241b51e6817ea6a047693d8e6fed13b31819c9a0dd6c5a726e1592d22f6e9",
			"windows/arm64": "cadcf7ea4efe3a68728893813643cebe1185e5b1d4be5b96245f65c9a4d5ea41",
		},
	},
	{
		// Licence-compatibility check. It publishes no binaries; building it takes seconds.
		name: "go-licenses", version: "2.0.1", pkg: "github.com/google/go-licenses/v2@v2.0.1",
	},
	{
		// Vulnerability scan of the release build (its tags); no published binaries either.
		name: "govulncheck", version: "1.9.0", pkg: "golang.org/x/vuln/cmd/govulncheck@v1.9.0",
	},
}

// toolNamed returns the pinned tool with the given name; it panics on a name not in the table,
// which is a programming error.
func toolNamed(name string) tool {
	for _, t := range tools {
		if t.name == name {
			return t
		}
	}
	panic("no pinned tool " + name)
}

// selectTools returns the tools named in a comma-separated list, or all of them for "".
func selectTools(list string) ([]tool, error) {
	if strings.TrimSpace(list) == "" {
		return tools, nil
	}
	var out []tool
	for _, n := range strings.Split(list, ",") {
		n = strings.TrimSpace(n)
		found := false
		for _, t := range tools {
			if t.name == n {
				out, found = append(out, t), true
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown tool %q", n)
		}
	}
	return out, nil
}

// hasVersion reports whether the binary at p is the pinned version of t.
func hasVersion(t tool, p string) (bool, string) {
	if t.versionArgs == nil {
		info, err := buildinfo.ReadFile(p)
		if err != nil {
			return false, err.Error()
		}
		return info.Main.Version == "v"+t.version, info.Main.Path + " " + info.Main.Version
	}
	out, err := output(p, t.versionArgs...)
	if err != nil {
		return false, err.Error()
	}
	return hasVersionToken(out, t.version), strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
}

// hasVersionToken reports whether out contains version, optionally preceded by "v", as a whole
// whitespace-separated token: 2.15.0 matches "version 2.15.0" and "v2.15.0" but not "2.15.01",
// "12.15.0" or "2.15.0-rc.1".
func hasVersionToken(out, version string) bool {
	for _, f := range strings.Fields(out) {
		if f == version || f == "v"+version {
			return true
		}
	}
	return false
}

// toolCandidates lists where to look for a tool: first the GOBIN copy that install-tools
// installed and verified, then whatever is on PATH.
func toolCandidates(name, dir string, lookPath func(string) (string, error)) []string {
	first := filepath.Join(dir, exe(name))
	out := []string{first}
	if p, err := lookPath(name); err == nil && filepath.Clean(p) != first {
		out = append(out, p)
	}
	return out
}

// find returns the path of a pinned tool at its pinned version, looking in GOBIN and then on PATH.
func find(t tool) (string, error) {
	var seen []string
	for _, p := range toolCandidates(t.name, gobin(), exec.LookPath) {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		ok, got := hasVersion(t, p)
		if ok {
			return p, nil
		}
		seen = append(seen, p+": "+got)
	}
	return "", fmt.Errorf("%s %s not found (found: %s); run: go run ./tools/dev install-tools", t.name, t.version, strings.Join(seen, "; "))
}

// fetchVerified downloads url, retrying transient failures, and checks its SHA-256.
func fetchVerified(c *http.Client, url, sum string, delay time.Duration) ([]byte, error) {
	var lastErr error
	for attempt := range 4 {
		if attempt > 0 {
			time.Sleep(delay * time.Duration(attempt))
		}
		resp, err := c.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 512<<20))
		_ = resp.Body.Close()
		if err == nil && resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("%s: %s", url, resp.Status)
		}
		if err != nil {
			lastErr = err
			continue
		}
		h := sha256.Sum256(b)
		if got := hex.EncodeToString(h[:]); got != sum {
			return nil, fmt.Errorf("%s: SHA-256 is %s, want %s", url, got, sum)
		}
		return b, nil
	}
	return nil, lastErr
}

// extractBinary writes the archive member whose base name is name to out, executable.
func extractBinary(data []byte, name string, zipped bool, out string) error {
	var r io.Reader
	if zipped {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return err
		}
		for _, f := range zr.File {
			if path.Base(f.Name) == name && !f.FileInfo().IsDir() {
				rc, err := f.Open()
				if err != nil {
					return err
				}
				defer func() { _ = rc.Close() }()
				r = rc
				break
			}
		}
	} else {
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return err
		}
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			if path.Base(h.Name) == name && h.Typeflag == tar.TypeReg {
				r = tr
				break
			}
		}
	}
	if r == nil {
		return fmt.Errorf("archive has no %s", name)
	}
	tmp := out + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, out)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// installTool installs one pinned tool into dir.
func installTool(c *http.Client, t tool, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if t.asset == nil {
		return run([]string{"CGO_ENABLED=0", "GOBIN=" + dir}, "go", "install", t.pkg)
	}
	key := runtime.GOOS + "/" + runtime.GOARCH
	sum, ok := t.sha256[key]
	if !ok {
		return fmt.Errorf("%s: no pinned release asset for %s", t.name, key)
	}
	url := t.asset(runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(os.Stderr, "+ download %s\n", url)
	data, err := fetchVerified(c, url, sum, 5*time.Second)
	if err != nil {
		return err
	}
	return extractBinary(data, exe(t.name), strings.HasSuffix(url, ".zip"), filepath.Join(dir, exe(t.name)))
}

func checkTools(args []string) error {
	fs := newFlags("check-tools")
	only := fs.String("only", "", "comma-separated tools to check (default all)")
	_ = fs.Parse(args) // ExitOnError: never returns an error
	sel, err := selectTools(*only)
	if err != nil {
		return err
	}
	var errs []error
	if err := checkGo(); err != nil {
		errs = append(errs, err)
	} else {
		fmt.Println("ok  go", goVersion)
	}
	if _, err := exec.LookPath("git"); err != nil {
		errs = append(errs, errors.New("git not found"))
	} else {
		fmt.Println("ok  git")
	}
	for _, t := range sel {
		if p, err := find(t); err != nil {
			errs = append(errs, err)
		} else {
			fmt.Printf("ok  %s %s (%s)\n", t.name, t.version, p)
		}
	}
	return errors.Join(errs...)
}

func installTools(args []string) error {
	fs := newFlags("install-tools")
	dry := fs.Bool("dry-run", false, "print what would be installed and stop")
	only := fs.String("only", "", "comma-separated tools to install (default all)")
	_ = fs.Parse(args) // ExitOnError: never returns an error
	if err := checkGo(); err != nil {
		return fmt.Errorf("%w; run scripts/setup.sh or scripts/setup.ps1 first", err)
	}
	sel, err := selectTools(*only)
	if err != nil {
		return err
	}
	var missing []tool
	for _, t := range sel {
		if _, err := find(t); err != nil {
			missing = append(missing, t)
		}
	}
	if len(missing) == 0 {
		fmt.Println("All pinned tools are installed.")
		return nil
	}
	dir := gobin()
	fmt.Printf("Will install into %s:\n", dir)
	for _, t := range missing {
		if t.asset != nil {
			fmt.Printf("  %s %s (release download, SHA-256 checked)\n", t.name, t.version)
		} else {
			fmt.Printf("  %s %s (go install %s)\n", t.name, t.version, t.pkg)
		}
	}
	if *dry {
		return nil
	}
	c := &http.Client{Timeout: 5 * time.Minute}
	for _, t := range missing {
		if err := installTool(c, t, dir); err != nil {
			return err
		}
	}
	return checkTools([]string{"-only", *only})
}
