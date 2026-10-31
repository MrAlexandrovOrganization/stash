//go:build integration

package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"stash/internal/migrate"
	"stash/internal/model"
	"stash/internal/repository"
	"stash/migrations"
)

// This suite is only run by make upload-test against a disposable database.
// It never reads POSTGRES_URL or any application configuration.
func uploadFixture(t *testing.T) (*pgxpool.Pool, repository.Repository, *uploadFiles) {
	t.Helper()
	url := os.Getenv("STASH_UPLOAD_TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("use make upload-test to supply an isolated PostgreSQL fixture")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	name := "upload_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config := admin.Config().Copy()
	config.ConnConfig.Database = name
	db, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		if _, err := admin.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	if err := migrate.Run(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return db, repository.NewPostgres(db), &uploadFiles{objects: make(map[string][]byte)}
}

type uploadFiles struct {
	mu       sync.Mutex
	objects  map[string][]byte
	afterPut func()
	putError error
}

func (f *uploadFiles) Put(ctx context.Context, path string, r io.Reader, size int64, contentType string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.mu.Lock()
	if _, exists := f.objects[path]; exists {
		f.mu.Unlock()
		return errors.New("attempt to overwrite an immutable key")
	}
	f.objects[path] = data
	f.mu.Unlock()
	if f.afterPut != nil {
		f.afterPut()
	}
	return f.putError
}

func (f *uploadFiles) Get(ctx context.Context, path string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.objects[path]
	if !ok {
		return nil, errors.New("object missing")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (f *uploadFiles) Delete(ctx context.Context, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, path)
	return nil
}

func (f *uploadFiles) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.objects)
}

func uploadMeta(name string) model.UploadMeta {
	return model.UploadMeta{
		Type: model.MediaTypeDocument, FileName: name,
		ContentType: "text/plain", Size: 3, FileUniqueID: "telegram-fixture",
		Description: "synthetic caption", Tags: []string{"fixture"},
	}
}

func mustUpload(t *testing.T, repo repository.Repository, files *uploadFiles, name, data string) *model.Item {
	t.Helper()
	s := &svc{repo: repo, files: files}
	meta := uploadMeta(name)
	meta.Size = int64(len(data))
	item, err := s.Upload(context.Background(), strings.NewReader(data), meta)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func assertStored(t *testing.T, repo repository.Repository, files *uploadFiles, id, data string) *model.Item {
	t.Helper()
	item, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	r, err := files.Get(context.Background(), item.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil || string(got) != data {
		t.Fatalf("stored file = %q, %v; want %q", got, err, data)
	}
	return item
}

func collectAll(t *testing.T, db *pgxpool.Pool, repo repository.Repository, files *uploadFiles) {
	t.Helper()
	// Advance only the fixture's journal deadlines; no sleeps or production data.
	if _, err := db.Exec(context.Background(), "UPDATE file_gc SET not_before = NOW() - INTERVAL '1 second'"); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		ok, err := repo.CollectFile(context.Background(), files.Delete)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return
		}
	}
	t.Fatal("garbage collection did not converge")
}

func TestUploadDatabaseFailurePreservesOldFile(t *testing.T) {
	for _, name := range []string{"same.txt", "renamed.txt"} {
		t.Run(name, func(t *testing.T) {
			db, repo, files := uploadFixture(t)
			old := mustUpload(t, repo, files, "same.txt", "old")
			meta := uploadMeta(name)
			meta.Type = "invalid" // A real SQL constraint failure inside SaveUpload.
			_, err := (&svc{repo: repo, files: files}).Upload(context.Background(), strings.NewReader("new"), meta)
			if err == nil {
				t.Fatal("expected database error")
			}
			current := assertStored(t, repo, files, old.ID, "old")
			if current.StoragePath != old.StoragePath || current.FileName != old.FileName || current.ProcessingGeneration != old.ProcessingGeneration {
				t.Fatal("failed upload changed published metadata")
			}
			collectAll(t, db, repo, files)
			assertStored(t, repo, files, old.ID, "old")
			if files.count() != 1 {
				t.Fatal("failed upload object was not collected")
			}
		})
	}
}

func TestUploadStorageFailureAndCancellation(t *testing.T) {
	for _, failure := range []string{"partial-put", "cancel-after-put"} {
		t.Run(failure, func(t *testing.T) {
			db, repo, files := uploadFixture(t)
			old := mustUpload(t, repo, files, "same.txt", "old")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if failure == "partial-put" {
				files.putError = errors.New("synthetic lost storage response")
			} else {
				files.afterPut = cancel
			}
			_, err := (&svc{repo: repo, files: files}).Upload(ctx, strings.NewReader("new"), uploadMeta("same.txt"))
			if err == nil {
				t.Fatal("expected upload failure")
			}
			assertStored(t, repo, files, old.ID, "old")
			collectAll(t, db, repo, files)
			assertStored(t, repo, files, old.ID, "old")
			if files.count() != 1 {
				t.Fatal("abandoned upload was not collected")
			}
		})
	}
}

type ambiguousUploadRepo struct{ repository.Repository }

func (r ambiguousUploadRepo) SaveUpload(ctx context.Context, item *model.Item) (*model.Item, error) {
	if _, err := r.Repository.SaveUpload(ctx, item); err != nil {
		return nil, err
	}
	return nil, errors.New("synthetic lost commit response")
}

func TestUploadUncertainCommitKeepsPublishedObject(t *testing.T) {
	db, repo, files := uploadFixture(t)
	old := mustUpload(t, repo, files, "same.txt", "old")
	_, err := (&svc{repo: ambiguousUploadRepo{repo}, files: files}).Upload(
		context.Background(), strings.NewReader("new"), uploadMeta("same.txt"))
	if err == nil {
		t.Fatal("expected lost response")
	}
	assertStored(t, repo, files, old.ID, "new")
	// A restart creates a new repository using only persisted state.
	restarted := repository.NewPostgres(db)
	collectAll(t, db, restarted, files)
	assertStored(t, restarted, files, old.ID, "new")
	if files.count() != 1 {
		t.Fatal("GC should retain only the published new version")
	}
}

func TestUploadConcurrentSameIdentity(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%t", existing), func(t *testing.T) {
			db, repo, files := uploadFixture(t)
			var old *model.Item
			if existing {
				old = mustUpload(t, repo, files, "same.txt", "old")
			}
			const n = 12
			ready := make(chan struct{}, n)
			release := make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			files.afterPut = func() { ready <- struct{}{}; <-release }
			type result struct {
				item *model.Item
				err  error
			}
			results := make(chan result, n)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			for i := range n {
				go func() {
					// Distinct repository instances model separate coordinators.
					s := &svc{repo: repository.NewPostgres(db), files: files}
					data := fmt.Sprintf("attempt-%d", i)
					meta := uploadMeta("same.txt")
					meta.Size = int64(len(data))
					item, err := s.Upload(ctx, strings.NewReader(data), meta)
					results <- result{item, err}
				}()
			}
			for range n {
				select {
				case <-ready:
				case <-ctx.Done():
					t.Fatal("concurrent puts did not reach the barrier")
				}
			}
			once.Do(func() { close(release) })
			paths := make(map[string]bool)
			var id string
			for range n {
				r := <-results
				if r.err != nil {
					t.Fatal(r.err)
				}
				if id != "" && id != r.item.ID {
					t.Fatal("concurrent uploads created different deduplication owners")
				}
				id = r.item.ID
				if paths[r.item.StoragePath] {
					t.Fatal("concurrent uploads reused an object key")
				}
				paths[r.item.StoragePath] = true
			}
			items, err := repo.Search(ctx, model.SearchQuery{})
			if err != nil || len(items) != 1 {
				t.Fatalf("deduplication: items=%d err=%v", len(items), err)
			}
			current := items[0]
			if old != nil && (id != old.ID || !current.CreatedAt.Equal(old.CreatedAt)) {
				// PostgreSQL timestamps have microsecond precision; both values
				// here were returned from the database.
				t.Fatal("re-upload changed identity or creation time")
			}
			if current.ProcessingGeneration != int64(n)+boolInt(existing) {
				t.Fatalf("unexpected processing generation: %d", current.ProcessingGeneration)
			}
			collectAll(t, db, repo, files)
			r, err := files.Get(ctx, current.StoragePath)
			if err != nil {
				t.Fatal("GC removed the winning upload", err)
			}
			r.Close()
			if files.count() != 1 {
				t.Fatalf("GC left %d objects", files.count())
			}
		})
	}
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func TestFileGCRetryAndDelete(t *testing.T) {
	db, repo, files := uploadFixture(t)
	old := mustUpload(t, repo, files, "same.txt", "old")
	current := mustUpload(t, repo, files, "renamed.txt", "new")
	if current.StoragePath == old.StoragePath {
		t.Fatal("re-upload reused an object key")
	}
	if ok, err := repo.CollectFile(context.Background(), files.Delete); ok || err != nil {
		t.Fatalf("GC ignored grace period: %t, %v", ok, err)
	}
	if _, err := db.Exec(context.Background(), "UPDATE file_gc SET not_before = NOW() - INTERVAL '1 second'"); err != nil {
		t.Fatal(err)
	}
	// Simulate successful removal followed by a lost response. The next attempt
	// must safely retry deleting the same object after a process restart.
	_, err := repo.CollectFile(context.Background(), func(ctx context.Context, path string) error {
		if err := files.Delete(ctx, path); err != nil {
			return err
		}
		return errors.New("synthetic lost delete response")
	})
	if err == nil {
		t.Fatal("expected deletion failure")
	}
	var pending int
	if err := db.QueryRow(context.Background(), "SELECT count(*) FROM file_gc").Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("lost durable retry: %d, %v", pending, err)
	}
	restarted := repository.NewPostgres(db)
	collectAll(t, db, restarted, files)
	assertStored(t, restarted, files, current.ID, "new")
	if err := (&svc{repo: restarted, files: files}).Delete(context.Background(), current.ID); err != nil {
		t.Fatal(err)
	}
	collectAll(t, db, restarted, files)
	if files.count() != 0 {
		t.Fatal("item deletion did not collect the current object")
	}
}

