package fec

import (
	"encoding/json"
	"math/rand/v2"
	"os"
	"testing"

	"github.com/klauspost/reedsolomon"
)

type planGolden struct {
	Plans []struct {
		StreamLen         int64
		ShardSize, Parity int
		Seed              uint32
		CodewordsPerFrame int
		Nd, B, K, M       int
		T                 int64
		F                 int
		Locate            []struct {
			Packet       int64
			Block, Shard int
		}
		Carried []struct {
			Frame, Codeword int
			Packet          int64
		}
	}
}

func TestPlanGolden(t *testing.T) {
	b, err := os.ReadFile("../../testdata/golden/plan.json")
	if err != nil {
		t.Fatal(err)
	}
	var g planGolden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	for _, c := range g.Plans {
		blocks, k, err := Shape(c.StreamLen, c.ShardSize)
		if err != nil || blocks != c.B || k != c.K {
			t.Errorf("Shape(%d, %d) = %d, %d, %v; want %d, %d", c.StreamLen, c.ShardSize, blocks, k, err, c.B, c.K)
		}
		m := DefaultM(k, c.Parity)
		if m != c.M {
			t.Errorf("DefaultM(%d, %d) = %d, want %d", k, c.Parity, m, c.M)
		}
		p, err := NewPlan(c.StreamLen, c.ShardSize, m, c.Seed)
		if err != nil {
			t.Fatal(err)
		}
		if p.Blocks != c.B || p.K != c.K || p.M != c.M || p.ShardSize != c.ShardSize || p.Packets != c.T || p.Seed != c.Seed {
			t.Errorf("plan = %+v, want B %d K %d M %d T %d", p, c.B, c.K, c.M, c.T)
		}
		if f := p.Frames(c.CodewordsPerFrame); f != c.F {
			t.Errorf("Frames(%d) = %d, want %d", c.CodewordsPerFrame, f, c.F)
		}
		for _, l := range c.Locate {
			if b, s := p.Locate(l.Packet); b != l.Block || s != l.Shard {
				t.Errorf("Locate(%d) = %d, %d, want %d, %d", l.Packet, b, s, l.Block, l.Shard)
			}
		}
		for _, w := range c.Carried {
			if got := p.Packet(w.Frame, w.Codeword, c.CodewordsPerFrame); got != w.Packet {
				t.Errorf("Packet(%d, %d) = %d, want %d", w.Frame, w.Codeword, got, w.Packet)
			}
		}
	}
}

// FORMAT.md section 13: the worked plans at 480 x 270, Gray4, normal (C = 151), parity 20, and the
// outer-order row (seed 1, B = 3, n = 32,769).
func TestWorkedPlans(t *testing.T) {
	cases := []struct {
		l            int64
		nd           int64
		blocks, k, m int
		packets      int64
		frames       int
	}{
		{10 << 20, 81920, 3, 27307, 5462, 98307, 656},
		{1 << 30, 8388608, 256, 32768, 6554, 10066432, 67112},
	}
	for _, c := range cases {
		if nd := (c.l + 127) / 128; nd != c.nd {
			t.Errorf("Nd = %d, want %d", nd, c.nd)
		}
		p, err := NewPlan(c.l, 128, DefaultM(c.k, DefaultParity), 1)
		if err != nil {
			t.Fatal(err)
		}
		if p.Blocks != c.blocks || p.K != c.k || p.M != c.m || p.Packets != c.packets || p.Frames(151) != c.frames {
			t.Errorf("plan for %d = %+v, F %d", c.l, p, p.Frames(151))
		}
	}
	p, _ := NewPlan(10<<20, 128, 5462, 1)
	want := [][3]int{{0, 0, 22956}, {1, 1, 9712}, {2, 2, 29115}, {3, 0, 2029}, {98306, 2, 21168}}
	for _, w := range want {
		if b, s := p.Locate(int64(w[0])); b != w[1] || s != w[2] {
			t.Errorf("packet %d = shard %d of block %d, want shard %d of block %d", w[0], s, b, w[2], w[1])
		}
	}
}

