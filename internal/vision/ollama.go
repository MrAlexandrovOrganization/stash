package vision

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Ollama is a vision.Provider backed by a local Ollama instance running a
// vision-capable model (e.g. llava, moondream, qwen2.5-vl).
type Ollama struct {
	baseURL string
	model   string
	prompt  string
	http    *http.Client
}

var _ Provider = (*Ollama)(nil)

func NewOllama(baseURL, model string) *Ollama {
	return &Ollama{
		baseURL: baseURL,
		model:   model,
		prompt:  "Опиши это изображение подробно: что изображено, объекты, люди, текст на картинке, атмосфера. Ответь одним абзацем.",
		http:    &http.Client{},
	}
}

// Describe sends the image to Ollama and returns the generated description.
func (c *Ollama) Describe(ctx context.Context, r io.Reader) (string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("read image: %w", err)
	}

	body, err := json.Marshal(map[string]any{
		"model":  c.model,
		"prompt": c.prompt,
		"images": []string{base64.StdEncoding.EncodeToString(data)},
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
		return "", fmt.Errorf("ollama status %d", resp.StatusCode)
	}

	var result struct {
		Response string `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("ollama decode: %w", err)
	}
	return result.Response, nil
}
