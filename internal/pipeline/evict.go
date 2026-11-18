package pipeline

import (
	"container/heap"
	"slices"
)

// The eviction order of the soft-bit store (SECURITY.md section 5): over the cap, the soft bits of
// the least confident frame go first, confidence being the mean magnitude of the soft bits it
// holds, then the record of the frame seen least recently. Two heaps keep that order as soft bits
// are added and codewords decoded, so that an eviction costs a few heap operations, not a scan of
// everything held (which cost about 0.5 s a capture at the full cap; DESIGN.md section 19).

// byConfidence orders the frames that hold soft bits, least confident first.
type byConfidence []*frameAcc

func (h byConfidence) Len() int { return len(h) }

// Less compares absSum/held without division: a's mean is below b's.
func (h byConfidence) Less(i, j int) bool {
	a, b := h[i], h[j]
	return a.absSum*b.held < b.absSum*a.held
}

func (h byConfidence) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].confPos, h[j].confPos = i, j
}

func (h *byConfidence) Push(x any) {
	fa := x.(*frameAcc) //nolint:forcetypeassert // only frames are pushed
	fa.confPos = len(*h)
	*h = append(*h, fa)
}

func (h *byConfidence) Pop() any {
	old := *h
	fa := old[len(old)-1]
	old[len(old)-1] = nil
	*h = old[:len(old)-1]
	fa.confPos = -1
	return fa
}

// bySeen orders the frame records, the one seen least recently first.
type bySeen []*frameAcc

func (h bySeen) Len() int           { return len(h) }
func (h bySeen) Less(i, j int) bool { return h[i].last < h[j].last }

func (h bySeen) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].seenPos, h[j].seenPos = i, j
}

func (h *bySeen) Push(x any) {
	fa := x.(*frameAcc) //nolint:forcetypeassert // only frames are pushed
	fa.seenPos = len(*h)
	*h = append(*h, fa)
}

func (h *bySeen) Pop() any {
	old := *h
	fa := old[len(old)-1]
	old[len(old)-1] = nil
	*h = old[:len(old)-1]
	fa.seenPos = -1
	return fa
}

// reorder puts fa back in its places in both orders after its soft bits or last capture changed.
func (d *decoder) reorder(fa *frameAcc) {
	switch {
	case fa.held > 0 && fa.confPos < 0:
		heap.Push(&d.byConf, fa)
	case fa.held > 0:
		heap.Fix(&d.byConf, fa.confPos)
	case fa.confPos >= 0:
		heap.Remove(&d.byConf, fa.confPos)
	}
	if fa.seenPos < 0 {
		heap.Push(&d.bySeen, fa)
	} else {
		heap.Fix(&d.bySeen, fa.seenPos)
	}
}

// dropSoft releases the soft bits of fa's codeword c.
func (d *decoder) dropSoft(fa *frameAcc, c int) {
	for _, v := range fa.soft[c] {
		fa.absSum -= int64(max(v, -v))
	}
	fa.held -= int64(len(fa.soft[c]))
	d.softBytes -= int64(len(fa.soft[c]))
	fa.soft[c] = nil
}

// evict keeps the soft bits and frame records held under the cap, in the order above. A frame
// whose record was dropped and that is seen again is decoded again; its shards are already stored,
// so nothing is counted twice (attempt).
func (d *decoder) evict() {
	for d.usedBytes() > d.softCap {
		switch {
		case len(d.byConf) > 0:
			fa := heap.Pop(&d.byConf).(*frameAcc) //nolint:forcetypeassert // only frames are pushed
			for c := range fa.soft {
				if fa.soft[c] != nil {
					d.dropSoft(fa, c)
				}
			}
		case len(d.bySeen) > 0:
			fa := heap.Pop(&d.bySeen).(*frameAcc) //nolint:forcetypeassert // only frames are pushed
			delete(d.frames, fa.frame)
			d.bookBytes -= frameRecordBytes(len(fa.done))
		default:
			return
		}
		d.evictSteps++
	}
	// A dropped frame may still be on the pending list (attemptDue and flush skip it), and a frame
	// seen again after its record went is listed again; compact the list, each frame held and
	// waiting once, when it outgrows twice the records, so that it stays within that.
	if len(d.pending) > 2*len(d.frames)+64 {
		listed := make(map[uint32]bool, len(d.frames))
		d.pending = slices.DeleteFunc(d.pending, func(f uint32) bool {
			fa := d.frames[f]
			drop := fa == nil || !fa.dirty || listed[f]
			listed[f] = true
			return drop
		})
	}
}
