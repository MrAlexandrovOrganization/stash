package handler

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"stash/internal/model"
	"stash/internal/service"
)

type listService struct {
	service.Service
	items []*model.Item
}

func (s listService) Search(context.Context, model.SearchQuery) ([]*model.Item, error) {
	return s.items, nil
}

func (s listService) Similar(context.Context, model.SimilarRequest) ([]*model.Item, error) {
	return s.items, nil
}

func listMux(items []*model.Item) *http.ServeMux {
	mux := http.NewServeMux()
	New(listService{items: items}).Register(mux)
	return mux
}

func TestListEncodingNegotiation(t *testing.T) {
	items := []*model.Item{{ID: "synthetic-item", Description: "Тест <описания>"}}
	for _, path := range []string{"/items", "/similar?q=test"} {
		for _, tc := range []struct {
			name    string
			headers []string
			gzip    bool
		}{
			{name: "identity"},
			{name: "gzip", headers: []string{"gzip"}, gzip: true},
			{name: "mixed", headers: []string{"br, GZip; q=0.5"}, gzip: true},
			{name: "multiple headers", headers: []string{"br", "gzip"}, gzip: true},
			{name: "disabled", headers: []string{"gzip;q=0, br"}},
			{name: "unsupported", headers: []string{"br"}},
			{name: "malformed quality", headers: []string{"gzip;q=oops"}},
			{name: "invalid quality", headers: []string{"gzip;q=2"}},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				for _, value := range tc.headers {
					req.Header.Add("Accept-Encoding", value)
				}
				w := httptest.NewRecorder()
				listMux(items).ServeHTTP(w, req)
				if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
					t.Fatalf("status=%d content-type=%q", w.Code, w.Header().Get("Content-Type"))
				}
				if w.Header().Get("Vary") != "Accept-Encoding" {
					t.Fatal("missing encoding cache variation")
				}
				if got := w.Header().Get("Content-Encoding") == "gzip"; got != tc.gzip {
					t.Fatalf("gzip=%v, want %v", got, tc.gzip)
				}
				var body io.Reader = w.Body
				if tc.gzip {
					zr, err := gzip.NewReader(body)
					if err != nil {
						t.Fatal(err)
					}
					defer zr.Close()
					body = zr
				}
				var got []*model.Item
				if err := json.NewDecoder(body).Decode(&got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, items) {
					t.Fatalf("list changed after encoding: %#v", got)
				}
			})
		}
	}
}

func TestStorageListCompressionAndGoClient(t *testing.T) {
	items := make([]*model.Item, 1000)
	for i := range items {
		items[i] = &model.Item{
			ID:          fmt.Sprintf("synthetic-%04d", i),
			Description: strings.Repeat("Synthetic description of stored media. ", 25),
		}
	}
	mux := listMux(items)
	plain := httptest.NewRecorder()
	mux.ServeHTTP(plain, httptest.NewRequest(http.MethodGet, "/items", nil))
	compressed := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	mux.ServeHTTP(compressed, req)
	if compressed.Body.Len()*4 >= plain.Body.Len() {
		t.Fatalf("list not substantially smaller: gzip=%d identity=%d", compressed.Body.Len(), plain.Body.Len())
	}
	t.Logf("list response: identity=%d bytes, gzip=%d bytes", plain.Body.Len(), compressed.Body.Len())

	// Exercise the same transparent negotiation/decompression that stash-bot
	// uses, including reading the gzip trailer at EOF.
	server := httptest.NewServer(mux)
	defer server.Close()
	resp, err := server.Client().Get(server.URL + "/items")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !resp.Uncompressed {
		t.Fatal("standard Go client did not negotiate gzip")
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var got []*model.Item
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, items) {
		t.Fatal("standard Go client received a different list")
	}
}
