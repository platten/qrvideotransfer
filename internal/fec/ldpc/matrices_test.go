package ldpc

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"testing/iotest"
)

// dataSHA256 pins ieee80211n_1944.txt. The values were compared entry for
// entry across independent sources (see the file's header) before this
// hash was recorded; change it only after repeating that comparison.
const dataSHA256 = "c51a02dd2b4916fb182df0e7d02d9d5824eb6b6c0f6dd7a6895a220cc7a3514e"

var allRates = []Rate{Rate1_2, Rate2_3, Rate3_4, Rate5_6}

func TestDataFileSHA256(t *testing.T) {
	sum := sha256.Sum256(baseData)
	if got := hex.EncodeToString(sum[:]); got != dataSHA256 {
		t.Fatalf("SHA-256 of ieee80211n_1944.txt = %s, want %s", got, dataSHA256)
	}
}

func TestRate(t *testing.T) {
	tests := []struct {
		r    Rate
		s    string
		rows int
	}{
		{Rate1_2, "1/2", 12}, {Rate2_3, "2/3", 8}, {Rate3_4, "3/4", 6}, {Rate5_6, "5/6", 4},
		{0, "invalid", 0}, {9, "invalid", 0},
	}
	for _, tt := range tests {
		if got := tt.r.String(); got != tt.s {
			t.Errorf("Rate(%d).String() = %q, want %q", tt.r, got, tt.s)
		}
		if got := tt.r.Rows(); got != tt.rows {
			t.Errorf("Rate(%d).Rows() = %d, want %d", tt.r, got, tt.rows)
		}
	}
}

func TestBase1944Dimensions(t *testing.T) {
	if N != 1944 || Z != 81 || BaseCols != 24 {
		t.Fatalf("N, Z, BaseCols = %d, %d, %d", N, Z, BaseCols)
	}
	wantK := map[Rate]int{Rate1_2: 972, Rate2_3: 1296, Rate3_4: 1458, Rate5_6: 1620}
	for _, r := range allRates {
		b, ok := Base1944(r)
		if !ok {
			t.Fatalf("Base1944(%v) not found", r)
		}
		if b.Rate != r {
			t.Errorf("Base1944(%v).Rate = %v", r, b.Rate)
		}
		if len(b.Shift) != r.Rows() {
			t.Fatalf("rate %v: %d rows, want %d", r, len(b.Shift), r.Rows())
		}
		for i, row := range b.Shift {
			if len(row) != BaseCols {
				t.Fatalf("rate %v row %d: %d columns", r, i, len(row))
			}
			for j, v := range row {
				if v < -1 || v >= Z {
					t.Errorf("rate %v (%d,%d) = %d out of range", r, i, j, v)
				}
			}
		}
		if b.K() != wantK[r] {
			t.Errorf("rate %v: K = %d, want %d", r, b.K(), wantK[r])
		}
	}
}

// TestBase1944DualDiagonal checks the parity part of every 802.11n base
// matrix: the first parity column has weight 3 with shift 1 at the top and
// bottom rows and shift 0 in one middle row; the remaining parity columns
// form a dual diagonal of unshifted identities.
func TestBase1944DualDiagonal(t *testing.T) {
	for _, r := range allRates {
		b, _ := Base1944(r)
		m := len(b.Shift)
		kb := BaseCols - m
		middle := 0
		for i := 0; i < m; i++ {
			v := b.Shift[i][kb]
			switch {
			case i == 0 || i == m-1:
				if v != 1 {
					t.Errorf("rate %v: column %d row %d = %d, want 1", r, kb, i, v)
				}
			case v == 0:
				middle++
			case v != -1:
				t.Errorf("rate %v: column %d row %d = %d, want 0 or -1", r, kb, i, v)
			}
		}
		if middle != 1 {
			t.Errorf("rate %v: column %d has %d middle entries, want 1", r, kb, middle)
		}
		for j := 1; j < m; j++ {
			for i := 0; i < m; i++ {
				want := -1
				if i == j-1 || i == j {
					want = 0
				}
				if got := b.Shift[i][kb+j]; got != want {
					t.Errorf("rate %v: (%d,%d) = %d, want %d", r, i, kb+j, got, want)
				}
			}
		}
	}
}

