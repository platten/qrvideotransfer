package qrvt

import "time"

// Modulation selects how cells carry bits. Values are the modulation ids of docs/FORMAT.md section 7.
type Modulation uint8

// Modulations. The zero value is not a modulation.
const (
	Gray2 Modulation = 1 // black and white, 1 bit per cell
	Gray4 Modulation = 2 // four greys, 2 bits per cell
	RGB8  Modulation = 3 // eight colours, 3 bits per cell
	Gray8 Modulation = 4 // eight greys in a 10-bit video, 3 bits per cell (experimental)
)

// Protection selects the inner code. Values are the inner-code ids of docs/FORMAT.md section 8.
type Protection uint8

// Protection levels. The zero value is not a protection level.
const (
	Fast   Protection = 1 // LDPC rate 5/6, 192-byte shards
	Normal Protection = 2 // LDPC rate 3/4, 128-byte shards
	Robust Protection = 4 // LDPC rate 1/2, 64-byte shards
)

// Compression selects how the archive is compressed before it is protected.
type Compression uint8

// Compression choices.
const (
	CompressAuto Compression = iota // zstd unless a sample shows the data does not compress
	CompressZstd
	CompressXZ
	CompressNone
)

// Profile is the set of encoder settings that shape the video.
type Profile struct {
	Width, Height int        // video pixels
	Cell          int        // 4, 8, 12 or 16
	FPS           int        // display frame rate
	Modulation    Modulation // Gray4, Gray2, RGB8, Gray8
	Protection    Protection // Fast, Normal, Robust
	ParityPercent int        // 5..100
}

// EncodeOptions are the inputs of [Encode].
type EncodeOptions struct {
	Inputs      []string
	Output      string
	Profile     Profile
	Compression Compression            // CompressAuto, CompressZstd, CompressXZ, CompressNone
	Passphrase  func() (string, error) // nil: no encryption
	// SignKey is the private key line of a qrvt key file, "qrvt-private-key-v1:" and the base64 of
	// the 32-byte seed (SECURITY.md section 6), without surrounding white space or line ending.
	// nil: unsigned.
	SignKey  []byte
	DryRun   bool
	Backend  string      // "embedded", "system" or a path
	Workers  int         // 0: the CPU count, reduced to fit memory
	TempDir  string      // "": the system temporary directory
	Progress func(Event) // called from one goroutine; nil allowed
}

// DecodeOptions are the inputs of [Decode].
type DecodeOptions struct {
	Recordings []string
	OutputDir  string
	StateDir   string // "": no resuming
	Passphrase func() (string, error)
	// VerifyKeys are trusted public key lines, each "qrvt-public-key-v1:" and the base64 of the
	// 32-byte key (SECURITY.md section 6), without surrounding white space or line ending.
	// Non-empty: the transfer must be signed by one of these.
	VerifyKeys [][]byte
	MaxSize    int64 // largest total size that may be unpacked
	// MaxEntries is the largest number of manifest entries that may be unpacked; the manifest-size
	// limit is raised in proportion. 0 means the decoder's default, about 100,000; the most is the
	// format limit of 1,000,000 entries and 64 MiB (flag --max-entries). A manifest over the limit
	// is a LimitError whose Flag is "--max-entries".
	MaxEntries int
	Overwrite  bool
	Report     string // optional path for a JSON report
	Backend    string
	// UnsandboxedHEVC opts in to decoding HEVC recordings in-process with the pure-Go decoder, outside
	// the WebAssembly sandbox (flag --unsandboxed-hevc). Only for recordings the user made: its memory is
	// not bounded against a crafted stream (SECURITY.md).
	UnsandboxedHEVC bool
	Workers         int
	Progress        func(Event)
}

// CalibrationOptions are the inputs of [CalibrationVideo].
type CalibrationOptions struct {
	Output        string
	Width, Height int // monitor resolution the video is made for
	Backend       string
	Workers       int
	Progress      func(Event)
}

// AnalyzeOptions are the inputs of [AnalyzeCalibration].
type AnalyzeOptions struct {
	Recordings []string
	Backend    string
	// UnsandboxedHEVC opts in to decoding HEVC recordings in-process with the pure-Go decoder, outside
	// the WebAssembly sandbox (flag --unsandboxed-hevc). Only for recordings the user made: its memory is
	// not bounded against a crafted stream (SECURITY.md).
	UnsandboxedHEVC bool
	Workers         int
	Progress        func(Event)
}

