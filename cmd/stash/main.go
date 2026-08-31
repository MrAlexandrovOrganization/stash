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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgvectorpgx "github.com/pgvector/pgvector-go/pgx"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/semconv/v1.30.0"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"

	"stash/internal/config"
	clipembed "stash/internal/embed/clip"
	ollamaembed "stash/internal/embed/ollama"
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
	tracerProvider, err := initTracing(context.Background())
	if err != nil {
		slog.Error("tracing", "error", err)
		os.Exit(1)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tracerProvider.Shutdown(ctx); err != nil {
			slog.Error("tracing shutdown", "error", err)
		}
	}()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "error", err)
		os.Exit(1)
	}

	logx.Setup("stash", cfg.MinioAccessKey, cfg.MinioSecretKey)

	baseCfg, err := pgxpool.ParseConfig(cfg.PGURL)
	if err != nil {
		slog.Error("postgres config", "error", err)
		os.Exit(1)
	}

	// Migrations must run before we register the pgvector type codec: RegisterTypes
	// fails when the `vector` type does not exist yet, and it is only created by
	// migration 007. Use a temporary pool without the codec for migrations, then
	// build the runtime pool with the codec.
	migDB, err := pgxpool.NewWithConfig(context.Background(), baseCfg.Copy())
	if err != nil {
		slog.Error("postgres", "error", err)
		os.Exit(1)
	}
	if err := migrate.Run(context.Background(), migDB, migrations.FS); err != nil {
		slog.Error("migrations", "error", err)
		os.Exit(1)
	}
	migDB.Close()

	// Register the pgvector type codec on every connection so we can pass and
	// scan vector columns via the pgvector.Vector type.
	baseCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return pgvectorpgx.RegisterTypes(ctx, conn)
	}
	db, err := pgxpool.NewWithConfig(context.Background(), baseCfg)
	if err != nil {
		slog.Error("postgres", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := db.Ping(context.Background()); err != nil {
		slog.Error("postgres ping", "error", err)
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

	var te *ollamaembed.Client
	if cfg.OllamaURL != "" && cfg.OllamaEmbedModel != "" {
		te = ollamaembed.NewClient(cfg.OllamaURL, cfg.OllamaEmbedModel)
		slog.Info("text embedder enabled", "model", cfg.OllamaEmbedModel)
	}

	var ie *clipembed.Client
	if cfg.ClipEmbedderAddr != "" {
		cl, err := clipembed.NewClient(cfg.ClipEmbedderAddr)
		if err != nil {
			slog.Error("clip embedder client", "error", err)
			os.Exit(1)
		}
		defer cl.Close()
		ie = cl
		probeClip(cfg.ClipEmbedderAddr)
	}

	var svcOpts []service.Option
	if vp != nil {
		// Interval from env is optional; New() falls back to a 5m default.
		if d, err := time.ParseDuration(cfg.AIDescriptionBackfillInterval); err == nil && d > 0 {
			svcOpts = append(svcOpts, service.WithAIBackfill(d, cfg.AIDescriptionBackfillBatch))
		}
	}
	// Embedding backfill interval from env is optional; New() falls back to a 5m default.
	if d, err := time.ParseDuration(cfg.EmbeddingBackfillInterval); err == nil && d > 0 {
		svcOpts = append(svcOpts, service.WithEmbeddingBackfill(d, cfg.EmbeddingBackfillBatch))
	}
	if te != nil {
		svcOpts = append(svcOpts, service.WithTextEmbedder(te))
	}
	if ie != nil {
		svcOpts = append(svcOpts, service.WithImageEmbedder(ie))
	}

	svc := service.New(repo, fs, wc, vp, svcOpts...)
	h := handler.New(svc)

	mux := http.NewServeMux()
	h.Register(mux)

	slog.Info("starting", "addr", cfg.Addr)
	server := otelhttp.NewHandler(accessLog(mux), "stash")
	if err := http.ListenAndServe(cfg.Addr, server); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func initTracing(ctx context.Context) (*sdktrace.TracerProvider, error) {
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName("stash")))
	if err != nil {
		return nil, err
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res))
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	return provider, nil
}

type accessLogResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *accessLogResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *accessLogResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += n
	return n, err
}

func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		ww := &accessLogResponseWriter{ResponseWriter: w}
		next.ServeHTTP(ww, r)
		if ww.status == 0 {
			ww.status = http.StatusOK
		}
		slog.InfoContext(r.Context(), "http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.status,
			"bytes", ww.bytes,
			"duration_ms", time.Since(started).Milliseconds(),
		)
	})
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

// probeClip checks the clip-embedder gRPC health so a missing/unreachable
// embedding service is obvious in the logs at startup.
func probeClip(addr string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		slog.Warn("clip probe: dial", "addr", addr, "error", err)
		return
	}
	defer conn.Close()

	hc := grpc_health_v1.NewHealthClient(conn)
	resp, err := hc.Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: "embed.EmbedderService"})
	if err != nil {
		slog.Warn("clip probe: health", "addr", addr, "error", err)
		return
	}
	slog.Info("clip probe: health", "addr", addr, "status", resp.Status)
}
