package repository

import (
	"context"
	"embed"
	"errors"
	"github.com/pgvector/pgvector-go"
)

//go:embed sql/*.sql
var processingQueries embed.FS

func processingSQL(name string) string {
	data, err := processingQueries.ReadFile("sql/" + name + ".sql")
	if err != nil {
		panic(err)
	}
	return string(data)
}

var ErrStaleProcessing = errors.New("object changed during processing")

func (r *postgres) writeProcessing(ctx context.Context, name string, args ...any) error {
	tag, err := r.db.Exec(ctx, processingSQL(name), args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrStaleProcessing
	}
	return nil
}
func (r *postgres) RequestDescription(ctx context.Context, id string) error {
	return r.writeProcessing(ctx, "request_description", id)
}
func (r *postgres) SetAIDescriptionAt(ctx context.Context, id, value string, generation int64) error {
	return r.writeProcessing(ctx, "set_ai_at", id, value, generation)
}
func (r *postgres) SetAIDescriptionErrorAt(ctx context.Context, id, value string, generation int64) error {
	return r.writeProcessing(ctx, "set_ai_error_at", id, value, generation)
}
func (r *postgres) SetTextEmbeddingAt(ctx context.Context, id string, vec []float32, generation int64) error {
	return r.writeProcessing(ctx, "set_text_at", id, pgvector.NewVector(vec), generation)
}
func (r *postgres) SetImageEmbeddingAt(ctx context.Context, id string, vec []float32, generation int64) error {
	return r.writeProcessing(ctx, "set_image_at", id, pgvector.NewVector(vec), generation)
}
