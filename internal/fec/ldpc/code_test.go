package ldpc

import (
	"encoding/hex"
	"encoding/json"
	"hash/crc32"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/platten/qrvideotransfer/internal/fec"
	"github.com/platten/qrvideotransfer/internal/frame"
)

var _ fec.InnerCode = (*Code)(nil)

var ids = []uint8{1, 2, 3, 4}

func mustNew(t testing.TB, id uint8) *Code {
	t.Helper()
	c, err := New(id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// syndrome counts the unsatisfied checks of a mother codeword (one bit per byte), multiplying by H
// directly from the base matrix, independently of the encoder and decoder.
func syndrome(r Rate, cw []uint8) int {
	b, _ := Base1944(r)
	bad := 0
	for _, row := range b.Shift {
		for q := range Z {
			var p uint8
			for j, s := range row {
				if s >= 0 {
					p ^= cw[j*Z+(q+s)%Z]
				}
			}
			bad += int(p)
		}
	}
	return bad
}

// unpack returns n bits of b, most significant bit first.
func unpack(b []byte, n int) []uint8 {
	out := make([]uint8, n)
	for i := range out {
		out[i] = b[i/8] >> (7 - i%8) & 1
	}
	return out
}

// mother rebuilds the mother codeword from the sent bits of code c: info, k - info zeros, parity.
func mother(c *Code, sent []uint8) []uint8 {
	b, _ := Base1944(c.Rate())
	info := c.N() - (N - b.K())
	cw := make([]uint8, N)
	copy(cw, sent[:info])
	copy(cw[b.K():], sent[info:])
	return cw
}

func randomBytes(r *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

func randomBits(r *rand.Rand, n int) []uint8 {
	u := make([]uint8, n)
	for i := range u {
		u[i] = uint8(r.IntN(2))
	}
	return u
}

func TestNewMatchesFormatTable(t *testing.T) {
	rates := map[uint8]Rate{1: Rate5_6, 2: Rate3_4, 3: Rate2_3, 4: Rate1_2}
	for _, id := range ids {
		c := mustNew(t, id)
		info, err := frame.LookupInner(id)
		if err != nil {
			t.Fatal(err)
		}
		if c.ID() != id || c.ShardSize() != info.ShardSize || c.N() != info.Sent || c.Rate() != rates[id] {
			t.Errorf("code %d: id %d, shard %d, N %d, rate %v; want %+v, rate %v", id, c.ID(), c.ShardSize(), c.N(), c.Rate(), info, rates[id])
		}
		if b, _ := Base1944(c.Rate()); b.K() != info.MotherK {
			t.Errorf("code %d: mother k %d, want %d", id, b.K(), info.MotherK)
		}
	}
	for _, id := range []uint8{0, 5, 7, 255} {
		if c, err := New(id); err == nil || c != nil {
			t.Errorf("New(%d) = %v, %v; want an error", id, c, err)
		}
	}
}

// H c = 0 for codewords of every unshortened mother code and every shortened code.
func TestEncodeSatisfiesChecks(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for _, rate := range allRates {
		b, _ := Base1944(rate)
		c := newCode(rate, b.K())
		for range 20 {
			cw := c.encodeBits(randomBits(r, b.K()))
			if s := syndrome(rate, cw); s != 0 {
				t.Fatalf("rate %v: %d unsatisfied checks", rate, s)
			}
		}
	}
	for _, id := range ids {
		c := mustNew(t, id)
		dst := make([]byte, (c.N()+7)/8)
		for range 20 {
			c.Encode(dst, randomBytes(r, c.ShardSize()))
			cw := mother(c, unpack(dst, c.N()))
			if s := syndrome(c.Rate(), cw); s != 0 {
				t.Fatalf("code %d: %d unsatisfied checks", id, s)
			}
			cw[r.IntN(N)] ^= 1
			if syndrome(c.Rate(), cw) == 0 {
				t.Fatalf("code %d: a flipped bit satisfies every check", id)
			}
		}
	}
}

// The sent bits are the shard MSB first, its CRC-32C, then the parity; the padding bits are zero.
func TestEncodeLayout(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for _, id := range ids {
		c := mustNew(t, id)
		shard := randomBytes(r, c.ShardSize())
		dst := make([]byte, (c.N()+7)/8+3)
		for i := range dst {
			dst[i] = 0xff
		}
		c.Encode(dst, shard)
		crc := crc32.Checksum(shard, crc32.MakeTable(crc32.Castagnoli))
		info := append(append([]byte(nil), shard...), byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
		bits := unpack(dst, len(dst)*8)
		want := unpack(info, 8*len(info))
		for i, w := range want {
			if bits[i] != w {
				t.Fatalf("code %d: sent bit %d = %d, want %d", id, i, bits[i], w)
			}
		}
		for i := c.N(); i < 8*((c.N()+7)/8); i++ {
			if bits[i] != 0 {
				t.Fatalf("code %d: padding bit %d is 1", id, i)
			}
		}
		for _, b := range dst[(c.N()+7)/8:] {
			if b != 0xff {
				t.Fatalf("code %d: Encode wrote beyond the packed bits", id)
			}
		}
	}
}

func TestEncodePanicsOnBadSizes(t *testing.T) {
	c := mustNew(t, 2)
	for name, f := range map[string]func(){
		"short dst":   func() { c.Encode(make([]byte, 10), make([]byte, 128)) },
		"short shard": func() { c.Encode(make([]byte, 200), make([]byte, 127)) },
		"long shard":  func() { c.Encode(make([]byte, 200), make([]byte, 129)) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			f()
		}()
	}
}

type ldpcGolden struct {
	Codes []struct {
		ID      uint8
		Vectors []struct{ Shard, Sent string }
	}
}

// TestGolden checks the frozen vectors of FORMAT.md section 13: for each inner code an all-zero
// shard and a fixed pseudo-random shard to sent bits, and H c = 0 for both.
func TestGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "golden", "ldpc.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g ldpcGolden
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	if len(g.Codes) != len(ids) {
		t.Fatalf("%d codes in the golden file", len(g.Codes))
	}
	for i, gc := range g.Codes {
		if gc.ID != ids[i] || len(gc.Vectors) != 2 {
			t.Fatalf("golden entry %d: id %d, %d vectors", i, gc.ID, len(gc.Vectors))
		}
		c := mustNew(t, gc.ID)
		for j, v := range gc.Vectors {
			shard, err1 := hex.DecodeString(v.Shard)
			sent, err2 := hex.DecodeString(v.Sent)
			if err1 != nil || err2 != nil || len(shard) != c.ShardSize() || len(sent) != (c.N()+7)/8 {
				t.Fatalf("code %d vector %d malformed", gc.ID, j)
			}
			if j == 0 && string(shard) != string(make([]byte, len(shard))) {
				t.Fatalf("code %d: first vector is not the all-zero shard", gc.ID)
			}
			if s := syndrome(c.Rate(), mother(c, unpack(sent, c.N()))); s != 0 {
				t.Fatalf("code %d vector %d: golden codeword has %d unsatisfied checks", gc.ID, j, s)
			}
			dst := make([]byte, len(sent))
			c.Encode(dst, shard)
			if got := hex.EncodeToString(dst); got != v.Sent {
				t.Errorf("code %d vector %d: sent bits\n got %s\nwant %s", gc.ID, j, got, v.Sent)
			}
		}
	}
}

// llrOf returns noiseless soft bits of magnitude a for packed sent bits.
func llrOf(packed []byte, n int, a int8) []int8 {
	llr := make([]int8, n)
	for i, b := range unpack(packed, n) {
		llr[i] = a * int8(1-2*int(b))
	}
	return llr
}

func TestDecodeNoiseless(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	for _, id := range ids {
		c := mustNew(t, id)
		shard := randomBytes(r, c.ShardSize())
		dst := make([]byte, (c.N()+7)/8)
		c.Encode(dst, shard)
		got := make([]byte, c.ShardSize())
		iters, ok := c.Decode(got, llrOf(dst, c.N(), 100))
		if !ok || iters != 0 || string(got) != string(shard) {
			t.Fatalf("code %d: iters %d ok %v, shard equal %v", id, iters, ok, string(got) == string(shard))
		}
	}
}

// Erasures and flipped soft bits that the code can correct.
func TestDecodeNoisy(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8))
	// Per code: percent erased, percent flipped.
	damage := map[uint8][2]int{1: {3, 0}, 2: {8, 1}, 3: {10, 2}, 4: {25, 3}}
	for _, id := range ids {
		c := mustNew(t, id)
		dst := make([]byte, (c.N()+7)/8)
		got := make([]byte, c.ShardSize())
		for range 20 {
			shard := randomBytes(r, c.ShardSize())
			c.Encode(dst, shard)
			llr := llrOf(dst, c.N(), 40)
			for i := range llr {
				switch x := r.IntN(100); {
				case x < damage[id][0]:
					llr[i] = 0
				case x < damage[id][0]+damage[id][1]:
					llr[i] = -llr[i]
				default:
					llr[i] = int8(int(llr[i]) + r.IntN(41) - 20)
				}
			}
			iters, ok := c.Decode(got, llr)
			if !ok || iters < 1 || iters > MaxIterations || string(got) != string(shard) {
				t.Fatalf("code %d: iters %d ok %v, shard equal %v", id, iters, ok, string(got) == string(shard))
			}
		}
	}
}

func TestDecodeFailure(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 10))
	for _, id := range ids {
		c := mustNew(t, id)
		llr := make([]int8, c.N())
		for i := range llr {
			llr[i] = int8(r.IntN(256) - 128)
		}
		iters, ok := c.Decode(make([]byte, c.ShardSize()), llr)
		if ok || iters != MaxIterations {
			t.Errorf("code %d: random soft bits gave iters %d ok %v", id, iters, ok)
		}
		for name, a := range map[string]struct {
			shard, llr int
		}{
			"short llr": {c.ShardSize(), c.N() - 1}, "long llr": {c.ShardSize(), c.N() + 1},
			"short shard": {c.ShardSize() - 1, c.N()}, "nil": {0, 0},
		} {
			if iters, ok := c.Decode(make([]byte, a.shard), make([]int8, a.llr)); ok || iters != 0 {
				t.Errorf("code %d %s: iters %d ok %v", id, name, iters, ok)
			}
		}
	}
}

