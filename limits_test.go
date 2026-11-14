package qrvt

import (
	"testing"

	"github.com/platten/qrvideotransfer/internal/payload"
)

// TestPayloadLimitsMatch: internal/payload may not import this package, so it keeps its own copies
// of the default manifest limits and warning codes; they must equal these.
func TestPayloadLimitsMatch(t *testing.T) {
	if payload.DefaultMaxEntries != DefaultMaxEntries || payload.DefaultMaxManifestBytes != DefaultMaxManifestBytes {
		t.Errorf("payload defaults %d and %d, root %d and %d", payload.DefaultMaxEntries,
			payload.DefaultMaxManifestBytes, DefaultMaxEntries, DefaultMaxManifestBytes)
	}
	for _, c := range []struct {
		root WarningCode
		pay  payload.WarningCode
	}{
		{WarnSkippedFile, payload.WarnSkippedFile},
		{WarnReceiverLimit, payload.WarnReceiverLimit},
	} {
		if string(c.root) != string(c.pay) {
			t.Errorf("warning code %q in the root package, %q in payload", c.root, c.pay)
		}
	}
}
