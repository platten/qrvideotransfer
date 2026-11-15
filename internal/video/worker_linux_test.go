package video

import "testing"

// TestWorkerParentKilledReserved: on Linux a worker whose parent dies is killed by the kernel (its
// parent-death signal), even when its guest has reserved 512 MiB and spins, which keeps it from
// exiting through its pipe (docs/spikes/S1.md section 14.4). The parent is a helper process (the
// test binary as a fake parent), killed while the worker spins.
func TestWorkerParentKilledReserved(t *testing.T) {
	t.Parallel()
	parentKilled(t, "spinbig")
}
