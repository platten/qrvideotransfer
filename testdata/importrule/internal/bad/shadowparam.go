package bad

import (
	"reflect"
	"sync/atomic"
)

var _ atomic.Int32

// G names a parameter after the imported atomic package.
func G(atomic reflect.Value) uintptr { return atomic.UnsafeAddr() }
