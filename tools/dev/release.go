package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var releaseTagsLine = regexp.MustCompile(`^\s*-\s*RELEASE_TAGS=(.*)$`)

// releaseTagsFrom returns the release build's tag set from the text of .goreleaser.yaml, where it
// is defined exactly once as `- RELEASE_TAGS=<comma-separated tags>`.
func releaseTagsFrom(b []byte) (string, error) {
	var found []string
	for _, l := range lines(b) {
		if m := releaseTagsLine.FindStringSubmatch(l); m != nil {
			found = append(found, strings.TrimSpace(m[1]))
		}
	}
	if len(found) != 1 {
		return "", fmt.Errorf(".goreleaser.yaml defines RELEASE_TAGS %d times, want once", len(found))
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_.,]*$`).MatchString(found[0]) {
		return "", fmt.Errorf("RELEASE_TAGS=%q is not a comma-separated list of build tags", found[0])
	}
	return found[0], nil
}

// releaseTags returns the release build's tag set from the repository's .goreleaser.yaml.
func releaseTags() (string, error) {
	b, err := os.ReadFile(".goreleaser.yaml")
	if err != nil {
		return "", err
	}
	return releaseTagsFrom(b)
}

// releaseSnapshot runs the whole release locally without publishing anything, then checks that
// every target and package format was produced.
func releaseSnapshot(args []string) error {
	_ = newFlags("release-snapshot").Parse(args) // ExitOnError: never returns an error
	gr, err := find(toolNamed("goreleaser"))
	if err != nil {
		return err
	}
	syft, err := find(toolNamed("syft"))
	if err != nil {
		return err
	}
	// GoReleaser runs syft from PATH.
	env := []string{"PATH=" + filepath.Dir(syft) + string(os.PathListSeparator) + os.Getenv("PATH")}
	if err := run(env, gr, "check"); err != nil {
		return err
	}
	// Signing needs the release workflow's OIDC identity, so the snapshot skips it.
	if err := run(env, gr, "release", "--snapshot", "--clean", "--skip=sign"); err != nil {
		return err
	}
	if err := checkDist("dist", checkStatic); err != nil {
		return err
	}
	fmt.Println("release snapshot: all six targets, deb, rpm, apk, SBOMs, checksums, Homebrew cask, Scoop and winget manifests")
	return nil
}

// distArtifact is the part of an entry of GoReleaser's dist/artifacts.json that checkDist reads.
type distArtifact struct {
	Name   string         `json:"name"`
	Path   string         `json:"path"`
	Goos   string         `json:"goos"`
	Goarch string         `json:"goarch"`
	Type   string         `json:"type"`
	Extra  map[string]any `json:"extra"`
}

// archiveMembers returns the base names of the regular files in a tar.gz or zip archive.
func archiveMembers(p string, zipped bool) ([]string, error) {
	var names []string
	if zipped {
		zr, err := zip.OpenReader(p)
		if err != nil {
			return nil, err
		}
		defer func() { _ = zr.Close() }()
		for _, f := range zr.File {
			if !f.FileInfo().IsDir() {
				names = append(names, path.Base(f.Name))
			}
		}
		return names, nil
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return names, nil
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg {
			names = append(names, path.Base(h.Name))
		}
	}
}

func fileSHA256(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// checkDist checks a GoReleaser dist directory: a static binary and an archive (zip on Windows,
// tar.gz elsewhere, holding the binary, LICENSE, NOTICE and README.md) for each of the six
// targets; deb, rpm and apk packages for both Linux targets; an SBOM per archive; a checksum file
// that covers them; the Homebrew cask and the Scoop and winget manifests.
func checkDist(dir string, checkBin func(path, goos string) error) error {
	b, err := os.ReadFile(filepath.Join(dir, "artifacts.json"))
	if err != nil {
		return err
	}
	var arts []distArtifact
	if err := json.Unmarshal(b, &arts); err != nil {
		return fmt.Errorf("artifacts.json: %w", err)
	}
	root := filepath.Dir(dir)
	resolve := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(root, filepath.FromSlash(p))
	}
	var errs []error
	fail := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	byType := map[string][]distArtifact{}
	for _, a := range arts {
		if _, err := os.Stat(resolve(a.Path)); err != nil {
			fail("%s %s: %v", a.Type, a.Name, err)
			continue
		}
		byType[a.Type] = append(byType[a.Type], a)
	}
	pick := func(typ, goos, goarch string, extra func(distArtifact) bool) []distArtifact {
		var out []distArtifact
		for _, a := range byType[typ] {
			if a.Goos == goos && a.Goarch == goarch && (extra == nil || extra(a)) {
				out = append(out, a)
			}
		}
		return out
	}
	for _, t := range targets {
		goos, goarch := t[0], t[1]
		bin := "qrvt"
		if goos == "windows" {
			bin += ".exe"
		}
		bins := pick("Binary", goos, goarch, nil)
		if len(bins) == 0 {
			fail("no binary for %s/%s", goos, goarch)
		}
		for _, a := range bins {
			if err := checkBin(resolve(a.Path), goos); err != nil {
				fail("binary for %s/%s: %v", goos, goarch, err)
			}
		}
		archives := pick("Archive", goos, goarch, nil)
		if len(archives) == 0 {
			fail("no archive for %s/%s", goos, goarch)
		}
		for _, a := range archives {
			want := archive(goos)
			if got, _ := a.Extra["Format"].(string); got != want {
				fail("%s/%s archive is %s, want %s", goos, goarch, got, want)
				continue
			}
			members, err := archiveMembers(resolve(a.Path), want == "zip")
			if err != nil {
				fail("%s: %v", a.Name, err)
				continue
			}
			for _, m := range []string{bin, "LICENSE", "NOTICE", "README.md"} {
				if !slices.Contains(members, m) {
					fail("%s lacks %s", a.Name, m)
				}
			}
		}
		if goos == "linux" {
			for _, f := range []string{"deb", "rpm", "apk"} {
				if len(pick("Linux Package", goos, goarch, func(a distArtifact) bool { return a.Extra["Format"] == f })) == 0 {
					fail("no %s package for %s/%s", f, goos, goarch)
				}
			}
		}
	}
	if s, a := len(byType["SBOM"]), len(byType["Archive"]); s != a {
		fail("%d SBOMs for %d archives", s, a)
	}
	for _, typ := range []string{"Checksum", "Homebrew Cask", "Scoop Manifest", "Winget Manifest"} {
		if len(byType[typ]) == 0 {
			fail("no %s", typ)
		}
	}
	if sums := byType["Checksum"]; len(sums) > 0 {
		errs = append(errs, checkSums(resolve(sums[0].Path), slices.Concat(byType["Archive"], byType["Linux Package"], byType["SBOM"]), resolve)...)
	}
	return errors.Join(errs...)
}

// checkSums checks that a checksum file lists each artifact with its current SHA-256.
func checkSums(file string, arts []distArtifact, resolve func(string) string) []error {
	f, err := os.Open(file)
	if err != nil {
		return []error{err}
	}
	defer func() { _ = f.Close() }()
	listed := map[string]string{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		if fl := strings.Fields(s.Text()); len(fl) == 2 {
			listed[fl[1]] = fl[0]
		}
	}
	var errs []error
	for _, a := range arts {
		got, err := fileSHA256(resolve(a.Path))
		switch want, ok := listed[a.Name]; {
		case err != nil:
			errs = append(errs, err)
		case !ok:
			errs = append(errs, fmt.Errorf("checksum file does not list %s", a.Name))
		case got != want:
			errs = append(errs, fmt.Errorf("checksum of %s is %s, file lists %s", a.Name, got, want))
		}
	}
	return errs
}

// osslsigncodeArgs returns the arguments that Authenticode-sign in with the PKCS#12 certificate
// pfx (password in the file pass) and an RFC 3161 timestamp from ts, writing out.
func osslsigncodeArgs(pfx, pass, ts, in, out string) []string {
	return []string{"sign", "-pkcs12", pfx, "-readpass", pass, "-n", "qrvt", "-i", "https://github.com/platten/qrvideotransfer",
		"-h", "sha256", "-ts", ts, "-in", in, "-out", out}
}

// signWindows is the sign-windows task, run by GoReleaser after each Windows build. It signs the
// binary in place with osslsigncode when WINDOWS_SIGN_PFX_BASE64 (a base64 PKCS#12 certificate)
// is set, with WINDOWS_SIGN_PASSWORD and optionally WINDOWS_SIGN_TIMESTAMP_URL; otherwise it does
// nothing.
func signWindows(args []string) error {
	fs := newFlags("sign-windows")
	_ = fs.Parse(args) // ExitOnError: never returns an error
	if fs.NArg() != 1 {
		return errors.New("usage: sign-windows <binary.exe>")
	}
	bin := fs.Arg(0)
	enc := os.Getenv("WINDOWS_SIGN_PFX_BASE64")
	if enc == "" {
		fmt.Println("sign-windows: no certificate configured; not signing", bin)
		return nil
	}
	pfx, err := base64.StdEncoding.DecodeString(strings.TrimSpace(enc))
	if err != nil {
		return fmt.Errorf("WINDOWS_SIGN_PFX_BASE64: %w", err)
	}
	tool, err := exec.LookPath("osslsigncode")
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "qrvt-sign-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	cert, pass := filepath.Join(tmp, "cert.pfx"), filepath.Join(tmp, "pass")
	if err := os.WriteFile(cert, pfx, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(pass, []byte(os.Getenv("WINDOWS_SIGN_PASSWORD")), 0o600); err != nil {
		return err
	}
	ts := os.Getenv("WINDOWS_SIGN_TIMESTAMP_URL")
	if ts == "" {
		ts = "http://timestamp.digicert.com"
	}
	signed := filepath.Join(tmp, filepath.Base(bin))
	if err := run(nil, tool, osslsigncodeArgs(cert, pass, ts, bin, signed)...); err != nil {
		return err
	}
	b, err := os.ReadFile(signed)
	if err != nil {
		return err
	}
	return os.WriteFile(bin, b, 0o755)
}
