package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"stash/internal/embed"
	"stash/internal/filestore"
	"stash/internal/model"
	"stash/internal/repository"
	"stash/internal/vision"
	"stash/internal/whisper"
)

type Service interface {
	Upload(ctx context.Context, r io.Reader, meta model.UploadMeta) (*model.Item, error)
	Get(ctx context.Context, id string) (*model.Item, error)
	GetFile(ctx context.Context, id string) (io.ReadCloser, *model.Item, error)
	Delete(ctx context.Context, id string) error
	Search(ctx context.Context, q model.SearchQuery) ([]*model.Item, error)
	Update(ctx context.Context, id string, meta model.UpdateMeta) (*model.Item, error)
	Describe(ctx context.Context, id string) (*model.Item, error)
	// Similar runs a hybrid embedding-based search (text and/or image).
	Similar(ctx context.Context, req model.SimilarRequest) ([]*model.Item, error)
}

// ErrNotDescribable is returned when AI description is requested for a media
// type the vision provider cannot handle (e.g. documents).
var ErrNotDescribable = errors.New("media type is not describable")

// ErrVisionDisabled is returned when no vision provider is configured.
var ErrVisionDisabled = errors.New("vision provider is not configured")

type svc struct {
	repo          repository.Repository
	files         filestore.FileStore
	whisper       *whisper.Client
	vision        vision.Provider
	textEmbedder  embed.Embedder
	imageEmbedder embed.Embedder
	pollDelay     time.Duration

	aiBackfillInterval time.Duration
	aiBackfillBatch    int

	embedBackfillInterval time.Duration
	embedBackfillBatch    int
}

func New(repo repository.Repository, files filestore.FileStore, wc *whisper.Client, vp vision.Provider, opts ...Option) Service {
	s := &svc{
		repo:      repo,
		files:     files,
		whisper:   wc,
		vision:    vp,
		pollDelay: 5 * time.Second,
	}
	for _, o := range opts {
		o(s)
	}
	if wc != nil {
		go s.transcriptPoller()
	}
	if vp != nil {
		if s.aiBackfillInterval <= 0 {
			s.aiBackfillInterval = 5 * time.Minute
		}
		if s.aiBackfillBatch <= 0 {
			s.aiBackfillBatch = 5
		}
		go s.aiBackfillPoller(s.aiBackfillInterval)
		slog.Info("ai description backfill enabled", "interval", s.aiBackfillInterval, "batch", s.aiBackfillBatch)
	}

	if s.textEmbedder != nil || s.imageEmbedder != nil {
		if s.embedBackfillInterval <= 0 {
			s.embedBackfillInterval = 5 * time.Minute
		}
		if s.embedBackfillBatch <= 0 {
			s.embedBackfillBatch = 5
		}
		go s.embeddingBackfillPoller(s.embedBackfillInterval)
		slog.Info("embedding backfill enabled", "interval", s.embedBackfillInterval, "batch", s.embedBackfillBatch)
	}
	return s
}

// Option configures optional service behaviour.
type Option func(*svc)

// WithAIBackfill enables the periodic worker that generates missing AI
// descriptions. interval is a parsed Go duration; batch is items per scan.
func WithAIBackfill(interval time.Duration, batch int) Option {
	return func(s *svc) {
		s.aiBackfillInterval = interval
		s.aiBackfillBatch = batch
	}
}

// WithTextEmbedder injects the provider used to embed item descriptions.
func WithTextEmbedder(e embed.Embedder) Option {
	return func(s *svc) {
		s.textEmbedder = e
	}
}

// WithImageEmbedder injects the provider used to embed image bytes (the
// external clip-embedder microservice).
func WithImageEmbedder(e embed.Embedder) Option {
	return func(s *svc) {
		s.imageEmbedder = e
	}
}

// WithEmbeddingBackfill enables the periodic worker that fills missing
// embedding vectors. interval is a parsed Go duration; batch is items per scan.
func WithEmbeddingBackfill(interval time.Duration, batch int) Option {
	return func(s *svc) {
		s.embedBackfillInterval = interval
		s.embedBackfillBatch = batch
	}
}

