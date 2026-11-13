package rs

import (
	"errors"
	"fmt"

	"github.com/klauspost/reedsolomon"

	"github.com/platten/qrvideotransfer/internal/fec"
)

// ErrTooFewShards reports that fewer than K shards of a block are present.
var ErrTooFewShards = errors.New("rs: too few shards to reconstruct the block")

// Codec encodes and reconstructs one block of K data and M parity shards with Leopard Reed-Solomon
// over GF(2^16) (FORMAT.md section 11). Shards are all the same size, a positive multiple of 64
// bytes; shard i of a block is shards[i], data first. The whole block is held in memory: the
// library's streaming API does not support GF(2^16).
type Codec struct {
	enc  reedsolomon.Encoder
	k, m int
}

// NewCodec returns a codec for k data and m parity shards, each 1 to fec.MaxShards.
func NewCodec(k, m int) (*Codec, error) {
	if k < 1 || k > fec.MaxShards || m < 1 || m > fec.MaxShards {
		return nil, fmt.Errorf("rs: %d data and %d parity shards out of range", k, m)
	}
	enc, err := reedsolomon.New(k, m, reedsolomon.WithLeopardGF16(true))
	if err != nil {
		return nil, fmt.Errorf("rs: %d data and %d parity shards: %w", k, m, err)
	}
	return &Codec{enc: enc, k: k, m: m}, nil
}

// Encode computes the parity shards shards[K:] from the data shards shards[:K]. Every shard must be
// allocated, all of the same size.
func (c *Codec) Encode(shards [][]byte) error {
	if len(shards) != c.k+c.m {
		return fmt.Errorf("rs: %d shards, want %d", len(shards), c.k+c.m)
	}
	for _, s := range shards {
		if len(s) == 0 {
			return errors.New("rs: Encode with a missing shard")
		}
	}
	if err := c.enc.Encode(shards); err != nil {
		return fmt.Errorf("rs: %w", err)
	}
	return nil
}

// Reconstruct restores the missing data shards of a block from any K present shards. A missing
// shard is a nil or empty entry; restored data shards reuse its capacity when it suffices. Missing
// parity shards are not restored.
func (c *Codec) Reconstruct(shards [][]byte) error {
	if len(shards) != c.k+c.m {
		return fmt.Errorf("rs: %d shards, want %d", len(shards), c.k+c.m)
	}
	n := 0
	for _, s := range shards {
		if len(s) != 0 {
			n++
		}
	}
	if n < c.k {
		return fmt.Errorf("%w: %d of %d", ErrTooFewShards, n, c.k)
	}
	if err := c.enc.ReconstructData(shards); err != nil {
		return fmt.Errorf("rs: %w", err)
	}
	return nil
}
