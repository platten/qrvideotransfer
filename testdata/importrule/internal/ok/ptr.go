package ok

type p struct{}

// Pointer is not reflect's.
func (p) Pointer() uintptr { return 0 }

var _ = p{}.Pointer()
