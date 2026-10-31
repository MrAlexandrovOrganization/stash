package filestore

import (
	"context"
	"io"
)

type FileStore interface {
	// Put writes to a newly reserved immutable object key, never an item filename.
	Put(ctx context.Context, path string, r io.Reader, size int64, contentType string) error
	Get(ctx context.Context, path string) (io.ReadCloser, error)
	Delete(ctx context.Context, path string) error
}
