package payload

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testPriv = "qrvt-private-key-v1:AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=" //nolint:gosec // the fixed, published test key of the golden vectors
	testPub  = "qrvt-public-key-v1:A6EHv/POEL4dcN0Y50vAmWfk1jCbpQ1fHdyGZBJVMbg="
	testFP   = "56475aa75463474c"
)

// testKey is the fixed key of the golden vectors: the seed is the bytes 0 to 31.
func testKey() ed25519.PrivateKey {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	return ed25519.NewKeyFromSeed(seed)
}

func TestKeyEncoding(t *testing.T) {
	k := testKey()
	pub := k.Public().(ed25519.PublicKey)
	if got := string(MarshalPrivateKey(k)); got != testPriv+"\n" {
		t.Fatalf("MarshalPrivateKey = %q", got)
	}
	if got := string(MarshalPublicKey(pub)); got != testPub+"\n" {
		t.Fatalf("MarshalPublicKey = %q", got)
	}
	if got := Fingerprint(pub); got != testFP {
		t.Fatalf("Fingerprint = %s", got)
	}
	for _, s := range []string{testPriv, testPriv + "\n", testPriv + "\r\n"} {
		p, err := ParsePrivateKey([]byte(s))
		if err != nil || !p.Equal(k) {
			t.Fatalf("ParsePrivateKey(%q) = %v", s, err)
		}
	}
	for _, s := range []string{testPub, testPub + "\n", testPub + "\r\n"} {
		p, err := ParsePublicKey([]byte(s))
		if err != nil || !p.Equal(pub) {
			t.Fatalf("ParsePublicKey(%q) = %v", s, err)
		}
	}
}

func TestParseKeyRejects(t *testing.T) {
	bad := []string{
		"", "\n", testPub, // a public key is not a private key
		strings.Replace(testPriv, "v1", "v2", 1),
		" " + testPriv, testPriv + " ", testPriv + "\n\n", testPriv + "\nx",
		strings.TrimSuffix(testPriv, "="), // padding required
		strings.Replace(testPriv, "AAEC", "AAE!", 1),
		"qrvt-private-key-v1:AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHg==", // 31 bytes
		"qrvt-private-key-v1:AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8gIQ==",
		"qrvt-private-key-v1:AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh9=", // non-canonical padding bits
		"qrvt-private-key-v1:AAECAwQFBgcICQoLDA0O\nDxAREhMUFRYXGBkaGxwdHh8=",
	}
	for _, s := range bad {
		if _, err := ParsePrivateKey([]byte(s)); !errors.Is(err, ErrKeyFormat) {
			t.Errorf("ParsePrivateKey(%q) = %v, want ErrKeyFormat", s, err)
		}
	}
	for _, s := range []string{"", testPriv, strings.Replace(testPub, "public", "Public", 1), testPub[:len(testPub)-4]} {
		if _, err := ParsePublicKey([]byte(s)); !errors.Is(err, ErrKeyFormat) {
			t.Errorf("ParsePublicKey(%q) = %v, want ErrKeyFormat", s, err)
		}
	}
}

func TestGenerateKey(t *testing.T) {
	pub, priv, err := GenerateKey(rand.Reader)
	if err != nil || !priv.Public().(ed25519.PublicKey).Equal(pub) {
		t.Fatalf("GenerateKey: %v", err)
	}
	pub2, _, err := GenerateKey(nil) // nil: crypto/rand
	if err != nil || pub2.Equal(pub) {
		t.Fatalf("GenerateKey(nil): %v", err)
	}
	if _, _, err := GenerateKey(bytes.NewReader(nil)); err == nil {
		t.Fatal("GenerateKey from an empty reader succeeded")
	}
}

