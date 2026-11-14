package payload

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/ulikunitz/xz/lzma"
)

// TestXZManyLZMAChunks: one block of 100,000 LZMA chunks of 64 equal bytes each (written by the
// library's LZMA2 writer, flushed after each) decodes, and allocates one dictionary plus at
// most 64 bytes per input byte. It measures the library's cost per LZMA chunk, which no rule limits.
func TestXZManyLZMAChunks(t *testing.T) {
	const n = 100_000
	var lz bytes.Buffer
	w, err := lzma.Writer2Config{DictCap: XZDictCap}.NewWriter2(&lz)
	must(t, err)
	for i := range n {
		_, err = w.Write(bytes.Repeat([]byte{byte('a' + i%26)}, 64))
		must(t, errors.Join(err, w.Flush()))
	}
	must(t, w.Close())
	chunks := bytes.TrimSuffix(lz.Bytes(), []byte{0}) // rawBlock adds the end marker
	b, r := rawBlock(chunks, 64*n)
	data := xzStream(xzNone, b, [][2]uint64{r})
	var got int64
	alloc, _ := allocated(func() { got, err = io.Copy(io.Discard, newXZReader(bytes.NewReader(data))) })
	if err != nil || got != 64*n {
		t.Fatalf("decode: %v, %d bytes", err, got)
	}
	if bound := uint64(XZDictCap + 64*len(data)); alloc > bound {
		t.Fatalf("%d input bytes: allocated %d MiB, bound %d MiB", len(data), alloc>>20, bound>>20)
	}
	t.Logf("%d LZMA chunks, %d input bytes: %d KiB allocated beyond the dictionary (%.1f bytes per input byte)",
		n, len(data), (alloc-XZDictCap)>>10, float64(alloc-XZDictCap)/float64(len(data)))
}
