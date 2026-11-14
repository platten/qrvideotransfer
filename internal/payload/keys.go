package payload

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
)

// Key file prefixes (SECURITY.md section 6).
const (
	privatePrefix = "qrvt-private-key-v1:"
	publicPrefix  = "qrvt-public-key-v1:"
	maxKeyFile    = 4096 // key files are one short line; anything larger is not a key
)

// GenerateKey returns a new Ed25519 key pair drawn from rand, or from crypto/rand when rand is nil.
func GenerateKey(rand io.Reader) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand)
}

// MarshalPrivateKey returns the private key file contents: `qrvt-private-key-v1:<base64 seed>` and a
// newline.
func MarshalPrivateKey(k ed25519.PrivateKey) []byte {
	return []byte(privatePrefix + base64.StdEncoding.EncodeToString(k.Seed()) + "\n")
}

// MarshalPublicKey returns the public key file contents: `qrvt-public-key-v1:<base64 key>` and a
// newline.
func MarshalPublicKey(k ed25519.PublicKey) []byte {
	return []byte(publicPrefix + base64.StdEncoding.EncodeToString(k) + "\n")
}

// ParsePrivateKey decodes a private key file. One trailing "\n" or "\r\n" is allowed; anything else
// that is not the exact form gives [ErrKeyFormat].
func ParsePrivateKey(b []byte) (ed25519.PrivateKey, error) {
	seed, err := parseKey(b, privatePrefix)
	if err != nil {
		return nil, err
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// ParsePublicKey decodes a public key file, with the same rules as [ParsePrivateKey].
func ParsePublicKey(b []byte) (ed25519.PublicKey, error) {
	k, err := parseKey(b, publicPrefix)
	return ed25519.PublicKey(k), err
}

// parseKey returns the 32 bytes encoded after prefix.
func parseKey(b []byte, prefix string) ([]byte, error) {
	if len(b) > maxKeyFile {
		return nil, fmt.Errorf("%w: file too large", ErrKeyFormat)
	}
	if rest, ok := bytes.CutSuffix(b, []byte("\n")); ok {
		b = bytes.TrimSuffix(rest, []byte("\r")) // LF or CRLF; a lone CR stays and is refused below
	}
	rest, ok := bytes.CutPrefix(b, []byte(prefix))
	if !ok {
		return nil, fmt.Errorf("%w: want a line starting %q", ErrKeyFormat, prefix)
	}
	k, err := base64.StdEncoding.Strict().DecodeString(string(rest))
	// The decoder skips '\r' and '\n'; re-encoding catches them and any other non-canonical form.
	if err != nil || len(k) != 32 || base64.StdEncoding.EncodeToString(k) != string(rest) {
		return nil, fmt.Errorf("%w: bad key data", ErrKeyFormat)
	}
	return k, nil
}

// Fingerprint returns the first 16 hexadecimal digits of the SHA-256 of a public key.
func Fingerprint(k ed25519.PublicKey) string {
	h := sha256.Sum256(k)
	return hex.EncodeToString(h[:8])
}

// WriteKeyPair generates a key pair from rand (crypto/rand when nil) and writes name.key, readable by
// its owner only, and name.pub. It never replaces an existing file, and removes name.key again if
// name.pub cannot be written.
func WriteKeyPair(name string, rand io.Reader) (ed25519.PublicKey, error) {
	pub, priv, err := GenerateKey(rand)
	if err != nil {
		return nil, err
	}
	if err := writeNew(name+".key", MarshalPrivateKey(priv), 0o600); err != nil {
		return nil, err
	}
	if err := writeNew(name+".pub", MarshalPublicKey(pub), 0o644); err != nil {
		return nil, errors.Join(err, os.Remove(name+".key"))
	}
	return pub, nil
}

// writeNew creates a file that must not exist yet.
func writeNew(name string, b []byte, perm os.FileMode) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm) //nolint:gosec // the user names the key file
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	return errors.Join(err, f.Close())
}

// ReadPrivateKeyFile reads a private key file. It returns a [WarnKeyReadable] warning when the file
// is readable or writable by anyone other than its owner. Windows has no such permission bits; the
// check is skipped there.
func ReadPrivateKeyFile(path string) (ed25519.PrivateKey, []Warning, error) {
	b, st, err := readKeyFile(path)
	if err != nil {
		return nil, nil, err
	}
	k, err := ParsePrivateKey(b)
	if err != nil {
		return nil, nil, err
	}
	var warns []Warning
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		warns = append(warns, Warning{WarnKeyReadable, path,
			fmt.Sprintf("the private key file has mode %v; it should be readable by its owner only (chmod 600)", st.Mode().Perm())})
	}
	return k, warns, nil
}

// ReadPublicKeyFile reads a public key file.
func ReadPublicKeyFile(path string) (ed25519.PublicKey, error) {
	b, _, err := readKeyFile(path)
	if err != nil {
		return nil, err
	}
	return ParsePublicKey(b)
}

// readKeyFile reads at most maxKeyFile+1 bytes of a key file.
func readKeyFile(path string) ([]byte, os.FileInfo, error) {
	f, err := os.Open(path) //nolint:gosec // the user names the key file
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()
	st, serr := f.Stat()
	b, err := io.ReadAll(io.LimitReader(f, maxKeyFile+1))
	return b, st, errors.Join(serr, err)
}
