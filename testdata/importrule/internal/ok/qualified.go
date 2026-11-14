package ok

import (
	"reflect"
	"sync/atomic"
)

// A package-qualified name is not a selector on a value: here a type and a Kind constant.
var (
	_ atomic.Pointer[int]
	_ = reflect.Pointer
)
