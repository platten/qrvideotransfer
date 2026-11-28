// Package qrvt is the public API of qrvt, which turns files into a looping video of on-screen codes
// and turns a camera recording of that video back into the files.
//
// The package is named qrvt although the module path ends in qrvideotransfer. It does no terminal
// I/O and never exits the process: progress is reported through typed [Event] values delivered to a
// callback, and cancellation is by context. See docs/DESIGN.md section 14.
package qrvt

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/platten/qrvideotransfer/internal/payload"
	"github.com/platten/qrvideotransfer/internal/pipeline"
)

// Profile defaults: a zero field of EncodeOptions.Profile takes these (DESIGN.md section 13).
const (
	defaultWidth, defaultHeight = 1920, 1080
	defaultCell                 = 4
	defaultFPS                  = 12
	defaultParity               = 20
)

// withDefaults fills the zero fields of p.
func (p Profile) withDefaults() Profile {
	if p.Width == 0 && p.Height == 0 {
		p.Width, p.Height = defaultWidth, defaultHeight
	}
	if p.Cell == 0 {
		p.Cell = defaultCell
	}
	if p.FPS == 0 {
		p.FPS = defaultFPS
	}
	if p.Modulation == 0 {
		p.Modulation = Gray4
	}
	if p.Protection == 0 {
		p.Protection = Normal
	}
	if p.ParityPercent == 0 {
		p.ParityPercent = defaultParity
	}
	return p
}

// Encode packs the inputs named in o and writes the code video. With DryRun it packs and plans
// only, writes no video, and reports the video's length, an estimate of its size and the loop's
// duration.
func Encode(ctx context.Context, o EncodeOptions) (*EncodeReport, error) {
	p := o.Profile.withDefaults()
	if p.Modulation == Gray8 {
		return nil, fmt.Errorf("%w: eight greys in a 10-bit video (--hdr)", ErrNotImplemented)
	}
	var key ed25519.PrivateKey
	if o.SignKey != nil {
		k, err := payload.ParsePrivateKey(o.SignKey)
		if err != nil {
			return nil, err
		}
		key = k
	}
	be, err := backend(o.Backend, false)
	if err != nil {
		return nil, err
	}
	r, err := pipeline.Encode(ctx, pipeline.EncodeConfig{Inputs: o.Inputs, Output: o.Output, Width: p.Width, Height: p.Height,
		Cell: p.Cell, FPS: p.FPS, Modulation: uint8(p.Modulation), Inner: uint8(p.Protection), Parity: p.ParityPercent,
		Compression: payload.Compression(o.Compression), Passphrase: o.Passphrase, SignKey: key, DryRun: o.DryRun,
		Backend: be, Workers: o.Workers, TempDir: o.TempDir, Progress: progress(o.Progress), Random: testRandom})
	if err != nil {
		return nil, convertError(err)
	}
	rep := &EncodeReport{Output: o.Output, Profile: p, Session: r.Session, Files: r.Files, ArchiveLen: r.ArchiveLen,
		StreamLen: r.StreamLen, Blocks: r.Blocks, K: r.K, M: r.M, Frames: r.Frames,
		Duration: time.Duration(int64(r.Frames) * int64(time.Second) / int64(p.FPS)), VideoBytes: r.VideoBytes,
		Warnings: convertWarnings(r.Warnings)}
	if o.DryRun {
		rep.Output = ""
	}
	return rep, nil
}

