package bad

import "reflect"

var v any = 0

// H reads the data word of an interface value, an address.
func H() uintptr { return reflect.ValueOf(&v).Elem().InterfaceData()[1] }
