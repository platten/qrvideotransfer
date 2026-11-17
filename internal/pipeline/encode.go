package pipeline

import (
	"context"
	"errors"
)

// Encode packs the inputs, protects the stream and writes the code video (DESIGN.md sections 8, 9
// and 12).
func Encode(ctx context.Context, c EncodeConfig) (*EncodeResult, error) {
	return nil, errors.New("pipeline: not implemented")
}
