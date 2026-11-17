package pipeline

import (
	"context"
	"errors"
)

// Decode recovers the stream carried by the recordings and unpacks it (DESIGN.md section 10).
func Decode(ctx context.Context, c DecodeConfig) (*DecodeResult, error) {
	return nil, errors.New("pipeline: not implemented")
}
