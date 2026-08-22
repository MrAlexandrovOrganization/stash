package repository

import (
	"context"
	"stash/internal/model"
)

type Repository interface {
	Save(ctx context.Context, item *model.Item) error
	Get(ctx context.Context, id string) (*model.Item, error)
	Delete(ctx context.Context, id string) error
	Search(ctx context.Context, q model.SearchQuery) ([]*model.Item, error)
	Update(ctx context.Context, id string, meta model.UpdateMeta) error
	SetTranscriptJob(ctx context.Context, id, jobID string) error
	UpdateTranscript(ctx context.Context, id, transcript string) error
	PendingTranscripts(ctx context.Context) ([]*model.Item, error)
	SetAIDescription(ctx context.Context, id, description string) error
	PendingAIDescriptions(ctx context.Context, limit int) ([]*model.Item, error)
}
