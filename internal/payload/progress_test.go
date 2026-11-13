package payload

import (
	"strings"
	"testing"
)

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
