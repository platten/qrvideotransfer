package payload

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// lzmaX returns the encoder's one LZMA chunk (control 0xE0, with properties) for 64 bytes "x" and
// that chunk's compressed data, which decodes from a fresh state to the same 64 bytes.
func lzmaX(t testing.TB) (first, data []byte) {
	t.Helper()
	good := xzOf(t, bytes.Repeat([]byte("x"), 64), 0)
	c := 12 + (int(good[12])+1)*4
	if good[c] < 0xe0 {
		t.Fatalf("first chunk %#x", good[c])
	}
	n := int(binary.BigEndian.Uint16(good[c+3:c+5])) + 1
	return good[c : c+6+n], good[c+6 : c+6+n]
}

// stateReset returns an LZMA chunk with a state reset (control 0xA0) holding data, which decodes to
// 64 bytes.
func stateReset(data []byte) []byte {
	return append([]byte{0xa0, 0, 63, byte((len(data) - 1) >> 8), byte(len(data) - 1)}, data...)
}

// stateResets is the round-4 review's stream (TestR4ManyStateResets): one block of the 64-byte
// chunk and then n-1 chunks that reset the LZMA state, each making the library allocate new
// literal tables.
func stateResets(t testing.TB, n int) []byte {
	first, data := lzmaX(t)
	chunks := bytes.Clone(first)
	for range n - 1 {
		chunks = append(chunks, stateReset(data)...)
	}
	b, r := rawBlock(chunks, uint64(64*n))
	return xzStream(xzNone, b, [][2]uint64{r})
}

// TestXZStateResets: an LZMA chunk may reset the state only directly after uncompressed data, and
// at most once per 32 KiB of uncompressed data so far in the block, which is how XZ Utils writes
// them (testdata/xz/mixed.xz). The review's streams of 1,000 and 200,000 state resets are refused
// with at most one dictionary allocated.
func TestXZStateResets(t *testing.T) {
	for _, n := range []int{1_000, 200_000} {
		data := stateResets(t, n)
		var err error
		alloc, _ := allocated(func() { _, err = decodeXZ(data) })
		if !isFormat(err) || alloc > XZDictCap+1<<20 {
			t.Fatalf("%d state resets: %v, allocated %d MiB", n, err, alloc>>20)
		}
	}
	first, data := lzmaX(t)
	one := func(chunks ...[]byte) []byte {
		var all []byte
		var out uint64
		for _, c := range chunks {
			all = append(all, c...)
			if c[0] < 0x80 {
				out += uint64(binary.BigEndian.Uint16(c[1:])) + 1
			} else {
				out += 64
			}
		}
		b, r := rawBlock(all, out)
		return xzStream(xzNone, b, [][2]uint64{r})
	}
	unc := func(n int) []byte { // an uncompressed chunk of n bytes, n at most 64 KiB
		return append([]byte{0x02, byte((n - 1) >> 8), byte(n - 1)}, pseudoRandom(n)...)
	}
	want := append(append(bytes.Repeat([]byte("x"), 64), pseudoRandom(40<<10)...), bytes.Repeat([]byte("x"), 64)...)
	if out, err := decodeXZ(one(first, unc(40<<10), stateReset(data))); err != nil || !bytes.Equal(out, want) {
		t.Fatalf("a state reset after 40 KiB of uncompressed data: %v", err)
	}
	for name, s := range map[string][]byte{
		"after an LZMA chunk":                 one(first, stateReset(data)),
		"after 1 KiB of uncompressed data":    one(first, unc(1<<10), stateReset(data)),
		"two after 40 KiB of uncompressed":    one(first, unc(40<<10), stateReset(data), unc(1), stateReset(data)),
		"after uncompressed and LZMA, in one": one(first, unc(40<<10), stateReset(data), stateReset(data)),
	} {
		if _, err := decodeXZ(s); !isFormat(err) {
			t.Errorf("state reset %s: %v", name, err)
		}
	}
}

// TestXZEncoderNeverResetsState: qrvt's encoder (ulikunitz/xz) writes no state reset other than the
// first LZMA chunk's (which also sets the properties), even for data that alternates incompressible
// and compressible stretches.
func TestXZEncoderNeverResetsState(t *testing.T) {
	good := xzOf(t, xzSample("mixed.xz"), 0)
	block, _ := xzParts(t, good)
	kinds := map[byte]int{}
	for i := (int(block[0]) + 1) * 4; block[i] != 0; {
		c := block[i]
		kinds[c&0xe0]++
		if c < 0x80 {
			i += 3 + int(binary.BigEndian.Uint16(block[i+1:])) + 1
			continue
		}
		n := 5
		if c&0x40 != 0 {
			n = 6
		}
		i += n + int(binary.BigEndian.Uint16(block[i+3:])) + 1
	}
	if kinds[0xa0] != 0 || kinds[0xc0]+kinds[0xe0] > 1 {
		t.Fatalf("chunk kinds %v", kinds)
	}
	t.Logf("chunk kinds by control & 0xE0: %v", kinds)
}
