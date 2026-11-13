package ui

import (
	"encoding/json"
	"time"

	qrvt "github.com/platten/qrvideotransfer"
)

// jsonOut prints one JSON object per line to Out, following events.schema.json: a start line,
// every event, then a summary or an error line.
type jsonOut struct {
	enc *json.Encoder
	err error
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
	j := &jsonOut{enc: json.NewEncoder(o.Out)}
	j.enc.SetEscapeHTML(false)
	start := jsonStart{Type: "start", Command: Clean(info.Command), Sources: cleanAll(info.Sources), Output: Clean(info.Output), Video: jsonFields(info.Video)}
	for _, s := range info.Stages {
		start.Stages = append(start.Stages, StageName(s))
	}
	j.write(start)
	return j
}

// write encodes one line, keeping the first error.
func (j *jsonOut) write(v any) {
	if err := j.enc.Encode(v); err != nil && j.err == nil {
		j.err = err
	}
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
		Done: e.Done, Total: e.Total, Unit: UnitName(e.Unit)}
	if s := e.Stats; s != nil {
		je.Stats = &jsonStats{s.Captures, s.FramesSeen, s.FramesTotal, s.CleanFraction, s.RawBitErrors, s.CodewordsOK, s.BlocksReady, s.BlocksTotal}
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
