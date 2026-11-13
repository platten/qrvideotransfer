package prng

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"testing"
)

type golden struct {
	SplitMix64 struct {
		State   uint64   `json:"state"`
		Outputs []string `json:"outputs"`
	} `json:"splitmix64"`
	Shuffle []struct {
		N    int    `json:"n"`
		Seed uint64 `json:"seed"`
		Perm []int  `json:"perm"`
	} `json:"shuffle"`
	Seed []struct {
		Label string `json:"label"`
		Bytes string `json:"bytes"`
		Seed  string `json:"seed"`
	} `json:"seed"`
}

func loadGolden(t *testing.T) golden {
	t.Helper()
	b, err := os.ReadFile("../../testdata/golden/prng.json")
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func hex64(t *testing.T, s string) uint64 {
	t.Helper()
	v, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// FORMAT.md section 13: SplitMix64 from state 0.
func TestSplitMix64(t *testing.T) {
	want := []uint64{0xe220a8397b1dcdaf, 0x6e789e6aa1b965f4, 0x06c45d188009454f, 0xf88bb8a8724c81ec}
	var g SplitMix64
	for i, w := range want {
		if got := g.Next(); got != w {
			t.Errorf("output %d = %016x, want %016x", i, got, w)
		}
	}
	gold := loadGolden(t)
	g = SplitMix64{State: gold.SplitMix64.State}
	for i, s := range gold.SplitMix64.Outputs {
		if got := g.Next(); got != hex64(t, s) {
			t.Errorf("golden output %d = %016x, want %s", i, got, s)
		}
	}
}

func TestAt(t *testing.T) {
	for _, seed := range []uint64{0, 1, 0xdeadbeef, ^uint64(0)} {
		g := SplitMix64{State: seed}
		for k := range uint64(200) {
			if got, want := At(seed, k), g.Next(); got != want {
				t.Fatalf("At(%x, %d) = %x, want %x", seed, k, got, want)
			}
		}
	}
}

// FORMAT.md section 13: Shuffle(10, 1).
func TestShuffle(t *testing.T) {
	if got, want := Shuffle(10, 1), []uint32{9, 0, 1, 4, 8, 2, 3, 7, 6, 5}; !slices.Equal(got, want) {
		t.Errorf("Shuffle(10, 1) = %v, want %v", got, want)
	}
	for _, c := range loadGolden(t).Shuffle {
		got := Shuffle(c.N, c.Seed)
		if len(got) != len(c.Perm) {
			t.Fatalf("Shuffle(%d, %d) has length %d", c.N, c.Seed, len(got))
		}
		for i := range got {
			if int(got[i]) != c.Perm[i] {
				t.Fatalf("Shuffle(%d, %d)[%d] = %d, want %d", c.N, c.Seed, i, got[i], c.Perm[i])
			}
		}
	}
	if Shuffle(0, 1) != nil || Shuffle(-3, 1) != nil {
		t.Error("Shuffle of n <= 0 is not nil")
	}
	if got := Shuffle(1, 7); !slices.Equal(got, []uint32{0}) {
		t.Errorf("Shuffle(1, 7) = %v", got)
	}
}

func TestShuffleIsPermutation(t *testing.T) {
	for _, n := range []int{2, 3, 17, 1000, 65537} {
		p := Shuffle(n, uint64(n)*31)
		seen := make([]bool, n)
		for _, v := range p {
			if int(v) >= n || seen[v] {
				t.Fatalf("Shuffle(%d) is not a permutation", n)
			}
			seen[v] = true
		}
	}
}

func TestSeed(t *testing.T) {
	// FORMAT.md section 13: the interleave seed of session 0, grid 480 x 270, modulation 2, inner code 2.
	b := []byte{0x00, 0x00, 0x01, 0xe0, 0x01, 0x0e, 0x02, 0x02}
	if got := Seed("qrvt/inner-interleave/v1", b); got != 0x6eca5aab84a46a75 {
		t.Errorf("Seed = %016x, want 6eca5aab84a46a75", got)
	}
	for _, c := range loadGolden(t).Seed {
		in, err := hex.DecodeString(c.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		if got := Seed(c.Label, in); got != hex64(t, c.Seed) {
			t.Errorf("Seed(%q, %s) = %016x, want %s", c.Label, c.Bytes, got, c.Seed)
		}
	}
}

func BenchmarkShuffle(b *testing.B) {
	for b.Loop() {
		Shuffle(234224, 1)
	}
}
