package bad

import "example.com/other"

// E takes an address from a reflect.Value made in another package, without importing reflect.
func E() uintptr { return other.V().UnsafePointer() }
