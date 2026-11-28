package calib

import (
	"errors"
	"testing"

	"github.com/platten/qrvideotransfer/internal/frame"
)

// presets are the video sizes of `--resolution` (DESIGN.md section 13).
var presets = []Size{{848, 480}, {1280, 720}, {1920, 1080}, {2560, 1440}, {3840, 2160}}

// monitors are the supported monitor sizes the property tests cover: the presets and common
// others, including sizes no cell divides.
var monitors = append([]Size{{1366, 768}, {1600, 900}, {1920, 1200}, {2560, 1600}, {3200, 2000}, {3440, 1440}, {5120, 2880}, {1024, 768}}, presets...)

// The slots are contiguous, in the plan's order, and their frames add up to F, a multiple of 4.
func TestSlots(t *testing.T) {
	if len(slots) != 36 || TotalFrames != 4896 || TotalFrames%4 != 0 {
		t.Fatalf("%d slots, F %d", len(slots), TotalFrames)
	}
	var idx uint32
	for i, s := range slots {
		if s.FirstIndex != idx || s.CodeFrames != s.Rate*SegmentSeconds {
			t.Fatalf("slot %d: %+v, want first index %d", i, s, idx)
		}
		idx += uint32(s.CodeFrames)
	}
}

// Every preset size has the plan of DESIGN.md section 13: the cells that divide it, no grid below
// 120 x 90, every segment within condition 4's cap, and the documented length.
func TestPlanPresets(t *testing.T) {
	want := map[Size]int{{848, 480}: 36, {1280, 720}: 72, {1920, 1080}: 104, {2560, 1440}: 100, {3840, 2160}: 120}
	for _, sz := range presets {
		p, err := NewPlan(sz.W, sz.H)
		if err != nil {
			t.Fatal(err)
		}
		if p.Seconds() != want[sz] {
			t.Errorf("%v: %d s, want %d", sz, p.Seconds(), want[sz])
		}
		shown := map[Slot]bool{}
		for i, s := range p.Segments {
			shown[s.Slot] = true
			if i > 0 && s.FirstIndex <= p.Segments[i-1].FirstIndex {
				t.Errorf("%v: segments out of order", sz)
			}
			if sz.W%s.Cell != 0 || sz.H%s.Cell != 0 || s.GridX*s.Cell != sz.W || s.GridY*s.Cell != sz.H {
				t.Errorf("%v: segment %+v", sz, s)
			}
			if s.GridX < MinGridX || s.GridY < MinGridY {
				t.Errorf("%v: small grid %d x %d", sz, s.GridX, s.GridY)
			}
			if DisplayBitrate(s.GridX, s.GridY, s.Rate, s.Mode) > MaxDisplayBitrate {
				t.Errorf("%v: segment %+v over the cap", sz, s)
			}
		}
		// Every slot left out has a reason.
		for _, s := range slots {
			if shown[s] {
				continue
			}
			gx, gy := sz.W/max(s.Cell, 1), sz.H/max(s.Cell, 1)
			if sz.W%s.Cell == 0 && sz.H%s.Cell == 0 && gx >= MinGridX && gy >= MinGridY &&
				DisplayBitrate(gx, gy, s.Rate, s.Mode) <= MaxDisplayBitrate {
				t.Errorf("%v: slot %+v left out", sz, s)
			}
		}
	}
	// 4K at 4-pixel cells: only black and white and four greys at 12 and 30 fps fit the cap.
	p, _ := NewPlan(3840, 2160)
	n := 0
	for _, s := range p.Segments {
		if s.Cell == 4 {
			n++
			if s.Rate == 60 || s.Mode == ModeColor {
				t.Errorf("4K segment %+v over the cap", s)
			}
		}
	}
	if n != 4 {
		t.Errorf("4K has %d segments of 4-pixel cells, want 4", n)
	}
}

// A size no cell gives a large enough grid has no plan.
func TestNewPlanRefuses(t *testing.T) {
	for _, sz := range []Size{{0, 0}, {-4, 4}, {400, 300}, {1366, 768}, {478, 360}} {
		if _, err := NewPlan(sz.W, sz.H); !errors.Is(err, ErrPlan) {
			t.Errorf("%v: %v, want ErrPlan", sz, err)
		}
	}
}

// Every frame of every supported monitor size's plan has a header a strict FORMAT.md parser
// accepts (encoded, decoded with DecodeHeaderReason, and Validate), and recovers to the same plan
// and segment.
func TestRecoverEveryFrame(t *testing.T) {
	for _, sz := range monitors {
		p, err := NewPlan(sz.W, sz.H)
		if err != nil {
			continue
		}
		for _, s := range p.Segments {
			for k := range s.CodeFrames {
				h := s.Header(s.FirstIndex + uint32(k))
				if k == 0 || k == s.CodeFrames-1 {
					// The strict parser, on the first and last frame of each segment.
					slots := h.Encode()
					soft := make([]int8, frame.HeaderSlots)
					for i, b := range slots {
						soft[i] = 100 // bit 0, dark
						if b {
							soft[i] = -100
						}
					}
					got, reason := frame.DecodeHeaderReason(soft)
					if reason != frame.HeaderOK || got != h {
						t.Fatalf("%v %+v: header %+v read as %+v (%v)", sz, s, h, got, reason)
					}
				}
				rp, rs, err := Recover(h)
				if err != nil {
					t.Fatalf("%v, segment %+v, frame %d: %v", sz, s, h.FrameIndex, err)
				}
				if rp.Width != sz.W || rp.Height != sz.H || rs != s || len(rp.Segments) != len(p.Segments) {
					t.Fatalf("%v, segment %+v: recovered %dx%d, %+v", sz, s, rp.Width, rp.Height, rs)
				}
			}
		}
	}
}

