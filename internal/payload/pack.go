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
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"filippo.io/age"
)

// scryptLogN is the scrypt work factor Pack writes; tests lower it.
var scryptLogN = ScryptWorkFactor

// PackOptions are the inputs of [Pack].
type PackOptions struct {
	Inputs      []string                // files and folders; each is stored under its base name
	Compression Compression             // CompressAuto by default
	Passphrase  func() (string, error)  // nil: no encryption
	SignKey     ed25519.PrivateKey      // nil: unsigned
	Progress    func(done, total int64) // file bytes packed; called from Pack's goroutine; nil allowed
}

// PackResult describes a packed stream.
type PackResult struct {
	Header     Header
	StreamLen  int64
	StreamHash [32]byte // SHA-256 of the whole stream (FORMAT.md section 10 carries its first 16 bytes)
	Files      int
	Dirs       int
	Bytes      int64 // total size of the files
	Warnings   []Warning
}

// source is a manifest entry and the file it is read from.
type source struct {
	Entry
	file string
}

// Pack writes the stream of FORMAT.md section 12 for o.Inputs to dst, which must be empty. Files are
// read twice: once to hash them for the manifest, once to write the archive; a file that changes in
// between stops Pack with [ErrInputChanged]. dst is read back to compute the signature and the stream
// hash, so memory does not grow with the payload.
//
// With CompressAuto, the first 4 MiB of the archive are compressed with zstd; the archive is stored
// uncompressed unless that sample shrinks by at least 2%.
func Pack(ctx context.Context, dst io.ReadWriteSeeker, o PackOptions) (*PackResult, error) {
	srcs, warns, err := collect(o.Inputs)
	if err != nil {
		return nil, err
	}
	res, err := writeStream(ctx, dst, srcs, o)
	if err != nil {
		return nil, err
	}
	res.Warnings = warns
	return res, nil
}

// collect walks the inputs, hashes every file, and returns the entries sorted by path. Links and
// special files are skipped with a warning.
func collect(inputs []string) ([]source, []Warning, error) {
	if len(inputs) == 0 {
		return nil, nil, &InputError{"", "no inputs"}
	}
	var (
		srcs  []source
		warns []Warning
		names = map[string]string{}
	)
	add := func(file, path string, info fs.FileInfo) error {
		e := Entry{Path: path, Mode: uint32(info.Mode().Perm()), MTime: info.ModTime().UTC().Truncate(time.Second)}
		switch {
		case info.IsDir():
			e.Dir = true
		case info.Mode().IsRegular():
			e.Size = info.Size()
			sum, err := hashFile(file, e.Size)
			if err != nil {
				return err
			}
			e.SHA256 = sum
		default:
			warns = append(warns, Warning{WarnSkippedFile, path,
				fmt.Sprintf("skipped %q: only regular files and folders are packed, not %v", path, info.Mode().Type())})
			return nil
		}
		srcs = append(srcs, source{e, file})
		return nil
	}
	for _, in := range inputs {
		abs, err := filepath.Abs(in)
		if err != nil {
			return nil, nil, err
		}
		if filepath.Dir(abs) == abs {
			return nil, nil, &InputError{in, "a root folder has no name to store it under"}
		}
		base := filepath.Base(abs)
		if prev, ok := names[base]; ok {
			return nil, nil, &InputError{base, fmt.Sprintf("the inputs %q and %q have the same name", prev, in)}
		}
		names[base] = in
		err = filepath.WalkDir(abs, func(file string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(abs, file)
			if err != nil {
				return err
			}
			path := base
			if rel != "." {
				path += "/" + filepath.ToSlash(rel)
			}
			return add(file, path, info)
		})
		if err != nil {
			return nil, nil, err
		}
	}
	slices.SortFunc(srcs, func(a, b source) int { return strings.Compare(a.Path, b.Path) })
	entries := make([]Entry, len(srcs))
	for i, s := range srcs {
		entries[i] = s.Entry
	}
	if err := validateInputs(entries); err != nil {
		return nil, nil, err
	}
	return srcs, warns, nil
}

// validateInputs applies the rules of FORMAT.md section 12 to the entries collected from the inputs
// and returns an [InputsError] listing every path that breaks one.
func validateInputs(entries []Entry) error {
	if err := ValidateManifest(entries, math.MaxInt64); err != nil {
		var u *UnsafeArchiveError
		if errors.As(err, &u) {
			err = &InputError{u.Path, u.Reason}
		}
		return err // or a total size beyond int64
	}
	return nil
}

