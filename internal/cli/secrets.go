package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"runtime"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	qrvt "github.com/platten/qrvideotransfer"
	"github.com/platten/qrvideotransfer/internal/cli/ui"
)

// Key-file prefixes (SECURITY.md section 6).
const (
	publicPrefix  = "qrvt-public-key-v1:"
	privatePrefix = "qrvt-private-key-v1:"
	maxSecretFile = 4096 // bytes read from a key or passphrase file
)

// hidden is said instead of echoing a value that names no file: it may be a mistyped secret.
const hidden = "(the value is not shown, in case it is a secret)"

// readPassphraseFile returns the first line of a passphrase file, without its line ending or a
// leading byte order mark. The line must be shorter than maxSecretFile bytes and hold no control
// characters. No error shows the path: a mistyped value may be the passphrase itself.
func readPassphraseFile(path string) (string, error) {
	fail := func(reason string) error { return usagef("--passphrase-file: %s %s", reason, hidden) }
	f, err := os.Open(path) //nolint:gosec // the user names the file
	if errors.Is(err, fs.ErrNotExist) {
		return "", fail("no such file")
	}
	if err != nil {
		return "", fail(pathError(err).Error())
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxSecretFile+1))
	if err != nil {
		return "", fail(pathError(err).Error())
	}
	first, _, _ := bytes.Cut(b, []byte("\n"))
	if len(first) >= maxSecretFile {
		return "", fail(fmt.Sprintf("the first line is longer than %d bytes", maxSecretFile-1))
	}
	line := strings.TrimPrefix(strings.TrimSuffix(string(first), "\r"), "\ufeff")
	if !utf8.ValidString(line) || strings.IndexFunc(line, unicode.IsControl) >= 0 {
		return "", fail("the first line holds control characters or is not UTF-8 text")
	}
	if line == "" {
		return "", fail("the first line is empty")
	}
	return line, nil
}

// promptNew asks for a new passphrase twice on the terminal.
func (a *app) promptNew() (string, error) {
	if !a.terminal(a.env.In) {
		return "", usagef("--encrypt needs a passphrase: run qrvt in a terminal to be asked, or use --passphrase-file")
	}
	p, err := a.env.ReadPassword("Passphrase: ")
	if err != nil {
		return "", fmt.Errorf("reading the passphrase: %w", err)
	}
	if p == "" {
		return "", errors.New("the passphrase is empty")
	}
	again, err := a.env.ReadPassword("Repeat the passphrase: ")
	if err != nil {
		return "", fmt.Errorf("reading the passphrase: %w", err)
	}
	if again != p {
		return "", errors.New("the passphrases do not match")
	}
	return p, nil
}

// decodePassphrase returns the Passphrase callback of a decode. A passphrase from a file is read
// already; otherwise the user is asked once, when the transfer turns out to be encrypted, with the
// renderer paused.
func (a *app) decodePassphrase(fromFile string, r func() ui.Renderer) func() (string, error) {
	var (
		once sync.Once
		p    string
		err  error
	)
	return func() (string, error) {
		once.Do(func() {
			switch {
			case fromFile != "":
				p = fromFile
			case !a.terminal(a.env.In):
				err = errors.New("the transfer is encrypted: give --passphrase-file, or run qrvt in a terminal to be asked")
			default:
				err = r().Pause(func() error {
					var e error
					p, e = a.env.ReadPassword("Passphrase: ")
					return e
				})
			}
		})
		return p, err
	}
}

// readKeyFile reads a key file of at most maxSecretFile bytes. Errors begin with label, which
// names the flag and, when it may be shown, the path.
func readKeyFile(label, path string) ([]byte, os.FileInfo, error) {
	f, err := os.Open(path) //nolint:gosec // the user names the file
	if err != nil {
		return nil, nil, usagef("%s: %v", label, pathError(err))
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, nil, usagef("%s: %v", label, pathError(err))
	}
	if st.IsDir() {
		return nil, nil, usagef("%s: is a folder", label)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxSecretFile))
	if err != nil {
		return nil, nil, usagef("%s: %v", label, pathError(err))
	}
	return b, st, nil
}

// keyBytes decodes a key line with the given prefix to its 32 bytes.
func keyBytes(line, prefix string) ([]byte, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), prefix)
	if !ok {
		return nil, false
	}
	b, err := base64.StdEncoding.DecodeString(rest)
	return b, err == nil && len(b) == 32
}

// readSignKey reads --sign-key and returns its key line, trimmed. It warns, through the renderer,
// when the file is readable by others (SECURITY.md T10).
func (a *app) readSignKey(path string) ([]byte, error) {
	b, st, err := readKeyFile("--sign-key "+path, path)
	if err != nil {
		return nil, err
	}
	line := strings.TrimSpace(string(b))
	if _, ok := keyBytes(line, privatePrefix); !ok {
		return nil, usagef("--sign-key %s: not a qrvt private key", path)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		a.warnings = append(a.warnings, qrvt.Warning{Code: warnReadableKey,
			Text: fmt.Sprintf("The private key %s is readable by others; make it owner-only (chmod 600 %s).", path, path)})
	}
	return []byte(line), nil
}

// warnReadableKey is the code of the warning for a private key file readable by others.
const warnReadableKey qrvt.WarningCode = "ReadableKey"

// readVerifyKey reads a --verify-key value, a key line or the path of a public key file, and
// returns the key line, trimmed. No error shows the value: a mistyped value may be a secret.
func readVerifyKey(v string) ([]byte, error) {
	const private = "this is a private key; give the public key"
	t := strings.TrimSpace(v)
	switch {
	case strings.HasPrefix(t, privatePrefix):
		return nil, usagef("--verify-key: %s", private)
	case strings.HasPrefix(t, publicPrefix):
		if _, ok := keyBytes(t, publicPrefix); !ok {
			return nil, usagef("--verify-key: not a valid qrvt public key %s", hidden)
		}
		return []byte(t), nil
	}
	if _, err := os.Stat(v); errors.Is(err, fs.ErrNotExist) {
		return nil, usagef("--verify-key: no such file, and not a qrvt public key %s", hidden)
	}
	b, _, err := readKeyFile("--verify-key", v)
	if err != nil {
		return nil, fmt.Errorf("%w %s", err, hidden)
	}
	line := strings.TrimSpace(string(b))
	if strings.HasPrefix(line, privatePrefix) {
		return nil, usagef("--verify-key: %s", private)
	}
	if _, ok := keyBytes(line, publicPrefix); !ok {
		return nil, usagef("--verify-key: not a qrvt public key %s", hidden)
	}
	return []byte(line), nil
}

// fingerprint is the first 16 hexadecimal digits of the SHA-256 of a raw 32-byte public key, as
// DecodeReport.SignerKey holds it.
func fingerprint(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:8])
}
