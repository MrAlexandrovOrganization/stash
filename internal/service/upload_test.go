package service

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"stash/internal/filestore"
	"stash/internal/model"
	"stash/internal/repository"
)

type unavailableUploadRepo struct{ repository.Repository }

var errUploadDatabaseUnavailable = errors.New("synthetic database unavailable")

func (unavailableUploadRepo) ReserveUpload(context.Context, string) error {
	return errUploadDatabaseUnavailable
}

type untouchedUploadFiles struct {
	filestore.FileStore
	touched bool
}

func (f *untouchedUploadFiles) Put(context.Context, string, io.Reader, int64, string) error {
	f.touched = true
	return nil
}

func TestUploadReservationFailureDoesNotWriteStorage(t *testing.T) {
	files := &untouchedUploadFiles{}
	s := &svc{repo: unavailableUploadRepo{}, files: files}
	_, err := s.Upload(context.Background(), strings.NewReader("synthetic"), model.UploadMeta{})
	if !errors.Is(err, errUploadDatabaseUnavailable) {
		t.Fatalf("expected reservation error, got %v", err)
	}
	if files.touched {
		t.Fatal("storage was written without a durable reservation")
	}
}
