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
	"sync"
	"time"

	"github.com/google/uuid"
	"stash/internal/embed"
	"stash/internal/filestore"
	"stash/internal/model"
	"stash/internal/processing"
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
	SearchPage(ctx context.Context, q model.SearchQuery) (*model.SearchPage, error)
	Update(ctx context.Context, id string, meta model.UpdateMeta) (*model.Item, error)
	Describe(ctx context.Context, id string) (*model.Item, error)
	// Similar runs a hybrid embedding-based search (text and/or image).
	Similar(ctx context.Context, req model.SimilarRequest) ([]*model.Item, error)
}

// ErrNotDescribable is returned when AI description is requested for a media
// type the vision provider cannot handle (e.g. documents).
var ErrNotDescribable = errors.New("media type is not describable")

// ErrSearchUnavailable means that no query embedding provider succeeded.
var ErrSearchUnavailable = errors.New("search providers unavailable")

// ErrIndexPending means the selected item has no usable image embedding yet.
var ErrIndexPending = errors.New("media index pending")

// ErrVisionDisabled is returned when no vision provider is configured.
var ErrVisionDisabled = errors.New("vision provider is not configured")

type svc struct {
	repo          repository.Repository
	files         filestore.FileStore
	whisper       transcriptionClient
	vision        vision.Provider
	textEmbedder  embed.Embedder
	imageEmbedder embed.Embedder
	mediaPreparer interface {
		PrepareMedia(context.Context, string, string) ([][]byte, error)
	}
	pollDelay          time.Duration
	transcriptSubmitMu sync.Mutex
	transcriptPollMu   sync.Mutex

	aiBackfillInterval time.Duration
	aiBackfillBatch    int

	embedBackfillInterval time.Duration
	embedBackfillBatch    int
}