func TestFileGCExpiredUploadCannotPublish(t *testing.T) {
	db, repo, files := uploadFixture(t)
	old := mustUpload(t, repo, files, "same.txt", "old")
	files.afterPut = func() {
		if _, err := db.Exec(context.Background(), "UPDATE file_gc SET not_before = NOW() - INTERVAL '1 second'"); err != nil {
			t.Error(err)
		}
		collectAll(t, db, repository.NewPostgres(db), files)
	}
	_, err := (&svc{repo: repo, files: files}).Upload(context.Background(), strings.NewReader("new"), uploadMeta("same.txt"))
	if !errors.Is(err, repository.ErrUploadExpired) {
		t.Fatalf("expired upload published: %v", err)
	}
	assertStored(t, repo, files, old.ID, "old")
}

func TestFileGCConcurrentCollectorsProtectReferences(t *testing.T) {
	db, repo, files := uploadFixture(t)
	current := mustUpload(t, repo, files, "same.txt", "live")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, path := range []string{"abandoned-a", "abandoned-b", current.StoragePath} {
		if err := repo.ReserveUpload(ctx, path); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, "UPDATE file_gc SET not_before = NOW() - INTERVAL '1 second'"); err != nil {
		t.Fatal(err)
	}
	claimed := make(chan string, 2)
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	results := make(chan error, 2)
	for range 2 {
		go func() {
			ok, err := repository.NewPostgres(db).CollectFile(ctx, func(ctx context.Context, path string) error {
				claimed <- path
				select {
				case <-release:
					return files.Delete(ctx, path)
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			if err == nil && !ok {
				err = errors.New("collector skipped an available abandoned key")
			}
			results <- err
		}()
	}
	paths := make(map[string]bool)
	for range 2 {
		select {
		case path := <-claimed:
			if paths[path] || path == current.StoragePath {
				t.Fatal("collectors selected a locked or referenced key")
			}
			paths[path] = true
		case <-ctx.Done():
			t.Fatal("concurrent collectors did not claim independent keys")
		}
	}
	once.Do(func() { close(release) })
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	assertStored(t, repo, files, current.ID, "live")
	if ok, err := repo.CollectFile(ctx, files.Delete); ok || err != nil {
		t.Fatalf("GC selected a referenced key: %t %v", ok, err)
	}
}

func TestUploadLegacyPathAndHistoricalDuplicate(t *testing.T) {
	db, repo, files := uploadFixture(t)
	ctx := context.Background()
	// Existing items can share a legacy key. Historical duplicates must remain
	// readable, and GC cannot remove that key until the last reference is gone.
	_, err := db.Exec(ctx, `INSERT INTO items
		(id,type,file_name,content_type,size,storage_path,file_unique_id,file_unique_id_legacy_duplicate)
		VALUES ('owner','document','same.txt','text/plain',3,'legacy/same.txt',$1,false),
		       ('history','document','same.txt','text/plain',3,'legacy/same.txt',$1,true)`, uploadMeta("").FileUniqueID)
	if err != nil {
		t.Fatal(err)
	}
	files.objects["legacy/same.txt"] = []byte("old")
	current := mustUpload(t, repo, files, "same.txt", "new")
	if current.ID != "owner" {
		t.Fatal("re-upload selected a historical duplicate")
	}
	collectAll(t, db, repo, files)
	assertStored(t, repo, files, "owner", "new")
	assertStored(t, repo, files, "history", "old")
	if err := repo.Delete(ctx, "history"); err != nil {
		t.Fatal(err)
	}
	collectAll(t, db, repo, files)
	assertStored(t, repo, files, "owner", "new")
	if files.count() != 1 {
		t.Fatal("unreferenced legacy key was not collected")
	}
}
