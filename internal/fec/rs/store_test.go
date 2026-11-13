package rs

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/platten/qrvideotransfer/internal/fec"
)

var _ fec.ShardStore = (*Store)(nil)

var smallPlan = fec.Plan{Blocks: 3, K: 5, M: 2, ShardSize: 64}

// content returns the deterministic bytes of a shard for the tests.
func content(p fec.Plan, block, shard int) []byte {
	b := make([]byte, p.ShardSize)
	for i := range b {
		b[i] = byte(block*31 + shard*7 + i + 1)
	}
	return b
}

func newStore(t *testing.T, p fec.Plan) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shards")
	s, err := Create(path, p)
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

func TestStoreSize(t *testing.T) {
	got, err := StoreSize(smallPlan)
	if want := int64(3 * 7 * (64 + slotTrailer)); err != nil || got != want {
		t.Fatalf("StoreSize = %d, %v; want %d", got, err, want)
	}
	p, err := fec.NewPlan(1<<30, 128, 6554, 1)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = StoreSize(p)
	if want := p.Packets * (128 + slotTrailer); got != want {
		t.Fatalf("StoreSize(1 GiB plan) = %d, want %d", got, want)
	}
	for _, p := range []fec.Plan{
		{}, {Blocks: 1, K: 1, M: 1, ShardSize: 65}, {Blocks: 1, K: 0, M: 1, ShardSize: 64},
		{Blocks: 1, K: 1, M: fec.MaxShards + 1, ShardSize: 64}, {Blocks: fec.MaxBlocks + 1, K: 1, M: 1, ShardSize: 64},
		{Blocks: 1, K: 1, M: 1, ShardSize: 0}, {Blocks: 1, K: 1, M: 1, ShardSize: 1 << 30},
	} {
		if _, err := StoreSize(p); err == nil {
			t.Errorf("StoreSize(%+v) accepted", p)
		}
		if _, err := Create(filepath.Join(t.TempDir(), "x"), p); err == nil {
			t.Errorf("Create(%+v) accepted", p)
		}
	}
}

