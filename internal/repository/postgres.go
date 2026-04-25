package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"stash/internal/model"
)

type postgres struct {
	db *pgxpool.Pool
}

func NewPostgres(db *pgxpool.Pool) Repository {
	return &postgres{db: db}
}

func (r *postgres) Save(ctx context.Context, item *model.Item) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO items (id, type, file_name, content_type, size, storage_path, description, tags, transcript, transcript_job_id, telegram_file_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		item.ID, string(item.Type), item.FileName, item.ContentType, item.Size,
		item.StoragePath, item.Description, item.Tags,
		item.Transcript, item.TranscriptJobID, item.TelegramFileID,
		item.CreatedAt, item.UpdatedAt,
	)
	return err
}

func (r *postgres) Get(ctx context.Context, id string) (*model.Item, error) {
	row := r.db.QueryRow(ctx, `
		SELECT id, type, file_name, content_type, size, storage_path, description, tags, transcript, transcript_job_id, telegram_file_id, created_at, updated_at
		FROM items WHERE id = $1`, id)
	return scanItem(row)
}

func (r *postgres) Delete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM items WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *postgres) Search(ctx context.Context, q model.SearchQuery) ([]*model.Item, error) {
	var conds []string
	var args []any
	i := 1

	if q.Text != "" {
		conds = append(conds, fmt.Sprintf(
			"(description ILIKE $%d OR transcript ILIKE $%d)", i, i,
		))
		args = append(args, "%"+q.Text+"%")
		i++
	}
	if len(q.Tags) > 0 {
		conds = append(conds, fmt.Sprintf("tags @> $%d", i))
		args = append(args, q.Tags)
		i++
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	rows, err := r.db.Query(ctx, fmt.Sprintf(`
		SELECT id, type, file_name, content_type, size, storage_path, description, tags, transcript, transcript_job_id, telegram_file_id, created_at, updated_at
		FROM items %s ORDER BY created_at DESC`, where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []*model.Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *postgres) Update(ctx context.Context, id string, meta model.UpdateMeta) error {
	var sets []string
	var args []any
	i := 1

	if meta.Description != nil {
		sets = append(sets, fmt.Sprintf("description = $%d", i))
		args = append(args, *meta.Description)
		i++
	}
	if meta.Tags != nil {
		sets = append(sets, fmt.Sprintf("tags = $%d", i))
		args = append(args, meta.Tags)
		i++
	}
	if meta.TelegramFileID != nil {
		sets = append(sets, fmt.Sprintf("telegram_file_id = $%d", i))
		args = append(args, *meta.TelegramFileID)
		i++
	}
	if len(sets) == 0 {
		return nil
	}

	sets = append(sets, fmt.Sprintf("updated_at = $%d", i))
	args = append(args, time.Now())
	i++

	args = append(args, id)
	tag, err := r.db.Exec(ctx, fmt.Sprintf(
		"UPDATE items SET %s WHERE id = $%d",
		strings.Join(sets, ", "), i,
	), args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *postgres) SetTranscriptJob(ctx context.Context, id, jobID string) error {
	_, err := r.db.Exec(ctx,
		"UPDATE items SET transcript_job_id = $1, updated_at = $2 WHERE id = $3",
		jobID, time.Now(), id,
	)
	return err
}

func (r *postgres) UpdateTranscript(ctx context.Context, id, transcript string) error {
	_, err := r.db.Exec(ctx,
		"UPDATE items SET transcript = $1, transcript_job_id = NULL, updated_at = $2 WHERE id = $3",
		transcript, time.Now(), id,
	)
	return err
}

func (r *postgres) PendingTranscripts(ctx context.Context) ([]*model.Item, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, type, file_name, content_type, size, storage_path, description, tags, transcript, transcript_job_id, telegram_file_id, created_at, updated_at
		FROM items WHERE transcript_job_id IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []*model.Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanItem(row scanner) (*model.Item, error) {
	var item model.Item
	var mediaType string
	err := row.Scan(
		&item.ID, &mediaType, &item.FileName, &item.ContentType, &item.Size,
		&item.StoragePath, &item.Description, &item.Tags,
		&item.Transcript, &item.TranscriptJobID, &item.TelegramFileID,
		&item.CreatedAt, &item.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	item.Type = model.MediaType(mediaType)
	return &item, nil
}
