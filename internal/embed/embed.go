package embed

import (
	"context"
	"errors"
)

// Embedder abstracts a vector-embedding provider (text and/or image) behind a
// single interface so the service never depends on a concrete transport or
// vendor. Implementations must be safe for concurrent use.
type Embedder interface {
	// EmbedText returns the embedding of a piece of text.
	EmbedText(ctx context.Context, text string) ([]float32, error)
	// EmbedImage returns the embedding of raw image bytes.
	EmbedImage(ctx context.Context, data []byte) ([]float32, error)
}

// ErrUnsupported is returned by an Embedder that cannot perform a requested
// operation (e.g. a text-only provider asked to embed an image).
var ErrUnsupported = errors.New("embedder: operation not supported")
