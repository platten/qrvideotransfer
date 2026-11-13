package video

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/tetratelabs/wazero/experimental"
)

// gray4 are the default Gray4 luma levels of FORMAT.md section 7.
var gray4 = [4]uint16{16, 89, 162, 235}

// codeFrame returns frame i of a test code: cells of cell x cell pixels, each one of the four grey
// levels, chosen from seed and i, and the symbols drawn.
func codeFrame(w, h, cell int, seed uint64, i int) (*Frame, []uint8) {
	r := rand.New(rand.NewPCG(seed, uint64(i)))
	gx, gy := w/cell, h/cell
	sym := make([]uint8, gx*gy)
	f := &Frame{W: w, H: h, Bits: 8, Y: make([]uint16, w*h), Cb: make([]uint16, w*h/4), Cr: make([]uint16, w*h/4)}
	for k := range sym {
		sym[k] = uint8(r.IntN(4))
		x0, y0 := (k%gx)*cell, (k/gx)*cell
		for y := y0; y < y0+cell; y++ {
			for x := x0; x < x0+cell; x++ {
				f.Y[y*w+x] = gray4[sym[k]]
			}
		}
	}
	for k := range f.Cb {
		f.Cb[k], f.Cr[k] = 128, 128
	}
	return f, sym
}

// readSymbols takes the nearest of the four levels at each cell's centre (the mean of its central
// 2x2 pixels).
func readSymbols(f *Frame, cell int) []uint8 {
	gx, gy := f.W/cell, f.H/cell
	out := make([]uint8, gx*gy)
	c := cell/2 - 1
	for k := range out {
		x, y := (k%gx)*cell+c, (k/gx)*cell+c
		v := int(f.Y[y*f.W+x]) + int(f.Y[y*f.W+x+1]) + int(f.Y[(y+1)*f.W+x]) + int(f.Y[(y+1)*f.W+x+1])
		best := 0
		for l := range gray4 {
			if abs(v-4*int(gray4[l])) < abs(v-4*int(gray4[best])) {
				best = l
			}
		}
		out[k] = uint8(best)
	}
	return out
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// encodeCode writes n frames of the test code to path and returns the symbols of each frame.
func encodeCode(t *testing.T, b Backend, path string, w, h, cell, fps, n int) [][]uint8 {
	t.Helper()
	e, err := b.NewEncoder(t.Context(), path, EncodeConfig{Width: w, Height: h, FPS: fps})
	if err != nil {
		t.Fatal(err)
	}
	var syms [][]uint8
	for i := range n {
		f, s := codeFrame(w, h, cell, 7, i)
		if err := e.WriteFrame(f); err != nil {
			t.Fatal(err)
		}
		syms = append(syms, s)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	return syms
}

// decodeAll reads every frame of a decoder, copying each.
func decodeAll(t *testing.T, d Decoder) []Frame {
	t.Helper()
	var out []Frame
	for {
		var f Frame
		err := d.Next(&f)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, f)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}

// embedded returns the embedded backend with the default compilation cache.
func embedded(t *testing.T) *Embedded {
	t.Helper()
	b, err := NewEmbedded(Options{})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// system returns the native backend, or skips the test when no ffmpeg is on PATH (the CI runners'
// images may lack one).
func system(t *testing.T) *System {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no system ffmpeg on PATH: the native backend is not tested on this machine")
	}
	b, err := NewSystem("system")
	if err != nil {
		t.Skip("system ffmpeg unusable: ", err)
	}
	return b
}

// clip is the path of a committed test clip.
func clip(name string) string { return filepath.Join("..", "..", "testdata", "video", name) }

// copyFile copies a file into dir and returns the new path.
func copyFile(t *testing.T, src, dir string) string {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, filepath.Base(src))
	if err := os.WriteFile(dst, b, 0o600); err != nil { //nolint:gosec // test
		t.Fatal(err)
	}
	return dst
}

// peakAlloc is a wazero memory allocator with the engine's ceiling that records the largest linear
// memory an instance reached. Unlike the engine's, it grows on demand instead of reserving the
// ceiling, so that the Go heap shows the memory an instance uses rather than its reservation.
type peakAlloc struct{ peak atomic.Uint64 }

func (p *peakAlloc) Allocate(c, maxSize uint64) experimental.LinearMemory {
	return &peakMem{max: maxSize, p: p} // c is the whole ceiling when the runtime reserves it
}

type peakMem struct {
	buf []byte
	max uint64
	p   *peakAlloc
}

func (m *peakMem) Reallocate(size uint64) []byte {
	if size > m.max {
		return nil
	}
	if size > uint64(cap(m.buf)) {
		nb := make([]byte, size, min(m.max, size+size/4))
		copy(nb, m.buf)
		m.buf = nb
	}
	m.buf = m.buf[:size]
	for {
		old := m.p.peak.Load()
		if size <= old || m.p.peak.CompareAndSwap(old, size) {
			return m.buf
		}
	}
}

func (m *peakMem) Free() { m.buf = nil }

// withPeak returns a context that makes instances started with it record their peak memory in p.
func withPeak(ctx context.Context, p *peakAlloc) context.Context {
	return experimental.WithMemoryAllocator(ctx, p)
}
