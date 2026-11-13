package ldpc

import (
	_ "embed"
	"errors"
	"io"
)

// Z is the lifting size of the 802.11n codes of length 1944.
const Z = 81

// BaseCols is the number of block columns of every base matrix.
const BaseCols = 24

// N is the codeword length in bits.
const N = BaseCols * Z

// Rate identifies one of the four 802.11n code rates.
type Rate uint8

// The four 802.11n code rates.
const (
	Rate1_2 Rate = iota + 1
	Rate2_3
	Rate3_4
	Rate5_6
)

// String returns the rate as "1/2", "2/3", "3/4" or "5/6", or "invalid".
func (r Rate) String() string { return "" }

// Rows returns the number of block rows of the base matrix of rate r
// (12, 8, 6 or 4), or 0 for an invalid rate.
func (r Rate) Rows() int { return 0 }

// BaseMatrix is a parity-check base matrix of the 802.11n code of length
// 1944. Shift[i][j] is -1 for an all-zero Z x Z block, otherwise the
// right cyclic shift (0 to Z-1) of the Z x Z identity.
type BaseMatrix struct {
	Rate  Rate
	Shift [][]int
}

// K returns the number of information bits of the mother code.
func (b BaseMatrix) K() int { return 0 }

//go:embed ieee80211n_1944.txt
var baseData []byte

// ParseBaseMatrices parses the text format of ieee80211n_1944.txt, which
// must hold each of the four rates exactly once.
func ParseBaseMatrices(r io.Reader) (map[Rate]BaseMatrix, error) {
	return nil, errors.New("not implemented")
}

// Base1944 returns a copy of the base matrix of rate r, and false if r is
// not one of the four rates.
func Base1944(r Rate) (BaseMatrix, bool) { return BaseMatrix{}, false }
