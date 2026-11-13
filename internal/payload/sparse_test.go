package payload

import (
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestSparseGigabyte packs and unpacks a 1 GiB sparse file and checks that memory allocated during
// the round trip stays far below the file size.
func TestSparseGigabyte(t *testing.T) {
	if testing.Short() {
		t.Skip("1 GiB round trip; skipped with -short")
	}
	if raceEnabled {
		t.Skip("1 GiB round trip is too slow under the race detector; it runs in the ordinary test task")
	}
	const size = 1 << 30
	src := t.TempDir()
	name := filepath.Join(src, "sparse.img")
	f, err := os.Create(name)
	must(t, err)
	must(t, f.Truncate(size))
	for _, off := range []int64{0, size / 3, size - 5} {
		_, err := f.WriteAt([]byte("qrvt!"), off)
		must(t, err)
	}
	must(t, f.Close())
	want := fileHash(t, name)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	spool, err := os.CreateTemp(t.TempDir(), "stream")
	must(t, err)
	defer func() { _ = spool.Close() }()
	res, err := Pack(context.Background(), spool, PackOptions{Inputs: []string{name}, SignKey: testKey()})
	must(t, err)
	if res.Header.Method != MethodZstd || res.StreamLen > 1<<20 {
		t.Fatalf("stream: %+v", res)
	}
	out := t.TempDir()
	ur, err := Unpack(context.Background(), spool, res.StreamLen, UnpackOptions{OutputDir: out})
	must(t, err)
	if ur.Bytes != size {
		t.Fatalf("unpacked %d bytes", ur.Bytes)
	}

	runtime.ReadMemStats(&after)
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 256<<20 {
		t.Fatalf("allocated %d MiB during the round trip", alloc>>20)
	}
	if got := fileHash(t, filepath.Join(out, "sparse.img")); got != want {
		t.Fatal("unpacked file differs")
	}
}

func fileHash(t *testing.T, name string) [32]byte {
	t.Helper()
	f, err := os.Open(name)
	must(t, err)
	defer func() { _ = f.Close() }()
	h := sha256.New()
	_, err = io.Copy(h, f)
	must(t, err)
	return [32]byte(h.Sum(nil))
}
