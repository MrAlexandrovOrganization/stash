package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"stash/internal/config"
	"stash/internal/filestore"
	"stash/internal/handler"
	"stash/internal/migrate"
	"stash/internal/repository"
	"stash/internal/service"
	"stash/internal/whisper"
	"stash/migrations"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "error", err)
		os.Exit(1)
	}

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

	svc := service.New(repo, fs, wc)
	h := handler.New(svc)

	mux := http.NewServeMux()
	h.Register(mux)

	slog.Info("starting", "addr", cfg.Addr)
	if err := http.ListenAndServe(cfg.Addr, mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
