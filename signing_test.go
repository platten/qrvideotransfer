package qrvt

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/platten/qrvideotransfer/internal/payload"
)

// TestGenerateKey: a new key pair in qrvt's key-file encoding, which the payload's parsers read
// back as a matching Ed25519 pair; two calls give different keys.
func TestGenerateKey(t *testing.T) {
	pub, priv, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	pk, err := payload.ParsePublicKey(pub)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	sk, err := payload.ParsePrivateKey(priv)
	if err != nil {
		t.Fatalf("private key: %v", err)
	}
	if !bytes.Equal(sk.Public().(ed25519.PublicKey), pk) {
		t.Error("the private key's public half differs from the public key")
	}
	pub2, _, err := GenerateKey()
	if err != nil || bytes.Equal(pub, pub2) {
		t.Errorf("a second key pair: %v, equal %v", err, bytes.Equal(pub, pub2))
	}
}

// TestSigningEndToEnd: files encoded with a generated key and decoded with its public key verify
// and name the signer; with another trusted key, or unsigned with any trusted key, the decode is
// refused before the passphrase is asked for, and nothing is written (SECURITY.md sections 6 and
// 9.4).
func TestSigningEndToEnd(t *testing.T) {
	in, want := payloadDir(t, 30, 4000, 300)
	pub, priv, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	asked := 0
	pass := func() (string, error) { asked++; return "a passphrase", nil }
	signed, _ := encodeTest(t, EncodeOptions{Inputs: []string{in}, SignKey: priv, Passphrase: pass})
	unsigned, _ := encodeTest(t, EncodeOptions{Inputs: []string{in}, Passphrase: pass})
	asked = 0

	out, rep, err := decodeTest(t, DecodeOptions{Recordings: []string{signed}, VerifyKeys: [][]byte{other, pub}, Passphrase: pass})
	if err != nil {
		t.Fatalf("signed, verified: %v", err)
	}
	sameFiles(t, out, want)
	pk, _ := payload.ParsePublicKey(pub)
	if !rep.Signed || !rep.Encrypted || !bytes.Equal(rep.SignerKey, pk) || asked != 1 {
		t.Errorf("report signed %v encrypted %v signer %x, passphrase asked %d times", rep.Signed, rep.Encrypted, rep.SignerKey, asked)
	}

	for name, c := range map[string]struct {
		rec  string
		keys [][]byte
	}{
		"signed by another key": {signed, [][]byte{other}},
		"unsigned":              {unsigned, [][]byte{pub}},
	} {
		asked = 0
		out, _, err := decodeTest(t, DecodeOptions{Recordings: []string{c.rec}, VerifyKeys: c.keys, Passphrase: pass})
		if !errors.Is(err, ErrMissingSignature) {
			t.Errorf("%s: %v, want ErrMissingSignature", name, err)
		}
		if asked != 0 {
			t.Errorf("%s: the passphrase was asked for %d times before verification failed", name, asked)
		}
		sameFiles(t, out, nil)
	}
}
