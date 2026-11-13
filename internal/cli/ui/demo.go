package ui

import (
	"time"

	qrvt "github.com/platten/qrvideotransfer"
)

// DemoInfo describes the recorded decode that DemoEvents plays.
func DemoInfo() Info {
	return Info{
		Command: "decode",
		Sources: []string{"IMG_0412.MOV", "IMG_0413.MOV"},
		Output:  "received",
		Video:   []Field{{"Recording", "3840x2160, 60 fps, HEVC"}, {"Code", "1920x1080, gray4, cell 4"}},
		Stages:  []qrvt.Stage{qrvt.StageDecodeVideo, qrvt.StageDemodulate, qrvt.StageRepair, qrvt.StageVerify, qrvt.StageUnpack},
		Signal:  true,
	}
}

// DemoEvents returns a recorded decode, as events stamped from start, for the hidden demo command
// and the golden tests. Every value is computed with integers so the script is the same everywhere.
func DemoEvents(start time.Time) []qrvt.Event {
	const (
		captures = 3600
		frames   = 840
		blocks   = 48
		size     = 50 << 20
	)
	ms := func(n int) time.Time { return start.Add(time.Duration(n) * time.Millisecond) }
	ev := func(t int, s qrvt.Stage, k qrvt.EventKind, done, total int64, u qrvt.Unit) qrvt.Event {
		return qrvt.Event{Time: ms(t), Stage: s, Kind: k, Done: done, Total: total, Unit: u}
	}
	warn := func(t int, s qrvt.Stage, c qrvt.WarningCode, text string) qrvt.Event {
		return qrvt.Event{Time: ms(t), Stage: s, Kind: qrvt.KindWarning, Warn: &qrvt.Warning{Code: c, Text: text}}
	}
	r := []qrvt.Event{
		ev(0, qrvt.StageDecodeVideo, qrvt.KindStarted, 0, captures, qrvt.UnitFrames),
		ev(0, qrvt.StageDemodulate, qrvt.KindStarted, 0, captures, qrvt.UnitFrames),
	}
	for i := 1; i <= 36; i++ {
		t := 500 * i
		ready := blocks * i * i / (36 * 36)
		r = append(r,
			ev(t, qrvt.StageDecodeVideo, qrvt.KindProgress, int64(100*i), captures, qrvt.UnitFrames),
			ev(t+100, qrvt.StageDemodulate, qrvt.KindProgress, int64(100*i-50), captures, qrvt.UnitFrames),
			qrvt.Event{Time: ms(t + 200), Kind: qrvt.KindStats, Stats: &qrvt.Stats{
				Captures: int64(100 * i), FramesSeen: min(frames, 30*i), FramesTotal: frames,
				CleanFraction: float64(900+i) / 1000, RawBitErrors: float64(200-3*i) / 10000,
				CodewordsOK: int64(1520 * i), BlocksReady: ready, BlocksTotal: blocks,
			}})
		switch i {
		case 10:
			r = append(r, warn(t+300, qrvt.StageDemodulate, qrvt.WarnClipping, "Highlights are clipped in 3.2% of cells; lower the screen brightness."))
		case 20:
			r = append(r, warn(t+300, qrvt.StageDecodeVideo, qrvt.WarnFramesDropped, "The recording skipped 12 frames."))
		}
	}
	r = append(r,
		ev(18400, qrvt.StageDecodeVideo, qrvt.KindFinished, captures, captures, qrvt.UnitFrames),
		ev(18500, qrvt.StageDemodulate, qrvt.KindFinished, captures, captures, qrvt.UnitFrames),
		ev(18500, qrvt.StageRepair, qrvt.KindStarted, 0, blocks, qrvt.UnitBlocks))
	for i := 1; i <= 4; i++ {
		r = append(r, ev(18500+250*i, qrvt.StageRepair, qrvt.KindProgress, int64(12*i), blocks, qrvt.UnitBlocks))
	}
	r = append(r,
		ev(19500, qrvt.StageRepair, qrvt.KindFinished, blocks, blocks, qrvt.UnitBlocks),
		ev(19500, qrvt.StageVerify, qrvt.KindStarted, 0, size, qrvt.UnitBytes),
		ev(19750, qrvt.StageVerify, qrvt.KindProgress, size/2, size, qrvt.UnitBytes),
		ev(20000, qrvt.StageVerify, qrvt.KindFinished, size, size, qrvt.UnitBytes),
		ev(20000, qrvt.StageUnpack, qrvt.KindStarted, 0, size, qrvt.UnitBytes))
	for i := 1; i <= 4; i++ {
		r = append(r, ev(20000+400*i, qrvt.StageUnpack, qrvt.KindProgress, int64(size/4*i), size, qrvt.UnitBytes))
	}
	return append(r, ev(21700, qrvt.StageUnpack, qrvt.KindFinished, size, size, qrvt.UnitBytes))
}

// DemoSummary is the summary that ends the demo.
func DemoSummary() *Summary {
	return &Summary{
		Title: "Decoded 12 files (50.0 MiB) into received",
		Fields: []Field{
			{"Recordings", "2 (3600 captures)"},
			{"Frames", "840 of 840 seen, 93.6% clean"},
			{"Encrypted", "no"},
			{"Signature", "unsigned"},
		},
	}
}
