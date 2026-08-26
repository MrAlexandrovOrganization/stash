// Package vision abstracts media description providers behind a single
// interface so the service never depends on a concrete vendor.
package vision

import (
	"context"
)

// Provider generates textual descriptions of media frames (a photo is a
// single frame; a video contributes several chronological frames).
// Implementations must be safe for concurrent use.
type Provider interface {
	Describe(ctx context.Context, frames [][]byte) (string, error)
}