// hashFile returns the SHA-256 of a file that must be size bytes long.
func hashFile(name string, size int64) ([32]byte, error) {
	f, err := os.Open(name) //nolint:gosec // the user names the inputs
	if err != nil {
		return [32]byte{}, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err == nil && n != size {
		err = fmt.Errorf("%w: %s", ErrInputChanged, name)
	}
	return [32]byte(h.Sum(nil)), err
}

// writeStream writes the stream for collected entries to dst.
func writeStream(ctx context.Context, dst io.ReadWriteSeeker, srcs []source, o PackOptions) (*PackResult, error) {
	res := &PackResult{}
	entries := make([]Entry, len(srcs))
	for i, s := range srcs {
		entries[i] = s.Entry
		if s.Dir {
			res.Dirs++
		} else {
			res.Files++
			res.Bytes += s.Size
		}
	}
	if len(entries) > MaxManifestEntries {
		return nil, &LimitError{"manifest entries", int64(len(entries)), MaxManifestEntries}
	}
	manifest := MarshalManifest(entries)
	if len(manifest) > MaxManifestBytes {
		return nil, &LimitError{"manifest size", int64(len(manifest)), MaxManifestBytes}
	}
	var pass string
	if o.Passphrase != nil {
		var err error
		if pass, err = o.Passphrase(); err != nil {
			return nil, err
		}
		if pass == "" {
			return nil, errors.New("payload: the passphrase is empty")
		}
	}
	method, err := methodFor(o.Compression, func(w io.Writer) error {
		return writeArchive(ctx, w, manifest, srcs, nil, 0)
	})
	if err != nil {
		return nil, err
	}

	h := Header{Method: method, Encrypted: pass != "", Signed: o.SignKey != nil}
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if _, err := dst.Write(make([]byte, HeaderSize)); err != nil {
		return nil, err
	}
	body := &countWriter{w: dst}
	var w io.Writer = body
	var closers []io.Closer
	if pass != "" {
		r, err := age.NewScryptRecipient(pass)
		if err != nil {
			return nil, err
		}
		r.SetWorkFactor(scryptLogN)
		ew, err := age.Encrypt(w, r)
		if err != nil {
			return nil, err
		}
		w, closers = ew, append(closers, ew)
	}
	cw, err := compressor(w, method)
	if err != nil {
		return nil, err
	}
	closers = append([]io.Closer{cw}, closers...)
	arch := &countWriter{w: cw}
	if err := writeArchive(ctx, arch, manifest, srcs, o.Progress, res.Bytes); err != nil {
		return nil, err
	}
	for _, c := range closers {
		if err := c.Close(); err != nil {
			return nil, err
		}
	}
	h.ArchiveLen, h.BodyLen = arch.n, body.n
	if h.StreamLen() > MaxStreamLen {
		return nil, &LimitError{"stream length", h.StreamLen(), MaxStreamLen}
	}

	hb := h.Marshal()
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if _, err := dst.Write(hb[:]); err != nil {
		return nil, err
	}
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	signed, whole := sha256.New(), sha256.New()
	if _, err := io.CopyN(io.MultiWriter(signed, whole), ctxReader{ctx, dst}, HeaderSize+h.BodyLen); err != nil {
		return nil, err
	}
	if h.Signed {
		sig := ed25519.Sign(o.SignKey, signMessage(signed.Sum(nil)))
		blk := append(slices.Clone([]byte(o.SignKey.Public().(ed25519.PublicKey))), sig...)
		if _, err := dst.Write(blk); err != nil {
			return nil, err
		}
		whole.Write(blk)
	}
	res.Header, res.StreamLen, res.StreamHash = h, h.StreamLen(), [32]byte(whole.Sum(nil))
	return res, nil
}

// signMessage returns the 44-byte message that is signed: "qrvt/sign/v1" | SHA-256(header | body).
func signMessage(sum []byte) []byte {
	return append([]byte(signLabel), sum...)
}

// writeArchive writes the archive: the manifest length, the manifest, and a pax tar stream of srcs.
// Each file is hashed again as it is written and must match its entry.
func writeArchive(ctx context.Context, w io.Writer, manifest []byte, srcs []source, progress func(done, total int64), total int64) error {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(manifest))) //nolint:gosec // at most MaxManifestBytes
	if _, err := w.Write(n[:]); err != nil {
		return err
	}
	if _, err := w.Write(manifest); err != nil {
		return err
	}
	tw := tar.NewWriter(w)
	pw := &progressWriter{fn: progress, total: total}
	for _, s := range srcs {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr := &tar.Header{Typeflag: tar.TypeReg, Name: s.Path, Size: s.Size, Mode: int64(s.perm()), ModTime: s.MTime,
			Format: tar.FormatPAX}
		if s.Dir {
			hdr.Typeflag, hdr.Name, hdr.Size = tar.TypeDir, s.Path+"/", 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !s.Dir {
			if err := copyFile(ctx, io.MultiWriter(tw, pw), s); err != nil {
				return err
			}
		}
	}
	return tw.Close()
}

// copyFile copies a source file to w and checks it against its entry.
func copyFile(ctx context.Context, w io.Writer, s source) error {
	f, err := os.Open(s.file)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.CopyN(io.MultiWriter(w, h), ctxReader{ctx, f}, s.Size); errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: %s became shorter", ErrInputChanged, s.file)
	} else if err != nil {
		return err
	}
	if [32]byte(h.Sum(nil)) != s.SHA256 {
		return fmt.Errorf("%w: %s", ErrInputChanged, s.file)
	}
	return nil
}

// countWriter counts the bytes it passes on.
type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// progressWriter reports the bytes written to it.
type progressWriter struct {
	fn          func(done, total int64)
	done, total int64
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if p.fn != nil {
		p.fn(p.done, p.total)
	}
	return len(b), nil
}

// ctxReader fails with the context's error once it is cancelled.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
