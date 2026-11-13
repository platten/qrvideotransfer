package cli

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/platten/qrvideotransfer/internal/cli/ui"
)

// Key-file prefixes (SECURITY.md section 6).
const (
	publicPrefix  = "qrvt-public-key-v1:"
	privatePrefix = "qrvt-private-key-v1:"
	maxSecretFile = 4096 // bytes read from a key or passphrase file
)

// readPassphraseFile returns the first line of a passphrase file, without its line ending.
func readPassphraseFile(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // the user names the file
	if err != nil {
		return "", usagef("--passphrase-file %s: %v", path, pathError(err))
	}
	defer func() { _ = f.Close() }()
	line, err := bufio.NewReader(io.LimitReader(f, maxSecretFile)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", usagef("--passphrase-file %s: %v", path, err)
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", usagef("--passphrase-file %s: the first line is empty", path)
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

// readKeyFile reads a key file of at most maxSecretFile bytes.
func readKeyFile(flag, path string) ([]byte, os.FileInfo, error) {
	f, err := os.Open(path) //nolint:gosec // the user names the file
	if err != nil {
		return nil, nil, usagef("%s %s: %v", flag, path, pathError(err))
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, nil, usagef("%s %s: %v", flag, path, pathError(err))
	}
	if st.IsDir() {
		return nil, nil, usagef("%s %s: is a folder", flag, path)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxSecretFile))
	if err != nil {
		return nil, nil, usagef("%s %s: %v", flag, path, pathError(err))
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

// readSignKey reads --sign-key and warns when the file is readable by others (SECURITY.md T10).
func (a *app) readSignKey(path string) ([]byte, error) {
	b, st, err := readKeyFile("--sign-key", path)
	if err != nil {
		return nil, err
	}
	if _, ok := keyBytes(string(b), privatePrefix); !ok {
		return nil, usagef("--sign-key %s: not a qrvt private key", path)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		_, _ = fmt.Fprintf(a.env.Err, "warning: the private key %s is readable by others; make it owner-only (chmod 600 %s)\n",
			ui.Clean(path), ui.Clean(path))
	}
	return b, nil
}

// readVerifyKey reads a --verify-key value: a key line, or the path of a public key file.
func readVerifyKey(v string) ([]byte, error) {
	if strings.HasPrefix(v, publicPrefix) {
		if _, ok := keyBytes(v, publicPrefix); !ok {
			return nil, usagef("--verify-key %q: not a qrvt public key", v)
		}
		return []byte(v), nil
	}
	b, _, err := readKeyFile("--verify-key", v)
	if err != nil {
		return nil, err
	}
	if _, ok := keyBytes(string(b), publicPrefix); !ok {
		return nil, usagef("--verify-key %s: not a qrvt public key", v)
	}
	return b, nil
}

// fingerprint is the first 16 hexadecimal digits of the SHA-256 of a 32-byte public key.
func fingerprint(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:8])
}