// Decode recovers the files carried by one or more recordings of a code video. Recordings add up:
// soft bits and recovered shards are keyed by session and frame index. A decode that lacks data
// returns a *NotEnoughDataError naming the short blocks (none when not even the stream descriptor
// could be read), with what the recordings showed, and writes nothing. When no header was read and
// at least 3 captures, half of those whose finders were found, agree that the header names an
// unsupported format version, modulation or inner code, in headers naming at least two frame
// indices, it returns an *UnsupportedVersionError naming the value instead.
func Decode(ctx context.Context, o DecodeOptions) (*DecodeReport, error) {
	if o.StateDir != "" {
		return nil, fmt.Errorf("%w: resuming with a state folder (--state-dir)", ErrNotImplemented)
	}
	if o.OutputDir == "" {
		return nil, errors.New("qrvt: no output folder")
	}
	var keys []ed25519.PublicKey
	for _, b := range o.VerifyKeys {
		k, err := payload.ParsePublicKey(b)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	be, err := backend(o.Backend, o.UnsandboxedHEVC)
	if err != nil {
		return nil, err
	}
	r, err := pipeline.Decode(ctx, pipeline.DecodeConfig{Recordings: o.Recordings, Backend: be, Workers: o.Workers,
		Unpack: payload.UnpackOptions{OutputDir: o.OutputDir, Passphrase: o.Passphrase, VerifyKeys: keys, MaxSize: o.MaxSize,
			Overwrite: o.Overwrite, MaxEntries: int64(o.MaxEntries)},
		Progress: progress(o.Progress)})
	if err != nil {
		return nil, convertError(err)
	}
	u := r.Unpacked
	rep := &DecodeReport{OutputDir: o.OutputDir, Session: r.Session, Files: u.Files, Bytes: u.Bytes, Captures: r.Stats.Captures,
		FramesSeen: r.Stats.FramesSeen, FramesTotal: r.Stats.FramesTotal, CleanFraction: r.Stats.CleanFraction,
		Encrypted: u.Header.Encrypted, Signed: u.Header.Signed, Warnings: convertWarnings(r.Warnings)}
	if u.Signer != nil {
		rep.SignerKey = []byte(u.Signer)
	}
	if o.Report != "" {
		b, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(o.Report, append(b, '\n'), 0o600); err != nil {
			return nil, err
		}
	}
	return rep, nil
}

// AnalyzeCalibration measures a recording of the calibration video and recommends a profile.
func AnalyzeCalibration(ctx context.Context, o AnalyzeOptions) (*Recommendation, error) {
	return nil, ErrNotImplemented
}

// Inspect reports what a code video or a recording of one carries, without unpacking it. It reads
// the recording as Decode does, stops as soon as the stream descriptor decodes, and writes
// nothing. When the recording ends first, it returns a *NotEnoughDataError with what the
// recording showed, as Decode's, and, if a frame header was read, the partial StreamInfo of that
// header (Descriptor false) beside it; when the captures agree on an unsupported format version,
// modulation or inner code, it returns an *UnsupportedVersionError, as Decode does.
func Inspect(ctx context.Context, o InspectOptions) (*StreamInfo, error) {
	if o.Path == "" {
		return nil, errors.New("qrvt: no video or recording")
	}
	be, err := backend(o.Backend, o.UnsandboxedHEVC)
	if err != nil {
		return nil, err
	}
	r, err := pipeline.Inspect(ctx, pipeline.InspectConfig{Path: o.Path, Backend: be, Workers: o.Workers,
		Progress: progress(o.Progress)})
	if r == nil || !r.HeaderRead {
		return nil, convertError(err)
	}
	h := r.Header
	si := &StreamInfo{Width: r.Probe.Width, Height: r.Probe.Height, GridX: int(h.GridX), GridY: int(h.GridY),
		Modulation: Modulation(h.Modulation), Protection: Protection(h.InnerCode), Frames: int(h.TotalFrames)}
	if d := r.Descriptor; d != nil {
		si.Descriptor = true
		si.Session, si.FPS, si.StreamLen, si.ShardSize = d.Session, int(d.DisplayFPS), d.StreamLen, d.ShardSize
		si.Blocks, si.K, si.M, si.StreamHash = d.Blocks, d.K, d.M, d.StreamHash
	}
	return si, convertError(err)
}

// GenerateKey returns a new Ed25519 signing key pair in qrvt's key-file encoding (SECURITY.md
// section 6): each the contents of a key file, one line and its line ending. The private key, as
// it is or without its line ending, is what EncodeOptions.SignKey takes, and the public key what
// DecodeOptions.VerifyKeys takes.
func GenerateKey() (public, private []byte, err error) {
	pub, priv, err := payload.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return payload.MarshalPublicKey(pub), payload.MarshalPrivateKey(priv), nil
}
