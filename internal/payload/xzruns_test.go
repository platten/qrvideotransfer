package payload

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/ulikunitz/xz/lzma"
)

// alternatingRuns returns one block of n pairs of a one-byte uncompressed chunk and a small LZMA
// chunk, written by the library's LZMA2 writer with a flush after each piece.
func alternatingRuns(t testing.TB, n int) ([]byte, int) {
	var lz bytes.Buffer
	w, err := lzma.Writer2Config{DictCap: XZDictCap}.NewWriter2(&lz)
	must(t, err)
	rnd := pseudoRandom(n)
	for i := range n {
		_, err = w.Write(rnd[i : i+1])
		must(t, errors.Join(err, w.Flush()))
		_, err = w.Write(bytes.Repeat([]byte{'a'}, 64))
		must(t, errors.Join(err, w.Flush()))
	}
	must(t, w.Close())
	b, r := rawBlock(bytes.TrimSuffix(lz.Bytes(), []byte{0}), uint64(65*n))
	return xzStream(xzNone, b, [][2]uint64{r}), 65 * n
}

// TestXZUncompressedRuns: the library allocates a 32 KiB copy buffer for each uncompressed chunk it
// is handed, so a block may start a new run of uncompressed chunks only once per 32 KiB of
// uncompressed data before it (plus the first run). 50,000 alternations of a one-byte uncompressed
// chunk and an LZMA chunk are refused at the second run, with at most one dictionary allocated;
// XZ Utils' runs of about 60 KiB (testdata/xz/mixed.xz) and qrvt's encoder's output decode.
func TestXZUncompressedRuns(t *testing.T) {
	data, _ := alternatingRuns(t, 50_000)
	var err error
	alloc, _ := allocated(func() { _, err = io.Copy(io.Discard, newXZReader(bytes.NewReader(data))) })
	if !isFormat(err) || alloc > XZDictCap+1<<20 {
		t.Fatalf("alternating runs: %v, allocated %d MiB", err, alloc>>20)
	}
	in := xzSample("mixed.xz")
	if out, err := decodeXZ(xzOf(t, in, 0)); err != nil || !bytes.Equal(out, in) {
		t.Fatalf("the encoder's output for the mixed sample: %v", err)
	}
}
