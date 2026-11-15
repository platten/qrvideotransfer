package video

import "testing"

// TestTimedAlloc: the worker's allocator reserves the capacity asked for at the first allocation,
// records when, grows within it without moving, copies past it, and refuses more than the maximum.
func TestTimedAlloc(t *testing.T) {
	t.Parallel()
	m := &meter{}
	mem := timedAlloc{m}.Allocate(64, 256)
	b := mem.Reallocate(16)
	if len(b) != 16 || cap(b) != 64 || m.memAt.Load() == 0 {
		t.Fatalf("first allocation: len %d cap %d, reserved at %d", len(b), cap(b), m.memAt.Load())
	}
	b[0] = 7
	if c := mem.Reallocate(64); &c[0] != &b[0] || len(c) != 64 {
		t.Error("growing within the reservation moved the memory")
	}
	if c := mem.Reallocate(128); len(c) != 128 || c[0] != 7 {
		t.Errorf("growing past the reservation: len %d, first byte %d", len(c), c[0])
	}
	if mem.Reallocate(257) != nil {
		t.Error("more than the maximum was allocated")
	}
	mem.Free()
}
