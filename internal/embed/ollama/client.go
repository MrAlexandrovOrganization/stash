// Package ollama implements the embed.Embedder port against a local Ollama
// instance's /api/embed endpoint. It is used for text embeddings of item
// descriptions (multilingual models such as bge-m3). Image embedding is not
// supported by Ollama's text embedding models, so EmbedImage returns
// embed.ErrUnsupported.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"stash/internal/embed"
)

const embedEndpoint = "/api/embed"

// Client embeds text via a local Ollama instance.
type Client struct {
	baseURL string
	model   string
	http    *http.Client
}

// NewClient creates a text-embedding client for the given Ollama base URL and
// model name (e.g. "bge-m3").
func NewClient(baseURL, model string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		http:    &http.Client{},
	}
}

func (c *Client) EmbedText(ctx context.Context, text string) ([]float32, error) {
	reqBody := map[string]any{"model": c.model, "input": text}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+embedEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama embed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama embed status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("ollama embed decode: %w", err)
	}
	if len(out.Embeddings) == 0 {
		return nil, fmt.Errorf("ollama embed: empty embeddings")
	}
	return out.Embeddings[0], nil
}

// EmbedImage is unsupported for Ollama text-embedding models.
func (c *Client) EmbedImage(ctx context.Context, data []byte) ([]float32, error) {
	return nil, embed.ErrUnsupported
}