func (s *svc) Upload(ctx context.Context, r io.Reader, meta model.UploadMeta) (*model.Item, error) {
	now := time.Now()

	// Deduplicate by Telegram's stable file_unique_id: re-sending the same file
	// (or re-sending one that previously failed) overwrites the existing record
	// in place instead of creating a duplicate.
	if meta.FileUniqueID != "" {
		if existing, err := s.repo.GetByFileUniqueID(ctx, meta.FileUniqueID); err == nil && existing != nil {
			path, perr := s.files.Put(ctx, existing.ID, meta.FileName, r, meta.Size, meta.ContentType)
			if perr != nil {
				return nil, fmt.Errorf("store file: %w", perr)
			}
			// Only remove the old object if the storage path actually changed.
			if path != existing.StoragePath {
				_ = s.files.Delete(ctx, existing.StoragePath)
			}

			fu := meta.FileUniqueID
			updated := &model.Item{
				ID:              existing.ID,
				Type:            meta.Type,
				FileName:        meta.FileName,
				ContentType:     meta.ContentType,
				Size:            meta.Size,
				StoragePath:     path,
				Description:     meta.Description,
				Tags:            meta.Tags,
				Source:          meta.Source,
				OriginalCaption: meta.OriginalCaption,
				FileUniqueID:    &fu,
				CreatedAt:       existing.CreatedAt,
				UpdatedAt:       now,
			}
			if updated.Tags == nil {
				updated.Tags = []string{}
			}
			if serr := s.repo.Save(ctx, updated); serr != nil {
				_ = s.files.Delete(ctx, path)
				return nil, fmt.Errorf("save item: %w", serr)
			}
			_ = s.repo.ClearAIDescriptionError(ctx, existing.ID)
			s.processUploadedMedia(existing.ID, path, meta)
			return updated, nil
		}
	}

	id := uuid.New().String()
	path, err := s.files.Put(ctx, id, meta.FileName, r, meta.Size, meta.ContentType)
	if err != nil {
		return nil, fmt.Errorf("store file: %w", err)
	}

	item := &model.Item{
		ID:              id,
		Type:            meta.Type,
		FileName:        meta.FileName,
		ContentType:     meta.ContentType,
		Size:            meta.Size,
		StoragePath:     path,
		Description:     meta.Description,
		Tags:            meta.Tags,
		Source:          meta.Source,
		OriginalCaption: meta.OriginalCaption,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if item.Tags == nil {
		item.Tags = []string{}
	}
	if meta.FileUniqueID != "" {
		fu := meta.FileUniqueID
		item.FileUniqueID = &fu
	}

	if err := s.repo.Save(ctx, item); err != nil {
		_ = s.files.Delete(ctx, path)
		return nil, fmt.Errorf("save item: %w", err)
	}

	s.processUploadedMedia(item.ID, path, meta)
	return item, nil
}

// processUploadedMedia kicks off async media processing (transcription for
// video, AI description for images/GIFs/videos) after a successful upload.
func (s *svc) processUploadedMedia(id, path string, meta model.UploadMeta) {
	if meta.Type == model.MediaTypeVideo && s.whisper != nil {
		go s.submitTranscription(id, path, meta.ContentType)
	}
	if describableType(meta.Type) && s.vision != nil {
		go s.runAIDescription(id, path, meta.ContentType)
	}
	if describableType(meta.Type) {
		if s.imageEmbedder != nil {
			go s.embedImage(context.Background(), id, path, meta.ContentType)
		}
		if s.textEmbedder != nil {
			go s.embedText(context.Background(), id)
		}
	}
}

// describableType reports whether the vision pipeline handles this media type.
// GIFs are stored as mp4 and go through the same frame-extraction path as
// videos; documents are skipped.
func describableType(t model.MediaType) bool {
	switch t {
	case model.MediaTypeImage, model.MediaTypeGIF, model.MediaTypeVideo:
		return true
	}
	return false
}

func (s *svc) Get(ctx context.Context, id string) (*model.Item, error) {
	return s.repo.Get(ctx, id)
}

func (s *svc) GetFile(ctx context.Context, id string) (io.ReadCloser, *model.Item, error) {
	item, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	rc, err := s.files.Get(ctx, item.StoragePath)
	if err != nil {
		return nil, nil, fmt.Errorf("get file: %w", err)
	}
	return rc, item, nil
}

func (s *svc) Delete(ctx context.Context, id string) error {
	item, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	return s.files.Delete(ctx, item.StoragePath)
}

func (s *svc) Search(ctx context.Context, q model.SearchQuery) ([]*model.Item, error) {
	return s.repo.Search(ctx, q)
}

func (s *svc) Update(ctx context.Context, id string, meta model.UpdateMeta) (*model.Item, error) {
	if err := s.repo.Update(ctx, id, meta); err != nil {
		return nil, err
	}
	return s.repo.Get(ctx, id)
}

// submitTranscription downloads the file from storage and submits it to Whisper.
// Runs in a goroutine; errors are logged.
func (s *svc) submitTranscription(itemID, path, contentType string) {
	ctx := context.Background()

	rc, err := s.files.Get(ctx, path)
	if err != nil {
		slog.Error("transcription: get file", "item", itemID, "error", err)
		return
	}
	defer rc.Close()

	format := formatFromContentType(contentType, path)
	jobID, err := s.whisper.Submit(ctx, rc, format)
	if err != nil {
		slog.Error("transcription: submit", "item", itemID, "error", err)
		return
	}

	if err := s.repo.SetTranscriptJob(ctx, itemID, jobID); err != nil {
		slog.Error("transcription: set job", "item", itemID, "error", err)
	}
	slog.Info("transcription submitted", "item", itemID, "job", jobID)
}

// transcriptPoller runs in the background, polls pending transcription jobs
// and periodically submits videos that were never transcribed (e.g. uploaded
// while Whisper was down).
func (s *svc) transcriptPoller() {
	tick := time.Tick(s.pollDelay)
	submitTick := time.Tick(time.Minute)
	for {
		select {
		case <-tick:
			s.pollPendingTranscripts()
		case <-submitTick:
			s.submitMissingTranscripts(transcriptBackfillBatch)
		}
	}
}

// transcriptBackfillBatch bounds how many unsubmitted videos are sent to
// Whisper per backfill pass.
const transcriptBackfillBatch = 2

func (s *svc) submitMissingTranscripts(limit int) {
	ctx := context.Background()
	items, err := s.repo.PendingTranscriptSubmissions(ctx, limit)
	if err != nil {
		slog.Error("transcript backfill: list", "error", err)
		return
	}
	if len(items) == 0 {
		return
	}
	slog.Info("transcript backfill: submitting", "count", len(items))
	for _, item := range items {
		s.submitTranscription(item.ID, item.StoragePath, item.ContentType)
	}
}

func (s *svc) pollPendingTranscripts() {
	ctx := context.Background()
	items, err := s.repo.PendingTranscripts(ctx)
	if err != nil {
		slog.Error("transcript poll: list", "error", err)
		return
	}
	for _, item := range items {
		if item.TranscriptJobID == nil {
			continue
		}
		result, err := s.whisper.GetStatus(ctx, *item.TranscriptJobID)
		if err != nil {
			slog.Error("transcript poll: status", "item", item.ID, "error", err)
			continue
		}
		switch result.Status {
		case whisper.StatusDone:
			if err := s.repo.UpdateTranscript(ctx, item.ID, result.Text); err != nil {
				slog.Error("transcript poll: update", "item", item.ID, "error", err)
			} else {
				slog.Info("transcript done", "item", item.ID)
			}
		case whisper.StatusFailed:
			slog.Error("transcript failed", "item", item.ID, "error", result.Error)
			if err := s.repo.UpdateTranscript(ctx, item.ID, ""); err != nil {
				slog.Error("transcript poll: clear job", "item", item.ID, "error", err)
			}
		}
	}
}

// Describe regenerates the AI description for a single item on demand:
// clears any previously recorded failure and runs the vision pipeline
// asynchronously. Returns the item as it was at request time.
func (s *svc) Describe(ctx context.Context, id string) (*model.Item, error) {
	if s.vision == nil {
		return nil, ErrVisionDisabled
	}
	item, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !describableType(item.Type) {
		return nil, ErrNotDescribable
	}
	if err := s.repo.ClearAIDescriptionError(ctx, id); err != nil {
		return nil, fmt.Errorf("clear ai description error: %w", err)
	}
	item.AIDescriptionError = nil

	go s.runAIDescription(item.ID, item.StoragePath, item.ContentType)
	slog.Info("ai description requested", "item", id)
	return item, nil
}

// runAIDescription asks the vision provider to describe the stored file and
// persists the result. It is synchronous; callers decide on concurrency
// (a goroutine on upload, a sequential loop in the backfill worker).
func (s *svc) runAIDescription(itemID, path, contentType string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	rc, err := s.files.Get(ctx, path)
	if err != nil {
		slog.Error("ai description: get file", "item", itemID, "error", err)
		return
	}
	defer rc.Close()

	var frames [][]byte
	if describableContentType(contentType) == "image" {
		data, err := io.ReadAll(rc)
		if err != nil {
			slog.Error("ai description: read file", "item", itemID, "error", err)
			return
		}
		frames = [][]byte{data}
	} else {
		// Videos/GIFs: sample several frames across the clip so the vision
		// model sees what happens in it, not just how it starts.
		var err error
		frames, err = extractFrames(ctx, rc)
		if err != nil {
			slog.Error("ai description: extract frames", "item", itemID, "error", err)
			_ = s.repo.SetAIDescriptionError(ctx, itemID, "extract frames: "+err.Error())
			return
		}
	}
	total := 0
	for _, f := range frames {
		total += len(f)
	}
	slog.Info("ai description: frame bytes", "item", itemID, "frames", len(frames), "bytes", total)
	if total == 0 {
		slog.Warn("ai description: empty image bytes, marking failed", "item", itemID)
		_ = s.repo.SetAIDescriptionError(ctx, itemID, "empty image bytes from storage")
		return
	}

	desc, err := s.vision.Describe(ctx, frames)
	if err != nil {
		slog.Error("ai description: describe", "item", itemID, "error", err)
		// Permanent-looking failure (e.g. Ollama 400 "failed to decode image
		// bytes"): record it so the backfill stops retrying this item forever.
		_ = s.repo.SetAIDescriptionError(ctx, itemID, err.Error())
		return
	}
	if strings.TrimSpace(desc) == "" {
		// Ollama occasionally returns an empty response; do not persist it,
		// otherwise the backfill would treat the item as "done" and skip it.
		slog.Warn("ai description: empty response, skipping", "item", itemID)
		return
	}

	if err := s.repo.SetAIDescription(ctx, itemID, desc); err != nil {
		slog.Error("ai description: save", "item", itemID, "error", err)
	} else {
		slog.Info("ai description done", "item", itemID)
	}

	// Now that we have a description, (re)compute embeddings: the text vector
	// gains the AI description, and the image vector is refreshed if it was not
	// embedded at upload time.
	if s.textEmbedder != nil || s.imageEmbedder != nil {
		go s.embedItem(context.Background(), itemID, path, contentType)
	}
}

// aiBackfillPoller periodically generates missing AI descriptions so that
// items uploaded while Ollama was down (or before it existed) are eventually
// described. Items are processed sequentially to avoid overloading a
// single-parallel Ollama instance.
func (s *svc) aiBackfillPoller(interval time.Duration) {
	for range time.Tick(interval) {
		s.backfillPendingAIDescriptions()
	}
}

func (s *svc) backfillPendingAIDescriptions() {
	ctx := context.Background()
	items, err := s.repo.PendingAIDescriptions(ctx, s.aiBackfillBatch)
	if err != nil {
		slog.Error("ai backfill: list", "error", err)
		return
	}
	if len(items) == 0 {
		return
	}
	slog.Info("ai backfill: processing", "count", len(items))
	for _, item := range items {
		s.runAIDescription(item.ID, item.StoragePath, item.ContentType)
	}
}

// --- embeddings ---

// defaultSimilarLimit bounds an unbounded similarity search.
const defaultSimilarLimit = 1000

// embedText computes the text embedding of an item's descriptions and stores
// it. It is safe to call concurrently.
func (s *svc) embedText(ctx context.Context, id string) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	item, err := s.repo.Get(ctx, id)
	if err != nil {
		slog.Error("embed text: get item", "item", id, "error", err)
		return
	}
	text := buildEmbedText(item)
	if text == "" {
		return
	}
	vec, err := s.textEmbedder.EmbedText(ctx, text)
	if err != nil {
		slog.Error("embed text: embed", "item", id, "error", err)
		return
	}
	if err := s.repo.SetTextEmbedding(ctx, id, vec); err != nil {
		slog.Error("embed text: save", "item", id, "error", err)
	}
}