func TestBase1944Values(t *testing.T) {
	// Corners and a few interior entries, read from the data file by eye;
	// the SHA-256 test covers the rest.
	tests := []struct {
		r       Rate
		i, j, v int
	}{
		{Rate1_2, 0, 0, 57}, {Rate1_2, 9, 0, -1}, {Rate1_2, 11, 23, 0},
		{Rate2_3, 0, 0, 61}, {Rate2_3, 7, 15, 58},
		{Rate3_4, 0, 0, 48}, {Rate3_4, 0, 11, 80}, {Rate3_4, 5, 18, 1},
		{Rate5_6, 0, 0, 13}, {Rate5_6, 3, 20, 1},
	}
	for _, tt := range tests {
		b, _ := Base1944(tt.r)
		if got := b.Shift[tt.i][tt.j]; got != tt.v {
			t.Errorf("rate %v (%d,%d) = %d, want %d", tt.r, tt.i, tt.j, got, tt.v)
		}
	}
}

func TestBase1944ReturnsCopy(t *testing.T) {
	b, _ := Base1944(Rate3_4)
	b.Shift[0][0] = 7
	c, _ := Base1944(Rate3_4)
	if c.Shift[0][0] != 48 {
		t.Fatalf("modifying a returned matrix changed the table")
	}
}

func TestBase1944InvalidRate(t *testing.T) {
	for _, r := range []Rate{0, 5, 255} {
		if _, ok := Base1944(r); ok {
			t.Errorf("Base1944(%d) ok", r)
		}
	}
}

// section returns the data file's lines for rate s, including its header.
func section(t *testing.T, s string) string {
	t.Helper()
	text := string(baseData)
	i := strings.Index(text, "rate "+s+"\n")
	if i < 0 {
		t.Fatalf("rate %s not in data", s)
	}
	j := strings.Index(text[i+1:], "rate ")
	if j < 0 {
		return text[i:]
	}
	return text[i : i+1+j]
}

// trimLast removes the last non-empty line of s.
func trimLast(s string) string {
	s = strings.TrimRight(s, "\n")
	return s[:strings.LastIndex(s, "\n")+1]
}

func TestParseBaseMatricesOK(t *testing.T) {
	// Reordered sections, CRLF line ends and extra spacing are accepted.
	in := "# c\r\n\r\n" + section(t, "5/6") + section(t, "1/2") + "  " + section(t, "3/4") + section(t, "2/3")
	in = strings.ReplaceAll(in, "\n", "\r\n")
	got, err := ParseBaseMatrices(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range allRates {
		want, _ := Base1944(r)
		g := got[r]
		if g.Rate != r || len(g.Shift) != len(want.Shift) {
			t.Fatalf("rate %v: got %v with %d rows", r, g.Rate, len(g.Shift))
		}
		for i := range want.Shift {
			for j := range want.Shift[i] {
				if g.Shift[i][j] != want.Shift[i][j] {
					t.Fatalf("rate %v (%d,%d) differs", r, i, j)
				}
			}
		}
	}
}

func TestParseBaseMatricesErrors(t *testing.T) {
	all := section(t, "1/2") + section(t, "2/3") + section(t, "3/4") + section(t, "5/6")
	row := strings.Repeat("-1 ", 24) + "\n"
	tests := []struct {
		name, in, want string
	}{
		{"empty", "", "0 of 4 rates"},
		{"row before header", row + all, "before a rate header"},
		{"unknown rate", "rate 1/3\n" + all, "bad header"},
		{"header without rate", "rate\n" + all, "bad header"},
		{"header with extra field", "rate 1/2 x\n" + all, "bad header"},
		{"duplicate rate", all + section(t, "3/4"), "repeated"},
		{"missing rate", section(t, "1/2") + section(t, "2/3") + section(t, "3/4"), "3 of 4 rates"},
		{"too few rows then header", trimLast(section(t, "1/2")) + section(t, "2/3") + section(t, "3/4") + section(t, "5/6"), "has 11 rows"},
		{"too few rows at end", trimLast(all), "has 3 rows"},
		{"too many rows", all + row, "more than 4 rows"},
		{"wrong column count", strings.Replace(all, "\n 3 -1 28", "\n 3 28", 1), "23 columns"},
		{"not an integer", strings.Replace(all, "57 -1", "5x -1", 1), "bad shift"},
		{"shift too large", strings.Replace(all, "57 -1", "81 -1", 1), "bad shift"},
		{"shift too small", strings.Replace(all, "57 -1", "-2 -1", 1), "bad shift"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseBaseMatrices(strings.NewReader(tt.in))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestParseBaseMatricesReadError(t *testing.T) {
	boom := errors.New("boom")
	_, err := ParseBaseMatrices(iotest.ErrReader(boom))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}
