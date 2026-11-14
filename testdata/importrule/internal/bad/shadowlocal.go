package bad

import "reflect"

var w int

// F names a local variable after the reflect package.
func F() uintptr {
	reflect := reflect.ValueOf(&w)
	return reflect.Pointer()
}
