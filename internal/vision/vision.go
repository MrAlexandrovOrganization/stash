// Package vision abstracts image description providers behind a single
// interface so the service never depends on a concrete vendor.
package vision

import (
	"context"
	"io"
)

// Provider generates textual descriptions of images (or video frames).
// Implementations must be safe for concurrent use.
type Provider interface {
	Describe(ctx context.Context, r io.Reader) (string, error)
}
