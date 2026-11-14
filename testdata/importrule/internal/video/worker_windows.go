package video

import "reflect"

var y int

// addr is the one reviewed exception.
func addr() uintptr { return reflect.ValueOf(&y).Pointer() }