// embedImage fetches the stored file (or a representative frame for video/gif)
// and stores its image embedding.
func (s *svc) embedImage(ctx context.Context, id, path, contentType string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	rc, err := s.files.Get(ctx, path)
	if err != nil {
		slog.Error("embed image: get file", "item", id, "error", err)
		return
	}
	defer rc.Close()

	data, err := s.representativeImage(ctx, rc, contentType)
	if err != nil {
		slog.Error("embed image: read image", "item", id, "error", err)
		return
	}
	vec, err := s.imageEmbedder.EmbedImage(ctx, data)
	if err != nil {
		slog.Error("embed image: embed", "item", id, "error", err)
		return
	}
	if err := s.repo.SetImageEmbedding(ctx, id, vec); err != nil {
		slog.Error("embed image: save", "item", id, "error", err)
	}
}

// embedItem computes and stores both vectors for an item. Used by the backfill
// worker and after description generation.
func (s *svc) embedItem(ctx context.Context, id, path, contentType string) {
	if s.textEmbedder != nil {
		s.embedText(ctx, id)
	}
	if s.imageEmbedder != nil {
		s.embedImage(ctx, id, path, contentType)
	}
}

// representativeImage returns a single image (bytes) to embed: the whole file
// for images, or a middle frame for video/gif.
func (s *svc) representativeImage(ctx context.Context, rc io.Reader, contentType string) ([]byte, error) {
	if describableContentType(contentType) == "image" {
		return io.ReadAll(rc)
	}
	frames, err := extractFrames(ctx, rc)
	if err != nil {
		return nil, err
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("no frames extracted")
	}
	return frames[len(frames)/2], nil
}

