//go:build integration

package service

import (
	"fmt"
	"stash/internal/model"
	"testing"
)

func TestSearchPagingLanguageAndTags(t *testing.T) {
	db, repo, _ := uploadFixture(t)
	_, err := db.Exec(t.Context(), `INSERT INTO items(id,type,file_name,content_type,size,storage_path,description,tags,created_at)
 SELECT 'fixture-'||n,'image','photo-'||n||'.jpg','image/jpeg',1,'fixture/'||n,
 CASE WHEN n=1 THEN 'Рыжие коты спят на диване' ELSE 'ordinary unrelated media, который' END,
 CASE WHEN n=1 THEN ARRAY['Коты','Дом'] ELSE ARRAY['skip'] END,
 '2026-01-01'::timestamptz + n * interval '1 second' FROM generate_series(1,1128) n`)
	if err != nil {
		t.Fatal(err)
	}
	s := &svc{repo: repo}
	page, err := s.SearchPage(t.Context(), model.SearchQuery{Limit: 10, Offset: 1120})
	if err != nil || page.Total != 1128 || len(page.Items) != 8 {
		t.Fatalf("last page: %#v %v", page, err)
	}
	for _, q := range []model.SearchQuery{
		{Text: "диване котами"}, {Text: "photo-1.jpg"}, {Text: "кот"}, {Text: "кот", Tags: []string{"коты"}}, {ExcludeTags: []string{"SKIP"}},
	} {
		got, err := repo.Search(t.Context(), q)
		if err != nil || len(got) != 1 || got[0].ID != "fixture-1" {
			t.Fatalf("query %#v returned %d: %v", q, len(got), err)
		}
	}
	got, err := repo.Search(t.Context(), model.SearchQuery{Text: "%"})
	if err != nil || len(got) != 0 {
		t.Fatalf("wildcard became match-all: %v", err)
	}
	page, err = s.SearchPage(t.Context(), model.SearchQuery{Limit: 10, Offset: 2000})
	if err != nil || page.Total != 1128 || len(page.Items) != 0 {
		t.Fatalf("empty offset: %#v %v", page, err)
	}
}

func TestSimilarExcludesUnindexedAndRanksPartialVectors(t *testing.T) {
	db, repo, _ := uploadFixture(t)
	_, err := db.Exec(t.Context(), `INSERT INTO items(id,type,file_name,content_type,size,storage_path)
 SELECT 'pending-'||n,'video','pending.mp4','video/mp4',1,'fixture/'||n FROM generate_series(1,80) n`)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"image-only", "text-only", "both", "opposite"} {
		_, err = db.Exec(t.Context(), `INSERT INTO items(id,type,file_name,content_type,size,storage_path,tags) VALUES($1,'image','image.jpg','image/jpeg',1,$1,ARRAY['keep'])`, id)
		if err != nil {
			t.Fatal(err)
		}
	}
	image := make([]float32, 512)
	image[0] = 1
	text := make([]float32, 1024)
	text[0] = 1
	opposite := make([]float32, 512)
	opposite[0] = -1
	for _, id := range []string{"image-only", "both"} {
		if err := repo.SetImageEmbedding(t.Context(), id, image); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"text-only", "both"} {
		if err := repo.SetTextEmbedding(t.Context(), id, text); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.SetImageEmbedding(t.Context(), "opposite", opposite); err != nil {
		t.Fatal(err)
	}
	for _, q := range []model.SimilarQuery{
		{ImageVector: image, ImageWeight: 1}, {TextVector: text, TextWeight: 1},
		{TextVector: text, ImageVector: image, TextWeight: 1, ImageWeight: 1},
	} {
		got, err := repo.Similar(t.Context(), q)
		want := 2
		if len(q.TextVector) > 0 && len(q.ImageVector) > 0 {
			want = 3
		}
		if err != nil || len(got) != want {
			t.Fatalf("got %d, want %d: %v", len(got), want, err)
		}
		for _, item := range got {
			if item.ID == "opposite" || len(item.ID) >= 8 && item.ID[:8] == "pending-" {
				t.Fatal(fmt.Sprint("unrankable item: ", item.ID))
			}
		}
		q.ExcludeTags = []string{"KEEP"}
		got, err = repo.Similar(t.Context(), q)
		if err != nil || len(got) != 0 {
			t.Fatalf("excluded tags leaked: %v", err)
		}
	}
	vector, err := repo.GetEmbedding(t.Context(), "pending-1", model.EmbeddingKindImage)
	if err != nil || len(vector) != 0 {
		t.Fatalf("NULL vector read: %v", err)
	}
}
