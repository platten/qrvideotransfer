package frame

import "fmt"

// LDPCN is the mother codeword length of the inner code.
const LDPCN = 1944

// ModulationInfo is one row of the modulation registry (FORMAT.md section 7).
type ModulationInfo struct {
	ID          uint8
	Name        string
	BitsPerCell int // b
	Levels      int // L
}

// InnerInfo is one row of the inner-code registry (FORMAT.md section 8).
type InnerInfo struct {
	ID        uint8
	MotherK   int // information length of the mother code
	ShardSize int // S, bytes
	InfoBits  int // 8 S + 32 (shard and CRC-32C)
	Shortened int // MotherK - InfoBits zero bits that are not sent
	Sent      int // LDPCN - Shortened
}

var modulations = [...]ModulationInfo{
	{1, "Gray2", 1, 2},
	{2, "Gray4", 2, 4},
	{3, "RGB8", 3, 8},
	{4, "Gray8", 3, 8},
}

// innerShapes holds the mother information length and shard size of inner codes 1 to 4.
var innerShapes = [...][2]int{{1620, 192}, {1458, 128}, {1296, 128}, {972, 64}}

// LookupModulation returns the registry row of a modulation id, or an error naming an unknown id.
func LookupModulation(id uint8) (ModulationInfo, error) {
	if id < 1 || int(id) > len(modulations) {
		return ModulationInfo{}, fmt.Errorf("frame: unknown modulation %d", id)
	}
	return modulations[id-1], nil
}

// LookupInner returns the registry row of an inner-code id, or an error naming an unknown id.
func LookupInner(id uint8) (InnerInfo, error) {
	if id < 1 || int(id) > len(innerShapes) {
		return InnerInfo{}, fmt.Errorf("frame: unknown inner code %d", id)
	}
	k, s := innerShapes[id-1][0], innerShapes[id-1][1]
	info := 8*s + 32
	return InnerInfo{ID: id, MotherK: k, ShardSize: s, InfoBits: info, Shortened: k - info, Sent: LDPCN - (k - info)}, nil
}
