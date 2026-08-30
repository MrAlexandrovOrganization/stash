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
	// PendingTranscriptSubmissions returns videos that have never been
	// submitted for transcription (no transcript, no job).
	PendingTranscriptSubmissions(ctx context.Context, limit int) ([]*model.Item, error)
	SetAIDescription(ctx context.Context, id, description string) error
	SetAIDescriptionError(ctx context.Context, id, errMsg string) error
	ClearAIDescriptionError(ctx context.Context, id string) error
	GetByFileUniqueID(ctx context.Context, fileUniqueID string) (*model.Item, error)
	PendingAIDescriptions(ctx context.Context, limit int) ([]*model.Item, error)

	// Embedding storage.
	SetTextEmbedding(ctx context.Context, id string, vec []float32) error
	SetImageEmbedding(ctx context.Context, id string, vec []float32) error
	// GetEmbedding returns the stored embedding vector of the given kind.
	GetEmbedding(ctx context.Context, id string, kind model.EmbeddingKind) ([]float32, error)
	// PendingEmbeddings returns describable items missing at least one vector.
	PendingEmbeddings(ctx context.Context, limit int) ([]*model.Item, error)
	// Similar ranks items by the weighted cosine similarity to the query vectors.
	Similar(ctx context.Context, q model.SimilarQuery) ([]*model.Item, error)
}
