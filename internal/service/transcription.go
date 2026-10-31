package service

import (
	"context"
	"io"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"stash/internal/model"
	"stash/internal/whisper"
)

type transcriptionClient interface {
	Submit(context.Context, io.Reader, string, string) (string, error)
	GetStatus(context.Context, string) (*whisper.JobResult, error)
	Ack(context.Context, string) error
}

const transcriptBackfillBatch = 2

func (s *svc) submitTranscription(itemID, path, contentType string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := s.repo.EnsureTranscription(ctx, itemID, path, formatFromContentType(contentType, path))
	cancel()
	if err != nil {
		slog.Error("transcription: persist intent failed")
		return // Untracked videos are recovered by the next backfill pass.
	}
	s.submitTranscriptionTasks(transcriptBackfillBatch)
}

func (s *svc) transcriptPoller() {
	s.submitMissingTranscripts(transcriptBackfillBatch)
	s.pollPendingTranscripts()
	tick := time.NewTicker(s.pollDelay)
	defer tick.Stop()
	submitTick := time.NewTicker(time.Minute)
	defer submitTick.Stop()
	for {
		select {
		case <-tick.C:
			s.pollPendingTranscripts()
		case <-submitTick.C:
			s.submitMissingTranscripts(transcriptBackfillBatch)
		}
	}
}

func (s *svc) submitMissingTranscripts(limit int) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	items, err := s.repo.PendingTranscriptSubmissions(ctx, limit)
	if err == nil {
		for _, item := range items {
			err = s.repo.EnsureTranscription(ctx, item.ID, item.StoragePath, formatFromContentType(item.ContentType, item.StoragePath))
			if err != nil {
				break
			}
		}
	}
	cancel()
	if err != nil {
		slog.Error("transcript backfill: persist intent failed")
	}
	s.submitTranscriptionTasks(limit)
}

func (s *svc) submitTranscriptionTasks(limit int) {
	if !s.transcriptSubmitMu.TryLock() {
		return
	}
	defer s.transcriptSubmitMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	tasks, err := s.repo.TranscriptionTasks(ctx, "pending", true, limit)
	cancel()
	if err != nil {
		slog.Error("transcript submissions: list failed")
		return
	}
	for _, task := range tasks {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		err := s.submitTranscriptTask(ctx, task)
		cancel()
		if err != nil {
			s.retryTranscript(task, "submit", err)
		}
	}
}

func (s *svc) submitTranscriptTask(ctx context.Context, t *model.TranscriptionTask) error {
	r, err := s.files.Get(ctx, t.StoragePath)
	if err != nil {
		return err
	}
	defer r.Close()
	jobID, err := s.whisper.Submit(ctx, r, t.Format, t.RequestID)
	if err != nil {
		return err
	}
	return s.repo.SetTranscriptionJob(ctx, t.RequestID, jobID)
}

func (s *svc) pollPendingTranscripts() {
	if !s.transcriptPollMu.TryLock() {
		return
	}
	defer s.transcriptPollMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	tasks, err := s.repo.TranscriptionTasks(ctx, "pending", false, 32)
	cancel()
	if err != nil {
		slog.Error("transcript poll: list failed")
		return
	}
	for _, t := range tasks {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err := s.pollTranscriptTask(ctx, t)
		cancel()
		if err != nil {
			s.retryTranscript(t, "poll", err)
		}
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	tasks, err = s.repo.TranscriptionTasks(ctx, "ack", false, 32)
	cancel()
	if err != nil {
		slog.Error("transcript acknowledgements: list failed")
		return
	}
	for _, t := range tasks {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err := s.whisper.Ack(ctx, *t.JobID)
		// NOT_FOUND after durable receipt means it was already collected. A
		// missing job before receipt instead goes through resubmission below.
		if err == nil || status.Code(err) == codes.NotFound {
			err = s.repo.AcknowledgeTranscription(ctx, t.RequestID, *t.JobID)
		}
		cancel()
		if err != nil {
			s.retryTranscript(t, "ack", err)
		}
	}
}

func (s *svc) pollTranscriptTask(ctx context.Context, t *model.TranscriptionTask) error {
	result, err := s.whisper.GetStatus(ctx, *t.JobID)
	if status.Code(err) == codes.NotFound {
		return s.repo.MissingTranscription(ctx, t.RequestID, *t.JobID)
	}
	if err != nil {
		return err
	}
	switch result.Status {
	case whisper.StatusDone:
		return s.repo.CompleteTranscription(ctx, t.RequestID, *t.JobID, "done", result.Text)
	case whisper.StatusFailed:
		// Preserve the established empty-transcript marker for a permanent
		// failure. Do not log upstream error text, which may contain input.
		return s.repo.CompleteTranscription(ctx, t.RequestID, *t.JobID, "failed", "")
	}
	return nil
}

func (s *svc) retryTranscript(t *model.TranscriptionTask, stage string, err error) {
	slog.Warn("transcription retry", "stage", stage, "code", status.Code(err).String())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.repo.RetryTranscription(ctx, t.RequestID); err != nil {
		slog.Error("transcription: persist retry failed")
	}
}
