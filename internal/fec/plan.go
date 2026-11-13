package fec

import (
	"encoding/binary"
	"fmt"

	"github.com/platten/qrvideotransfer/internal/prng"
)

// Limits of the outer code (docs/FORMAT.md sections 10 and 11).
const (
	MaxShards     = 32768     // largest K and largest M
	MaxBlocks     = 1<<24 - 1 // B travels in 3 bytes
	MaxStreamLen  = 1<<48 - 1 // L travels in 6 bytes
	DefaultParity = 20        // default parity percentage
)

// orderStride spreads the blocks over the shard order (FORMAT.md section 11).
const orderStride = 40503

// Shape returns the block count B and data shards per block K for a stream of streamLen bytes cut
// into shards of shardSize bytes (FORMAT.md section 11). shardSize must be a positive multiple of
// 64, the stream non-empty and at most MaxStreamLen, and B at most MaxBlocks.
func Shape(streamLen int64, shardSize int) (blocks, k int, err error) {
	if shardSize <= 0 || shardSize%64 != 0 {
		return 0, 0, fmt.Errorf("fec: shard size %d is not a positive multiple of 64", shardSize)
	}
	if streamLen < 1 || streamLen > MaxStreamLen {
		return 0, 0, fmt.Errorf("fec: stream length %d out of range", streamLen)
	}
	nd := ceilDiv(streamLen, int64(shardSize))
	b := ceilDiv(nd, MaxShards)
	if b > MaxBlocks {
		return 0, 0, fmt.Errorf("fec: stream of %d bytes needs %d blocks, more than %d", streamLen, b, MaxBlocks)
	}
	return int(b), int(ceilDiv(nd, b)), nil
}

func ceilDiv(a, b int64) int64 { return (a + b - 1) / b }

// DefaultM returns the encoder's default parity shards per block, max(1, ceil(k*parityPercent/100)),
// clamped to MaxShards.
func DefaultM(k, parityPercent int) int {
	return min(MaxShards, max(1, int(ceilDiv(int64(k)*int64(max(parityPercent, 0)), 100))))
}

// Plan is the outer-code plan of a stream: its blocks, shards and transmission order.
type Plan struct {
	Blocks, K, M, ShardSize int
	Packets                 int64  // T = Blocks * (K + M)
	Seed                    uint32 // outer order seed of the descriptor

	sigma []uint32 // Shuffle(K+M, Seed("qrvt/outer-order/v1", Seed)); nil in a Plan literal
}

// NewPlan returns the plan for a stream of streamLen bytes, shards of shardSize bytes, m parity
// shards per block and the outer order seed.
func NewPlan(streamLen int64, shardSize, m int, seed uint32) (Plan, error) {
	blocks, k, err := Shape(streamLen, shardSize)
	if err != nil {
		return Plan{}, err
	}
	if m < 1 || m > MaxShards {
		return Plan{}, fmt.Errorf("fec: parity shards per block %d out of range", m)
	}
	p := Plan{Blocks: blocks, K: k, M: m, ShardSize: shardSize, Packets: int64(blocks) * int64(k+m), Seed: seed}
	p.sigma = p.order()
	return p, nil
}

func (p Plan) order() []uint32 {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], p.Seed)
	return prng.Shuffle(p.K+p.M, prng.Seed("qrvt/outer-order/v1", b[:]))
}

// Locate returns the block and the shard within it carried by packet, which must be in
// 0..Packets-1. On a Plan literal it recomputes the shard order on every call.
func (p Plan) Locate(packet int64) (block, shard int) {
	sigma := p.sigma
	if sigma == nil {
		sigma = p.order()
	}
	b, n := int64(p.Blocks), int64(p.K+p.M)
	return int(packet % b), int(sigma[(packet/b+orderStride*(packet%b))%n])
}

// Packet returns the packet carried by codeword c, in 1..C-1, of frame f, where C is the number of
// codewords per frame. Codeword 0 carries the descriptor.
func (p Plan) Packet(frame, codeword, codewordsPerFrame int) int64 {
	return (int64(frame)*int64(codewordsPerFrame-1) + int64(codeword) - 1) % p.Packets
}

// Frames returns the frame count F for codewordsPerFrame codewords per frame, or 0 when
// codewordsPerFrame is below 2.
func (p Plan) Frames(codewordsPerFrame int) int {
	if codewordsPerFrame < 2 {
		return 0
	}
	return int(4 * ceilDiv(ceilDiv(p.Packets, int64(codewordsPerFrame-1)), 4))
}

// DataOffset returns the offset in the stream of data shard i of block b. Bytes from the offset that
// lie beyond the stream are zero in the shard.
func (p Plan) DataOffset(block, shard int) int64 {
	return (int64(block)*int64(p.K) + int64(shard)) * int64(p.ShardSize)
}