// Every shard of every block is sent exactly once in packets 0..T-1, and a Plan literal without the
// cached permutation locates the same shards.
func TestLocateIsBijection(t *testing.T) {
	for _, c := range []struct {
		l    int64
		s, m int
	}{{1, 64, 1}, {5000, 64, 7}, {32768*64 + 1, 64, 3}, {3 * 32768 * 64, 64, 2}} {
		p, err := NewPlan(c.l, c.s, c.m, 99)
		if err != nil {
			t.Fatal(err)
		}
		lit := Plan{Blocks: p.Blocks, K: p.K, M: p.M, ShardSize: p.ShardSize, Packets: p.Packets, Seed: p.Seed}
		n := p.K + p.M
		seen := make([]bool, p.Blocks*n)
		for pk := range p.Packets {
			b, s := p.Locate(pk)
			if b != int(pk%int64(p.Blocks)) || s < 0 || s >= n || seen[b*n+s] {
				t.Fatalf("packet %d: block %d shard %d", pk, b, s)
			}
			seen[b*n+s] = true
			if pk%101 == 0 {
				if lb, ls := lit.Locate(pk); lb != b || ls != s {
					t.Fatalf("literal plan locates packet %d differently", pk)
				}
			}
		}
	}
}

func TestShapeAndPlanErrors(t *testing.T) {
	for _, c := range []struct {
		l int64
		s int
	}{{0, 64}, {-1, 64}, {MaxStreamLen + 1, 64}, {100, 0}, {100, 100}, {100, -64}, {int64(MaxBlocks+1) * MaxShards * 64, 64}} {
		if _, _, err := Shape(c.l, c.s); err == nil {
			t.Errorf("Shape(%d, %d) succeeded", c.l, c.s)
		}
		if _, err := NewPlan(c.l, c.s, 1, 0); err == nil {
			t.Errorf("NewPlan(%d, %d) succeeded", c.l, c.s)
		}
	}
	// The block count, not the 6-byte length, is the binding limit: 3 bytes of B at most.
	largest := int64(MaxBlocks) * MaxShards * 192
	if b, k, err := Shape(largest, 192); err != nil || b != MaxBlocks || k != MaxShards {
		t.Errorf("Shape(largest, 192) = %d, %d, %v", b, k, err)
	}
	if _, _, err := Shape(largest+1, 192); err == nil {
		t.Error("Shape(largest + 1, 192) succeeded")
	}
	for _, m := range []int{0, -1, MaxShards + 1} {
		if _, err := NewPlan(1000, 64, m, 0); err == nil {
			t.Errorf("NewPlan with M = %d succeeded", m)
		}
	}
}

func TestShapeEdges(t *testing.T) {
	for _, c := range []struct {
		l         int64
		blocks, k int
	}{{1, 1, 1}, {128, 1, 1}, {129, 1, 2}, {32768 * 128, 1, 32768}, {32768*128 + 1, 2, 16385}} {
		if b, k, err := Shape(c.l, 128); err != nil || b != c.blocks || k != c.k {
			t.Errorf("Shape(%d) = %d, %d, %v; want %d, %d", c.l, b, k, err, c.blocks, c.k)
		}
	}
}

func TestDefaultM(t *testing.T) {
	for _, c := range [][3]int{{27307, 20, 5462}, {32768, 20, 6554}, {1, 20, 1}, {1, 5, 1}, {32768, 100, 32768}, {100, 5, 5}, {10, 0, 1}, {32768, 200, 32768}} {
		if got := DefaultM(c[0], c[1]); got != c[2] {
			t.Errorf("DefaultM(%d, %d) = %d, want %d", c[0], c[1], got, c[2])
		}
	}
}

func TestFramesAndPackets(t *testing.T) {
	p, _ := NewPlan(1000, 64, 1, 7) // K 16, M 1, T 17
	if p.Frames(1) != 0 || p.Frames(0) != 0 {
		t.Error("Frames with C < 2 is not 0")
	}
	if p.Frames(2) != 20 || p.Frames(18) != 4 {
		t.Errorf("Frames = %d, %d", p.Frames(2), p.Frames(18))
	}
	if got := p.Packet(3, 2, 6); got != (3*5+1)%17 {
		t.Errorf("Packet = %d", got)
	}
	if got := p.DataOffset(0, 3); got != 192 {
		t.Errorf("DataOffset = %d", got)
	}
	q, _ := NewPlan(3*32768*64, 64, 1, 0)
	if got := q.DataOffset(2, 5); got != (2*32768+5)*64 {
		t.Errorf("DataOffset = %d", got)
	}
}

