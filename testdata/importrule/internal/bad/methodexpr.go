package bad

import "reflect"

var y int

// C takes an address through a method expression.
func C() uintptr { return reflect.Value.Pointer(reflect.ValueOf(&y)) }
