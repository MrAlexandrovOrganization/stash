package repository

import (
	"context"
	"embed"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"stash/internal/model"
)

//go:embed sql/transcript_*.sql
var transcriptQueries embed.FS

func transcriptSQL(name string) string {
	data, err := transcriptQueries.ReadFile("sql/transcript_" + name + ".sql")
	if err != nil {
		panic(err)
	}
	return string(data)
}

func (r *postgres) EnsureTranscription(ctx context.Context, id, path, format string) error {
	_, err := r.db.Exec(ctx, transcriptSQL("ensure"), id, path, "stash:transcript:"+uuid.NewString(), format)
	return err
}

func scanTranscription(row scanner) (*model.TranscriptionTask, error) {
	var t model.TranscriptionTask
	err := row.Scan(&t.RequestID, &t.ItemID, &t.Generation, &t.StoragePath, &t.Format, &t.JobID, &t.State)
	return &t, err
}

func (r *postgres) TranscriptionTasks(ctx context.Context, state string, submit bool, limit int) ([]*model.TranscriptionTask, error) {
	rows, err := r.db.Query(ctx, transcriptSQL("tasks"), state, submit, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []*model.TranscriptionTask
	for rows.Next() {
		t, err := scanTranscription(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (r *postgres) SetTranscriptionJob(ctx context.Context, requestID, jobID string) error {
	_, err := r.db.Exec(ctx, transcriptSQL("set_job"), requestID, jobID)
	return err
}

func (r *postgres) CompleteTranscription(ctx context.Context, requestID, jobID, terminalStatus, text string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	t, err := scanTranscription(tx.QueryRow(ctx, transcriptSQL("lock"), requestID, jobID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrStaleProcessing
	}
	if err != nil {
		return err
	}
	if t.State != "pending" {
		return nil
	}
	// Zero updated item rows mean a newer upload/manual edit/deletion won.
	// Still durably save the terminal outcome so the obsolete remote job is Acked.
	if _, err := tx.Exec(ctx, transcriptSQL("apply"), t.ItemID, t.Generation, t.StoragePath, text, jobID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, transcriptSQL("terminal"), requestID, jobID, terminalStatus, text); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *postgres) MissingTranscription(ctx context.Context, requestID, jobID string) error {
	_, err := r.db.Exec(ctx, transcriptSQL("missing"), requestID, jobID)
	return err
}

func (r *postgres) AcknowledgeTranscription(ctx context.Context, requestID, jobID string) error {
	_, err := r.db.Exec(ctx, transcriptSQL("ack"), requestID, jobID)
	return err
}

func (r *postgres) RetryTranscription(ctx context.Context, requestID string) error {
	_, err := r.db.Exec(ctx, transcriptSQL("retry"), requestID)
	return err
}

func (r *postgres) PendingTranscriptSubmissions(ctx context.Context, limit int) ([]*model.Item, error) {
	rows, err := r.db.Query(ctx, transcriptSQL("untracked"), limit)
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
