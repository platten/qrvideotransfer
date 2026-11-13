package payload

import (
	"archive/tar"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"

	"filippo.io/age"
)

// Verified is the outcome of [Verify].
type Verified struct {
	Header  Header
	Signer  ed25519.PublicKey // the signer's key when the stream is signed and the signature is valid
	Trusted bool              // the signer is one of the trusted keys
}

// Verify checks a stream's length, header and signature (FORMAT.md section 12, order of checks; the
// caller has already checked the descriptor's stream hash). A signed stream must carry a valid
// signature ([ErrBadSignature]). When trusted is not empty, the stream must be signed by one of those
// keys ([ErrMissingSignature]); there is no mode that accepts a missing signature when keys are given.
func Verify(ctx context.Context, stream io.ReaderAt, streamLen int64, trusted []ed25519.PublicKey) (*Verified, error) {
	if streamLen > MaxStreamLen {
		return nil, &LimitError{"stream length", streamLen, MaxStreamLen}
	}
	if streamLen < HeaderSize {
		return nil, fmt.Errorf("%w: stream shorter than its header", ErrFormat)
	}
	var hb [HeaderSize]byte
	if _, err := stream.ReadAt(hb[:], 0); err != nil {
		return nil, err
	}
	h, err := ParseHeader(hb[:])
	if err != nil {
		return nil, err
	}
	if h.StreamLen() != streamLen {
		return nil, fmt.Errorf("%w: the stream is %d bytes, its header implies %d", ErrFormat, streamLen, h.StreamLen())
	}
	v := &Verified{Header: h}
	if h.Signed {
		sum := sha256.New()
		if _, err := io.Copy(sum, ctxReader{ctx, io.NewSectionReader(stream, 0, HeaderSize+h.BodyLen)}); err != nil {
			return nil, err
		}
		blk := make([]byte, SignatureSize)
		if _, err := stream.ReadAt(blk, HeaderSize+h.BodyLen); err != nil {
			return nil, err
		}
		pub := ed25519.PublicKey(blk[:ed25519.PublicKeySize])
		if !ed25519.Verify(pub, signMessage(sum.Sum(nil)), blk[ed25519.PublicKeySize:]) {
			return nil, ErrBadSignature
		}
		v.Signer = pub
	}
	if len(trusted) > 0 {
		v.Trusted = v.Signer != nil && slices.ContainsFunc(trusted, func(k ed25519.PublicKey) bool { return v.Signer.Equal(k) })
		if !v.Trusted {
			return nil, ErrMissingSignature
		}
	}
	return v, nil
}

// UnpackOptions are the inputs of [Unpack].
type UnpackOptions struct {
	OutputDir  string                  // created if missing
	Passphrase func() (string, error)  // asked only after the signature checks, and only if encrypted
	VerifyKeys []ed25519.PublicKey     // non-empty: the stream must be signed by one of these
	MaxSize    int64                   // largest archive and total file size; 0: DefaultMaxSize
	Overwrite  bool                    // replace existing regular files
	Progress   func(done, total int64) // file bytes unpacked; nil allowed
}

// UnpackResult describes an unpacked stream.
type UnpackResult struct {
	Verified
	Files int
	Dirs  int
	Bytes int64
	Paths []string // every extracted entry, in manifest order, as the manifest names it
}

// Unpack verifies a stream, then asks for the passphrase if it is encrypted, decrypts, decompresses
// and extracts it into o.OutputDir. Nothing is written to the output folder unless the whole manifest
// passes the rules of FORMAT.md section 12; files are written into a staging folder inside it, checked
// against their SHA-256, and moved into place only when all of them are verified.
func Unpack(ctx context.Context, stream io.ReaderAt, streamLen int64, o UnpackOptions) (*UnpackResult, error) {
	v, err := Verify(ctx, stream, streamLen, o.VerifyKeys)
	if err != nil {
		return nil, err
	}
	h := v.Header
	if max := maxSize(o); h.ArchiveLen > max {
		return nil, &LimitError{"archive length", h.ArchiveLen, max}
	}
	var body io.Reader = io.NewSectionReader(stream, HeaderSize, h.BodyLen)
	if h.Encrypted {
		if body, err = decrypt(body, o.Passphrase); err != nil {
			return nil, err
		}
	}
	r, release, err := decompressor(body, h.Method)
	if err != nil {
		return nil, err
	}
	defer release()
	res, err := unpackArchive(ctx, r, h.ArchiveLen, o)
	if err != nil {
		return nil, err
	}
	res.Verified = *v
	return res, nil
}

func maxSize(o UnpackOptions) int64 {
	if o.MaxSize > 0 {
		return o.MaxSize
	}
	return DefaultMaxSize
}

// decrypt asks for the passphrase and opens an age body.
func decrypt(body io.Reader, passphrase func() (string, error)) (io.Reader, error) {
	if passphrase == nil {
		return nil, ErrPassphraseRequired
	}
	pass, err := passphrase()
	if err != nil {
		return nil, fmt.Errorf("payload: reading the passphrase: %w", err)
	}
	id, err := age.NewScryptIdentity(pass)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrWrongPassphrase, err)
	}
	id.SetMaxWorkFactor(ScryptWorkFactor)
	r, err := age.Decrypt(body, id)
	var nm *age.NoIdentityMatchError
	switch {
	case errors.As(err, &nm) && slices.Equal(nm.StanzaTypes, []string{"scrypt"}):
		return nil, ErrWrongPassphrase
	case err != nil:
		return nil, fmt.Errorf("%w: age: %w", ErrFormat, err)
	}
	return r, nil
}

