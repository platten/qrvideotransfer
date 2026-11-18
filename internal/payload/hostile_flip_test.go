package payload

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"testing"
)

// TestEveryBitFlipRefused (SECURITY.md section 9.4): flipping any single bit of a signed and
// encrypted stream is refused through Unpack with a named error, before the passphrase is asked
// for, and nothing is written. A bit of the body, the public key or the signature fails the
// signature (ErrBadSignature), with or without a trusted key; a bit of the header fails as the
// header's own checks or the signature decide: a malformed header (ErrFormat), an unknown version
// or compression (UnsupportedVersionError), a length beyond a limit (LimitError), or, where the
// header still parses with the stream's length, the signature, which covers the header.
func TestEveryBitFlipRefused(t *testing.T) {
	stream := signedStream(t, true)
	pub := testKey().Public().(ed25519.PublicKey)
	bodyEnd := len(stream) - SignatureSize
	out := t.TempDir()
	asked := 0
	pass := func() (string, error) { asked++; return "pw", nil }
	for bit := range len(stream) * 8 {
		s := bytes.Clone(stream)
		s[bit/8] ^= 1 << (bit % 8)
		keys := []ed25519.PublicKey(nil)
		if bit%2 == 1 {
			keys = []ed25519.PublicKey{pub}
		}
		_, err := unpack(s, UnpackOptions{OutputDir: out, Passphrase: pass, VerifyKeys: keys})
		var (
			version *UnsupportedVersionError
			limit   *LimitError
		)
		switch off := bit / 8; {
		case err == nil:
			t.Fatalf("bit %d (byte %d) flipped: unpacked", bit, off)
		case off >= HeaderSize && off < bodyEnd+SignatureSize:
			if !errors.Is(err, ErrBadSignature) {
				t.Fatalf("bit %d (byte %d of the body, key or signature): %v, want ErrBadSignature", bit, off, err)
			}
		case !errors.Is(err, ErrFormat) && !errors.As(err, &version) && !errors.As(err, &limit) && !errors.Is(err, ErrBadSignature):
			t.Fatalf("bit %d (header byte %d): %v (%T), not a named refusal", bit, off, err, err)
		}
		if asked != 0 {
			t.Fatalf("bit %d flipped: the passphrase was asked for", bit)
		}
		expectEmpty(t, out)
	}
}
