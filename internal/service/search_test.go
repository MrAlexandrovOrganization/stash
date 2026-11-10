package service

import (
	"context"
	"errors"
	"stash/internal/model"
	"stash/internal/repository"
	"testing"
)

type searchEmbedder struct {
	err    error
	vector []float32
}

func (e searchEmbedder) EmbedText(context.Context, string) ([]float32, error) { return e.vector, e.err }
func (e searchEmbedder) EmbedImage(context.Context, []byte) ([]float32, error) {
	return e.vector, e.err
}

type searchRepo struct {
	repository.Repository
	query  model.SimilarQuery
	called bool
}

func (r *searchRepo) Similar(_ context.Context, q model.SimilarQuery) ([]*model.Item, error) {
	r.query = q
	r.called = true
	return []*model.Item{{ID: "match"}}, nil
}
func (r *searchRepo) GetEmbedding(context.Context, string, model.EmbeddingKind) ([]float32, error) {
	return nil, nil
}

func TestSimilarSurvivesOneUnavailableProvider(t *testing.T) {
	for _, failed := range []string{"text", "image", "both"} {
		t.Run(failed, func(t *testing.T) {
			repo := &searchRepo{}
			text, image := searchEmbedder{vector: []float32{1}}, searchEmbedder{vector: []float32{2}}
			if failed == "text" || failed == "both" {
				text.err = errors.New("synthetic failure")
			}
			if failed == "image" || failed == "both" {
				image.err = errors.New("synthetic failure")
			}
			s := &svc{repo: repo, textEmbedder: text, imageEmbedder: image}
			got, err := s.Similar(t.Context(), model.SimilarRequest{Text: "query", Tags: []string{"keep"}, ExcludeTags: []string{"skip"}})
			if failed == "both" {
				if !errors.Is(err, ErrSearchUnavailable) || repo.called {
					t.Fatalf("both failed: %v", err)
				}
				return
			}
			if err != nil || len(got) != 1 || len(repo.query.Tags) != 1 || len(repo.query.ExcludeTags) != 1 {
				t.Fatalf("partial search failed: %v", err)
			}
			if (len(repo.query.TextVector) == 0) != (failed == "text") || (len(repo.query.ImageVector) == 0) != (failed == "image") {
				t.Fatal("failed vector used")
			}
		})
	}
}

func TestSimilarWithoutItemVectorReturnsPending(t *testing.T) {
	s := &svc{repo: &searchRepo{}}
	_, err := s.Similar(t.Context(), model.SimilarRequest{ItemID: "pending"})
	if !errors.Is(err, ErrIndexPending) {
		t.Fatal(err)
	}
}

func TestSimilarHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s := &svc{repo: &searchRepo{}, textEmbedder: searchEmbedder{vector: []float32{1}}}
	_, err := s.Similar(ctx, model.SimilarRequest{Text: "query"})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
