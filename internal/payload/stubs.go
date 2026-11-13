package payload

import (
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"time"
)

var errStub = errors.New("payload: not implemented")

var scryptLogN = ScryptWorkFactor

// compressor returns a writer that compresses with m.
func compressor(w io.Writer, m Method) (io.WriteCloser, error) { return nil, errStub }

// Compression selects how Pack compresses the archive. The values equal the root package's.
type Compression uint8

// Compression choices.
const (
	CompressAuto Compression = iota // zstd unless a sample does not compress (see Pack)
	CompressZstd
	CompressXZ
	CompressNone
)

// Method is the compression byte of the stream header.
type Method uint8

// Compression methods of FORMAT.md section 12.
const (
	MethodNone Method = 0
	MethodZstd Method = 1
	MethodXZ   Method = 2
)

// Stream layout constants.
const (
	HeaderSize    = 24
	SignatureSize = ed25519.PublicKeySize + ed25519.SignatureSize
	FlagEncrypted = 0x01
	FlagSigned    = 0x02
)

// Header is the 24-byte stream header.
type Header struct {
	Method     Method
	Encrypted  bool
	Signed     bool
	ArchiveLen int64
	BodyLen    int64
}

// Marshal returns the encoding of h.
func (h Header) Marshal() [HeaderSize]byte { return [HeaderSize]byte{} }

// StreamLen returns the stream length L that h implies.
func (h Header) StreamLen() int64 { return 0 }

// ParseHeader decodes and checks a stream header.
func ParseHeader(b []byte) (Header, error) { return Header{}, errStub }

// ValidatePath checks one manifest path against the path rules.
func ValidatePath(p string) error { return errStub }

// Entry is one manifest entry.
type Entry struct {
	Path   string
	Dir    bool
	Size   int64
	Mode   uint32
	MTime  time.Time
	SHA256 [32]byte
}

// MarshalManifest encodes entries.
func MarshalManifest(entries []Entry) []byte { return nil }

// ParseManifest decodes a manifest.
func ParseManifest(b []byte) ([]Entry, error) { return nil, errStub }

// ValidateManifest checks every entry.
func ValidateManifest(entries []Entry, maxSize int64) error { return errStub }

// GenerateKey makes a key pair.
func GenerateKey(rand io.Reader) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return nil, nil, errStub
}

// MarshalPrivateKey encodes a private key.
func MarshalPrivateKey(k ed25519.PrivateKey) []byte { return nil }

// MarshalPublicKey encodes a public key.
func MarshalPublicKey(k ed25519.PublicKey) []byte { return nil }

// ParsePrivateKey decodes a private key file.
func ParsePrivateKey(b []byte) (ed25519.PrivateKey, error) { return nil, errStub }

// ParsePublicKey decodes a public key file.
func ParsePublicKey(b []byte) (ed25519.PublicKey, error) { return nil, errStub }

// Fingerprint returns the key fingerprint.
func Fingerprint(k ed25519.PublicKey) string { return "" }

// WriteKeyPair writes name.key and name.pub.
func WriteKeyPair(name string, rand io.Reader) (ed25519.PublicKey, error) { return nil, errStub }

// ReadPrivateKeyFile reads a private key file.
func ReadPrivateKeyFile(path string) (ed25519.PrivateKey, []Warning, error) {
	return nil, nil, errStub
}

// ReadPublicKeyFile reads a public key file.
func ReadPublicKeyFile(path string) (ed25519.PublicKey, error) { return nil, errStub }

// PackOptions are the inputs of Pack.
type PackOptions struct {
	Inputs      []string
	Compression Compression
	Passphrase  func() (string, error)
	SignKey     ed25519.PrivateKey
	Progress    func(done, total int64)
}

// PackResult describes a packed stream.
type PackResult struct {
	Header     Header
	StreamLen  int64
	StreamHash [32]byte
	Files      int
	Dirs       int
	Bytes      int64
	Warnings   []Warning
}

// Pack writes the stream.
func Pack(ctx context.Context, dst io.ReadWriteSeeker, o PackOptions) (*PackResult, error) {
	return nil, errStub
}

// Verified is the outcome of Verify.
type Verified struct {
	Header  Header
	Signer  ed25519.PublicKey
	Trusted bool
}

// Verify checks header and signature.
func Verify(stream io.ReaderAt, streamLen int64, trusted []ed25519.PublicKey) (*Verified, error) {
	return nil, errStub
}

// UnpackOptions are the inputs of Unpack.
type UnpackOptions struct {
	OutputDir  string
	Passphrase func() (string, error)
	VerifyKeys []ed25519.PublicKey
	MaxSize    int64
	Overwrite  bool
	Progress   func(done, total int64)
}

// UnpackResult describes an unpacked stream.
type UnpackResult struct {
	Verified
	Files int
	Dirs  int
	Bytes int64
}

// Unpack verifies, decrypts, decompresses and extracts a stream.
func Unpack(ctx context.Context, stream io.ReaderAt, streamLen int64, o UnpackOptions) (*UnpackResult, error) {
	return nil, errStub
}

// unpackArchive extracts archive bytes into the output folder.
func unpackArchive(ctx context.Context, r io.Reader, archiveLen int64, o UnpackOptions) (*UnpackResult, error) {
	return nil, errStub
}

// writeStream writes collected entries as a stream (used by Pack and the golden test).
func writeStream(ctx context.Context, dst io.ReadWriteSeeker, entries []source, o PackOptions) (*PackResult, error) {
	return nil, errStub
}

// source is an entry and the file it comes from.
type source struct {
	Entry
	file string
}

// collect walks the inputs.
func collect(inputs []string) ([]source, []Warning, error) { return nil, nil, errStub }
