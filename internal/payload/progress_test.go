package payload

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"strings"
	"testing"
)

// TestCancelEveryPhase cancels at the second progress report of each phase. In the check, write and
// commit phases Unpack returns the cancellation and leaves the output folder as it was. The finish
// phase only sets folder modes and times, bounded by the number of folders: it ignores cancellation,
// and Unpack succeeds with every file in place.
func TestCancelEveryPhase(t *testing.T) {
	src := tree{"d/": "", "d/a": "alpha", "d/e/": "", "d/e/b": strings.Repeat("b", 200<<10), "f": "f"}
	stream, _ := pack(t, PackOptions{Inputs: src.write(t, t.TempDir())})
	for _, ph := range []Phase{PhaseCheck, PhaseWrite, PhaseCommit, PhaseFinish} {
		ctx, cancel := context.WithCancel(context.Background())
		out := t.TempDir()
		_, err := Unpack(ctx, bytes.NewReader(stream), int64(len(stream)), UnpackOptions{OutputDir: out,
			Progress: func(p Progress) {
				if p.Phase == ph && p.Done > 0 {
					cancel()
				}
			}})
		cancel()
		if ph == PhaseFinish {
			if err != nil || !maps.Equal(read(t, out), src) {
				t.Errorf("cancelled in the finish phase: %v, output %v", err, read(t, out))
			}
			continue
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled in phase %d: %v", ph, err)
		}
		expectEmpty(t, out)
	}
}

// TestUnpackProgressEveryPhase checks that Unpack reports progress in each phase: the pre-check and the
// two commit walks count entries, writing counts file bytes. Within a phase Done never falls, never
// exceeds Total, and ends at Total.
func TestUnpackProgressEveryPhase(t *testing.T) {
	stream, res := pack(t, PackOptions{Inputs: tree{"d/": "", "d/a": "alpha", "d/e/": "", "d/e/b": strings.Repeat("b", 1000), "f": "f"}.write(t, t.TempDir())})
	entries := int64(res.Files + res.Dirs)
	var order []Phase
	last := map[Phase]Progress{}
	_, err := unpack(stream, UnpackOptions{OutputDir: t.TempDir(), Progress: func(p Progress) {
		prev, seen := last[p.Phase]
		if !seen {
			order = append(order, p.Phase)
		}
		if p.Done > p.Total || (seen && (p.Done < prev.Done || p.Total != prev.Total)) {
			t.Errorf("phase %d: %+v after %+v", p.Phase, p, prev)
		}
		last[p.Phase] = p
	}})
	must(t, err)
	want := []Phase{PhaseCheck, PhaseWrite, PhaseCommit, PhaseFinish}
	if len(order) != len(want) {
		t.Fatalf("phases reported in the order %v, want %v", order, want)
	}
	for i, ph := range want {
		total := entries
		if ph == PhaseWrite {
			total = res.Bytes
		}
		if order[i] != ph || last[ph].Done != total || last[ph].Total != total {
			t.Errorf("phase %d: last report %+v, want %d of %d", ph, last[ph], total, total)
		}
	}
}