// Property: for every supported monitor size and every frame it shows (every index of every
// segment), exactly one slot, and so
// one video size, is consistent with the header's (index, grid, mode), counted by brute force over
// every slot rather than through Recover.
func TestRecoverUnique(t *testing.T) {
	for _, sz := range monitors {
		p, err := NewPlan(sz.W, sz.H)
		if err != nil {
			continue
		}
		for _, s := range p.Segments {
			for k := range s.CodeFrames {
				idx := s.FirstIndex + uint32(k) //nolint:gosec // small
				n := 0
				for _, o := range slots {
					if idx < o.FirstIndex || idx >= o.FirstIndex+uint32(o.CodeFrames) || o.Mode != s.Mode {
						continue
					}
					if seg, ok := segmentOf(o, s.GridX*o.Cell, s.GridY*o.Cell); ok && seg.GridX == s.GridX && seg.GridY == s.GridY {
						n++
					}
				}
				if n != 1 {
					t.Errorf("%v, segment %+v, frame %d: %d consistent slots", sz, s, idx, n)
				}
			}
		}
	}
}

// Inconsistent headers are refused with ErrNotCalibration.
func TestRecoverRefuses(t *testing.T) {
	p, _ := NewPlan(1920, 1080)
	s := p.Segments[0] // 12 fps, 4-pixel cells, black and white: 480 x 270
	ok := s.Header(s.FirstIndex)
	slot4K := func(rate, cell int, mode uint8) Slot {
		for _, o := range slots {
			if o.Rate == rate && o.Cell == cell && o.Mode == mode {
				return o
			}
		}
		t.Fatal("no slot")
		return Slot{}
	}
	capped := slot4K(30, 4, ModeColor)
	small := slot4K(12, 16, ModeBW)
	cases := map[string]func(h *frame.Header){
		"session":        func(h *frame.Header) { h.Session = 0x5333 },
		"inner code":     func(h *frame.Header) { h.InnerCode = 4 },
		"frame count":    func(h *frame.Header) { h.TotalFrames = 4892 },
		"mode":           func(h *frame.Header) { h.Modulation = ModeGray4 },
		"invalid":        func(h *frame.Header) { h.Reserved = 1 },
		"index beyond F": func(h *frame.Header) { h.FrameIndex = TotalFrames },
		"small grid":     func(h *frame.Header) { h.FrameIndex, h.GridX, h.GridY = small.FirstIndex, 120, 67 },
		"over the cap": func(h *frame.Header) {
			h.FrameIndex, h.Modulation, h.GridX, h.GridY = capped.FirstIndex, ModeColor, 960, 540
		},
		"grid too small": func(h *frame.Header) { h.GridX, h.GridY = 100, 60 },
	}
	for name, edit := range cases {
		h := ok
		edit(&h)
		if _, _, err := Recover(h); !errors.Is(err, ErrNotCalibration) {
			t.Errorf("%s: %v, want ErrNotCalibration", name, err)
		}
	}
	if _, _, err := Recover(ok); err != nil {
		t.Fatal(err)
	}
}

// FuzzRecoverPlan: Recover never panics on any header, and accepts one only when it is a frame of
// the plan it returns: the plan is NewPlan's for its size, shows the segment, and the segment's
// header for that index is the header given (SECURITY.md section 9.1).
func FuzzRecoverPlan(f *testing.F) {
	p, _ := NewPlan(3840, 2160)
	for _, s := range p.Segments {
		h := s.Header(s.FirstIndex)
		f.Add(h.Version, h.Session, h.FrameIndex, h.TotalFrames, h.GridX, h.GridY, h.Modulation, h.InnerCode, h.Reserved)
	}
	f.Add(uint8(1), uint16(Session), uint32(0), TotalFrames, uint16(2047), uint16(2047), uint8(1), uint8(2), uint8(0))
	f.Fuzz(func(t *testing.T, version uint8, session uint16, idx, total uint32, gx, gy uint16, mode, inner, reserved uint8) {
		h := frame.Header{Version: version, Session: session, FrameIndex: idx, TotalFrames: total,
			GridX: gx, GridY: gy, Modulation: mode, InnerCode: inner, Reserved: reserved}
		rp, rs, err := Recover(h)
		if err != nil {
			if !errors.Is(err, ErrNotCalibration) {
				t.Fatalf("error %v does not wrap ErrNotCalibration", err)
			}
			return
		}
		want, err := NewPlan(rp.Width, rp.Height)
		if err != nil || len(want.Segments) != len(rp.Segments) {
			t.Fatalf("recovered plan %dx%d is not NewPlan's: %v", rp.Width, rp.Height, err)
		}
		found := false
		for _, s := range want.Segments {
			found = found || s == rs
		}
		if !found || idx < rs.FirstIndex || idx >= rs.FirstIndex+uint32(rs.CodeFrames) || rs.Header(idx) != h {
			t.Fatalf("header %+v accepted as segment %+v of %dx%d", h, rs, rp.Width, rp.Height)
		}
	})
}

func TestSegmentSetting(t *testing.T) {
	p, _ := NewPlan(1920, 1080)
	s := p.Segments[len(p.Segments)-1]
	if got := s.Setting(Fast); got != (Setting{Rate: s.Rate, Cell: s.Cell, Mode: s.Mode, Inner: Fast}) {
		t.Errorf("%+v", got)
	}
}