func TestStorePutGetHave(t *testing.T) {
	s, path := newStore(t, smallPlan)
	if fi, err := os.Stat(path); err != nil || fi.Size() != 3*7*(64+slotTrailer) {
		t.Fatalf("file size: %v %v", fi, err)
	}
	buf := make([]byte, 64)
	if ok, err := s.Get(1, 2, buf); ok || err != nil {
		t.Fatalf("Get on an empty store = %v, %v", ok, err)
	}
	for _, bs := range [][2]int{{0, 0}, {1, 6}, {1, 2}, {2, 4}, {1, 2}} { // {1, 2} twice
		if err := s.Put(bs[0], bs[1], content(smallPlan, bs[0], bs[1])); err != nil {
			t.Fatal(err)
		}
	}
	for b, want := range []int{1, 2, 1} {
		if got := s.Have(b); got != want {
			t.Errorf("Have(%d) = %d, want %d", b, got, want)
		}
	}
	if s.Have(-1) != 0 || s.Have(3) != 0 {
		t.Error("Have out of range is not 0")
	}
	if ok, err := s.Get(1, 6, buf); !ok || err != nil || !bytes.Equal(buf, content(smallPlan, 1, 6)) {
		t.Fatalf("Get(1, 6) = %v, %v", ok, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Reopening counts the stored shards again.
	s, err := Open(path, smallPlan)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for b, want := range []int{1, 2, 1} {
		if got := s.Have(b); got != want {
			t.Errorf("after Open, Have(%d) = %d, want %d", b, got, want)
		}
	}
	if ok, err := s.Get(2, 4, buf); !ok || err != nil || !bytes.Equal(buf, content(smallPlan, 2, 4)) {
		t.Fatalf("after Open, Get(2, 4) = %v, %v", ok, err)
	}
}

func TestStoreRefusals(t *testing.T) {
	s, path := newStore(t, smallPlan)
	buf := make([]byte, 64)
	for _, bs := range [][2]int{{-1, 0}, {3, 0}, {0, -1}, {0, 7}} {
		if err := s.Put(bs[0], bs[1], buf); err == nil {
			t.Errorf("Put(%d, %d) accepted", bs[0], bs[1])
		}
		if _, err := s.Get(bs[0], bs[1], buf); err == nil {
			t.Errorf("Get(%d, %d) accepted", bs[0], bs[1])
		}
	}
	if err := s.Put(0, 0, make([]byte, 63)); err == nil {
		t.Error("Put of a short shard accepted")
	}
	if _, err := s.Get(0, 0, make([]byte, 65)); err == nil {
		t.Error("Get into a long buffer accepted")
	}
	// Create never replaces a file.
	if _, err := Create(path, smallPlan); !errors.Is(err, fs.ErrExist) {
		t.Errorf("Create over an existing file: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(0, 0, buf); err == nil {
		t.Error("Put after Close accepted")
	}
	if _, err := s.Get(0, 0, buf); err == nil {
		t.Error("Get after Close accepted")
	}
	// Open refuses a file of the wrong size, a missing file and a bad plan.
	if _, err := Open(path, fec.Plan{Blocks: 3, K: 5, M: 3, ShardSize: 64}); err == nil {
		t.Error("Open with another plan accepted")
	}
	if _, err := Open(filepath.Join(t.TempDir(), "none"), smallPlan); err == nil {
		t.Error("Open of a missing file accepted")
	}
	if _, err := Open(path, fec.Plan{}); err == nil {
		t.Error("Open with an invalid plan accepted")
	}
	if _, err := Create(filepath.Join(t.TempDir(), "no", "dir"), smallPlan); err == nil {
		t.Error("Create in a missing directory accepted")
	}
}

// A write torn at any byte, in order or out of order, or a stored slot with any one byte corrupted,
// is never read back as a valid shard; the complete write is.
func TestStoreTornWrite(t *testing.T) {
	const block, shard = 1, 3
	slot := slotBytes(block, shard, content(smallPlan, block, shard))
	if len(slot) != 64+slotTrailer {
		t.Fatalf("slot is %d bytes", len(slot))
	}
	s, path := newStore(t, smallPlan)
	off := s.offset(block, shard)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	check := func(name string, write func(f *os.File), want bool) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteAt(make([]byte, len(slot)), off) // the slot as never written
		write(f)
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		s, err := Open(path, smallPlan)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		buf := make([]byte, 64)
		ok, err := s.Get(block, shard, buf)
		if err != nil || ok != want || s.Have(block) != map[bool]int{true: 1, false: 0}[want] {
			t.Fatalf("%s: Get = %v, %v, Have = %d; want %v", name, ok, err, s.Have(block), want)
		}
		if ok && !bytes.Equal(buf, content(smallPlan, block, shard)) {
			t.Fatalf("%s: wrong content", name)
		}
		// A Put after the crash repairs the slot.
		if !ok {
			if err := s.Put(block, shard, content(smallPlan, block, shard)); err != nil {
				t.Fatal(err)
			}
			if ok, _ := s.Get(block, shard, buf); !ok || s.Have(block) != 1 {
				t.Fatalf("%s: Put did not repair the slot", name)
			}
		}
	}
	for p := range len(slot) {
		check(fmt.Sprintf("prefix %d", p), func(f *os.File) { _, _ = f.WriteAt(slot[:p], off) }, false)
		check(fmt.Sprintf("suffix from %d", p+1), func(f *os.File) { _, _ = f.WriteAt(slot[p+1:], off+int64(p+1)) }, false)
		bad := append([]byte(nil), slot...)
		bad[p] ^= 0x5a
		check(fmt.Sprintf("byte %d corrupted", p), func(f *os.File) { _, _ = f.WriteAt(bad, off) }, false)
	}
	check("complete", func(f *os.File) { _, _ = f.WriteAt(slot, off) }, true)
	// A valid slot of another shard copied here is refused: the checksum covers the indices.
	other := slotBytes(block, shard+1, content(smallPlan, block, shard))
	check("misplaced", func(f *os.File) { _, _ = f.WriteAt(other, off) }, false)
}

// writerEnv makes the test binary act as a writer process for TestStoreSurvivesKill.
const writerEnv = "QRVT_RS_STORE_WRITER"

// TestStoreWriterProcess is the child of TestStoreSurvivesKill: it writes shards until killed.
func TestStoreWriterProcess(t *testing.T) {
	path := os.Getenv(writerEnv)
	if path == "" {
		t.Skip("helper process for TestStoreSurvivesKill")
	}
	s, err := Open(path, killPlan)
	if err != nil {
		t.Fatal(err)
	}
	// Erase each slot and store it again, forever, so that the kill lands inside a write.
	zero := make([]byte, killPlan.ShardSize+slotTrailer)
	for pass := 0; ; pass++ {
		for b := range killPlan.Blocks {
			for i := range killPlan.K + killPlan.M {
				if _, err := s.f.WriteAt(zero, s.offset(b, i)); err != nil {
					t.Fatal(err)
				}
				if err := s.Put(b, i, content(killPlan, b, i)); err != nil {
					t.Fatal(err)
				}
			}
		}
		if pass == 0 {
			_ = os.WriteFile(path+".started", nil, 0o600)
		}
	}
}

var killPlan = fec.Plan{Blocks: 8, K: 200, M: 40, ShardSize: 128}

// A process killed (SIGKILL on Unix, TerminateProcess on Windows) while writing leaves a store
// whose every readable shard is correct.
func TestStoreSurvivesKill(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a subprocess")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	for round := range 3 {
		s, path := newStore(t, killPlan)
		_ = s.Close()
		p, err := os.StartProcess(exe, []string{exe, "-test.run=^TestStoreWriterProcess$"}, &os.ProcAttr{
			Env:   append(os.Environ(), writerEnv+"="+path),
			Files: []*os.File{nil, nil, os.Stderr},
		})
		if err != nil {
			t.Skip(err)
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			if _, err := os.Stat(path + ".started"); err == nil || time.Now().After(deadline) {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(time.Duration(1+round*7) * time.Millisecond)
		if err := p.Kill(); err != nil {
			t.Fatal(err)
		}
		_, _ = p.Wait()
		s, err = Open(path, killPlan)
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, killPlan.ShardSize)
		total := 0
		for b := range killPlan.Blocks {
			n := 0
			for i := range killPlan.K + killPlan.M {
				ok, err := s.Get(b, i, buf)
				if err != nil {
					t.Fatal(err)
				}
				if ok {
					n++
					if !bytes.Equal(buf, content(killPlan, b, i)) {
						t.Fatalf("round %d: shard %d of block %d read back wrong", round, i, b)
					}
				}
			}
			if n != s.Have(b) {
				t.Fatalf("round %d: Have(%d) = %d, %d readable", round, b, s.Have(b), n)
			}
			total += n
		}
		t.Logf("round %d: %d of %s shards present after the kill", round, total, strconv.Itoa(killPlan.Blocks*(killPlan.K+killPlan.M)))
		_ = s.Close()
	}
}
