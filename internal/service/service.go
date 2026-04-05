package service

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"stash/internal/filestore"
	"stash/internal/model"
	"stash/internal/repository"
	"stash/internal/whisper"
)

type Service interface {
	Upload(ctx context.Context, r io.Reader, meta model.UploadMeta) (*model.Item, error)
	Get(ctx context.Context, id string) (*model.Item, error)
	GetFile(ctx context.Context, id string) (io.ReadCloser, *model.Item, error)
	Delete(ctx context.Context, id string) error
	Search(ctx context.Context, q model.SearchQuery) ([]*model.Item, error)
	Update(ctx context.Context, id string, meta model.UpdateMeta) (*model.Item, error)
}

type svc struct {
	repo      repository.Repository
	files     filestore.FileStore
	whisper   *whisper.Client
	pollDelay time.Duration
}

func New(repo repository.Repository, files filestore.FileStore, wc *whisper.Client) Service {
	s := &svc{
		repo:      repo,
		files:     files,
		whisper:   wc,
		pollDelay: 5 * time.Second,
	}
	if wc != nil {
		go s.transcriptPoller()
	}
	return s
}

func (s *svc) Upload(ctx context.Context, r io.Reader, meta model.UploadMeta) (*model.Item, error) {
	id := uuid.New().String()
	now := time.Now()

	path, err := s.files.Put(ctx, id, meta.FileName, r, meta.Size, meta.ContentType)
	if err != nil {
		return nil, fmt.Errorf("store file: %w", err)
	}

	item := &model.Item{
		ID:          id,
		Type:        meta.Type,
		FileName:    meta.FileName,
		ContentType: meta.ContentType,
		Size:        meta.Size,
		StoragePath: path,
		Description: meta.Description,
		Tags:        meta.Tags,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if item.Tags == nil {
		item.Tags = []string{}
	}

	if err := s.repo.Save(ctx, item); err != nil {
		_ = s.files.Delete(ctx, path)
		return nil, fmt.Errorf("save item: %w", err)
	}

	if meta.Type == model.MediaTypeVideo && s.whisper != nil {
		go s.submitTranscription(item.ID, path, meta.ContentType)
	}

	return item, nil
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

// transcriptPoller runs in the background and polls pending transcription jobs.
func (s *svc) transcriptPoller() {
	for range time.Tick(s.pollDelay) {
		s.pollPendingTranscripts()
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