// InspectOptions are the inputs of [Inspect].
type InspectOptions struct {
	Path    string // a code video or a recording of one
	Backend string
	// UnsandboxedHEVC opts in to decoding HEVC recordings in-process with the pure-Go decoder, outside
	// the WebAssembly sandbox (flag --unsandboxed-hevc). Only for recordings the user made: its memory is
	// not bounded against a crafted stream (SECURITY.md).
	UnsandboxedHEVC bool
	Workers         int
	Progress        func(Event)
}

// Stage names a step of encoding or decoding.
type Stage uint8

// Stages, in pipeline order.
const (
	StagePack Stage = iota + 1
	StageProtect
	StageRender
	StageEncodeVideo
	StageDecodeVideo
	StageDemodulate
	StageRepair
	StageVerify
	StageUnpack
)

// EventKind says what an [Event] reports.
type EventKind uint8

// Event kinds.
const (
	KindStarted EventKind = iota + 1
	KindProgress
	KindFinished
	KindStats
	KindWarning
)

// Unit is the unit of an event's Done and Total.
type Unit uint8

// Units.
const (
	UnitBytes Unit = iota + 1
	UnitFrames
	UnitBlocks
)

// Event is one progress report. Events are delivered in order from a single goroutine.
type Event struct {
	Time        time.Time
	Stage       Stage
	Kind        EventKind
	Done, Total int64
	Unit        Unit
	Stats       *Stats   // set when Kind is KindStats
	Warn        *Warning // set when Kind is KindWarning
}

// Stats is a snapshot of decoding progress, shown by the dashboard and the report.
type Stats struct {
	Captures      int64   // captures read from the recordings
	FramesSeen    int     // distinct code frames identified
	FramesTotal   int     // F, from the header
	CleanFraction float64 // share of cells assigned to a single code frame
	RawBitErrors  float64 // raw bit error rate before the inner code
	CodewordsOK   int64   // inner codewords that passed their CRC
	BlocksReady   int     // outer-code blocks that can be reconstructed
	BlocksTotal   int
}

// WarningCode identifies a kind of warning.
type WarningCode string

// Warning codes.
const (
	WarnExposureTooLong WarningCode = "ExposureTooLong"
	WarnClipping        WarningCode = "Clipping"
	WarnFramesDropped   WarningCode = "FramesDropped"
	WarnSkippedFile     WarningCode = "SkippedFile"
	WarnDownscaled      WarningCode = "Downscaled"
	WarnMoire           WarningCode = "Moire"
)

// Warning is a condition the user should know about that does not stop the work.
type Warning struct {
	Code WarningCode
	Text string // one sentence for a person
}

// StreamInfo is what a code video carries, as reported by [Inspect].
type StreamInfo struct {
	Session       uint64
	Width, Height int // video pixels, when known
	GridX, GridY  int
	Modulation    Modulation
	Protection    Protection
	Frames        int // F
	FPS           int // display rate; 0 if unknown
	StreamLen     int64
	ShardSize     int
	Blocks, K, M  int
	StreamHash    [16]byte // first 16 bytes of the SHA-256 of the stream
}

// EncodeReport describes a finished (or, with DryRun, planned) encode.
type EncodeReport struct {
	Output       string
	Profile      Profile // the profile actually used, after size reduction
	Session      uint64
	Files        int
	ArchiveLen   int64 // bytes before compression
	StreamLen    int64
	Blocks, K, M int
	Frames       int
	Duration     time.Duration // one loop of the video
	VideoBytes   int64         // estimated with DryRun
	Warnings     []Warning
}

// DecodeReport describes a finished decode; it is also written as JSON with DecodeOptions.Report.
type DecodeReport struct {
	OutputDir     string
	Session       uint64
	Files         int
	Bytes         int64
	Captures      int64
	FramesSeen    int
	FramesTotal   int
	CleanFraction float64
	Encrypted     bool
	Signed        bool
	SignerKey     []byte // the signer's raw 32-byte Ed25519 public key, when signed
	Warnings      []Warning
}

// Recommendation is the outcome of [AnalyzeCalibration].
type Recommendation struct {
	Profile     Profile
	Command     string  // the suggested encode command line
	BytesPerSec float64 // planning rate
	Warnings    []Warning
}
