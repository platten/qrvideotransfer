package bad

import "reflect"

var z int

// D takes an address through a method value.
func D() uintptr {
	f := reflect.ValueOf(&z).Pointer
	return f()
}
