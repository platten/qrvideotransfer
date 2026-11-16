package video

import (
	"time"

	"github.com/tetratelabs/wazero/experimental"
)

// timedAlloc is the linear-memory allocator of every engine instance: it allocates as wazero's own
// does (a Go slice with the capacity wazero asks for, here the whole 512 MiB reservation, grown by
// copying only past it), and records in m when the reservation has been made, for a time-bound
// error's breakdown, when the memory is freed (as the module closes), and the largest size the
// instance reached.
type timedAlloc struct{ m *meter }

// Allocate implements experimental.MemoryAllocator.
func (a timedAlloc) Allocate(capacity, maxSize uint64) experimental.LinearMemory {
	return &timedMem{m: a.m, capacity: capacity, max: maxSize}
}

type timedMem struct {
	m             *meter
	buf           []byte
	capacity, max uint64
}

// Reallocate implements experimental.LinearMemory.
func (t *timedMem) Reallocate(size uint64) []byte {
	if size > t.max {
		return nil
	}
	if t.buf == nil || size > uint64(cap(t.buf)) {
		if t.buf == nil {
			beforeReserve(t.m)
		}
		nb := make([]byte, size, max(size, t.capacity))
		copy(nb, t.buf)
		t.buf = nb
		t.m.reserved()
	}
	t.buf = t.buf[:size]
	if int64(size) > t.m.memPeak.Load() { //nolint:gosec // size is at most the 512 MiB ceiling
		t.m.memPeak.Store(int64(size)) //nolint:gosec // as above
	}
	return t.buf
}

// beforeReserve is called with the instance's meter just before its linear memory is reserved;
// tests delay the reservation with it.
var beforeReserve = func(*meter) {}

// Free implements experimental.LinearMemory. wazero frees the memory as it closes the module.
func (t *timedMem) Free() {
	t.buf = nil
	t.m.freedAt.CompareAndSwap(0, meterStamp(time.Now()))
}
