package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
	"stash/internal/model"
)

type postgres struct {
	db *pgxpool.Pool
}

func NewPostgres(db *pgxpool.Pool) Repository {
	return &postgres{db: db}
}

const itemColumns = `id, type, file_name, content_type, size, storage_path, description, tags,
	source, original_caption, transcript, ai_description, ai_description_error,
	transcript_job_id, telegram_file_id, file_unique_id, created_at, updated_at`

func (r *postgres) Save(ctx context.Context, item *model.Item) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO items (id, type, file_name, content_type, size, storage_path, description, tags,
			source, original_caption, transcript, ai_description, transcript_job_id, telegram_file_id,
			file_unique_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT (id) DO UPDATE SET
			type = EXCLUDED.type,
			file_name = EXCLUDED.file_name,
			content_type = EXCLUDED.content_type,
			size = EXCLUDED.size,
			storage_path = EXCLUDED.storage_path,
			description = EXCLUDED.description,
			tags = EXCLUDED.tags,
			source = EXCLUDED.source,
			original_caption = EXCLUDED.original_caption,
			transcript = EXCLUDED.transcript,
			ai_description = EXCLUDED.ai_description,
			transcript_job_id = EXCLUDED.transcript_job_id,
			telegram_file_id = EXCLUDED.telegram_file_id,
			file_unique_id = EXCLUDED.file_unique_id,
			updated_at = EXCLUDED.updated_at`,
		item.ID, string(item.Type), item.FileName, item.ContentType, item.Size,
		item.StoragePath, item.Description, item.Tags,
		item.Source, item.OriginalCaption,
		item.Transcript, item.AIDescription, item.TranscriptJobID, item.TelegramFileID,
		item.FileUniqueID,
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
	if meta.FileUniqueID != nil {
		sets = append(sets, fmt.Sprintf("file_unique_id = $%d", i))
		args = append(args, *meta.FileUniqueID)
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

// PendingTranscriptSubmissions returns videos that were uploaded while Whisper
// was unavailable (or before it existed) and thus never got a transcription
// job. Newest first. An empty transcript (as opposed to NULL) marks a job that
// already failed permanently and is not retried.
func (r *postgres) PendingTranscriptSubmissions(ctx context.Context, limit int) ([]*model.Item, error) {
	if limit <= 0 {
		limit = 1
	}
	rows, err := r.db.Query(ctx, `
		SELECT `+itemColumns+`
		FROM items
		WHERE type = 'video'
		  AND transcript IS NULL
		  AND transcript_job_id IS NULL
		ORDER BY created_at DESC
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

func (r *postgres) SetAIDescription(ctx context.Context, id, description string) error {
	_, err := r.db.Exec(ctx,
		"UPDATE items SET ai_description = $1, ai_description_error = NULL, updated_at = $2 WHERE id = $3",
		description, time.Now(), id,
	)
	return err
}

// PendingAIDescriptions returns up to limit describable items (images/gifs/
// videos) that still lack an AI description and have not previously failed.
// Newest first, so a permanently-failing old item cannot block the whole queue.
func (r *postgres) PendingAIDescriptions(ctx context.Context, limit int) ([]*model.Item, error) {
	if limit <= 0 {
		limit = 1
	}
	rows, err := r.db.Query(ctx, `
		SELECT `+itemColumns+`
		FROM items
		WHERE ai_description IS NULL
		  AND ai_description_error IS NULL
		  AND type IN ('image', 'gif', 'video')
		ORDER BY created_at DESC
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

// SetAIDescriptionError records a permanent failure so the backfill worker
// skips the item on subsequent passes.
func (r *postgres) SetAIDescriptionError(ctx context.Context, id, errMsg string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE items SET ai_description_error = $1, updated_at = $2 WHERE id = $3`,
		errMsg, time.Now(), id)
	return err
}

// ClearAIDescriptionError removes a previously recorded failure (e.g. when a
// re-uploaded file replaces a corrupt one and description generation retries).
func (r *postgres) ClearAIDescriptionError(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE items SET ai_description_error = NULL, updated_at = $1 WHERE id = $2`,
		time.Now(), id)
	return err
}

// GetByFileUniqueID returns the item uploaded from the given Telegram file, if
// any. Used to overwrite duplicates (and repair failed uploads) on re-send.
func (r *postgres) GetByFileUniqueID(ctx context.Context, fileUniqueID string) (*model.Item, error) {
	row := r.db.QueryRow(ctx, `SELECT `+itemColumns+` FROM items WHERE file_unique_id = $1`, fileUniqueID)
	return scanItem(row)
}

type scanner interface {
	Scan(dest ...any) error
}

// --- embedding storage ---

func (r *postgres) SetTextEmbedding(ctx context.Context, id string, vec []float32) error {
	_, err := r.db.Exec(ctx,
		"UPDATE items SET embedding_text = $1, updated_at = $2 WHERE id = $3",
		pgvector.NewVector(vec), time.Now(), id)
	return err
}

func (r *postgres) SetImageEmbedding(ctx context.Context, id string, vec []float32) error {
	_, err := r.db.Exec(ctx,
		"UPDATE items SET embedding_image = $1, updated_at = $2 WHERE id = $3",
		pgvector.NewVector(vec), time.Now(), id)
	return err
}

func (r *postgres) GetEmbedding(ctx context.Context, id string, kind model.EmbeddingKind) ([]float32, error) {
	col := "embedding_text"
	if kind == model.EmbeddingKindImage {
		col = "embedding_image"
	}
	var v pgvector.Vector
	if err := r.db.QueryRow(ctx, fmt.Sprintf("SELECT %s FROM items WHERE id = $1", col), id).Scan(&v); err != nil {
		return nil, err
	}
	return v.Slice(), nil
}

// PendingEmbeddings returns describable items (image/gif/video) that are still
// missing at least one embedding vector, newest first.
func (r *postgres) PendingEmbeddings(ctx context.Context, limit int) ([]*model.Item, error) {
	if limit <= 0 {
		limit = 1
	}
	rows, err := r.db.Query(ctx, `
		SELECT `+itemColumns+`
		FROM items
		WHERE type IN ('image', 'gif', 'video')
		  AND (embedding_text IS NULL OR embedding_image IS NULL)
		ORDER BY created_at DESC
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

// Similar ranks items by the weighted sum of cosine similarities to the query
// vectors, using pgvector's <=> (cosine distance) operator.
func (r *postgres) Similar(ctx context.Context, q model.SimilarQuery) ([]*model.Item, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}

	var terms []string
	var args []any
	i := 1
	if len(q.TextVector) > 0 {
		terms = append(terms, fmt.Sprintf("($%d * (1 - (embedding_text <=> $%d)))", i, i+1))
		args = append(args, q.TextWeight, pgvector.NewVector(q.TextVector))
		i += 2
	}
	if len(q.ImageVector) > 0 {
		terms = append(terms, fmt.Sprintf("($%d * (1 - (embedding_image <=> $%d)))", i, i+1))
		args = append(args, q.ImageWeight, pgvector.NewVector(q.ImageVector))
		i += 2
	}
	if len(terms) == 0 {
		return nil, fmt.Errorf("similar: no embedding vector provided")
	}

	args = append(args, limit, q.Offset)
	query := fmt.Sprintf(`
		SELECT %s
		FROM items
		ORDER BY %s DESC
		LIMIT $%d OFFSET $%d`, itemColumns, strings.Join(terms, " + "), i, i+1)

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

func scanItem(row scanner) (*model.Item, error) {
	var item model.Item
	var mediaType string
	err := row.Scan(
		&item.ID, &mediaType, &item.FileName, &item.ContentType, &item.Size,
		&item.StoragePath, &item.Description, &item.Tags,
		&item.Source, &item.OriginalCaption,
		&item.Transcript, &item.AIDescription, &item.AIDescriptionError,
		&item.TranscriptJobID, &item.TelegramFileID,
		&item.FileUniqueID,
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
