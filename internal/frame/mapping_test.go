package frame

import (
	"strconv"
	"testing"
)

type mappingGolden struct {
	Tables struct {
		Modulations []struct{ ID, BitsPerCell, Levels int }
		Inner       []struct{ ID, MotherK, ShardSize, InfoBits, Shortened, Sent int }
	}
	Capacity []struct {
		GridX, GridY, Modulation, InnerCode, Nbits, CodewordsPerFrame int
	}
	Mappings []struct {
		Session                             uint16
		GridX, GridY, Modulation, InnerCode int
		Nbits, Sent, CodewordsPerFrame      int
		InterleaveSeed                      string
		Probes                              []struct {
			Frame                 uint32
			Codeword, Bit, Rho, Q int
			Scramble              uint8
		}
	}
}

// FORMAT.md sections 7 and 8.
func TestTables(t *testing.T) {
	mods := map[uint8][2]int{1: {1, 2}, 2: {2, 4}, 3: {3, 8}, 4: {3, 8}}
	for id := range uint8(8) {
		m, err := LookupModulation(id)
		want, known := mods[id]
		if known != (err == nil) {
			t.Errorf("modulation %d: err = %v", id, err)
			continue
		}
		if known && (m.ID != id || m.BitsPerCell != want[0] || m.Levels != want[1] || m.Name == "") {
			t.Errorf("modulation %d = %+v", id, m)
		}
	}
	// Id: mother k, shard, info bits, shortened, sent.
	inner := map[uint8][5]int{
		1: {1620, 192, 1568, 52, 1892},
		2: {1458, 128, 1056, 402, 1542},
		3: {1296, 128, 1056, 240, 1704},
		4: {972, 64, 544, 428, 1516},
	}
	for id := range uint8(8) {
		c, err := LookupInner(id)
		want, known := inner[id]
		if known != (err == nil) {
			t.Errorf("inner code %d: err = %v", id, err)
			continue
		}
		if known && (c != InnerInfo{id, want[0], want[1], want[2], want[3], want[4]}) {
			t.Errorf("inner code %d = %+v, want %v", id, c, want)
		}
	}
	var g mappingGolden
	readGolden(t, "mapping.json", &g)
	for _, w := range g.Tables.Modulations {
		if m, _ := LookupModulation(uint8(w.ID)); m.BitsPerCell != w.BitsPerCell || m.Levels != w.Levels {
			t.Errorf("modulation %d = %+v, golden %+v", w.ID, m, w)
		}
	}
	for _, w := range g.Tables.Inner {
		c, _ := LookupInner(uint8(w.ID))
		if c.MotherK != w.MotherK || c.ShardSize != w.ShardSize || c.InfoBits != w.InfoBits || c.Shortened != w.Shortened || c.Sent != w.Sent {
			t.Errorf("inner code %d = %+v, golden %+v", w.ID, c, w)
		}
	}
}

func header(session uint16, gx, gy, mod, inner int) Header {
	return Header{Version: 1, Session: session, TotalFrames: 4, GridX: uint16(gx), GridY: uint16(gy), Modulation: uint8(mod), InnerCode: uint8(inner)}
}

