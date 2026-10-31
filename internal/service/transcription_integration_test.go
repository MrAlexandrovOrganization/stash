//go:build integration

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"stash/internal/model"
	"stash/internal/repository"
	"stash/internal/whisper"
)

type transcriptRemote struct {
	mu           sync.Mutex
	requests     map[string]string
	results      map[string]*whisper.JobResult
	calls        []string
	acks         []string
	lostSubmit   bool
	lostAck      bool
	beforeSubmit func(string)
	beforeAck    func(string)
}

func newTranscriptRemote() *transcriptRemote {
	return &transcriptRemote{requests: make(map[string]string), results: make(map[string]*whisper.JobResult)}
}

func (r *transcriptRemote) Submit(ctx context.Context, data io.Reader, format, id string) (string, error) {
	if r.beforeSubmit != nil {
		r.beforeSubmit(id)
	}
	bytes, err := io.ReadAll(data)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, id)
	if previous, ok := r.requests[id]; ok && previous != string(bytes) {
		return "", errors.New("request ID reused with different input")
	}
	r.requests[id] = string(bytes)
	job := "job:" + id
	if _, exists := r.results[job]; !exists {
		r.results[job] = &whisper.JobResult{Status: whisper.StatusRunning}
	}
	if r.lostSubmit {
		r.lostSubmit = false
		return "", status.Error(codes.Unavailable, "lost Submit response")
	}
	return job, nil
}

func (r *transcriptRemote) GetStatus(ctx context.Context, id string) (*whisper.JobResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result, ok := r.results[id]
	if !ok {
		return nil, status.Error(codes.NotFound, "fixture missing")
	}
	copy := *result
	return &copy, nil
}

