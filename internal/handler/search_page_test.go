package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"stash/internal/model"
	"stash/internal/service"
	"testing"
)

type pageService struct {
	service.Service
	query  model.SearchQuery
	called bool
}

func (s *pageService) SearchPage(_ context.Context, q model.SearchQuery) (*model.SearchPage, error) {
	s.called = true
	s.query = q
	return &model.SearchPage{Items: []*model.Item{}, Total: 1128}, nil
}
func TestSearchPageContractAndValidation(t *testing.T) {
	for _, suffix := range []string{"?limit=10&offset=1120&q=кот&tags=Дом&exclude_tags=skip", "?limit=-1", "?limit=0", "?limit=101", "?offset=-1", "?offset=oops"} {
		t.Run(suffix, func(t *testing.T) {
			svc := &pageService{}
			mux := http.NewServeMux()
			New(svc).Register(mux)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest("GET", "/items/page"+suffix, nil))
			if suffix != "?limit=10&offset=1120&q=кот&tags=Дом&exclude_tags=skip" {
				if w.Code != 400 || svc.called {
					t.Fatalf("invalid request accepted: %d", w.Code)
				}
				return
			}
			var page model.SearchPage
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Total != 1128 || page.Items == nil {
				t.Fatalf("bad page: %d %s", w.Code, w.Body)
			}
			if svc.query.Offset != 1120 || len(svc.query.ExcludeTags) != 1 || len(svc.query.Tags) != 1 {
				t.Fatalf("filters lost: %#v", svc.query)
			}
		})
	}
}
