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

const itemColumns = `id, type, file_name, content_type, size, storage_path, description, tags,
	source, original_caption, transcript, ai_description, transcript_job_id, telegram_file_id,
	created_at, updated_at`

func (r *postgres) Save(ctx context.Context, item *model.Item) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO items (id, type, file_name, content_type, size, storage_path, description, tags,
			source, original_caption, transcript, ai_description, transcript_job_id, telegram_file_id,
			created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		item.ID, string(item.Type), item.FileName, item.ContentType, item.Size,
		item.StoragePath, item.Description, item.Tags,
		item.Source, item.OriginalCaption,
		item.Transcript, item.AIDescription, item.TranscriptJobID, item.TelegramFileID,
		item.CreatedAt, item.UpdatedAt,
	)
	return err
}

func (r *postgres) Get(ctx context.Context, id string) (*model.Item, error) {
	row := r.db.QueryRow(ctx, `SELECT `+itemColumns+` FROM items WHERE id = $1`, id)
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

// defaultSearchLimit bounds an unbounded Search so the backend never scans and
// serializes the entire table. Callers may override via q.Limit.
const defaultSearchLimit = 1000

func (r *postgres) Search(ctx context.Context, q model.SearchQuery) ([]*model.Item, error) {
	var conds []string
	var args []any
	i := 1

	limit := q.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}

	if q.Text != "" {
		// Full-text-ish search across every textual field: description,
		// transcript, original caption, AI description and tags.
		conds = append(conds, fmt.Sprintf(
			"(description ILIKE $%d OR transcript ILIKE $%d OR original_caption ILIKE $%d OR ai_description ILIKE $%d OR EXISTS (SELECT 1 FROM unnest(tags) AS t WHERE t ILIKE $%d))",
			i, i, i, i, i,
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

	query := fmt.Sprintf(`
		SELECT %s
		FROM items %s ORDER BY created_at DESC`, itemColumns, where)

	if q.Limit > 0 {
		query += fmt.Sprintf(" LIMIT $%d OFFSET $%d", i, i+1)
		args = append(args, q.Limit, q.Offset)
	} else {
		query += fmt.Sprintf(" LIMIT $%d", i)
		args = append(args, limit)
	}

	rows, err := r.db.Query(ctx, query, args...)
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
	if meta.Transcript != nil {
		sets = append(sets, fmt.Sprintf("transcript = $%d", i))
		args = append(args, *meta.Transcript)
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
		SELECT `+itemColumns+`
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

func (r *postgres) SetAIDescription(ctx context.Context, id, description string) error {
	_, err := r.db.Exec(ctx,
		"UPDATE items SET ai_description = $1, updated_at = $2 WHERE id = $3",
		description, time.Now(), id,
	)
	return err
}

// PendingAIDescriptions returns up to limit items that are describable
// (images/gifs) but still lack an AI description. Used by the background
// backfill worker to catch items missed at upload time.
func (r *postgres) PendingAIDescriptions(ctx context.Context, limit int) ([]*model.Item, error) {
	if limit <= 0 {
		limit = 1
	}
	rows, err := r.db.Query(ctx, `
		SELECT `+itemColumns+`
		FROM items
		WHERE ai_description IS NULL AND type IN ('image', 'gif')
		ORDER BY created_at ASC
		LIMIT $1`, limit)
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
		&item.Source, &item.OriginalCaption,
		&item.Transcript, &item.AIDescription, &item.TranscriptJobID, &item.TelegramFileID,
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