func New(repo repository.Repository, files filestore.FileStore, wc *whisper.Client, vp vision.Provider, opts ...Option) Service {
	s := &svc{
		repo:      repo,
		files:     files,
		vision:    vp,
		pollDelay: 5 * time.Second,
	}
	for _, o := range opts {
		o(s)
	}
	if wc != nil {
		s.whisper = wc
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

// WithMediaPreparer delegates video/GIF decoding to the durable worker queue.
func WithMediaPreparer(p interface {
	PrepareMedia(context.Context, string, string) ([][]byte, error)
}) Option {
	return func(s *svc) { s.mediaPreparer = p }
}

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

// uploadTimeout is shorter than the database reservation's one-hour lifetime.
// A reservation that expires can never be published, even if storage was slow.
const uploadTimeout = 30 * time.Minute

func (s *svc) Upload(ctx context.Context, r io.Reader, meta model.UploadMeta) (*model.Item, error) {
	ctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()
	now := time.Now()
	id := uuid.New().String()
	// The key is independent of the user-supplied filename and deduplicated item
	// ID. Concurrent attempts always write different objects, including on retry.
	path := "uploads/" + uuid.NewString()
	if err := s.repo.ReserveUpload(ctx, path); err != nil {
		return nil, fmt.Errorf("reserve upload: %w", err)
	}
	if err := s.files.Put(ctx, path, r, meta.Size, meta.ContentType); err != nil {
		return nil, fmt.Errorf("store file: %w", err)
	}
	item := &model.Item{
		ID: id, Type: meta.Type, FileName: meta.FileName,
		ContentType: meta.ContentType, Size: meta.Size, StoragePath: path,
		Description: meta.Description, Tags: meta.Tags, Source: meta.Source,
		OriginalCaption: meta.OriginalCaption, CreatedAt: now, UpdatedAt: now,
	}
	if item.Tags == nil {
		item.Tags = []string{}
	}
	if meta.FileUniqueID != "" {
		fu := meta.FileUniqueID
		item.FileUniqueID = &fu
	}
	saved, err := s.repo.SaveUpload(ctx, item)
	if err != nil {
		// This includes ambiguous commit outcomes: the journal decides whether the
		// new object is live. Never delete either version from a failed request.
		return nil, fmt.Errorf("save item: %w", err)
	}
	s.processUploadedMedia(saved.ID, saved.StoragePath, meta)
	return saved, nil
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
	return s.repo.Delete(ctx, id)
}

func (s *svc) Search(ctx context.Context, q model.SearchQuery) ([]*model.Item, error) {
	return s.repo.Search(ctx, q)
}

// SearchPage preserves the legacy list API and adds an explicit bounded page.
func (s *svc) SearchPage(ctx context.Context, q model.SearchQuery) (*model.SearchPage, error) {
	total, err := s.repo.CountSearch(ctx, q)
	if err != nil {
		return nil, err
	}
	items, err := s.repo.Search(ctx, q)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []*model.Item{}
	}
	return &model.SearchPage{Items: items, Total: total}, nil
}

func (s *svc) Update(ctx context.Context, id string, meta model.UpdateMeta) (*model.Item, error) {
	if err := s.repo.Update(ctx, id, meta); err != nil {
		return nil, err
	}
	return s.repo.Get(ctx, id)
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
	if err := s.repo.RequestDescription(ctx, id); err != nil {
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
	s.describeWithContext(context.Background(), itemID, path, contentType)
}
func (s *svc) describeWithContext(base context.Context, itemID, path, contentType string) {
	ctx, cancel := context.WithTimeout(base, 10*time.Minute)
	defer cancel()
	item, err := s.repo.Get(ctx, itemID)
	if err != nil || item.StoragePath != path {
		return
	}
	ctx = processing.RequestIdentity(ctx, fmt.Sprintf("stash:vision:%s:%d", itemID, item.ProcessingGeneration))

	if provider, ok := s.vision.(interface {
		DescribeMedia(context.Context, string, string) (string, error)
	}); ok {
		desc, err := provider.DescribeMedia(ctx, path, contentType)
		s.saveDescription(ctx, item, desc, err)
		return
	}
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
			_ = s.repo.SetAIDescriptionErrorAt(ctx, itemID, "frame extraction failed", item.ProcessingGeneration)
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
		_ = s.repo.SetAIDescriptionErrorAt(ctx, itemID, "empty image bytes from storage", item.ProcessingGeneration)
		return
	}

	desc, err := s.vision.Describe(ctx, frames)
	s.saveDescription(ctx, item, desc, err)
}

func (s *svc) saveDescription(ctx context.Context, item *model.Item, desc string, err error) {
	itemID := item.ID
	if err != nil {
		slog.Error("ai description: describe", "item", itemID, "error", err)
		// Permanent-looking failure (e.g. Ollama 400 "failed to decode image
		// bytes"): record it so the backfill stops retrying this item forever.
		var terminal processing.TerminalError
		if errors.As(err, &terminal) {
			_ = s.repo.SetAIDescriptionErrorAt(ctx, itemID, terminal.Error(), item.ProcessingGeneration)
		}
		return
	}
	if strings.TrimSpace(desc) == "" {
		// Ollama occasionally returns an empty response; do not persist it,
		// otherwise the backfill would treat the item as "done" and skip it.
		slog.Warn("ai description: empty response, skipping", "item", itemID)
		return
	}

	if err := s.repo.SetAIDescriptionAt(ctx, itemID, desc, item.ProcessingGeneration); err != nil {
		slog.Error("ai description: save", "item", itemID, "error", err)
	} else {
		slog.Info("ai description done", "item", itemID)
	}

	// Now that we have a description, (re)compute embeddings: the text vector
	// gains the AI description, and the image vector is refreshed if it was not
	// embedded at upload time.
	if s.textEmbedder != nil || s.imageEmbedder != nil {
		go s.embedItem(context.Background(), itemID, item.StoragePath, item.ContentType)
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
	exists, err := s.repo.HasEmbedding(ctx, id, model.EmbeddingKindText)
	if err != nil {
		slog.Error("embed text: check stored vector", "item", id, "error", err)
		return
	}
	if exists {
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
	if err := s.repo.SetTextEmbeddingAt(ctx, id, vec, item.ProcessingGeneration); err != nil {
		slog.Error("embed text: save", "item", id, "error", err)
	}
}

// embedImage fetches the stored file (or a representative frame for video/gif)
// and stores its image embedding.
func (s *svc) embedImage(ctx context.Context, id, path, contentType string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	item, err := s.repo.Get(ctx, id)
	if err != nil || item.StoragePath != path {
		return
	}

	exists, err := s.repo.HasEmbedding(ctx, id, model.EmbeddingKindImage)
	if err != nil {
		slog.Error("embed image: check stored vector", "item", id, "error", err)
		return
	}
	if exists {
		return
	}
	var data []byte
	if s.mediaPreparer != nil && describableContentType(contentType) != "image" {
		ctx = processing.RequestIdentity(ctx, fmt.Sprintf("stash:frames:%s:%d", id, item.ProcessingGeneration))
		var frames [][]byte
		frames, err = s.mediaPreparer.PrepareMedia(ctx, path, contentType)
		if err == nil && len(frames) > 0 {
			data = frames[len(frames)/2]
		}
		if err == nil && len(data) == 0 {
			err = errors.New("empty prepared frames")
		}
	} else {
		var rc io.ReadCloser
		rc, err = s.files.Get(ctx, path)
		if err != nil {
			slog.Error("embed image: get file", "item", id, "error", err)
			return
		}
		defer rc.Close()
		data, err = s.representativeImage(ctx, rc, contentType)
	}
	if err != nil {
		slog.Error("embed image: read image", "item", id, "error", err)
		return
	}
	vec, err := s.imageEmbedder.EmbedImage(ctx, data)
	if err != nil {
		slog.Error("embed image: embed", "item", id, "error", err)
		return
	}
	if err := s.repo.SetImageEmbeddingAt(ctx, id, vec, item.ProcessingGeneration); err != nil {
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

// Similar uses each available embedding branch independently. A failed optional
// provider must not discard a successful branch. Bound query inference so a
// missing worker does not consume the caller's entire request budget.
func (s *svc) Similar(ctx context.Context, req model.SimilarRequest) ([]*model.Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q := model.SimilarQuery{Limit: req.Limit, Offset: req.Offset, Tags: req.Tags, ExcludeTags: req.ExcludeTags, TextWeight: req.TextWeight, ImageWeight: req.ImageWeight}
	if q.TextWeight <= 0 {
		q.TextWeight = 1
	}
	if q.ImageWeight <= 0 {
		q.ImageWeight = 1
	}
	if req.Text != "" {
		type result struct {
			kind   model.EmbeddingKind
			vector []float32
			err    error
		}
		results := make(chan result, 2)
		count := 0
		for _, branch := range []struct {
			kind     model.EmbeddingKind
			provider embed.Embedder
		}{{model.EmbeddingKindText, s.textEmbedder}, {model.EmbeddingKindImage, s.imageEmbedder}} {
			if branch.provider == nil {
				continue
			}
			count++
			go func() {
				branchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				defer cancel()
				vec, err := branch.provider.EmbedText(branchCtx, req.Text)
				results <- result{branch.kind, vec, err}
			}()
		}
		for range count {
			select {
			case r := <-results:
				if r.err != nil {
					slog.WarnContext(ctx, "search embedding branch unavailable", "kind", r.kind, "error", r.err)
					continue
				}
				if r.kind == model.EmbeddingKindText {
					q.TextVector = r.vector
				} else {
					q.ImageVector = r.vector
				}
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	if req.ItemID != "" {
		iv, err := s.repo.GetEmbedding(ctx, req.ItemID, model.EmbeddingKindImage)
		if err != nil {
			return nil, fmt.Errorf("get item embedding: %w", err)
		}
		if len(iv) == 0 {
			return nil, ErrIndexPending
		}
		q.ImageVector = iv
	}
	if len(req.ImageBytes) > 0 {
		if s.imageEmbedder == nil {
			return nil, ErrSearchUnavailable
		}
		queryCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		iv, err := s.imageEmbedder.EmbedImage(queryCtx, req.ImageBytes)
		if err != nil {
			return nil, fmt.Errorf("%w: image provider", ErrSearchUnavailable)
		}
		q.ImageVector = iv
	}
	if len(q.TextVector) == 0 && len(q.ImageVector) == 0 {
		return nil, ErrSearchUnavailable
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
