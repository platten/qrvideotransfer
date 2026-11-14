package ui

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"time"

	qrvt "github.com/platten/qrvideotransfer"
)

// jsonOut prints one JSON object per line to Out, following events.schema.json: a start line,
// every event, then a summary or an error line.
type jsonOut struct {
	out io.Writer
	err error // first write error
}

type jsonField struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type jsonWarning struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

type jsonStats struct {
	Captures      int64   `json:"captures"`
	FramesSeen    int     `json:"frames_seen"`
	FramesTotal   int     `json:"frames_total"`
	CleanFraction float64 `json:"clean_fraction"`
	RawBitErrors  float64 `json:"raw_bit_errors"`
	CodewordsOK   int64   `json:"codewords_ok"`
	BlocksReady   int     `json:"blocks_ready"`
	BlocksTotal   int     `json:"blocks_total"`
}

type jsonStart struct {
	Type    string      `json:"type"`
	Command string      `json:"command"`
	Sources []string    `json:"sources,omitempty"`
	Output  string      `json:"output,omitempty"`
	Video   []jsonField `json:"video,omitempty"`
	Stages  []string    `json:"stages,omitempty"`
}

type jsonEvent struct {
	Type    string       `json:"type"`
	Time    string       `json:"time"`
	Stage   string       `json:"stage"`
	Kind    string       `json:"kind"`
	Done    int64        `json:"done"`
	Total   int64        `json:"total"`
	Unit    string       `json:"unit,omitempty"`
	Stats   *jsonStats   `json:"stats,omitempty"`
	Warning *jsonWarning `json:"warning,omitempty"`
}

type jsonSummary struct {
	Type     string        `json:"type"`
	Title    string        `json:"title"`
	Fields   []jsonField   `json:"fields"`
	Warnings []jsonWarning `json:"warnings,omitempty"`
}

type jsonError struct {
	Type     string `json:"type"`
	Message  string `json:"message"`
	Guidance string `json:"guidance,omitempty"`
	Exit     int    `json:"exit"`
}

func newJSON(o Options, info Info) *jsonOut {
	j := &jsonOut{out: o.Out}
	start := jsonStart{Type: "start", Command: Clean(info.Command), Sources: cleanAll(info.Sources), Output: Clean(info.Output), Video: jsonFields(info.Video)}
	for _, s := range info.Stages {
		start.Stages = append(start.Stages, StageName(s))
	}
	j.write(start)
	return j
}

// write encodes one line, keeping the first write error. Values are clamped to the schema before,
// so encoding cannot fail; if it did, the line would be dropped rather than fail the job.
func (j *jsonOut) write(v any) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if enc.Encode(v) != nil {
		return
	}
	if _, err := j.out.Write(b.Bytes()); err != nil && j.err == nil {
		j.err = err
	}
}

// WriteJSONFailure prints a failure as the single error line of the --json output, for failures
// that happen before a job starts, such as usage errors.
func WriteJSONFailure(w io.Writer, f Failure) error {
	j := &jsonOut{out: w}
	j.write(jsonError{Type: "error", Message: Clean(f.Message), Guidance: Clean(f.Guidance), Exit: f.Exit})
	return j.err
}

// count clamps a count to the schema's minimum of 0.
func count[T int | int64](n T) T { return max(n, 0) }

// fraction clamps a fraction to [0, 1]; NaN becomes 0.
func fraction(x float64) float64 {
	if math.IsNaN(x) {
		return 0
	}
	return min(max(x, 0), 1)
}

func jsonFields(fs []Field) []jsonField {
	r := make([]jsonField, len(fs))
	for i, f := range fs {
		r[i] = jsonField{Clean(f.Key), Clean(f.Value)}
	}
	return r
}

func jsonWarn(w qrvt.Warning) jsonWarning {
	return jsonWarning{Clean(string(w.Code)), Clean(w.Text)}
}

// Event implements Renderer.
func (j *jsonOut) Event(e qrvt.Event) {
	je := jsonEvent{Type: "event", Time: e.Time.UTC().Format(time.RFC3339Nano), Stage: StageName(e.Stage), Kind: KindName(e.Kind),
		Done: count(e.Done), Total: count(e.Total), Unit: UnitName(e.Unit)}
	if s := e.Stats; s != nil {
		je.Stats = &jsonStats{count(s.Captures), count(s.FramesSeen), count(s.FramesTotal), fraction(s.CleanFraction),
			fraction(s.RawBitErrors), count(s.CodewordsOK), count(s.BlocksReady), count(s.BlocksTotal)}
	}
	if e.Warn != nil {
		w := jsonWarn(*e.Warn)
		je.Warning = &w
	}
	j.write(je)
}

// Pause implements Renderer.
func (j *jsonOut) Pause(fn func() error) error { return fn() }

// Finish implements Renderer.
func (j *jsonOut) Finish(s *Summary, f *Failure) error {
	switch {
	case f != nil:
		j.write(jsonError{Type: "error", Message: Clean(f.Message), Guidance: Clean(f.Guidance), Exit: f.Exit})
	case s != nil:
		js := jsonSummary{Type: "summary", Title: Clean(s.Title), Fields: jsonFields(s.Fields)}
		for _, w := range s.Warnings {
			js.Warnings = append(js.Warnings, jsonWarn(w))
		}
		j.write(js)
	}
	return j.err
}
