package bad

type p struct{}

// Pointer is not reflect's, but a selector of that name is refused all the same.
func (p) Pointer() uintptr { return 0 }

var _ = p{}.Pointer()