// FORMAT.md section 11: every (K, M) the plan can produce must be accepted by the Reed-Solomon
// library with Leopard GF(2^16). K is 1..32768 and M is 1..32768; the default M covers parity 5..100.
func TestReedSolomonAcceptsPlans(t *testing.T) {
	r := rand.New(rand.NewPCG(11, 12))
	ks := []int{1, 2, 3, 63, 64, 255, 256, 257, 1000, 16385, 27307, 32767, 32768}
	for range 40 {
		ks = append(ks, 1+r.IntN(MaxShards))
	}
	accept := func(k, m int) {
		if _, err := reedsolomon.New(k, m, reedsolomon.WithLeopardGF16(true)); err != nil {
			t.Errorf("reedsolomon.New(%d, %d): %v", k, m, err)
		}
	}
	for _, k := range ks {
		for _, m := range []int{1, 2, 32767, 32768, 1 + r.IntN(MaxShards)} {
			accept(k, m)
		}
		for parity := 5; parity <= 100; parity += 5 + r.IntN(10) {
			accept(k, DefaultM(k, parity))
		}
		accept(k, DefaultM(k, 100))
	}
}

// The library encodes and reconstructs with the shard sizes of the inner codes.
func TestReedSolomonRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(13, 14))
	for _, c := range [][3]int{{1, 1, 64}, {3, 2, 128}, {300, 60, 192}, {32768, 32768, 64}} {
		k, m, size := c[0], c[1], c[2]
		if testing.Short() && k > 1000 {
			continue
		}
		enc, err := reedsolomon.New(k, m, reedsolomon.WithLeopardGF16(true))
		if err != nil {
			t.Fatal(err)
		}
		shards := make([][]byte, k+m)
		for i := range shards {
			shards[i] = make([]byte, size)
			if i < k {
				for j := range shards[i] {
					shards[i][j] = byte(r.Uint32())
				}
			}
		}
		if err := enc.Encode(shards); err != nil {
			t.Fatal(err)
		}
		want := string(shards[0])
		for i := range min(m, k) {
			shards[i] = nil
		}
		if err := enc.Reconstruct(shards); err != nil || string(shards[0]) != want {
			t.Errorf("(%d, %d, %d): reconstruct failed: %v", k, m, size, err)
		}
	}
}

// FORMAT.md section 11: F travels in 22 bits, so an encoder refuses a plan that needs more frames.
func TestCheckFrames(t *testing.T) {
	p, _ := NewPlan(10<<20, 128, 5462, 1)
	if f, err := p.CheckFrames(151); err != nil || f != 656 {
		t.Errorf("CheckFrames(151) = %d, %v", f, err)
	}
	if _, err := p.CheckFrames(1); err == nil {
		t.Error("CheckFrames(1) succeeded")
	}
	// 1 GiB in 64-byte shards over 3 codewords per frame needs about 8.4 million frames.
	big, _ := NewPlan(1<<30, 64, 1, 1)
	if _, err := big.CheckFrames(3); err == nil {
		t.Errorf("CheckFrames(3) for %d packets succeeded", big.Packets)
	}
	// Exactly MaxFrames is allowed; one frame's worth of packets more is not.
	edge := Plan{Blocks: 1, K: 2, M: 2, Packets: MaxFrames}
	if f, err := edge.CheckFrames(2); err != nil || f != MaxFrames {
		t.Errorf("CheckFrames at the limit = %d, %v", f, err)
	}
	edge.Packets++
	if _, err := edge.CheckFrames(2); err == nil {
		t.Error("CheckFrames above the limit succeeded")
	}
}

func BenchmarkNewPlan1GiB(b *testing.B) {
	for b.Loop() {
		if _, err := NewPlan(1<<30, 128, 6554, 1); err != nil {
			b.Fatal(err)
		}
	}
}
