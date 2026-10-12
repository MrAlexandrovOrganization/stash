package processing

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	pb "stash/gen/processing"
	ollamaembed "stash/internal/embed/ollama"
	"stash/internal/vision"
)

type WorkerConfig struct {
	Endpoint, Transport, ID, TokenFile, OllamaURL, VisionModel, TextModel string
	NumCtx                                                                int
}

func (c WorkerConfig) validate() error {
	if c.Endpoint == "" || !identityPattern.MatchString(c.ID) || c.OllamaURL == "" || c.VisionModel == "" || c.TextModel == "" {
		return errors.New("invalid worker configuration")
	}
	return nil
}
func modelsReady(ctx context.Context, c WorkerConfig) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.OllamaURL, "/")+"/api/tags", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false
	}
	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if json.NewDecoder(resp.Body).Decode(&result) != nil {
		return false
	}
	models := map[string]bool{}
	for _, model := range result.Models {
		models[model.Name] = true
	}
	return models[c.VisionModel] && models[c.TextModel]
}
func RunWorker(ctx context.Context, c WorkerConfig) error {
	if err := c.validate(); err != nil {
		return err
	}
	raw, err := os.ReadFile(c.TokenFile)
	if err != nil {
		return errors.New("worker token unavailable")
	}
	token := strings.TrimSpace(string(raw))
	if len(token) < 32 || len(token) > 512 {
		return errors.New("invalid worker token")
	}
	transport := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	if c.Transport == "internal" {
		if c.Endpoint != "stash:50058" {
			return errors.New("plaintext allowed only inside server compose network")
		}
		transport = insecure.NewCredentials()
	} else if c.Transport != "tls" {
		return errors.New("invalid worker transport")
	}
	conn, err := grpc.NewClient(c.Endpoint, grpc.WithTransportCredentials(transport), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(20*1024*1024), grpc.MaxCallSendMsgSize(20*1024*1024)))
	if err != nil {
		return errors.New("worker connection configuration invalid")
	}
	defer conn.Close()
	client := pb.NewWorkerServiceClient(conn)
	instance := uuid.NewString()
	base := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	var draining, busy atomic.Bool
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGUSR1, syscall.SIGUSR2)
	defer signal.Stop(signals)
	os.Remove("/tmp/worker-ready")
	os.Remove("/tmp/worker-drained")
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case sig := <-signals:
				if sig == syscall.SIGUSR1 {
					draining.Store(true)
					os.Remove("/tmp/worker-ready")
					if !busy.Load() {
						os.WriteFile("/tmp/worker-drained", nil, 0600)
					}
				} else {
					os.Remove("/tmp/worker-drained")
					draining.Store(false)
				}
			}
		}
	}()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			check, cancel := context.WithTimeout(base, 10*time.Second)
			ready := modelsReady(check, c) && !draining.Load()
			cancel()
			report, cancel := context.WithTimeout(base, 10*time.Second)
			_, err := client.Report(report, &pb.ReportRequest{WorkerId: c.ID, InstanceId: instance, Ready: ready, VisionModel: c.VisionModel, TextModel: c.TextModel})
			cancel()
			if err != nil {
				os.Remove("/tmp/worker-ready")
				slog.Warn("worker report failed", "worker_id", c.ID)
			} else if ready {
				os.WriteFile("/tmp/worker-ready", nil, 0600)
			} else {
				os.Remove("/tmp/worker-ready")
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	defer func() {
		wg.Wait()
		withdraw, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		withdraw = metadata.AppendToOutgoingContext(withdraw, "authorization", "Bearer "+token)
		client.Report(withdraw, &pb.ReportRequest{WorkerId: c.ID, InstanceId: instance, Ready: false, VisionModel: c.VisionModel, TextModel: c.TextModel})
	}()
	slog.Info("worker started", "worker_id", c.ID, "vision_model", c.VisionModel, "text_model", c.TextModel)
	for ctx.Err() == nil {
		if draining.Load() {
			os.WriteFile("/tmp/worker-drained", nil, 0600)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}
		busy.Store(true)
		claim, cancel := context.WithTimeout(base, 10*time.Second)
		job, err := client.Claim(claim, &pb.ClaimRequest{WorkerId: c.ID, InstanceId: instance})
		cancel()
		if err != nil || job.Lease == nil {
			busy.Store(false)
			if err != nil {
				slog.Warn("worker claim failed", "worker_id", c.ID)
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(5 * time.Second):
			}
			continue
		}
		process(ctx, base, client, c, job)
		busy.Store(false)
		if draining.Load() {
			os.WriteFile("/tmp/worker-drained", nil, 0600)
		}
	}
	return nil
}
func process(parent, base context.Context, client pb.WorkerServiceClient, c WorkerConfig, job *pb.ClaimResponse) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Minute)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				heartbeat, stop := context.WithTimeout(base, 10*time.Second)
				reply, err := client.Heartbeat(heartbeat, job.Lease)
				stop()
				if err == nil && !reply.Accepted {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); wg.Wait() }()
	started := time.Now()
	result := &pb.Result{Model: job.Task.Model}
	var err error
	switch job.Task.Kind {
	case "vision":
		if job.Task.Model != c.VisionModel {
			err = errors.New("incompatible model")
			break
		}
		result.Description, err = vision.NewOllama(c.OllamaURL, c.VisionModel, int(job.Task.NumCtx)).Describe(ctx, job.Task.Frames)
	case "text":
		if job.Task.Model != c.TextModel {
			err = errors.New("incompatible model")
			break
		}
		result.Vector, err = ollamaembed.NewClient(c.OllamaURL, c.TextModel).EmbedText(ctx, job.Task.Text)
	default:
		err = errors.New("unsupported task kind")
	}
	if err != nil || strings.TrimSpace(result.Description) == "" && len(result.Vector) == 0 {
		failure, stop := context.WithTimeout(base, 10*time.Second)
		defer stop()
		client.Fail(failure, &pb.FailRequest{Lease: job.Lease, Permanent: false})
		slog.Warn("processing attempt failed", "worker_id", c.ID, "kind", job.Task.Kind)
		return
	}
	for attempt := 0; attempt < 6 && ctx.Err() == nil; attempt++ {
		complete, stop := context.WithTimeout(base, 10*time.Second)
		reply, err := client.Complete(complete, &pb.CompleteRequest{Lease: job.Lease, Result: result})
		stop()
		if err == nil {
			slog.Info("processing attempt complete", "worker_id", c.ID, "kind", job.Task.Kind, "accepted", reply.Accepted, "duration_seconds", time.Since(started).Seconds())
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}
