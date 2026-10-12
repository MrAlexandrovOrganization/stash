package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"stash/internal/logx"
	"stash/internal/processing"
	"syscall"
)

func main() {
	logx.Setup("stash-worker")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c := processing.WorkerConfig{Endpoint: os.Getenv("WORKER_ENDPOINT"), Transport: os.Getenv("WORKER_TRANSPORT"), ID: os.Getenv("WORKER_ID"), TokenFile: os.Getenv("WORKER_TOKEN_FILE"), OllamaURL: os.Getenv("OLLAMA_URL"), VisionModel: os.Getenv("OLLAMA_MODEL"), TextModel: os.Getenv("OLLAMA_EMBED_MODEL")}
	if err := processing.RunWorker(ctx, c); err != nil {
		slog.Error("worker failed", "error", err)
		os.Exit(1)
	}
}