func mustMapping(t testing.TB, h Header) *Mapping {
	t.Helper()
	m, err := NewMapping(h)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// FORMAT.md section 13: the worked numbers at 480 x 270, Gray4, normal, and the position rows.
func TestMappingWorkedExample(t *testing.T) {
	m := mustMapping(t, header(0, 480, 270, 2, 2))
	if m.Bits() != 234224 || m.CodewordsPerFrame() != 151 || m.Sent() != 1542 || m.BitsPerCell() != 2 {
		t.Errorf("Nbits, C, sent, b = %d, %d, %d, %d", m.Bits(), m.CodewordsPerFrame(), m.Sent(), m.BitsPerCell())
	}
	if got := InterleaveSeed(header(0, 480, 270, 2, 2)); got != 0x6eca5aab84a46a75 {
		t.Errorf("interleave seed = %016x", got)
	}
	for _, c := range []struct {
		frame    uint32
		rho, q   int
		scramble uint8
	}{{0, 0, 1885, 1}, {1, 209393, 138625, 0}} {
		if got := m.Rotation(c.frame); got != c.rho {
			t.Errorf("frame %d rho = %d, want %d", c.frame, got, c.rho)
		}
		if got := m.Position(c.frame, 1, 0); got != c.q {
			t.Errorf("frame %d q = %d, want %d", c.frame, got, c.q)
		}
		if got := m.ScrambleBit(c.frame, c.q); got != c.scramble {
			t.Errorf("frame %d scramble bit = %d, want %d", c.frame, got, c.scramble)
		}
	}
}

func TestMappingGolden(t *testing.T) {
	var g mappingGolden
	readGolden(t, "mapping.json", &g)
	for _, c := range g.Capacity {
		m := mustMapping(t, header(1, c.GridX, c.GridY, c.Modulation, c.InnerCode))
		if m.Bits() != c.Nbits || m.CodewordsPerFrame() != c.CodewordsPerFrame {
			t.Errorf("%+v: Nbits, C = %d, %d", c, m.Bits(), m.CodewordsPerFrame())
		}
	}
	for _, c := range g.Mappings {
		h := header(c.Session, c.GridX, c.GridY, c.Modulation, c.InnerCode)
		m := mustMapping(t, h)
		if m.Bits() != c.Nbits || m.Sent() != c.Sent || m.CodewordsPerFrame() != c.CodewordsPerFrame {
			t.Errorf("%+v: Nbits, sent, C = %d, %d, %d", h, m.Bits(), m.Sent(), m.CodewordsPerFrame())
		}
		if got := strconv.FormatUint(InterleaveSeed(h), 16); got != trimZeros(c.InterleaveSeed) {
			t.Errorf("%+v: interleave seed = %s, want %s", h, got, c.InterleaveSeed)
		}
		for _, p := range c.Probes {
			if got := m.Rotation(p.Frame); got != p.Rho {
				t.Errorf("%+v %+v: rho = %d", h, p, got)
			}
			if got := m.Position(p.Frame, p.Codeword, p.Bit); got != p.Q {
				t.Errorf("%+v %+v: q = %d", h, p, got)
			}
			if got := m.ScrambleBit(p.Frame, p.Q); got != p.Scramble {
				t.Errorf("%+v %+v: scramble bit = %d", h, p, got)
			}
		}
	}
}

func trimZeros(s string) string {
	for len(s) > 1 && s[0] == '0' {
		s = s[1:]
	}
	return s
}

// Within a frame, distinct (codeword, bit) pairs land on distinct positions.
func TestPositionsDistinct(t *testing.T) {
	m := mustMapping(t, header(7, 212, 120, 1, 4))
	for _, f := range []uint32{0, 3, 1 << 21} {
		seen := make([]bool, m.Bits())
		for c := range m.CodewordsPerFrame() {
			for b := range m.Sent() {
				q := m.Position(f, c, b)
				if seen[q] {
					t.Fatalf("frame %d: position %d used twice", f, q)
				}
				seen[q] = true
			}
		}
	}
}

// The scrambler is SplitMix64 output bits, most significant first, so about half are ones and
// Scrambler agrees with ScrambleBit.
func TestScrambler(t *testing.T) {
	m := mustMapping(t, header(0x5332, 212, 120, 2, 2))
	s := m.Scrambler(9)
	ones := 0
	for q := range m.Bits() {
		b := s.Bit(q)
		ones += int(b)
		if q%997 == 0 && b != m.ScrambleBit(9, q) {
			t.Fatalf("Scrambler and ScrambleBit differ at %d", q)
		}
	}
	if n := m.Bits(); ones < n*45/100 || ones > n*55/100 {
		t.Errorf("%d ones of %d", ones, n)
	}
}

func TestNewMappingRejects(t *testing.T) {
	bad := []Header{
		header(0, 480, 270, 0, 2), // unknown modulation
		header(0, 480, 270, 2, 5), // unknown inner code
		header(0, 480, 29, 2, 2),  // grid
		{Version: 2, TotalFrames: 4, GridX: 480, GridY: 270, Modulation: 2, InnerCode: 2}, // version
		header(0, 53, 30, 1, 1), // C < 2
	}
	for _, h := range bad {
		if _, err := NewMapping(h); err == nil {
			t.Errorf("NewMapping(%+v) succeeded", h)
		}
	}
}

func BenchmarkNewMapping1080p(b *testing.B) {
	h := header(0, 480, 270, 2, 2)
	for b.Loop() {
		if _, err := NewMapping(h); err != nil {
			b.Fatal(err)
		}
	}
}