// unpackArchive reads archive bytes from r, of which exactly archiveLen must come, and extracts them.
func unpackArchive(ctx context.Context, r io.Reader, archiveLen int64, o UnpackOptions) (*UnpackResult, error) {
	ar := &exactReader{r: ctxReader{ctx, r}, n: archiveLen, limit: archiveLen}
	var lb [4]byte
	if _, err := io.ReadFull(ar, lb[:]); err != nil {
		return nil, archiveErr(err)
	}
	ml := int64(binary.BigEndian.Uint32(lb[:]))
	if ml > MaxManifestBytes {
		return nil, &LimitError{"manifest size", ml, MaxManifestBytes}
	}
	if ml > archiveLen-4 {
		return nil, fmt.Errorf("%w: the manifest runs past the archive", ErrFormat)
	}
	mb := make([]byte, ml)
	if _, err := io.ReadFull(ar, mb); err != nil {
		return nil, archiveErr(err)
	}
	entries, err := ParseManifest(mb)
	if err != nil {
		return nil, err
	}
	if err := ValidateManifest(entries, maxSize(o)); err != nil {
		return nil, err
	}
	x, err := newExtractor(ctx, o.OutputDir, entries, o.Overwrite)
	if err != nil {
		return nil, err
	}
	defer x.close()
	res := &UnpackResult{Paths: make([]string, len(entries))}
	for i, e := range entries {
		res.Paths[i] = e.Path
	}
	pw := &progressWriter{fn: o.Progress}
	for _, e := range entries {
		pw.total += e.Size
	}
	tr := tar.NewReader(ar)
	for i, e := range entries {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, &UnsafeArchiveError{e.Path, "in the manifest but missing from the archive"}
		} else if err != nil {
			return nil, archiveErr(err)
		}
		if err := checkHeader(hdr, e); err != nil {
			return nil, err
		}
		if e.Dir {
			res.Dirs++
			continue
		}
		if err := x.write(i, e, tr, pw); err != nil {
			return nil, err
		}
		res.Files++
		res.Bytes += e.Size
	}
	if hdr, err := tr.Next(); err == nil {
		return nil, &UnsafeArchiveError{hdr.Name, "in the archive but not in the manifest"}
	} else if !errors.Is(err, io.EOF) {
		return nil, archiveErr(err)
	}
	if _, err := io.Copy(io.Discard, ar); err != nil { // to the declared end, authenticating the rest
		return nil, err
	}
	if err := x.commit(); err != nil {
		return nil, err
	}
	return res, nil
}

// archiveErr marks an error met while reading the archive as a format error, keeping what it wraps.
func archiveErr(err error) error {
	if errors.Is(err, ErrFormat) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var l *LimitError
	if errors.As(err, &l) {
		return err
	}
	return fmt.Errorf("%w: archive: %w", ErrFormat, err)
}

// checkHeader compares a tar header with the manifest entry expected at its place.
func checkHeader(h *tar.Header, e Entry) error {
	want, kind := byte(tar.TypeReg), "regular file"
	if e.Dir {
		want, kind = tar.TypeDir, "directory"
	}
	if h.Typeflag != want {
		return &UnsafeArchiveError{h.Name, fmt.Sprintf("tar entry of type %q where the manifest has the %s %q", h.Typeflag, kind, e.Path)}
	}
	if h.Name != e.Path && (!e.Dir || h.Name != e.Path+"/") {
		return &UnsafeArchiveError{h.Name, fmt.Sprintf("found where the manifest has %q (out of order or not in the manifest)", e.Path)}
	}
	if !e.Dir && h.Size != e.Size {
		return &UnsafeArchiveError{e.Path, fmt.Sprintf("size %d in the archive, %d in the manifest", h.Size, e.Size)}
	}
	return nil
}

// exactReader yields exactly n more bytes from r: fewer is a format error, more a LimitError. This
// is the decompression bomb defence of SECURITY.md T4.
type exactReader struct {
	r        io.Reader
	n, limit int64
}

func (e *exactReader) Read(p []byte) (int, error) {
	if e.n == 0 {
		var b [1]byte
		k, err := io.ReadFull(e.r, b[:])
		switch {
		case k > 0:
			return 0, &LimitError{"archive length", e.limit + 1, e.limit}
		case errors.Is(err, io.EOF):
			return 0, io.EOF
		}
		return 0, archiveErr(err)
	}
	if int64(len(p)) > e.n {
		p = p[:e.n]
	}
	k, err := e.r.Read(p)
	e.n -= int64(k)
	if errors.Is(err, io.EOF) {
		if e.n > 0 {
			return k, fmt.Errorf("%w: the archive ends %d bytes before its declared length", ErrFormat, e.n)
		}
		err = nil // the next call checks that nothing follows
	}
	if err != nil {
		err = archiveErr(err)
	}
	return k, err
}
