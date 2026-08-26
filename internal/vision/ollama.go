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
	http    *http.Client
}

var _ Provider = (*Ollama)(nil)

func NewOllama(baseURL, model string) *Ollama {
	return &Ollama{
		baseURL: baseURL,
		model:   model,
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

	body, err := json.Marshal(map[string]any{
		"model":  c.model,
		"prompt": prompt,
		"images": images,
		"stream": false,
	})
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
