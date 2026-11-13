// Package noasm is a throwaway M1 measurement: Reed-Solomon (GF(2^16)) and zstd throughput on one
// core with and without the noasm and nounsafe build tags (DESIGN.md section 12). Run:
//
//	go test -run '^$' -bench . -cpu 1 ./spikes/noasm/
//	go test -tags noasm -run '^$' -bench . -cpu 1 ./spikes/noasm/
//	go test -tags nounsafe ... and -tags noasm,nounsafe ...
package noasm

import (
	"bytes"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/klauspost/reedsolomon"
)

func rsBlock(b *testing.B, k, m, size int) (reedsolomon.Encoder, [][]byte) {
	enc, err := reedsolomon.New(k, m, reedsolomon.WithLeopardGF16(true), reedsolomon.WithMaxGoroutines(1))
	if err != nil {
		b.Fatal(err)
	}
	r := rand.New(rand.NewPCG(1, 2))
	shards := make([][]byte, k+m)
	for i := range shards {
		shards[i] = make([]byte, size)
		for j := range shards[i] {
			shards[i][j] = byte(r.Uint32())
		}
	}
	if err := enc.Encode(shards); err != nil {
		b.Fatal(err)
	}
	return enc, shards
}

var rsShapes = []struct {
	name       string
	k, m, size int
}{
	{"K32768_M6554_S128", 32768, 6554, 128},
	{"K100_M20_S128", 100, 20, 128},
}

// BenchmarkRSEncode reports MB/s of data shards encoded.
func BenchmarkRSEncode(b *testing.B) {
	for _, s := range rsShapes {
		b.Run(s.name, func(b *testing.B) {
			enc, shards := rsBlock(b, s.k, s.m, s.size)
			b.SetBytes(int64(s.k * s.size))
			for b.Loop() {
				if err := enc.Encode(shards); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkRSReconstruct loses M data shards (the worst case, "lostM") or M/10 ("lostM10") and
// reports MB/s of block data.
func BenchmarkRSReconstruct(b *testing.B) {
	for _, s := range rsShapes {
		for _, frac := range []int{1, 10} {
			name := s.name + "_lostM"
			if frac != 1 {
				name += "10"
			}
			b.Run(name, func(b *testing.B) {
				enc, shards := rsBlock(b, s.k, s.m, s.size)
				work := make([][]byte, len(shards))
				lost := max(1, s.m/frac)
				b.SetBytes(int64(s.k * s.size))
				for b.Loop() {
					copy(work, shards)
					for i := range lost {
						work[i*s.k/lost] = work[i*s.k/lost][:0]
					}
					if err := enc.ReconstructData(work); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// corpus returns about 16 MiB of compressible data (Go source of the standard library) and as much
// random data.
func corpus(b *testing.B) (text, random []byte) {
	var buf bytes.Buffer
	_ = filepath.WalkDir(filepath.Join(runtime.GOROOT(), "src"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || buf.Len() >= 16<<20 {
			return fs.SkipAll
		}
		if !d.IsDir() && strings.HasSuffix(p, ".go") {
			if data, err := os.ReadFile(p); err == nil {
				buf.Write(data)
			}
		}
		return nil
	})
	if buf.Len() < 1<<20 {
		b.Skip("no Go source found")
	}
	text = buf.Bytes()
	random = make([]byte, len(text))
	r := rand.New(rand.NewPCG(3, 4))
	for i := range random {
		random[i] = byte(r.Uint32())
	}
	return text, random
}

var levels = []struct {
	name  string
	level zstd.EncoderLevel
}{{"default", zstd.SpeedDefault}, {"best", zstd.SpeedBestCompression}}

// BenchmarkZstd reports MB/s of uncompressed data, compressing and decompressing on one goroutine.
func BenchmarkZstd(b *testing.B) {
	text, random := corpus(b)
	for _, in := range []struct {
		name string
		data []byte
	}{{"text", text}, {"random", random}} {
		for _, l := range levels {
			enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(l.level), zstd.WithEncoderConcurrency(1))
			if err != nil {
				b.Fatal(err)
			}
			dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
			if err != nil {
				b.Fatal(err)
			}
			comp := enc.EncodeAll(in.data, nil)
			b.Run("compress_"+in.name+"_"+l.name, func(b *testing.B) {
				b.SetBytes(int64(len(in.data)))
				b.ReportMetric(float64(len(comp))/float64(len(in.data)), "ratio")
				dst := make([]byte, 0, len(comp)+1024)
				for b.Loop() {
					dst = enc.EncodeAll(in.data, dst[:0])
				}
			})
			b.Run("decompress_"+in.name+"_"+l.name, func(b *testing.B) {
				b.SetBytes(int64(len(in.data)))
				dst := make([]byte, 0, len(in.data))
				for b.Loop() {
					if dst, err = dec.DecodeAll(comp, dst[:0]); err != nil {
						b.Fatal(err)
					}
				}
			})
			_ = enc.Close()
			dec.Close()
		}
	}
}
