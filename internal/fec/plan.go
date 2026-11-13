package fec

// Limits of the outer code (docs/FORMAT.md sections 10 and 11).
const (
	MaxShards     = 32768     // largest K and largest M
	MaxBlocks     = 1<<24 - 1 // B travels in 3 bytes
	MaxStreamLen  = 1<<48 - 1 // L travels in 6 bytes
	DefaultParity = 20        // default parity percentage
)

// Shape returns the block count B and data shards per block K for a stream of streamLen bytes cut
// into shards of shardSize bytes (FORMAT.md section 11).
func Shape(streamLen int64, shardSize int) (blocks, k int, err error) {
	return 0, 0, nil
}

// DefaultM returns the encoder's default parity shards per block, max(1, ceil(k*parityPercent/100)),
// clamped to MaxShards.
func DefaultM(k, parityPercent int) int {
	return 0
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
	return Plan{}, nil
}

// Locate returns the block and the shard within it carried by packet, which must be in
// 0..Packets-1.
func (p Plan) Locate(packet int64) (block, shard int) {
	return 0, 0
}

// Packet returns the packet carried by codeword c, in 1..C-1, of frame f, where C is the number of
// codewords per frame. Codeword 0 carries the descriptor.
func (p Plan) Packet(frame, codeword, codewordsPerFrame int) int64 {
	return 0
}

// Frames returns the frame count F for codewordsPerFrame codewords per frame, or 0 when
// codewordsPerFrame is below 2.
func (p Plan) Frames(codewordsPerFrame int) int {
	return 0
}

// DataOffset returns the offset in the stream of data shard i of block b. Bytes from the offset that
// lie beyond the stream are zero in the shard.
func (p Plan) DataOffset(block, shard int) int64 {
	return 0
}
