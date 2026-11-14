package bad

import "reflect"

var x int

// A takes an address with reflect.
func A() uintptr { return reflect.ValueOf(&x).Pointer() }

// B takes an address with reflect.
func B() uintptr { return reflect.ValueOf(&x).Elem().UnsafeAddr() }
