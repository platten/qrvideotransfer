package ldpc

import (
	"bufio"
	_ "embed"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
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

var rateNames = [...]string{Rate1_2: "1/2", Rate2_3: "2/3", Rate3_4: "3/4", Rate5_6: "5/6"}
var rateRows = [...]int{Rate1_2: 12, Rate2_3: 8, Rate3_4: 6, Rate5_6: 4}

func (r Rate) valid() bool { return r >= Rate1_2 && r <= Rate5_6 }

// String returns the rate as "1/2", "2/3", "3/4" or "5/6", or "invalid".
func (r Rate) String() string {
	if !r.valid() {
		return "invalid"
	}
	return rateNames[r]
}

// Rows returns the number of block rows of the base matrix of rate r
// (12, 8, 6 or 4), or 0 for an invalid rate.
func (r Rate) Rows() int {
	if !r.valid() {
		return 0
	}
	return rateRows[r]
}

// BaseMatrix is a parity-check base matrix of the 802.11n code of length
// 1944. Shift[i][j] is -1 for an all-zero Z x Z block, otherwise the
// right cyclic shift (0 to Z-1) of the Z x Z identity.
type BaseMatrix struct {
	Rate  Rate
	Shift [][]int
}

// K returns the number of information bits of the mother code.
func (b BaseMatrix) K() int { return (BaseCols - len(b.Shift)) * Z }

//go:embed ieee80211n_1944.txt
var baseData []byte

// ParseBaseMatrices parses the text format of ieee80211n_1944.txt, which
// must hold each of the four rates exactly once.
func ParseBaseMatrices(r io.Reader) (map[Rate]BaseMatrix, error) {
	out := make(map[Rate]BaseMatrix, 4)
	var cur *BaseMatrix
	finish := func(line int) error {
		if cur != nil && len(cur.Shift) != cur.Rate.Rows() {
			return fmt.Errorf("line %d: rate %v has %d rows, want %d", line, cur.Rate, len(cur.Shift), cur.Rate.Rows())
		}
		return nil
	}
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		f := strings.Fields(sc.Text())
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		if f[0] == "rate" {
			if err := finish(n); err != nil {
				return nil, err
			}
			rate := Rate(0)
			for k := Rate1_2; k <= Rate5_6 && len(f) == 2; k++ {
				if f[1] == k.String() {
					rate = k
				}
			}
			if rate == 0 {
				return nil, fmt.Errorf("line %d: bad header %q", n, sc.Text())
			}
			if _, dup := out[rate]; dup {
				return nil, fmt.Errorf("line %d: rate %v repeated", n, rate)
			}
			out[rate] = BaseMatrix{Rate: rate}
			cur = &BaseMatrix{Rate: rate}
			continue
		}
		if cur == nil {
			return nil, fmt.Errorf("line %d: row before a rate header", n)
		}
		if len(cur.Shift) == cur.Rate.Rows() {
			return nil, fmt.Errorf("line %d: rate %v has more than %d rows", n, cur.Rate, cur.Rate.Rows())
		}
		if len(f) != BaseCols {
			return nil, fmt.Errorf("line %d: %d columns, want %d", n, len(f), BaseCols)
		}
		row := make([]int, BaseCols)
		for j, s := range f {
			v, err := strconv.Atoi(s)
			if err != nil || v < -1 || v >= Z {
				return nil, fmt.Errorf("line %d: bad shift %q", n, s)
			}
			row[j] = v
		}
		cur.Shift = append(cur.Shift, row)
		out[cur.Rate] = *cur
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if err := finish(n); err != nil {
		return nil, err
	}
	if len(out) != 4 {
		return nil, fmt.Errorf("%d of 4 rates present", len(out))
	}
	return out, nil
}

var embedded = sync.OnceValue(func() map[Rate]BaseMatrix {
	m, err := ParseBaseMatrices(strings.NewReader(string(baseData)))
	if err != nil {
		panic("ldpc: embedded matrices: " + err.Error())
	}
	return m
})

// Base1944 returns a copy of the base matrix of rate r, and false if r is
// not one of the four rates.
func Base1944(r Rate) (BaseMatrix, bool) {
	b, ok := embedded()[r]
	if !ok {
		return BaseMatrix{}, false
	}
	c := BaseMatrix{Rate: r, Shift: make([][]int, len(b.Shift))}
	for i, row := range b.Shift {
		c.Shift[i] = append([]int(nil), row...)
	}
	return c, true
}
