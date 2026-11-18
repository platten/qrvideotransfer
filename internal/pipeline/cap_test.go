package pipeline

import "testing"

// TestBookkeepingUnderCap: the bytes a decode holds for frames it has seen, their soft bits and
// their per-frame records alike, stay under the cap however many frames a recording shows
// (SECURITY.md section 5, soft-bit store; DESIGN.md section 19). Records go once the soft bits are
// gone, least recently seen first, and the frames seen are still counted once each.
func TestBookkeepingUnderCap(t *testing.T) {
	key, truth := fixture(t)
	key.frames = 4096 // a long transfer whose descriptor has not decoded: nothing is ever attempted
	d := &decoder{c: &DecodeConfig{}, tmp: t.TempDir(), frames: map[uint32]*frameAcc{}, softCap: MaxSoftBytes}
	defer d.close()
	g, err := newGeometry(key)
	if err != nil {
		t.Fatal(err)
	}
	d.softCap = 40 * (frameRecordBytes(g.cpf) + int64(g.cpf*g.sent)) // room for about 40 frames
	soft := append([]int8(nil), truth[0]...)
	clear(soft[:g.sent]) // no descriptor: its frame count would contradict the altered header
	for i := range 1000 {
		if err := d.apply(captureResult{index: 3 * i, located: true, key: key, cells: 1, clean: 1,
			phases: []phaseBits{{frame: uint32(i), soft: soft}}}); err != nil {
			t.Fatal(err)
		}
		if u := d.usedBytes(); u > d.softCap {
			t.Fatalf("after %d frames %d bytes held, cap %d", i+1, u, d.softCap)
		}
	}
	d.sendStats()
	if d.stats.FramesSeen != 1000 {
		t.Errorf("%d frames seen, want 1000", d.stats.FramesSeen)
	}
	if n := len(d.frames); n == 0 || n >= 100 {
		t.Errorf("%d frame records kept", n)
	}
	if _, ok := d.frames[999]; !ok {
		t.Error("the most recent frame's record was dropped")
	}
}

// TestDroppedRecordCountsOnce: a frame whose record was dropped after its codewords were stored is
// decoded again when seen again, and its shards are not counted twice.
func TestDroppedRecordCountsOnce(t *testing.T) {
	key, truth := fixture(t)
	d := &decoder{c: &DecodeConfig{}, tmp: t.TempDir(), frames: map[uint32]*frameAcc{}, softCap: MaxSoftBytes}
	defer d.close()
	feed := func(base int) {
		for f := range uint32(4) {
			if err := d.apply(captureResult{index: base + int(f), located: true, key: key, cells: 1, clean: 1,
				phases: []phaseBits{{frame: f, soft: truth[f]}}}); err != nil {
				t.Fatal(err)
			}
		}
		if err := d.flush(); err != nil {
			t.Fatal(err)
		}
	}
	feed(0)
	ok := d.stats.CodewordsOK
	if ok == 0 {
		t.Fatal("nothing decoded")
	}
	d.softCap = -1
	d.evict()
	if len(d.frames) != 0 || d.usedBytes() != 0 {
		t.Fatalf("%d records and %d bytes left with no room", len(d.frames), d.usedBytes())
	}
	d.softCap = MaxSoftBytes
	feed(10)
	if d.stats.CodewordsOK != ok {
		t.Errorf("%d codewords counted after decoding the frames again, %d before", d.stats.CodewordsOK, ok)
	}
	d.sendStats()
	if d.stats.FramesSeen != 4 {
		t.Errorf("%d frames seen, want 4", d.stats.FramesSeen)
	}
}
