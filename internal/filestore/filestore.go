package filestore

import (
	"context"
	"io"
)

type FileStore interface {
	Put(ctx context.Context, itemID, fileName string, r io.Reader, size int64, contentType string) (path string, err error)
	Get(ctx context.Context, path string) (io.ReadCloser, error)
	Delete(ctx context.Context, path string) error
}
