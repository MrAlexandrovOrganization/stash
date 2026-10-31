package repository

import (
	"context"
	"embed"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"stash/internal/model"
)

//go:embed sql/upload_*.sql
var uploadQueries embed.FS

func uploadSQL(name string) string {
	data, err := uploadQueries.ReadFile("sql/upload_" + name + ".sql")
	if err != nil {
		panic(err)
	}
	return string(data)
}

var ErrUploadExpired = errors.New("upload reservation expired or collected")

func (r *postgres) ReserveUpload(ctx context.Context, path string) error {
	_, err := r.db.Exec(ctx, uploadSQL("reserve"), path)
	return err
}

func (r *postgres) SaveUpload(ctx context.Context, item *model.Item) (*model.Item, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var path string
	if err := tx.QueryRow(ctx, uploadSQL("lock"), item.StoragePath).Scan(&path); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUploadExpired
		}
		return nil, err
	}
	// Serialize even the first concurrent uploads of one Telegram identity.
	// No database lock is held while uploading bytes to MinIO.
	updated := *item
	var oldPath string
	if item.FileUniqueID != nil {
		if _, err := tx.Exec(ctx, uploadSQL("identity_lock"), *item.FileUniqueID); err != nil {
			return nil, err
		}
		err := tx.QueryRow(ctx, uploadSQL("owner"), *item.FileUniqueID).
			Scan(&updated.ID, &oldPath, &updated.CreatedAt)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	saved, err := scanItem(tx.QueryRow(ctx, uploadSQL("save"),
		updated.ID, string(updated.Type), updated.FileName, updated.ContentType,
		updated.Size, updated.StoragePath, updated.Description, updated.Tags,
		updated.Source, updated.OriginalCaption, updated.FileUniqueID,
		updated.CreatedAt, updated.UpdatedAt))
	if err != nil {
		return nil, err
	}
	if oldPath != "" && oldPath != item.StoragePath {
		if _, err := tx.Exec(ctx, uploadSQL("retire"), oldPath); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, uploadSQL("forget"), item.StoragePath); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		// The server may have committed despite a lost response. Leave all
		// object removal to the journal; never compensate by deleting here.
		return nil, err
	}
	return saved, nil
}

func (r *postgres) Delete(ctx context.Context, id string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var path string
	if err := tx.QueryRow(ctx, uploadSQL("delete_item"), id).Scan(&path); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.Exec(ctx, uploadSQL("retire"), path); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *postgres) CollectFile(ctx context.Context, remove func(context.Context, string) error) (bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var path string
	if err := tx.QueryRow(ctx, uploadSQL("gc_claim")).Scan(&path); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	// Hold the journal row lock through deletion. An expired upload cannot
	// publish this key concurrently; SaveUpload requires this same row lock.
	if err := remove(ctx, path); err != nil {
		if _, retryErr := tx.Exec(ctx, uploadSQL("gc_retry"), path); retryErr != nil {
			return false, retryErr
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return false, commitErr
		}
		return false, fmt.Errorf("remove retired file: %w", err)
	}
	if _, err := tx.Exec(ctx, uploadSQL("forget"), path); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
