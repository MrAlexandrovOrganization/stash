package repository

import (
	"context"
	_ "embed"
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
	transcript_job_id, telegram_file_id, file_unique_id, created_at, updated_at, processing_generation`

func (r *postgres) Get(ctx context.Context, id string) (*model.Item, error) {
	row := r.db.QueryRow(ctx, `SELECT `+itemColumns+` FROM items WHERE id = $1`, id)
	return scanItem(row)
}

// defaultSearchLimit bounds an unbounded Search so the backend never scans and
// serializes the entire table. Callers may override via q.Limit.
const defaultSearchLimit = 1000

//go:embed sql/search_base.sql
var searchBaseSQL string

//go:embed sql/similar.sql
var similarSQL string

func (r *postgres) CountSearch(ctx context.Context, q model.SearchQuery) (int, error) {
	var count int
	err := r.db.QueryRow(ctx, searchBaseSQL+" SELECT count(*) FROM matched", q.Text, q.Tags, q.ExcludeTags).Scan(&count)
	return count, err
}

func (r *postgres) Search(ctx context.Context, q model.SearchQuery) ([]*model.Item, error) {
	limit := q.Limit
	if limit <= 0 || limit > defaultSearchLimit {
		limit = defaultSearchLimit
	}
	query := searchBaseSQL + fmt.Sprintf(" SELECT %s FROM matched ORDER BY relevance DESC, created_at DESC, id DESC LIMIT $4 OFFSET $5", itemColumns)
	rows, err := r.db.Query(ctx, query, q.Text, q.Tags, q.ExcludeTags, limit, max(0, q.Offset))
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
		sets = append(sets, "transcript_job_id = NULL")
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
// any. Historical duplicates retain their original IDs/media but do not own
// re-upload deduplication. The partial unique index enforces a single owner.
func (r *postgres) GetByFileUniqueID(ctx context.Context, fileUniqueID string) (*model.Item, error) {
	row := r.db.QueryRow(ctx, processingSQL("get_by_file_unique_id"), fileUniqueID)
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
	var v *pgvector.Vector
	if err := r.db.QueryRow(ctx, fmt.Sprintf("SELECT %s FROM items WHERE id = $1", col), id).Scan(&v); err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	return v.Slice(), nil
}

// PendingEmbeddings returns describable items (image/gif/video) that are still
// missing at least one embedding vector, newest first.
func (r *postgres) PendingEmbeddings(ctx context.Context, limit int) ([]*model.Item, error) {
	if limit <= 0 {
		limit = 1
	}
	rows, err := r.db.Query(ctx, processingSQL("pending_embeddings"), limit)
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
	if limit <= 0 || limit > defaultSearchLimit {
		limit = defaultSearchLimit
	}
	var textVector, imageVector any
	if len(q.TextVector) > 0 && q.TextWeight > 0 {
		textVector = pgvector.NewVector(q.TextVector)
	}
	if len(q.ImageVector) > 0 && q.ImageWeight > 0 {
		imageVector = pgvector.NewVector(q.ImageVector)
	}
	if textVector == nil && imageVector == nil {
		return nil, fmt.Errorf("similar: no embedding vector provided")
	}
	rows, err := r.db.Query(ctx, fmt.Sprintf(similarSQL, itemColumns), textVector, imageVector, q.TextWeight, q.ImageWeight, limit, max(0, q.Offset), q.Tags, q.ExcludeTags)
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
		&item.CreatedAt, &item.UpdatedAt, &item.ProcessingGeneration,
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
