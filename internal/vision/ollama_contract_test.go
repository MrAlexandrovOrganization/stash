package vision

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestOllamaDescribeAgainstAPIContract verifies the client speaks the Ollama
// /api/generate protocol: it must POST base64 images + prompt and parse the
// "response" field. We mimic Ollama so no GPU/model is required.
func TestOllamaDescribeAgainstAPIContract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		imgs, _ := payload["images"].([]any)
		if len(imgs) != 1 {
			t.Errorf("expected 1 image, got %d", len(imgs))
		}
		if _, ok := imgs[0].(string); !ok {
			t.Errorf("image must be base64 string")
		}
		if payload["stream"] != false {
			t.Errorf("stream must be false")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":    payload["model"],
			"response": "На изображении кот, сидящий на подоконнике.",
			"done":     true,
		})
	}))
	defer srv.Close()

	c := NewOllama(srv.URL, "qwen2.5vl:3b")
	got, err := c.Describe(context.Background(), io.NopCloser(stringReader("fake-bytes")))
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if got != "На изображении кот, сидящий на подоконнике." {
		t.Fatalf("unexpected description: %q", got)
	}
}

type stringReader string

func (s stringReader) Read(p []byte) (int, error) {
	n := copy(p, s)
	if n == len(s) {
		return n, io.EOF
	}
	return n, nil
}
