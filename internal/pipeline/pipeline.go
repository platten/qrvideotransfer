package pipeline

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"

	"github.com/platten/qrvideotransfer/internal/frame"
	"github.com/platten/qrvideotransfer/internal/payload"
	"github.com/platten/qrvideotransfer/internal/video"
)

// EncodeConfig are the inputs of [Encode]. The profile fields are validated by Encode.
type EncodeConfig struct {
	Inputs        []string
	Output        string
	Width, Height int   // video pixels, multiples of Cell
	Cell          int   // pixels per cell: 4, 8, 12 or 16
	FPS           int   // display rate, 1 to 240
	Modulation    uint8 // FORMAT.md section 7: 1, 2 or 3
	Inner         uint8 // FORMAT.md section 8: 1, 2, 3 or 4
	Parity        int   // parity shards as a percentage of data shards, 5 to 100
	Compression   payload.Compression
	Passphrase    func() (string, error) // nil: no encryption
	SignKey       ed25519.PrivateKey     // nil: unsigned
	DryRun        bool                   // pack and plan only; write no video
	Backend       video.Backend
	Workers       int    // 0: GOMAXPROCS
	TempDir       string // "": the system temporary directory
	Progress      func(Event)
	// Random supplies the session id and the outer order seed; nil means crypto/rand. Tests fix
	// it so that a run can be repeated.
	Random io.Reader
}

// EncodeResult describes a finished or planned encode.
type EncodeResult struct {
	Session      uint64
	Files        int
	ArchiveLen   int64 // bytes before compression
	StreamLen    int64
	Blocks, K, M int
	Codewords    int   // C, codewords per frame, the descriptor's included
	Frames       int   // F
	VideoBytes   int64 // the video's size; estimated with DryRun
	Warnings     []Warning
}

// DecodeConfig are the inputs of [Decode].
type DecodeConfig struct {
	Recordings []string
	Backend    video.Backend
	Workers    int    // 0: GOMAXPROCS
	TempDir    string // "": the system temporary directory
	// Unpack configures extraction; its Progress is replaced by the pipeline's events.
	Unpack   payload.UnpackOptions
	Progress func(Event)
}

// DecodeResult describes a finished decode.
type DecodeResult struct {
	Session  uint64
	Stats    Stats
	Unpacked *payload.UnpackResult
	Warnings []Warning
}

// Shortfall says how many shards a block lacks.
type Shortfall struct {
	Block      int
	Have, Need int
}

// ShortError reports that the recordings do not hold enough data. Blocks lists the short blocks;
// it is empty when no stream descriptor could be read, so that nothing about the blocks is known.
type ShortError struct {
	Blocks []Shortfall
	// What the recordings showed, for the failure guidance of DESIGN.md section 13.
	Captures    int64              // captures read
	Located     int64              // captures in which the code's finders were found (a header was tried, whether or not it was read)
	HeadersRead int64              // captures whose header was read, of any transfer
	Other       int64              // of those, captures of a transfer other than the one decoded (or, when none was, the one read most often)
	FramesSeen  int                // code frames of the decoded transfer seen
	FramesTotal int                // its code frames, F; 0 when no header was read
	Descriptor  bool               // the stream descriptor was read
	Reject      *frame.HeaderError // the refusal agreed by the captures (agreeCaptures), or nil
	LockedRate  bool               // the camera is locked to the display rate (lockTrack)
}

// Error implements the error interface.
func (e *ShortError) Error() string {
	if len(e.Blocks) == 0 {
		return "pipeline: no stream descriptor could be read from the recordings"
	}
	missing := 0
	for _, s := range e.Blocks {
		missing += s.Need - s.Have
	}
	return fmt.Sprintf("pipeline: %d blocks short by %d shards", len(e.Blocks), missing)
}

// ErrStreamHash reports a recovered stream whose SHA-256 differs from the descriptor's.
var ErrStreamHash = errors.New("pipeline: the recovered stream does not match the descriptor's hash")
