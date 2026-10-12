package service

import (
	"context"
	"io"
	"stash/internal/media"
)

func extractFrames(ctx context.Context, r io.Reader) ([][]byte, error) {
	return media.ExtractFrames(ctx, r)
}
