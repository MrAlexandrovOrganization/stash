package repository

import (
	"context"
	"stash/internal/model"
)

type Repository interface {
	RequestDescription(ctx context.Context, id string) error
	SetAIDescriptionAt(ctx context.Context, id, description string, generation int64) error
	SetAIDescriptionErrorAt(ctx context.Context, id, message string, generation int64) error
	SetTextEmbeddingAt(ctx context.Context, id string, vec []float32, generation int64) error
	SetImageEmbeddingAt(ctx context.Context, id string, vec []float32, generation int64) error
	// ReserveUpload journals a fresh key before storage I/O. SaveUpload atomically
	// publishes it, chooses the deduplication owner and retires the previous key.
	ReserveUpload(ctx context.Context, path string) error
	SaveUpload(ctx context.Context, item *model.Item) (*model.Item, error)
	// CollectFile locks one due journal entry through the idempotent removal.
	CollectFile(ctx context.Context, remove func(context.Context, string) error) (bool, error)
	Get(ctx context.Context, id string) (*model.Item, error)
	Delete(ctx context.Context, id string) error
	Search(ctx context.Context, q model.SearchQuery) ([]*model.Item, error)
	Update(ctx context.Context, id string, meta model.UpdateMeta) error
	EnsureTranscription(ctx context.Context, id, path, format string) error
	TranscriptionTasks(ctx context.Context, state string, submit bool, limit int) ([]*model.TranscriptionTask, error)
	SetTranscriptionJob(ctx context.Context, requestID, jobID string) error
	CompleteTranscription(ctx context.Context, requestID, jobID, terminalStatus, text string) error
	MissingTranscription(ctx context.Context, requestID, jobID string) error
	AcknowledgeTranscription(ctx context.Context, requestID, jobID string) error
	RetryTranscription(ctx context.Context, requestID string) error
	// PendingTranscriptSubmissions includes legacy jobs not yet in the journal.
	PendingTranscriptSubmissions(ctx context.Context, limit int) ([]*model.Item, error)
	SetAIDescription(ctx context.Context, id, description string) error
	SetAIDescriptionError(ctx context.Context, id, errMsg string) error
	ClearAIDescriptionError(ctx context.Context, id string) error
	GetByFileUniqueID(ctx context.Context, fileUniqueID string) (*model.Item, error)
	PendingAIDescriptions(ctx context.Context, limit int) ([]*model.Item, error)

	// Embedding storage.
	SetTextEmbedding(ctx context.Context, id string, vec []float32) error
	SetImageEmbedding(ctx context.Context, id string, vec []float32) error
	// HasEmbedding checks presence without decoding a nullable vector.
	HasEmbedding(ctx context.Context, id string, kind model.EmbeddingKind) (bool, error)
	// GetEmbedding returns the stored embedding vector of the given kind.
	GetEmbedding(ctx context.Context, id string, kind model.EmbeddingKind) ([]float32, error)
	// PendingEmbeddings returns describable items missing at least one vector.
	PendingEmbeddings(ctx context.Context, limit int) ([]*model.Item, error)
	// Similar ranks items by the weighted cosine similarity to the query vectors.
	Similar(ctx context.Context, q model.SimilarQuery) ([]*model.Item, error)
}
