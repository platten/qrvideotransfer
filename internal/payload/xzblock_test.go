package payload

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"strings"
	"testing"
	"time"
)

// xzNone is a stream header with check type none.
var xzNone = func() []byte {
	h := []byte{0xfd, '7', 'z', 'X', 'Z', 0, 0, 0}
	return binary.LittleEndian.AppendUint32(h, crc32.ChecksumIEEE(h[6:8]))
}()

// rawBlock returns a block (check none) of the given LZMA2 chunks with a header of dictionary code
// 22 (8 MiB) and its index record; out is its uncompressed size.
func rawBlock(chunks []byte, out uint64) ([]byte, [2]uint64) {
	h := blockHeader(0, 0x21, 0x01, 22)
	b := append(append(bytes.Clone(h), chunks...), 0) // the end marker
	rec := [2]uint64{uint64(len(b)), out}
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b, rec
}

// tinyBlocks is the round-3 review's stream: n blocks, each one uncompressed byte with a dictionary
// reset and an 8 MiB dictionary.
func tinyBlocks(n int) []byte {
	var blocks []byte
	var recs [][2]uint64
	for range n {
		b, r := rawBlock([]byte{0x01, 0, 0, 'x'}, 1)
		blocks, recs = append(blocks, b...), append(recs, r)
	}
	return xzStream(xzNone, blocks, recs)
}

// TestXZOneBlock: a body has exactly one block. The review's streams of 20,000 and 100,000 tiny
// blocks are refused at the second block header, and what they allocate does not depend on their
// length: one dictionary for the first block, nothing for the rest.
func TestXZOneBlock(t *testing.T) {
	var allocs []uint64
	for _, n := range []int{20_000, 100_000} {
		data := tinyBlocks(n)
		caps := withDictHook(t)
		var err error
		alloc, _ := allocated(func() { _, err = decodeXZ(data) })
		if !isFormat(err) || !strings.Contains(err.Error(), "block") || len(*caps) > 1 {
			t.Fatalf("%d blocks: %v, %d decoders", n, err, len(*caps))
		}
		if alloc > XZDictCap+1<<20 {
			t.Fatalf("%d blocks: allocated %d MiB", n, alloc>>20)
		}
		allocs = append(allocs, alloc)
	}
	if d := int64(allocs[1]) - int64(allocs[0]); d > 64<<10 {
		t.Fatalf("allocation grows with the stream: %d then %d bytes", allocs[0], allocs[1])
	}
}

// TestXZManyChunks: one block of 500,000 one-byte uncompressed chunks (2 MB of input) decodes within
// 10 s and allocates one dictionary, at most 64 bytes per input byte, and the output buffer.
func TestXZManyChunks(t *testing.T) {
	const n = 500_000
	chunks := []byte{0x01, 0, 0, 'x'}
	for range n - 1 {
		chunks = append(chunks, 0x02, 0, 0, 'y')
	}
	b, r := rawBlock(chunks, n)
	data := xzStream(xzNone, b, [][2]uint64{r})
	var (
		out []byte
		err error
	)
	start := time.Now()
	alloc, _ := allocated(func() { out, err = decodeXZ(data) })
	d := time.Since(start)
	if err != nil || len(out) != n {
		t.Fatalf("decode: %v, %d bytes", err, len(out))
	}
	// io.ReadAll's growing output buffer is counted too: at most a few times the output.
	if bound := uint64(XZDictCap + 64*len(data) + 4*n); alloc > bound || (!raceEnabled && d > 10*time.Second) {
		t.Fatalf("%d input bytes: allocated %d MiB (bound %d MiB) in %v", len(data), alloc>>20, bound>>20, d)
	}
	t.Logf("%d chunks: %v, %d MiB allocated", n, d, alloc>>20)
}

// TestXZChunkRules: the block's first chunk resets the dictionary, no later chunk does, and only the
// first LZMA chunk sets properties, as encoders write a block. Later resets would let a stream make
// the decoder rebuild its state every few bytes.
func TestXZChunkRules(t *testing.T) {
	one := func(chunks []byte, out uint64) []byte {
		b, r := rawBlock(chunks, out)
		return xzStream(xzNone, b, [][2]uint64{r})
	}
	if out, err := decodeXZ(one([]byte{0x01, 0, 0, 'x', 0x02, 0, 0, 'y'}, 2)); err != nil || string(out) != "xy" {
		t.Fatalf("reference block: %v", err)
	}
	for name, data := range map[string][]byte{
		"first chunk without reset": one([]byte{0x02, 0, 0, 'x'}, 1),
		"second dictionary reset":   one([]byte{0x01, 0, 0, 'x', 0x01, 0, 0, 'y'}, 2),
		"properties mid-block":      propsMidBlock(t),
	} {
		if _, err := decodeXZ(data); !isFormat(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// propsMidBlock returns the encoder's one-block stream of the fixture sample with the properties
// byte of its first LZMA chunk repeated in its second LZMA chunk (control bit 0x40 set). The stream
// is otherwise valid: other decoders accept a properties change mid-block.
func propsMidBlock(t *testing.T) []byte {
	t.Helper()
	good := xzOf(t, xzSample(""), 0)
	block, recs := xzParts(t, good)
	var props byte
	for i, lz := (int(block[0])+1)*4, 0; block[i] != 0; lz++ {
		c := block[i]
		if c < 0x80 { // uncompressed
			i += 3 + int(binary.BigEndian.Uint16(block[i+1:])) + 1
			lz--
			continue
		}
		if lz == 0 {
			props = block[i+5]
		} else if c&0x40 == 0 {
			b := append(bytes.Clone(block[:i]), c|0x40)
			b = append(append(b, block[i+1:i+5]...), props)
			b = append(b, block[i+5:]...)
			recs[0][0]++
			return xzStream(good, repad(b, int(recs[0][0])-8, 8), recs)
		}
		n := 5
		if c&0x40 != 0 {
			n = 6
		}
		i += n + int(binary.BigEndian.Uint16(block[i+3:])) + 1
	}
	t.Fatal("no second LZMA chunk without properties")
	return nil
}

// repad returns block with its padding rebuilt for n bytes of header and data before a check of c
// bytes, the check being the last c bytes of block.
func repad(block []byte, n, c int) []byte {
	check := block[len(block)-c:]
	b := bytes.Clone(block[:n])
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return append(b, check...)
}