// A valid codeword whose CRC does not match passes every check and is still refused.
func TestDecodeBadCRC(t *testing.T) {
	r := rand.New(rand.NewPCG(11, 12))
	for _, id := range ids {
		c := mustNew(t, id)
		info := randomBits(r, 8*c.ShardSize()+32) // the last 32 bits are not the CRC
		cw := c.encodeBits(info)
		b, _ := Base1944(c.Rate())
		sent := append(append([]uint8(nil), cw[:len(info)]...), cw[b.K():]...)
		llr := make([]int8, len(sent))
		for i, v := range sent {
			llr[i] = int8(50 - 100*int(v))
		}
		iters, ok := c.Decode(make([]byte, c.ShardSize()), llr)
		if ok || iters != 0 {
			t.Errorf("code %d: iters %d ok %v, want 0 false", id, iters, ok)
		}
	}
}

// Extreme soft bits never overflow the decoder's arithmetic: the all-zero codeword at full
// confidence decodes, and the all-one word at full confidence (not a codeword: the shortened bits
// are zero) terminates.
func TestDecodeExtremes(t *testing.T) {
	for _, id := range ids {
		c := mustNew(t, id)
		shard := make([]byte, c.ShardSize())
		dst := make([]byte, (c.N()+7)/8)
		c.Encode(dst, shard)
		llr := llrOf(dst, c.N(), 127)
		for i := range llr {
			if llr[i] < 0 {
				llr[i] = -128
			}
		}
		if _, ok := c.Decode(shard, llr); !ok {
			t.Errorf("code %d: the zero shard at full confidence failed", id)
		}
		for _, v := range []int8{-128, 0, 1, -1} {
			for i := range llr {
				llr[i] = v
			}
			if iters, _ := c.Decode(shard, llr); iters < 0 || iters > MaxIterations {
				t.Errorf("code %d, all %d: %d iterations", id, v, iters)
			}
		}
	}
}

