package pipeline

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// decodedFixture returns a decoder fed every frame of the shared fixture once, with the
// descriptor decoded and the shard store made; with all true, every frame's codewords decoded.
func decodedFixture(t *testing.T, all bool) *decoder {
	t.Helper()
	key, truth := fixture(t)
	d := &decoder{c: &DecodeConfig{}, tmp: t.TempDir(), frames: map[uint32]*frameAcc{}, softCap: MaxSoftBytes}
	t.Cleanup(d.close)
	n := uint32(4)
	if !all {
		n = 1
	}
	for f := range n {
		if err := d.apply(captureResult{index: int(f), located: true, key: key, cells: 1, clean: 1,
			phases: []phaseBits{{frame: f, soft: truth[f]}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.flush(); err != nil {
		t.Fatal(err)
	}
	if d.desc == nil || d.store == nil {
		t.Fatal("the descriptor did not decode")
	}
	return d
}

// TestRepairFaults: a stream that does not match the descriptor's hash, too few shards, a store
// that cannot be read and a stream file that cannot be made each stop the repair with an error.
func TestRepairFaults(t *testing.T) {
	ctx := context.Background()
	d := decodedFixture(t, true)
	if !d.complete() {
		t.Fatal("the fixture did not decode completely")
	}
	d.desc.StreamHash[0] ^= 1
	if _, err := d.repair(ctx); !errors.Is(err, ErrStreamHash) {
		t.Errorf("altered hash: %v", err)
	}
	d.desc.StreamHash[0] ^= 1

	short := decodedFixture(t, false)
	if _, err := short.repair(ctx); err == nil || !strings.Contains(err.Error(), "too few shards") {
		t.Errorf("one frame's shards: %v", err)
	}

	blocked := decodedFixture(t, true)
	if err := os.Mkdir(filepath.Join(blocked.tmp, "stream"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := blocked.repair(ctx); err == nil {
		t.Error("a folder where the stream file goes")
	}

	closed := decodedFixture(t, true)
	if err := closed.store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := closed.repair(ctx); err == nil {
		t.Error("a closed shard store")
	}
}

// TestStoreFaults: a shard store that cannot be made stops the decode when the descriptor decodes,
// and one that cannot be written stops it at the next attempt or flush.
func TestStoreFaults(t *testing.T) {
	key, truth := fixture(t)
	d := &decoder{c: &DecodeConfig{}, tmp: t.TempDir(), frames: map[uint32]*frameAcc{}, softCap: MaxSoftBytes}
	defer d.close()
	if err := os.WriteFile(filepath.Join(d.tmp, "shards"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := d.apply(captureResult{index: 0, located: true, key: key, cells: 1, clean: 1,
		phases: []phaseBits{{frame: 0, soft: truth[0]}}})
	if err == nil || d.store != nil {
		t.Errorf("a shard store over an existing file: %v", err)
	}

	for _, viaFlush := range []bool{false, true} {
		d := &decoder{c: &DecodeConfig{}, tmp: t.TempDir(), frames: map[uint32]*frameAcc{}, softCap: MaxSoftBytes}
		// Frame 0 gives the descriptor; its codewords wait to be attempted.
		if err := d.apply(captureResult{index: 0, located: true, key: key, cells: 1, clean: 1,
			phases: []phaseBits{{frame: 0, soft: truth[0]}}}); err != nil {
			t.Fatal(err)
		}
		if d.store == nil || len(d.pending) == 0 {
			t.Fatal("no store, or nothing pending")
		}
		if err := d.store.Close(); err != nil {
			t.Fatal(err)
		}
		if viaFlush {
			err = d.flush()
		} else {
			err = d.apply(captureResult{index: 5}) // two captures later: frame 0 is attempted
		}
		if err == nil {
			t.Errorf("a shard store that cannot be written (flush %v)", viaFlush)
		}
	}
}

// failingReader fails every read.
type failingReader struct{}

func (failingReader) ReadAt([]byte, int64) (int, error) { return 0, io.ErrClosedPipe }

// TestCheckHashReadError: a stream that cannot be read fails the check with the read's error.
func TestCheckHashReadError(t *testing.T) {
	if err := checkHash(context.Background(), failingReader{}, 10, [16]byte{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("got %v", err)
	}
}
