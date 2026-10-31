package service

import (
	"context"
	"log/slog"
	"time"

	"stash/internal/filestore"
	"stash/internal/repository"
)

// RunFileGC resumes the durable journal on startup and every minute. Work is
// bounded; MinIO deletion is idempotent, including after an uncertain DB commit.
func RunFileGC(ctx context.Context, repo repository.Repository, files filestore.FileStore) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		for range 100 {
			attempt, cancel := context.WithTimeout(ctx, 15*time.Second)
			collected, err := repo.CollectFile(attempt, files.Delete)
			cancel()
			if err != nil {
				if ctx.Err() == nil {
					slog.ErrorContext(ctx, "file garbage collection failed", "error", err)
				}
				break
			}
			if !collected {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
