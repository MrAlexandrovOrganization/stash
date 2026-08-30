package config

import (
	"fmt"
	"os"
)

type Config struct {
	Addr string

	PGURL string

	MinioEndpoint  string
	MinioAccessKey string
	MinioSecretKey string
	MinioUseSSL    bool

	WhisperHost string
	WhisperPort string

	OllamaURL   string
	OllamaModel string
	// OllamaNumCtx sets the vision model context window (tokens) sent to
	// Ollama via options.num_ctx. 0 leaves Ollama's default (often 4096,
	// which is too small for multi-frame/video requests).
	OllamaNumCtx int

	// OllamaEmbedModel is the Ollama text-embedding model used for the textual
	// embedding of item descriptions (e.g. "bge-m3"). Empty disables text
	// embeddings.
	OllamaEmbedModel string
	// ClipEmbedderAddr is the host:port of the external clip-embedder gRPC
	// microservice (open_clip). Empty disables image embeddings.
	ClipEmbedderAddr string

	// AIDescriptionBackfillInterval controls how often the background worker
	// scans for items without an AI description and generates them.
	// Accepts a Go duration string (e.g. "5m"). Empty disables the worker.
	AIDescriptionBackfillInterval string
	// AIDescriptionBackfillBatch is how many items are processed per scan.
	AIDescriptionBackfillBatch int

	// EmbeddingBackfillInterval controls how often the background worker scans
	// for items missing an embedding and fills them. Accepts a Go duration
	// string (e.g. "5m"). Empty leaves the in-code default (5m).
	EmbeddingBackfillInterval string
	// EmbeddingBackfillBatch is how many missing items are processed per scan.
	EmbeddingBackfillBatch int
}

func Load() (*Config, error) {
	cfg := &Config{
		Addr:           getenv("ADDR", ":8080"),
		PGURL:          getenv("POSTGRES_URL", "postgres://stash:stash@localhost:5432/stash?sslmode=disable"),
		MinioEndpoint:  getenv("MINIO_ENDPOINT", "localhost:9000"),
		MinioAccessKey: getenv("MINIO_ACCESS_KEY", ""),
		MinioSecretKey: getenv("MINIO_SECRET_KEY", ""),
		MinioUseSSL:    getenv("MINIO_USE_SSL", "false") == "true",
		WhisperHost:    getenv("WHISPER_HOST", ""),
		WhisperPort:    getenv("WHISPER_PORT", "50053"),
		OllamaURL:      getenv("OLLAMA_URL", ""),
		OllamaModel:    getenv("OLLAMA_MODEL", "llava"),
		OllamaNumCtx:   atoiDefault(getenv("OLLAMA_NUM_CTX", "16384"), 16384),

		OllamaEmbedModel: getenv("OLLAMA_EMBED_MODEL", "bge-m3"),
		ClipEmbedderAddr: getenv("CLIP_EMBEDDER_ADDR", ""),

		AIDescriptionBackfillInterval: getenv("AI_DESCRIPTION_BACKFILL_INTERVAL", "5m"),
		AIDescriptionBackfillBatch:    atoiDefault(getenv("AI_DESCRIPTION_BACKFILL_BATCH", "5"), 5),

		EmbeddingBackfillInterval: getenv("EMBEDDING_BACKFILL_INTERVAL", ""),
		EmbeddingBackfillBatch:    atoiDefault(getenv("EMBEDDING_BACKFILL_BATCH", "5"), 5),
	}

	if cfg.MinioAccessKey == "" {
		return nil, fmt.Errorf("MINIO_ACCESS_KEY is required")
	}
	if cfg.MinioSecretKey == "" {
		return nil, fmt.Errorf("MINIO_SECRET_KEY is required")
	}

	return cfg, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func atoiDefault(s string, def int) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	if n <= 0 {
		return def
	}
	return n
}
