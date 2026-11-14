package bad

import (
	"github.com/platten/qrvideotransfer/spikes/addr"
	"github.com/platten/qrvideotransfer/tools/dev"
)

// I reaches code the rule does not scan.
func I() uintptr { return addr.Of(&w) + uintptr(dev.N) }
