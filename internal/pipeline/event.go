package pipeline

import "time"

// Stage names a step of encoding or decoding. The values equal the root package's.
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

// Kind says what an [Event] reports. The values equal the root package's.
type Kind uint8

// Event kinds.
const (
	KindStarted Kind = iota + 1
	KindProgress
	KindFinished
	KindStats
	KindWarning
)

// Unit is the unit of an event's Done and Total. The values equal the root package's.
type Unit uint8

// Units.
const (
	UnitBytes Unit = iota + 1
	UnitFrames
	UnitBlocks
)

// Stats is a snapshot of decoding progress. Its fields are those of the root package's Stats.
type Stats struct {
	Captures      int64   // captures read from the recordings
	FramesSeen    int     // distinct code frames identified
	FramesTotal   int     // F, from the header
	CleanFraction float64 // share of cells assigned to a single code frame, over located captures
	RawBitErrors  float64 // bit error rate of the summed soft bits of decoded codewords
	CodewordsOK   int64   // inner codewords that passed their CRC
	BlocksReady   int     // outer-code blocks that can be reconstructed
	BlocksTotal   int
}

// Warning codes the pipeline raises; the root package has the same strings.
const (
	WarnExposureTooLong = "ExposureTooLong"
	WarnEngineInProcess = "EngineInProcess"
	WarnSkippedFile     = "SkippedFile"
	WarnReceiverLimit   = "ReceiverLimit"
)

// Warning is a condition the user should know about that does not stop the work.
type Warning struct {
	Code string
	Text string
}

// Event is one progress report, delivered from a single goroutine.
type Event struct {
	Time        time.Time
	Stage       Stage
	Kind        Kind
	Done, Total int64
	Unit        Unit
	Stats       *Stats   // set when Kind is KindStats
	Warn        *Warning // set when Kind is KindWarning
}

// emitter delivers events to an optional callback. It is used by one goroutine only.
type emitter func(Event)

func (e emitter) send(ev Event) {
	if e != nil {
		ev.Time = time.Now()
		e(ev)
	}
}

func (e emitter) stage(s Stage, k Kind, done, total int64, u Unit) {
	e.send(Event{Stage: s, Kind: k, Done: done, Total: total, Unit: u})
}

func (e emitter) warn(s Stage, w Warning) { e.send(Event{Stage: s, Kind: KindWarning, Warn: &w}) }
