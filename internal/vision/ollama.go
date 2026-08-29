package vision

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	imagePrompt = "Опиши это изображение подробно: что изображено, объекты, люди, текст на картинке, атмосфера. Ответь одним абзацем."
	videoPrompt = "Перед тобой кадры одного видео в хронологическом порядке. Опиши подробно, что в нём происходит: объекты, люди, их действия, текст на экране, атмосфера. Ответь одним абзацем."
)

// Ollama is a vision.Provider backed by a local Ollama instance running a
// vision-capable model (e.g. llava, moondream, qwen2.5-vl).
type Ollama struct {
	baseURL string
	model   string
	numCtx  int
	http    *http.Client
}

var _ Provider = (*Ollama)(nil)

// NewOllama creates a provider for a local Ollama instance. numCtx sets the
// model context window (in tokens) sent via options.num_ctx; pass 0 to use
// Ollama's default. It must be large enough to hold the prompt plus all the
// base64 images, otherwise Ollama returns a 400 exceed_context_size error.
func NewOllama(baseURL, model string, numCtx int) *Ollama {
	return &Ollama{
		baseURL: baseURL,
		model:   model,
		numCtx:  numCtx,
		http:    &http.Client{},
	}
}

// Describe sends the frames to Ollama and returns the generated description.
func (c *Ollama) Describe(ctx context.Context, frames [][]byte) (string, error) {
	if len(frames) == 0 {
		return "", fmt.Errorf("no frames to describe")
	}

	images := make([]string, len(frames))
	for i, f := range frames {
		images[i] = base64.StdEncoding.EncodeToString(f)
	}
	prompt := imagePrompt
	if len(frames) > 1 {
		prompt = videoPrompt
	}

	reqBody := map[string]any{
		"model":  c.model,
		"prompt": prompt,
		"images": images,
		"stream": false,
	}
	if c.numCtx > 0 {
		reqBody["options"] = map[string]any{"num_ctx": c.numCtx}
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ollama status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		Response string `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("ollama decode: %w", err)
	}
	return result.Response, nil
}
