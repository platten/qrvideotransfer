package rs

import "errors"

// ErrTooFewShards reports that fewer than K shards of a block are present.
var ErrTooFewShards = errors.New("rs: too few shards to reconstruct the block")

// Codec encodes and reconstructs one block of K data and M parity shards.
type Codec struct{}

// NewCodec returns a codec for k data and m parity shards.
func NewCodec(k, m int) (*Codec, error) { return nil, errors.New("not implemented") }

// Encode computes the parity shards.
func (c *Codec) Encode(shards [][]byte) error { return errors.New("not implemented") }

// Reconstruct restores the missing data shards.
func (c *Codec) Reconstruct(shards [][]byte) error { return errors.New("not implemented") }
