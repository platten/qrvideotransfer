// Package fec holds the error-correction interfaces, the outer-code block plan and the transmission
// order of docs/FORMAT.md section 11. The inner code is in package ldpc, the header code in package
// conv and the Reed-Solomon codec in package rs.
package fec

// InnerCode is one inner code of docs/FORMAT.md section 8: it carries exactly one shard per codeword.
type InnerCode interface {
	// ID returns the inner-code id of the header.
	ID() uint8
	// ShardSize returns the shard size S in bytes.
	ShardSize() int
	// N returns the number of sent bits per codeword.
	N() int
	// Encode appends the CRC-32C to shard, encodes, drops the shortened bits and packs the N sent
	// bits into dst, eight per byte, most significant bit first.
	Encode(dst []byte, shard []byte)
	// Decode decodes N soft bits (positive means 0) into shard and reports the iterations used and
	// whether the codeword and its CRC were valid.
	Decode(shard []byte, llr []int8) (iters int, ok bool)
}

// ShardStore holds shards of the outer code, typically on disk. Implementations need not be safe for
// concurrent use; one goroutine owns a store.
type ShardStore interface {
	// Put stores shard number shard of block block.
	Put(block, shard int, b []byte) error
	// Get copies the shard into b and reports whether it was present.
	Get(block, shard int, b []byte) (bool, error)
	// Have returns the number of distinct shards stored for block.
	Have(block int) int
	// Close releases the store's resources.
	Close() error
}