func (r *transcriptRemote) Ack(ctx context.Context, id string) error {
	if r.beforeAck != nil {
		r.beforeAck(id)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.acks = append(r.acks, id)
	if _, ok := r.results[id]; !ok {
		return status.Error(codes.NotFound, "fixture collected")
	}
	if r.lostAck {
		r.lostAck = false
		delete(r.results, id)
		return status.Error(codes.Unavailable, "lost Ack response")
	}
	delete(r.results, id)
	return nil
}

func videoFixture(t *testing.T, repo repository.Repository, files *uploadFiles, body string) *model.Item {
	t.Helper()
	meta := uploadMeta("fixture.mp4")
	meta.Type = model.MediaTypeVideo
	meta.ContentType = "video/mp4"
	meta.Size = int64(len(body))
	item, err := (&svc{repo: repo, files: files}).Upload(context.Background(), strings.NewReader(body), meta)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func transcriptTask(t *testing.T, db *pgxpool.Pool, id string) *model.TranscriptionTask {
	t.Helper()
	var task model.TranscriptionTask
	err := db.QueryRow(context.Background(), `SELECT request_id,item_id,generation,storage_path,audio_format,job_id,state
		FROM transcription_tasks WHERE item_id=$1 ORDER BY generation DESC LIMIT 1`, id).
		Scan(&task.RequestID, &task.ItemID, &task.Generation, &task.StoragePath, &task.Format, &task.JobID, &task.State)
	if err != nil {
		t.Fatal(err)
	}
	return &task
}

func dueTranscripts(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	if _, err := db.Exec(context.Background(), "UPDATE transcription_tasks SET next_attempt_at = NOW()"); err != nil {
		t.Fatal(err)
	}
}

func assertTranscript(t *testing.T, repo repository.Repository, id string, want *string) {
	t.Helper()
	item, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if want == nil {
		if item.Transcript != nil {
			t.Fatalf("unexpected transcript %q", *item.Transcript)
		}
	} else if item.Transcript == nil || *item.Transcript != *want {
		t.Fatalf("transcript mismatch; want %q", *want)
	}
}

func TestTranscriptionLostSubmitRestartAndLostAck(t *testing.T) {
	db, repo, files := uploadFixture(t)
	item := videoFixture(t, repo, files, "synthetic audio")
	remote := newTranscriptRemote()
	remote.lostSubmit = true
	remote.beforeSubmit = func(id string) {
		if transcriptTask(t, db, item.ID).RequestID != id {
			t.Error("Submit preceded durable request identity")
		}
	}
	s := &svc{repo: repo, files: files, whisper: remote}
	s.submitMissingTranscripts(2)
	first := transcriptTask(t, db, item.ID)
	if first.JobID != nil {
		t.Fatal("expected lost Submit response")
	}
	dueTranscripts(t, db)
	s = &svc{repo: repository.NewPostgres(db), files: files, whisper: remote} // Restart.
	s.submitMissingTranscripts(2)
	task := transcriptTask(t, db, item.ID)
	if task.JobID == nil || task.RequestID != first.RequestID || len(remote.requests) != 1 {
		t.Fatal("restart duplicated submission identity")
	}
	text := "durable result after a long outage"
	remote.results[*task.JobID] = &whisper.JobResult{Status: whisper.StatusDone, Text: text}
	// Database journal remains pending across an outage longer than legacy TTL.
	if _, err := db.Exec(context.Background(), "UPDATE transcription_tasks SET created_at = NOW() - INTERVAL '3 hours'"); err != nil {
		t.Fatal(err)
	}
	remote.beforeAck = func(job string) {
		if transcriptTask(t, db, item.ID).State != "ack" {
			t.Error("Ack preceded durable terminal state")
		}
		assertTranscript(t, repo, item.ID, &text)
	}
	remote.lostAck = true
	s.pollPendingTranscripts()
	assertTranscript(t, repo, item.ID, &text)
	if transcriptTask(t, db, item.ID).State != "ack" {
		t.Fatal("lost Ack discarded the retry")
	}
	dueTranscripts(t, db)
	s = &svc{repo: repository.NewPostgres(db), files: files, whisper: remote}
	s.pollPendingTranscripts() // Ack NOT_FOUND is safe only after durable receipt.
	if transcriptTask(t, db, item.ID).State != "done" {
		t.Fatal("Ack retry did not finish")
	}
}

func TestTranscriptionSaveFailureDoesNotAck(t *testing.T) {
	db, repo, files := uploadFixture(t)
	item := videoFixture(t, repo, files, "audio")
	remote := newTranscriptRemote()
	s := &svc{repo: repo, files: files, whisper: remote}
	s.submitMissingTranscripts(2)
	task := transcriptTask(t, db, item.ID)
	text := "saved after recovery"
	remote.results[*task.JobID] = &whisper.JobResult{Status: whisper.StatusDone, Text: text}
	if _, err := db.Exec(context.Background(), `CREATE FUNCTION fixture_fail_terminal() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'synthetic SQL failure'; END; $$;
 CREATE TRIGGER fixture_fail_terminal BEFORE UPDATE ON transcription_tasks
 FOR EACH ROW WHEN (NEW.state = 'ack') EXECUTE FUNCTION fixture_fail_terminal();`); err != nil {
		t.Fatal(err)
	}
	s.pollPendingTranscripts()
	if len(remote.acks) != 0 {
		t.Fatal("Ack after database failure")
	}
	assertTranscript(t, repo, item.ID, nil)
	dueTranscripts(t, db)
	if _, err := db.Exec(context.Background(), "DROP TRIGGER fixture_fail_terminal ON transcription_tasks"); err != nil {
		t.Fatal(err)
	}
	s.repo = repository.NewPostgres(db)
	s.pollPendingTranscripts()
	assertTranscript(t, repo, item.ID, &text)
	if transcriptTask(t, db, item.ID).State != "done" {
		t.Fatal("result was not recovered")
	}
}

func TestTranscriptionLegacyNotFoundRecovers(t *testing.T) {
	db, repo, files := uploadFixture(t)
	item := videoFixture(t, repo, files, "audio")
	if _, err := db.Exec(context.Background(), "UPDATE items SET transcript_job_id='expired-legacy' WHERE id=$1", item.ID); err != nil {
		t.Fatal(err)
	}
	remote := newTranscriptRemote()
	s := &svc{repo: repo, files: files, whisper: remote}
	s.submitMissingTranscripts(2)
	if len(remote.calls) != 0 {
		t.Fatal("legacy job was duplicated before polling")
	}
	s.pollPendingTranscripts()
	if transcriptTask(t, db, item.ID).JobID != nil {
		t.Fatal("missing legacy job was not reset")
	}
	s.submitMissingTranscripts(2)
	task := transcriptTask(t, db, item.ID)
	if task.JobID == nil || *task.JobID == "expired-legacy" {
		t.Fatal("legacy task was not resubmitted")
	}
	remote.results[*task.JobID] = &whisper.JobResult{Status: whisper.StatusDone, Text: "recovered"}
	s.pollPendingTranscripts()
	text := "recovered"
	assertTranscript(t, repo, item.ID, &text)
}

func TestTranscriptionStaleResultsAndMediaRetention(t *testing.T) {
	for _, change := range []string{"reupload", "manual-edit", "delete", "description"} {
		t.Run(change, func(t *testing.T) {
			db, repo, files := uploadFixture(t)
			item := videoFixture(t, repo, files, "old audio")
			remote := newTranscriptRemote()
			remote.lostSubmit = true
			s := &svc{repo: repo, files: files, whisper: remote}
			s.submitMissingTranscripts(2)
			oldTask := transcriptTask(t, db, item.ID)
			manual := "user's own transcript"
			switch change {
			case "reupload":
				videoFixture(t, repo, files, "new audio")
			case "manual-edit":
				if err := repo.Update(context.Background(), item.ID, model.UpdateMeta{Transcript: &manual}); err != nil {
					t.Fatal(err)
				}
			case "description":
				if err := repo.Update(context.Background(), item.ID, model.UpdateMeta{Description: &manual}); err != nil {
					t.Fatal(err)
				}
			case "delete":
				if err := repo.Delete(context.Background(), item.ID); err != nil {
					t.Fatal(err)
				}
			}
			collectAll(t, db, repo, files)
			if r, err := files.Get(context.Background(), oldTask.StoragePath); err != nil {
				t.Fatal("GC removed unresolved transcription input")
			} else {
				r.Close()
			}
			dueTranscripts(t, db)
			s.submitTranscriptionTasks(2) // Recover lost job ID using the original bytes.
			job := "job:" + oldTask.RequestID
			remote.results[job] = &whisper.JobResult{Status: whisper.StatusDone, Text: "late result"}
			s.pollPendingTranscripts()
			switch change {
			case "reupload":
				assertTranscript(t, repo, item.ID, nil)
			case "manual-edit":
				assertTranscript(t, repo, item.ID, &manual)
				stored, err := repo.Get(context.Background(), item.ID)
				if err != nil || stored.TranscriptJobID != nil {
					t.Fatal("manual transcript retained an obsolete job link")
				}
			case "description":
				text := "late result"
				assertTranscript(t, repo, item.ID, &text)
			case "delete":
				if _, err := repo.Get(context.Background(), item.ID); !errors.Is(err, repository.ErrNotFound) {
					t.Fatal("deleted item was recreated")
				}
			}
			if len(remote.acks) != 1 {
				t.Fatal("obsolete or current terminal result was not acknowledged")
			}
			collectAll(t, db, repo, files)
			if change == "reupload" || change == "delete" {
				if _, err := files.Get(context.Background(), oldTask.StoragePath); err == nil {
					t.Fatal("finished obsolete input was not released")
				}
			}
		})
	}
}

func TestTranscriptionConcurrentIntentAndCompletion(t *testing.T) {
	db, repo, files := uploadFixture(t)
	item := videoFixture(t, repo, files, "audio")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := repository.NewPostgres(db).EnsureTranscription(ctx, item.ID, item.StoragePath, "mp4"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var count int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM transcription_tasks").Scan(&count); err != nil || count != 1 {
		t.Fatalf("concurrent intents: %d %v", count, err)
	}
	task := transcriptTask(t, db, item.ID)
	if err := repo.SetTranscriptionJob(ctx, task.RequestID, "fixture-job"); err != nil {
		t.Fatal(err)
	}
	for range 8 {
		wg.Go(func() {
			if err := repository.NewPostgres(db).CompleteTranscription(ctx, task.RequestID, "fixture-job", "done", "one result"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	text := "one result"
	assertTranscript(t, repo, item.ID, &text)
	if err := repo.CompleteTranscription(ctx, task.RequestID, "obsolete-job", "done", "bad"); err == nil {
		t.Fatal("wrong job ID accepted")
	}
	assertTranscript(t, repo, item.ID, &text)
	if transcriptTask(t, db, item.ID).State != "ack" {
		t.Fatal(fmt.Sprintf("unexpected terminal state for %s", task.RequestID))
	}
}

func TestTranscriptionNewJobSurvivesOldResult(t *testing.T) {
	db, repo, files := uploadFixture(t)
	item := videoFixture(t, repo, files, "old")
	remote := newTranscriptRemote()
	s := &svc{repo: repo, files: files, whisper: remote}
	s.submitMissingTranscripts(2)
	old := transcriptTask(t, db, item.ID)
	videoFixture(t, repo, files, "new")
	s.submitMissingTranscripts(2)
	current := transcriptTask(t, db, item.ID)
	if old.RequestID == current.RequestID || *old.JobID == *current.JobID {
		t.Fatal("new media reused the old identity")
	}
	remote.results[*old.JobID] = &whisper.JobResult{Status: whisper.StatusDone, Text: "obsolete"}
	s.pollPendingTranscripts()
	stored, err := repo.Get(context.Background(), item.ID)
	if err != nil || stored.Transcript != nil || stored.TranscriptJobID == nil || *stored.TranscriptJobID != *current.JobID {
		t.Fatal("old completion changed the new transcript job")
	}
	remote.results[*current.JobID] = &whisper.JobResult{Status: whisper.StatusDone, Text: "current"}
	s.pollPendingTranscripts()
	text := "current"
	assertTranscript(t, repo, item.ID, &text)
}

func TestTranscriptionTerminalFailureAndEmptyResult(t *testing.T) {
	for _, terminal := range []whisper.JobStatus{whisper.StatusFailed, whisper.StatusDone} {
		t.Run(terminal.String(), func(t *testing.T) {
			db, repo, files := uploadFixture(t)
			item := videoFixture(t, repo, files, "audio")
			remote := newTranscriptRemote()
			s := &svc{repo: repo, files: files, whisper: remote}
			s.submitMissingTranscripts(2)
			task := transcriptTask(t, db, item.ID)
			remote.results[*task.JobID] = &whisper.JobResult{Status: terminal, Error: "synthetic upstream detail"}
			s.pollPendingTranscripts()
			empty := ""
			assertTranscript(t, repo, item.ID, &empty)
			s.submitMissingTranscripts(2)
			if len(remote.calls) != 1 || len(remote.acks) != 1 || transcriptTask(t, db, item.ID).State != "done" {
				t.Fatal("terminal result was resubmitted or left unacknowledged")
			}
		})
	}
}

type lostJobWrite struct {
	repository.Repository
	afterCommit bool
	lost        bool
}

func (r *lostJobWrite) SetTranscriptionJob(ctx context.Context, requestID, jobID string) error {
	if !r.lost {
		r.lost = true
		if r.afterCommit {
			if err := r.Repository.SetTranscriptionJob(ctx, requestID, jobID); err != nil {
				return err
			}
		}
		return errors.New("synthetic lost job persistence")
	}
	return r.Repository.SetTranscriptionJob(ctx, requestID, jobID)
}

func TestTranscriptionJobPersistenceFailure(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(fmt.Sprintf("after-commit=%t", after), func(t *testing.T) {
			db, repo, files := uploadFixture(t)
			item := videoFixture(t, repo, files, "audio")
			remote := newTranscriptRemote()
			s := &svc{repo: &lostJobWrite{Repository: repo, afterCommit: after}, files: files, whisper: remote}
			s.submitMissingTranscripts(2)
			before := transcriptTask(t, db, item.ID)
			dueTranscripts(t, db)
			s = &svc{repo: repository.NewPostgres(db), files: files, whisper: remote}
			s.submitMissingTranscripts(2)
			after := transcriptTask(t, db, item.ID)
			if before.RequestID != after.RequestID || after.JobID == nil || len(remote.requests) != 1 {
				t.Fatal("database recovery changed the request or duplicated remote work")
			}
		})
	}
}
