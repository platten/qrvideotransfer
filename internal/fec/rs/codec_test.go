package rs

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"testing"

	"github.com/platten/qrvideotransfer/internal/fec"
)

// block returns k random data shards and m empty parity shards of size bytes.
func block(r *rand.Rand, k, m, size int) [][]byte {
	shards := make([][]byte, k+m)
	for i := range shards {
		shards[i] = make([]byte, size)
		if i < k {
			for j := range shards[i] {
				shards[i][j] = byte(r.Uint32())
			}
		}
	}
	return shards
}

func clone(shards [][]byte) [][]byte {
	out := make([][]byte, len(shards))
	for i, s := range shards {
		out[i] = append([]byte(nil), s...)
	}
	return out
}

// erase removes the shards whose indices are listed, and checks that Reconstruct restores the data.
func checkRecover(t *testing.T, c *Codec, full [][]byte, k int, lost []int) {
	t.Helper()
	got := clone(full)
	for _, i := range lost {
		got[i] = nil
	}
	if err := c.Reconstruct(got); err != nil {
		t.Fatalf("K=%d M=%d, %d lost: %v", k, len(full)-k, len(lost), err)
	}
	for i := range k {
		if !bytes.Equal(got[i], full[i]) {
			t.Fatalf("K=%d M=%d, %d lost: data shard %d differs", k, len(full)-k, len(lost), i)
		}
	}
}

// Any K of the K + M shards recover the block: random erasure sets of exactly M and of fewer, all
// parity lost, and all data lost where M >= K.
func TestRecoverAnyK(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 200 {
		k, m := 1+r.IntN(48), 1+r.IntN(48)
		size := 64 * (1 + r.IntN(3))
		c, err := NewCodec(k, m)
		if err != nil {
			t.Fatal(err)
		}
		full := block(r, k, m, size)
		if err := c.Encode(full); err != nil {
			t.Fatal(err)
		}
		perm := r.Perm(k + m)
		checkRecover(t, c, full, k, perm[:m])
		checkRecover(t, c, full, k, perm[:r.IntN(m+1)])
		parity := make([]int, m)
		for i := range parity {
			parity[i] = k + i
		}
		checkRecover(t, c, full, k, parity)
		if m >= k {
			data := make([]int, k)
			for i := range data {
				data[i] = i
			}
			checkRecover(t, c, full, k, data)
		}
	}
}

// The largest block of FORMAT.md section 13's 1 GiB example, with M shards lost at random.
func TestRecoverRealisticBlock(t *testing.T) {
	k, m := 32768, 6554
	if testing.Short() {
		k, m = 4096, 820
	}
	r := rand.New(rand.NewPCG(3, 4))
	c, err := NewCodec(k, m)
	if err != nil {
		t.Fatal(err)
	}
	full := block(r, k, m, 128)
	if err := c.Encode(full); err != nil {
		t.Fatal(err)
	}
	checkRecover(t, c, full, k, r.Perm(k + m)[:m])
}

func TestTooFewShards(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	c, _ := NewCodec(10, 4)
	full := block(r, 10, 4, 64)
	if err := c.Encode(full); err != nil {
		t.Fatal(err)
	}
	for _, i := range r.Perm(14)[:5] {
		full[i] = nil
	}
	if err := c.Reconstruct(full); !errors.Is(err, ErrTooFewShards) {
		t.Fatalf("err = %v, want ErrTooFewShards", err)
	}
}

// Every (K, M) the plan of FORMAT.md section 11 can produce is accepted.
func TestNewCodecAcceptsEveryPlanShape(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8))
	pairs := [][2]int{{1, 1}, {1, fec.MaxShards}, {fec.MaxShards, 1}, {fec.MaxShards, fec.MaxShards}, {32767, 16385}, {16385, 16385}}
	for range 2000 {
		pairs = append(pairs, [2]int{1 + r.IntN(fec.MaxShards), 1 + r.IntN(fec.MaxShards)})
	}
	for _, p := range pairs {
		if _, err := NewCodec(p[0], p[1]); err != nil {
			t.Fatalf("NewCodec(%d, %d): %v", p[0], p[1], err)
		}
	}
	for _, p := range [][2]int{{0, 1}, {1, 0}, {-1, 5}, {fec.MaxShards + 1, 1}, {1, fec.MaxShards + 1}} {
		if _, err := NewCodec(p[0], p[1]); err == nil {
			t.Errorf("NewCodec(%d, %d) accepted", p[0], p[1])
		}
	}
}

func TestBadShards(t *testing.T) {
	c, _ := NewCodec(2, 2)
	for name, shards := range map[string][][]byte{
		"count":          make([][]byte, 3),
		"not 64 bytes":   {make([]byte, 65), make([]byte, 65), make([]byte, 65), make([]byte, 65)},
		"unequal":        {make([]byte, 64), make([]byte, 128), make([]byte, 64), make([]byte, 64)},
		"missing parity": {make([]byte, 64), make([]byte, 64), nil, make([]byte, 64)},
	} {
		if err := c.Encode(shards); err == nil {
			t.Errorf("Encode %s: no error", name)
		}
	}
	if err := c.Reconstruct(make([][]byte, 3)); err == nil {
		t.Error("Reconstruct with 3 shards: no error")
	}
	if err := c.Reconstruct([][]byte{make([]byte, 65), nil, make([]byte, 65), make([]byte, 65)}); err == nil {
		t.Error("Reconstruct with 65-byte shards: no error")
	}
}

func BenchmarkEncode(b *testing.B) {
	k, m := 32768, 6554
	c, _ := NewCodec(k, m)
	full := block(rand.New(rand.NewPCG(1, 1)), k, m, 128)
	b.SetBytes(int64(k * 128))
	for b.Loop() {
		_ = c.Encode(full)
	}
}
