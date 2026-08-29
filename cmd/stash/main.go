package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"stash/internal/config"
	"stash/internal/filestore"
	"stash/internal/handler"
	"stash/internal/logx"
	"stash/internal/migrate"
	"stash/internal/repository"
	"stash/internal/service"
	"stash/internal/vision"
	"stash/internal/whisper"
	"stash/migrations"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "error", err)
		os.Exit(1)
	}

	logx.Setup("stash", cfg.MinioAccessKey, cfg.MinioSecretKey)

	db, err := pgxpool.New(context.Background(), cfg.PGURL)
	if err != nil {
		slog.Error("postgres", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := db.Ping(context.Background()); err != nil {
		slog.Error("postgres ping", "error", err)
		os.Exit(1)
	}

	if err := migrate.Run(context.Background(), db, migrations.FS); err != nil {
		slog.Error("migrations", "error", err)
		os.Exit(1)
	}

	fs, err := filestore.NewMinio(cfg.MinioEndpoint, cfg.MinioAccessKey, cfg.MinioSecretKey, cfg.MinioUseSSL)
	if err != nil {
		slog.Error("minio", "error", err)
		os.Exit(1)
	}

	repo := repository.NewPostgres(db)

	var wc *whisper.Client
	if cfg.WhisperHost != "" {
		wc, err = whisper.NewClient(cfg.WhisperHost, cfg.WhisperPort)
		if err != nil {
			slog.Error("whisper client", "error", err)
			os.Exit(1)
		}
		defer wc.Close()
		slog.Info("whisper connected", "host", cfg.WhisperHost, "port", cfg.WhisperPort)
	}

	var vp vision.Provider
	if cfg.OllamaURL != "" {
		vp = vision.NewOllama(cfg.OllamaURL, cfg.OllamaModel, cfg.OllamaNumCtx)
		slog.Info("vision provider enabled", "url", cfg.OllamaURL, "model", cfg.OllamaModel, "num_ctx", cfg.OllamaNumCtx)
		probeOllama(cfg.OllamaURL)
	}

	var svcOpts []service.Option
	if vp != nil {
		// Interval from env is optional; New() falls back to a 5m default.
		if d, err := time.ParseDuration(cfg.AIDescriptionBackfillInterval); err == nil && d > 0 {
			svcOpts = append(svcOpts, service.WithAIBackfill(d, cfg.AIDescriptionBackfillBatch))
		}
	}

	svc := service.New(repo, fs, wc, vp, svcOpts...)
	h := handler.New(svc)

	mux := http.NewServeMux()
	h.Register(mux)

	slog.Info("starting", "addr", cfg.Addr)
	if err := http.ListenAndServe(cfg.Addr, mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// probeOllama logs model availability at startup so a missing/unreachable
// vision model is obvious in the logs instead of failing silently per request.
func probeOllama(baseURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/tags", nil)
	if err != nil {
		slog.Warn("ollama probe: build request", "error", err)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("ollama probe: unreachable", "url", baseURL, "error", err)
		return
	}
	defer resp.Body.Close()

	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		slog.Warn("ollama probe: decode", "error", err)
		return
	}
	names := make([]string, 0, len(out.Models))
	for _, m := range out.Models {
		names = append(names, m.Name)
	}
	slog.Info("ollama probe: models", "models", strings.Join(names, ", "))
}
