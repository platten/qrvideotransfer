package payload

import (
	"archive/tar"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strconv"

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
	x, err := newExtractor(o.OutputDir, entries, o.Overwrite)
	if err != nil {
		return nil, err
	}
	defer x.close()
	res := &UnpackResult{}
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

// extractor writes verified entries into a staging folder inside the output folder, through os.Root,
// and moves them into place on commit.
type extractor struct {
	root      *os.Root
	stage     string
	entries   []Entry
	created   []Entry // directories created in the output folder, in manifest order
	committed bool
}

// stagePrefix starts the name of the staging folder; a random suffix follows. A manifest cannot
// predict the suffix, so an entry of the same name is left to chance (2^-64), not checked.
const stagePrefix = ".qrvt-staging-"

// newExtractor opens the output folder, checks that no entry would replace something it may not, and
// creates the staging folder.
func newExtractor(dir string, entries []Entry, overwrite bool) (*extractor, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	x := &extractor{root: root, entries: entries}
	var r [8]byte
	_, _ = rand.Read(r[:]) // never fails
	x.stage = stagePrefix + hex.EncodeToString(r[:])
	for _, e := range entries {
		err = errors.Join(err, x.check(e, overwrite))
	}
	if err == nil {
		err = root.Mkdir(x.stage, 0o700)
	}
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	return x, nil
}

// check refuses an entry whose path exists in a form it may not replace.
func (x *extractor) check(e Entry, overwrite bool) error {
	st, err := x.root.Lstat(e.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// Windows reports a path beneath a file as missing; the nearest existing ancestor must be a
		// real folder.
		for p := path.Dir(e.Path); p != "."; p = path.Dir(p) {
			if st, err := x.root.Lstat(p); err == nil {
				if !st.IsDir() {
					return &ExistsError{e.Path, fmt.Sprintf("cannot be created: %q is not a folder", p)}
				}
				break
			}
		}
		return nil
	case err != nil:
		return &ExistsError{e.Path, fmt.Sprintf("cannot be created: %v", err)}
	case e.Dir && st.IsDir():
		return nil
	case e.Dir:
		return &ExistsError{e.Path, "exists and is not a folder"}
	case !st.Mode().IsRegular():
		return &ExistsError{e.Path, "exists and is not a regular file"}
	case !overwrite:
		return &ExistsError{e.Path, "already exists (allow overwriting to replace it)"}
	}
	return nil
}

// staged names the staging file of entry i; the staging folder is flat.
func (x *extractor) staged(i int) string { return x.stage + "/" + strconv.Itoa(i) }

// write stores entry i in the staging folder, leaving holes for runs of zeros, and checks its hash.
func (x *extractor) write(i int, e Entry, r io.Reader, pw *progressWriter) error {
	f, err := x.root.OpenFile(x.staged(i), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	// The tar reader yields exactly e.Size bytes, or fails.
	if _, err := io.CopyBuffer(io.MultiWriter(sparseWriter{f}, h, pw), r, make([]byte, 64<<10)); err != nil {
		return archiveErr(err)
	}
	if [32]byte(h.Sum(nil)) != e.SHA256 {
		return &UnsafeArchiveError{e.Path, "SHA-256 differs from the manifest"}
	}
	return errors.Join(f.Truncate(e.Size), f.Close())
}

// sparseWriter seeks over blocks of zeros instead of writing them; the caller truncates the file to
// its length at the end.
type sparseWriter struct{ f *os.File }

func (s sparseWriter) Write(b []byte) (int, error) {
	for _, c := range b {
		if c != 0 {
			return s.f.Write(b)
		}
	}
	if _, err := s.f.Seek(int64(len(b)), io.SeekCurrent); err != nil {
		return 0, err
	}
	return len(b), nil
}

// commit moves every staged file into place, creates the directories, and then sets directory modes
// and times, deepest first.
func (x *extractor) commit() error {
	for i, e := range x.entries {
		if e.Dir {
			if st, err := x.root.Lstat(e.Path); err == nil && st.IsDir() {
				continue // an existing folder is merged into, and left as it was
			}
			x.created = append(x.created, e)
			if err := x.root.MkdirAll(e.Path, 0o700); err != nil {
				return err
			}
			continue
		}
		s := x.staged(i)
		err := errors.Join(x.root.MkdirAll(path.Dir(e.Path), 0o750), x.root.Chmod(s, os.FileMode(e.perm())),
			x.root.Chtimes(s, e.MTime, e.MTime))
		if err == nil {
			err = x.root.Rename(s, e.Path)
		}
		if err != nil {
			return err
		}
	}
	x.committed = true
	err := x.root.RemoveAll(x.stage)
	for _, e := range slices.Backward(x.created) {
		err = errors.Join(err, x.root.Chmod(e.Path, os.FileMode(e.perm())), x.root.Chtimes(e.Path, e.MTime, e.MTime))
	}
	return err
}

// close removes the staging folder unless commit got past moving the files.
func (x *extractor) close() {
	if !x.committed {
		_ = x.root.RemoveAll(x.stage)
	}
	_ = x.root.Close()
}
