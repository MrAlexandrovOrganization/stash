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

	// AIDescriptionBackfillInterval controls how often the background worker
	// scans for items without an AI description and generates them.
	// Accepts a Go duration string (e.g. "5m"). Empty disables the worker.
	AIDescriptionBackfillInterval string
	// AIDescriptionBackfillBatch is how many items are processed per scan.
	AIDescriptionBackfillBatch int
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

		AIDescriptionBackfillInterval: getenv("AI_DESCRIPTION_BACKFILL_INTERVAL", "5m"),
		AIDescriptionBackfillBatch:    atoiDefault(getenv("AI_DESCRIPTION_BACKFILL_BATCH", "5"), 5),
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
