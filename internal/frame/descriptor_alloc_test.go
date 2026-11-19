package frame

import (
	"math"
	"runtime"
	"testing"

	"github.com/platten/qrvideotransfer/internal/fec"
)

// SECURITY.md section 9.1 asks of descriptor parsing and plan derivation "no allocation before
// validation": nothing read from the descriptor sizes an allocation before ParseDescriptor has
// accepted it. Allocation is measured in bytes, so a buffer sized by a claimed field shows however
// few calls make it; the claims below are the largest each field can carry, where such a buffer
// would be hundreds of kilobytes or more.

// maxRefusalBytes bounds what ParseDescriptor may allocate to refuse a descriptor: its error value
// and message, a few hundred bytes whatever the descriptor claims. A buffer sized by K + M, the
// smallest of the claims below, would be 4 x 65,535 bytes.
const maxRefusalBytes = 1024

// planSlack is what fec.NewPlan may allocate for a plan besides its shard order of 4 (K + M)
// bytes: the allocator rounds a large buffer up to whole 8 KiB pages (measured: 139,296 bytes for
// K + M = 32,769, against 131,076).
const planSlack = 16 << 10

// allocBytes returns the fewest heap bytes one call of f allocated over three calls; another
// goroutine's allocations can only add to a call's count.
func allocBytes(f func()) uint64 {
	best := uint64(math.MaxUint64)
	var a, b runtime.MemStats
	for range 3 {
		runtime.ReadMemStats(&a)
		f()
		runtime.ReadMemStats(&b)
		best = min(best, b.TotalAlloc-a.TotalAlloc)
	}
	return best
}

// checkDescriptorAllocs parses b as a descriptor of the frame header h with c codewords per frame
// and derives the plan of an accepted descriptor, as the decoder does. A refusal must allocate at
// most maxRefusalBytes; an acceptance nothing, and its plan at most its shard order and
// planSlack, after validation.
func checkDescriptorAllocs(t *testing.T, b []byte, h Header, c int) {
	t.Helper()
	var d Descriptor
	var err error
	n := allocBytes(func() { d, err = ParseDescriptor(b, h, c) })
	if err != nil {
		if n > maxRefusalBytes {
			t.Fatalf("refusing %x (%v) allocated %d bytes, more than %d", b, err, n, maxRefusalBytes)
		}
		return
	}
	if n != 0 {
		t.Fatalf("accepting %x allocated %d bytes", b, n)
	}
	var perr error
	n = allocBytes(func() { _, perr = fec.NewPlan(d.StreamLen, d.ShardSize, d.M, d.OrderSeed) })
	if perr != nil {
		t.Fatalf("accepted descriptor %+v has no plan: %v", d, perr)
	}
	if limit := uint64(4*(d.K+d.M) + planSlack); n > limit {
		t.Fatalf("the plan of %+v allocated %d bytes, more than %d", d, n, limit)
	}
}

// hostileClaims returns valid-CRC descriptors that pass the checks before the one they defeat and
// claim the largest values their fields carry, each with the header and C it is parsed against.
func hostileClaims() []struct {
	name string
	b    []byte
	h    Header
	c    int
} {
	d, h, c := exampleDescriptor()
	with := func(f func(*Descriptor)) []byte {
		x := d
		f(&x)
		b := x.Marshal()
		return b[:]
	}
	maxFramesHeader := h
	maxFramesHeader.TotalFrames = fec.MaxFrames
	largest := fec.Plan{Blocks: 1, K: fec.MaxShards, M: fec.MaxShards, Packets: 2 * fec.MaxShards}.Frames(c)
	largestHeader := h
	largestHeader.TotalFrames = uint32(largest)
	return []struct {
		name string
		b    []byte
		h    Header
		c    int
	}{
		{"M 65535", with(func(x *Descriptor) { x.M = 0xffff }), h, c},
		{"K 65535", with(func(x *Descriptor) { x.K = 0xffff }), h, c},
		{"blocks 2^24-1", with(func(x *Descriptor) { x.Blocks = 1<<24 - 1 }), h, c},
		{"stream 2^48-1", with(func(x *Descriptor) { x.StreamLen = fec.MaxStreamLen }), h, c},
		{"stream, blocks and K at their largest", with(func(x *Descriptor) {
			x.StreamLen, x.Blocks, x.K, x.M = fec.MaxStreamLen, 1<<24-1, 0xffff, fec.MaxShards
		}), h, c},
		{"frames at the header's largest", with(func(x *Descriptor) { x.Frames = fec.MaxFrames }), maxFramesHeader, c},
		{"C 65535", with(func(x *Descriptor) { x.CodewordsPerFrame = 0xffff }), h, 0xffff},
		{"largest valid plan", with(func(x *Descriptor) {
			x.StreamLen, x.Blocks, x.K, x.M, x.Frames = int64(fec.MaxShards)*128, 1, fec.MaxShards, fec.MaxShards, largest
		}), largestHeader, c},
	}
}

// TestDescriptorNoAllocBeforeValidation: the hostile corpus and the largest claims allocate no
// more than an error to refuse, and a valid descriptor nothing to accept.
func TestDescriptorNoAllocBeforeValidation(t *testing.T) {
	d, h, c := exampleDescriptor()
	good := d.Marshal()
	if n := allocBytes(func() { _, _ = ParseDescriptor(good[:], h, c) }); n != 0 {
		t.Errorf("accepting a valid descriptor allocated %d bytes", n)
	}
	checkDescriptorAllocs(t, good[:], h, c)
	for _, k := range hostileClaims() {
		t.Run(k.name, func(t *testing.T) {
			if _, err := ParseDescriptor(k.b, k.h, k.c); (err == nil) != (k.name == "largest valid plan") {
				t.Fatalf("ParseDescriptor: %v", err)
			}
			checkDescriptorAllocs(t, k.b, k.h, k.c)
		})
	}
	for _, k := range hostileDescriptors(t) {
		t.Run(k.Name, func(t *testing.T) { checkDescriptorAllocs(t, k.bytes, h, c) })
	}
}
