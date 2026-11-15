package video

// TEMPORARY measurement, removed before review.

import (
	"encoding/binary"
	"fmt"

	"golang.org/x/sys/unix"
)

// loadAverage is the 1, 5 and 15 minute load averages (struct loadavg: three uint32 and a scale).
func loadAverage() string {
	b, err := unix.SysctlRaw("vm.loadavg")
	if err != nil || len(b) < 16 {
		return "unknown"
	}
	scale := float64(binary.LittleEndian.Uint64(b[len(b)-8:]))
	if scale == 0 {
		return "unknown"
	}
	l := func(i int) float64 { return float64(binary.LittleEndian.Uint32(b[4*i:])) / scale }
	return fmt.Sprintf("%.2f %.2f %.2f", l(0), l(1), l(2))
}
