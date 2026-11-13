package ldpc

import "errors"

// MaxIterations is the most decoding iterations spent on one codeword (docs/SECURITY.md section 5).
const MaxIterations = 25

// Code is one shortened inner code of docs/FORMAT.md section 8. It implements fec.InnerCode. A Code
// holds its decoder's working memory, so it is not safe for concurrent use: each worker makes its own.
type Code struct {
	rate Rate
	info int
}

// New returns the inner code with header id 1 to 4.
func New(id uint8) (*Code, error) { return nil, errors.New("not implemented") }

func newCode(r Rate, info int) *Code { return &Code{rate: r, info: info} }

// ID returns the inner-code id.
func (c *Code) ID() uint8 { return 0 }

// ShardSize returns the shard size in bytes.
func (c *Code) ShardSize() int { return 0 }

// N returns the number of sent bits.
func (c *Code) N() int { return 0 }

// Rate returns the rate of the mother code.
func (c *Code) Rate() Rate { return 0 }

// Encode packs the sent bits of shard into dst.
func (c *Code) Encode(dst, shard []byte) {}

// Decode decodes llr into shard.
func (c *Code) Decode(shard []byte, llr []int8) (iters int, ok bool) { return 0, false }

func (c *Code) encodeBits(info []uint8) []uint8 { return make([]uint8, N) }

func (c *Code) decodeBits(llr []int8) (iters int, ok bool) { return 0, false }

func (c *Code) bit(i int) uint8 { return 0 }
