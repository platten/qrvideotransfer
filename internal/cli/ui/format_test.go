package ui

import (
	"testing"
	"time"

	qrvt "github.com/platten/qrvideotransfer"
)

func TestNames(t *testing.T) {
	stages := map[qrvt.Stage]string{
		0: "none", qrvt.StagePack: "pack", qrvt.StageProtect: "protect", qrvt.StageRender: "render",
		qrvt.StageEncodeVideo: "encode-video", qrvt.StageDecodeVideo: "decode-video",
		qrvt.StageDemodulate: "demodulate", qrvt.StageRepair: "repair", qrvt.StageVerify: "verify",
		qrvt.StageUnpack: "unpack", 99: "unknown",
	}
	for s, want := range stages {
		if got := StageName(s); got != want {
			t.Errorf("StageName(%d) = %q, want %q", s, got, want)
		}
	}
	kinds := map[qrvt.EventKind]string{
		qrvt.KindStarted: "started", qrvt.KindProgress: "progress", qrvt.KindFinished: "finished",
		qrvt.KindStats: "stats", qrvt.KindWarning: "warning", 0: "unknown", 42: "unknown",
	}
	for k, want := range kinds {
		if got := KindName(k); got != want {
			t.Errorf("KindName(%d) = %q, want %q", k, got, want)
		}
	}
	units := map[qrvt.Unit]string{qrvt.UnitBytes: "bytes", qrvt.UnitFrames: "frames", qrvt.UnitBlocks: "blocks", 0: "", 9: ""}
	for u, want := range units {
		if got := UnitName(u); got != want {
			t.Errorf("UnitName(%d) = %q, want %q", u, got, want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	tests := map[int64]string{
		0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 1536: "1.5 KiB", 10 << 20: "10.0 MiB",
		16 << 30: "16.0 GiB", 3 << 40: "3.0 TiB", 5 << 50: "5120.0 TiB", -5: "0 B",
	}
	for n, want := range tests {
		if got := FormatBytes(n); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := map[time.Duration]string{
		0: "0:00", -time.Second: "0:00", 499 * time.Millisecond: "0:00", 500 * time.Millisecond: "0:01",
		65 * time.Second: "1:05", 3725 * time.Second: "1:02:05", 100 * time.Hour: "100:00:00",
	}
	for d, want := range tests {
		if got := FormatDuration(d); got != want {
			t.Errorf("FormatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestFormatRate(t *testing.T) {
	tests := []struct {
		u    qrvt.Unit
		r    float64
		want string
	}{
		{qrvt.UnitBytes, 1536, "1.5 KiB/s"},
		{qrvt.UnitFrames, 8.44, "8.4 frames/s"},
		{qrvt.UnitBlocks, 3, "3.0 blocks/s"},
		{0, 2, "2.0/s"},
	}
	for _, tc := range tests {
		if got := formatRate(tc.u, tc.r); got != tc.want {
			t.Errorf("formatRate(%d, %v) = %q, want %q", tc.u, tc.r, got, tc.want)
		}
	}
}

func TestFormatAmount(t *testing.T) {
	tests := []struct {
		u    qrvt.Unit
		n    int64
		want string
	}{
		{qrvt.UnitBytes, 2048, "2.0 KiB"},
		{qrvt.UnitFrames, 12, "12 frames"},
		{qrvt.UnitBlocks, 1, "1 block"},
		{qrvt.UnitFrames, 1, "1 frame"},
		{0, 7, "7"},
	}
	for _, tc := range tests {
		if got := formatAmount(tc.u, tc.n); got != tc.want {
			t.Errorf("formatAmount(%d, %d) = %q, want %q", tc.u, tc.n, got, tc.want)
		}
	}
}
