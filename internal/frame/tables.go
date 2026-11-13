package frame

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

// LookupModulation returns the registry row of a modulation id, or an error naming an unknown id.
func LookupModulation(id uint8) (ModulationInfo, error) {
	return ModulationInfo{}, nil
}

// LookupInner returns the registry row of an inner-code id, or an error naming an unknown id.
func LookupInner(id uint8) (InnerInfo, error) {
	return InnerInfo{}, nil
}