// buildEmbedText joins the available textual descriptions of an item into a
// single string for embedding.
func buildEmbedText(item *model.Item) string {
	var parts []string
	if item.Description != "" {
		parts = append(parts, item.Description)
	}
	if item.OriginalCaption != "" {
		parts = append(parts, item.OriginalCaption)
	}
	if item.AIDescription != nil && *item.AIDescription != "" {
		parts = append(parts, *item.AIDescription)
	}
	return strings.Join(parts, "\n")
}

func (s *svc) embeddingBackfillPoller(interval time.Duration) {
	for range time.Tick(interval) {
		s.backfillPendingEmbeddings()
	}
}

func (s *svc) backfillPendingEmbeddings() {
	ctx := context.Background()
	items, err := s.repo.PendingEmbeddings(ctx, s.embedBackfillBatch)
	if err != nil {
		slog.Error("embedding backfill: list", "error", err)
		return
	}
	if len(items) == 0 {
		return
	}
	slog.Info("embedding backfill: processing", "count", len(items))
	for _, item := range items {
		s.embedItem(context.Background(), item.ID, item.StoragePath, item.ContentType)
	}
}

// Similar turns the request into one or more embedding vectors and ranks items
// by the weighted cosine similarity.
func (s *svc) Similar(ctx context.Context, req model.SimilarRequest) ([]*model.Item, error) {
	q := model.SimilarQuery{Limit: req.Limit, Offset: req.Offset}
	if q.Limit <= 0 {
		q.Limit = defaultSimilarLimit
	}

	if req.Text != "" {
		if s.textEmbedder != nil {
			tv, err := s.textEmbedder.EmbedText(ctx, req.Text)
			if err != nil {
				return nil, fmt.Errorf("embed text query: %w", err)
			}
			q.TextVector = tv
			q.TextWeight = req.TextWeight
		}
		if s.imageEmbedder != nil {
			iv, err := s.imageEmbedder.EmbedText(ctx, req.Text)
			if err != nil {
				return nil, fmt.Errorf("embed text query (image space): %w", err)
			}
			q.ImageVector = iv
			q.ImageWeight = req.ImageWeight
		}
	}

	if req.ItemID != "" {
		if s.imageEmbedder == nil {
			return nil, fmt.Errorf("image embeddings are disabled")
		}
		iv, err := s.repo.GetEmbedding(ctx, req.ItemID, model.EmbeddingKindImage)
		if err != nil {
			return nil, fmt.Errorf("get item embedding: %w", err)
		}
		q.ImageVector = iv
		if q.ImageWeight == 0 {
			q.ImageWeight = 1
		}
	}

	if len(req.ImageBytes) > 0 {
		if s.imageEmbedder == nil {
			return nil, fmt.Errorf("image embeddings are disabled")
		}
		iv, err := s.imageEmbedder.EmbedImage(ctx, req.ImageBytes)
		if err != nil {
			return nil, fmt.Errorf("embed image query: %w", err)
		}
		q.ImageVector = iv
		if q.ImageWeight == 0 {
			q.ImageWeight = 1
		}
	}

	if q.TextWeight == 0 && q.ImageWeight == 0 {
		q.TextWeight, q.ImageWeight = 1, 1
	} else if q.TextWeight == 0 {
		q.TextWeight = 1
	} else if q.ImageWeight == 0 {
		q.ImageWeight = 1
	}

	return s.repo.Similar(ctx, q)
}

// describableContentType classifies a content type for the vision pipeline:
// "image" goes to the model as-is, "video" needs frame extraction, "" is not
// handled (e.g. documents).
func describableContentType(contentType string) string {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	switch {
	case strings.HasPrefix(mt, "image/"):
		return "image"
	case strings.HasPrefix(mt, "video/"):
		return "video"
	}
	return ""
}

func formatFromContentType(contentType, path string) string {
	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	if ext != "" {
		return ext
	}
	_, params, err := mime.ParseMediaType(contentType)
	if err == nil {
		if exts, err := mime.ExtensionsByType(contentType); err == nil && len(exts) > 0 {
			_ = params
			return strings.TrimPrefix(exts[0], ".")
		}
	}
	return "mp4"
}
