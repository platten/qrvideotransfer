//go:build !unsandboxedhevc

package video

import "context"

// FastPathBuilt reports whether this build contains the HEVC fast path (build tag unsandboxedhevc).
// It stays out of release builds until the decoder can be built without its hand-written assembly
// (DESIGN.md section 12).
const FastPathBuilt = false

func newFastDecoder(context.Context, *ff, string, probeInfo, DecodeConfig) (Decoder, error) {
	panic("not implemented")
}