func TestKeyFiles(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "alice")
	pub, err := WriteKeyPair(name, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	priv, warns, err := ReadPrivateKeyFile(name + ".key")
	if err != nil || len(warns) != 0 || !priv.Public().(ed25519.PublicKey).Equal(pub) {
		t.Fatalf("ReadPrivateKeyFile: %v %v", warns, err)
	}
	p, err := ReadPublicKeyFile(name + ".pub")
	if err != nil || !p.Equal(pub) {
		t.Fatalf("ReadPublicKeyFile: %v", err)
	}
	if _, err := WriteKeyPair(name, rand.Reader); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("WriteKeyPair over an existing key: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "nope.pub")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("unexpected file")
	}
	if _, err := WriteKeyPair(filepath.Join(dir, "missing", "k"), rand.Reader); err == nil {
		t.Fatal("WriteKeyPair into a missing folder succeeded")
	}
	// A failure writing the public key removes the private key it just wrote.
	must(t, os.Mkdir(filepath.Join(dir, "bob.pub"), 0o700))
	if _, err := WriteKeyPair(filepath.Join(dir, "bob"), rand.Reader); err == nil {
		t.Fatal("WriteKeyPair succeeded with bob.pub in the way")
	}
	if _, err := os.Stat(filepath.Join(dir, "bob.key")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("bob.key left behind: %v", err)
	}
	if _, err := WriteKeyPair(filepath.Join(dir, "carol"), bytes.NewReader(nil)); err == nil {
		t.Fatal("WriteKeyPair with a failing random source succeeded")
	}
	if _, _, err := ReadPrivateKeyFile(filepath.Join(dir, "none.key")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing key: %v", err)
	}
	if _, err := ReadPublicKeyFile(filepath.Join(dir, "none.pub")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing key: %v", err)
	}
	big := filepath.Join(dir, "big.key")
	must(t, os.WriteFile(big, bytes.Repeat([]byte("a"), 1<<20), 0o600))
	if _, _, err := ReadPrivateKeyFile(big); !errors.Is(err, ErrKeyFormat) {
		t.Fatalf("1 MiB key file: %v", err)
	}
	if _, err := ReadPublicKeyFile(big); !errors.Is(err, ErrKeyFormat) {
		t.Fatalf("1 MiB key file: %v", err)
	}
}

func TestKeyFilePermissions(t *testing.T) {
	if isWindows() {
		t.Skip("Windows has no Unix permission bits; access to key files is governed by ACLs, which qrvt does not inspect")
	}
	dir := t.TempDir()
	name := filepath.Join(dir, "k")
	_, err := WriteKeyPair(name, rand.Reader)
	must(t, err)
	st, err := os.Stat(name + ".key")
	must(t, err)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode %v, want 0600", st.Mode().Perm())
	}
	st, err = os.Stat(name + ".pub")
	must(t, err)
	if st.Mode().Perm()&0o600 != 0o600 {
		t.Fatalf("public key mode %v", st.Mode().Perm())
	}
	for _, mode := range []os.FileMode{0o640, 0o604, 0o644} {
		must(t, os.Chmod(name+".key", mode))
		k, warns, err := ReadPrivateKeyFile(name + ".key")
		if err != nil || k == nil {
			t.Fatal(err)
		}
		if len(warns) != 1 || warns[0].Code != WarnKeyReadable || warns[0].Path != name+".key" {
			t.Fatalf("mode %v: warnings %+v", mode, warns)
		}
	}
}

func FuzzParseKey(f *testing.F) {
	f.Add([]byte(testPriv))
	f.Add([]byte(testPub + "\n"))
	f.Add([]byte("qrvt-private-key-v1:"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if k, err := ParsePrivateKey(b); err == nil {
			if len(k) != ed25519.PrivateKeySize || !bytes.Equal(bytes.TrimRight(MarshalPrivateKey(k), "\n"), bytes.TrimRight(b, "\r\n")) {
				t.Fatalf("accepted %q", b)
			}
		}
		if k, err := ParsePublicKey(b); err == nil {
			if len(k) != ed25519.PublicKeySize || !bytes.Equal(bytes.TrimRight(MarshalPublicKey(k), "\n"), bytes.TrimRight(b, "\r\n")) {
				t.Fatalf("accepted %q", b)
			}
		}
	})
}
