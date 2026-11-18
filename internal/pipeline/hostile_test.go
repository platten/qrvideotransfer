package pipeline

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/platten/qrvideotransfer/internal/payload"
	"github.com/platten/qrvideotransfer/internal/prng"
	"github.com/platten/qrvideotransfer/internal/video"
)

// encodeStream writes stream, any bytes, as a code video exactly as Encode writes a packed stream,
// its descriptor carrying the stream's true length and hash, as a sender who altered a stream and
// re-encoded it would; it returns the video's path.
func encodeStream(t *testing.T, stream []byte) string {
	t.Helper()
	tmp := t.TempDir()
	spool := filepath.Join(tmp, "stream")
	if err := os.WriteFile(spool, stream, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(spool)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	c := profile480(t.TempDir())
	c.Output, c.Backend, c.TempDir = filepath.Join(t.TempDir(), "x.raw"), video.NewRaw(), tmp
	encodes++
	c.Random = &prngReader{prng.SplitMix64{State: 0xf11b + encodes}}
	packed := &payload.PackResult{StreamLen: int64(len(stream)), StreamHash: sha256.Sum256(stream)}
	e, err := newEncoding(&c, packed)
	if err != nil {
		t.Fatal(err)
	}
	parity, err := os.Create(filepath.Join(tmp, "parity"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parity.Close() }()
	if err := e.protect(context.Background(), nil, f, parity); err != nil {
		t.Fatal(err)
	}
	if _, err := e.writeVideo(context.Background(), nil, f, parity, tmp); err != nil {
		t.Fatal(err)
	}
	return c.Output
}

// signedStream packs a small folder signed with key and encrypted with "pw".
func signedStream(t *testing.T, key ed25519.PrivateKey) []byte {
	t.Helper()
	in, _ := writeInputs(t, 40, 2000)
	path := filepath.Join(t.TempDir(), "stream")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := payload.Pack(context.Background(), f, payload.PackOptions{Inputs: []string{in}, SignKey: key,
		Compression: payload.CompressNone, Passphrase: func() (string, error) { return "pw", nil }}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestTamperedStreamEndToEnd (SECURITY.md section 9.4, through the whole decode): a signed and
// encrypted stream with one bit flipped, re-encoded as a video whose descriptor matches it, is
// refused with the named error, with or without a trusted key, before the passphrase is asked
// for, and nothing is written; the stream as signed decodes. The payload's tests flip every bit;
// here one bit of each field.
func TestTamperedStreamEndToEnd(t *testing.T) {
	_, key, err := payload.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	pub := key.Public().(ed25519.PublicKey)
	stream := signedStream(t, key)
	body := len(stream) - payload.SignatureSize
	var version *payload.UnsupportedVersionError
	cases := []struct {
		name string
		off  int
		bit  byte
		ok   func(error) bool
	}{
		{"none", -1, 0, func(err error) bool { return err == nil }},
		{"header version", 4, 1, func(err error) bool { return errors.As(err, &version) }},
		{"header encrypted flag", 6, 1, func(err error) bool { return errors.Is(err, payload.ErrBadSignature) }},
		{"header signed flag", 6, 2, func(err error) bool { return errors.Is(err, payload.ErrFormat) }},
		{"header reserved byte", 7, 0x80, func(err error) bool { return errors.Is(err, payload.ErrFormat) }},
		{"body", payload.HeaderSize + 10, 0x04, func(err error) bool { return errors.Is(err, payload.ErrBadSignature) }},
		{"public key", body + 3, 0x01, func(err error) bool { return errors.Is(err, payload.ErrBadSignature) }},
		{"signature", body + ed25519.PublicKeySize + 40, 0x20, func(err error) bool { return errors.Is(err, payload.ErrBadSignature) }},
	}
	for _, c := range cases {
		for _, keys := range [][]ed25519.PublicKey{nil, {pub}} {
			s := bytes.Clone(stream)
			if c.off >= 0 {
				s[c.off] ^= c.bit
			}
			asked := 0
			out := filepath.Join(t.TempDir(), "out")
			_, err := Decode(context.Background(), DecodeConfig{Recordings: []string{encodeStream(t, s)}, Backend: video.NewRaw(),
				TempDir: t.TempDir(), Unpack: payload.UnpackOptions{OutputDir: out, VerifyKeys: keys,
					Passphrase: func() (string, error) { asked++; return "pw", nil }}})
			if !c.ok(err) {
				t.Errorf("%s (%d trusted keys): %v", c.name, len(keys), err)
			}
			if c.off < 0 {
				continue
			}
			if asked != 0 {
				t.Errorf("%s: the passphrase was asked for", c.name)
			}
			if fs := filesUnder(t, out); len(fs) != 0 {
				t.Errorf("%s: files written: %v", c.name, fs)
			}
		}
	}
}

// TestInterleavedSessions (SECURITY.md sections 9.2 and T2): a recording that alternates frame by
// frame between two transfers locks onto the one it shows first, decodes it, and ignores the other;
// either order.
func TestInterleavedSessions(t *testing.T) {
	inA, wantA := writeInputs(t, 41, 3000)
	inB, wantB := writeInputs(t, 42, 3000)
	pa, _ := encodeRaw(t, profile480(inA))
	pb, _ := encodeRaw(t, profile480(inB))
	fa, info := readFrames(t, video.NewRaw(), pa)
	fb, _ := readFrames(t, video.NewRaw(), pb)
	for _, c := range []struct {
		first, second []*video.Frame
		want          map[string][32]byte
	}{{fa, fb, wantA}, {fb, fa, wantB}} {
		var mixed []*video.Frame
		for i := range c.first {
			mixed = append(mixed, c.first[i])
			if i < len(c.second) {
				mixed = append(mixed, c.second[i])
			}
		}
		out, _, err := decodeTo(t, writeRaw(t, mixed, info.FPSNum/info.FPSDen))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		checkOutput(t, out, c.want)
	}
}