// Known-zero (shortened) bits stay zero in the decoder even when the checks pull them towards one.
func TestShortenedBitsStayKnown(t *testing.T) {
	r := rand.New(rand.NewPCG(13, 14))
	c := mustNew(t, 2)
	llr := make([]int8, c.N())
	for range 50 {
		for i := range llr {
			llr[i] = int8(r.IntN(256) - 128)
		}
		c.decodeBits(llr)
		b, _ := Base1944(c.Rate())
		for i := 8*c.ShardSize() + 32; i < b.K(); i++ {
			if c.bit(i) != 0 {
				t.Fatalf("shortened bit %d decided as 1", i)
			}
		}
	}
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte{0})
	f.Add([]byte{1, 0x80, 0x7f, 0x00, 0x01})
	c := mustNew(f, 2)
	dst := make([]byte, (c.N()+7)/8)
	c.Encode(dst, make([]byte, c.ShardSize()))
	seed := []byte{1}
	for _, v := range llrOf(dst, c.N(), 30) {
		seed = append(seed, byte(v))
	}
	f.Add(seed)
	codes := make([]*Code, len(ids))
	for i, id := range ids {
		codes[i] = mustNew(f, id)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		c := codes[int(data[0])%len(codes)]
		data = data[1:]
		llr := make([]int8, c.N())
		for i := range llr {
			if len(data) > 0 {
				llr[i] = int8(data[i%len(data)])
			}
		}
		shard := make([]byte, c.ShardSize())
		iters, ok := c.Decode(shard, llr)
		if iters < 0 || iters > MaxIterations {
			t.Fatalf("%d iterations", iters)
		}
		if ok {
			// A success must be a codeword carrying its CRC: re-encoding reproduces the decisions.
			packed := make([]byte, (c.N()+7)/8)
			c.Encode(packed, shard)
			if again, ok2 := c.Decode(make([]byte, c.ShardSize()), llrOf(packed, c.N(), 50)); !ok2 || again != 0 {
				t.Fatalf("decoded shard does not re-encode to a valid codeword")
			}
		}
	})
}

func BenchmarkEncode(b *testing.B) {
	c := mustNew(b, 2)
	shard := randomBytes(rand.New(rand.NewPCG(1, 1)), c.ShardSize())
	dst := make([]byte, (c.N()+7)/8)
	b.SetBytes(int64(c.ShardSize()))
	for b.Loop() {
		c.Encode(dst, shard)
	}
}
