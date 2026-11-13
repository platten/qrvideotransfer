//go:build !unsandboxedhevc

package video

import (
	"context"
	"fmt"
)

// FastPathBuilt reports whether this build contains the HEVC fast path (build tag unsandboxedhevc).
// It stays out of release builds until the decoder can be built without its hand-written assembly
// (DESIGN.md section 12).
const FastPathBuilt = false

// newFastDecoder is never reached in this build: NewEmbedded refuses the option first.
func newFastDecoder(context.Context, *ff, string, probeInfo, DecodeConfig) (Decoder, error) {
	return nil, fmt.Errorf("%w: this build has no HEVC fast path", ErrUnsupported)
}
