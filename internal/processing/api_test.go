package processing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestScopedWorkerCredentials(t *testing.T) {
	token := "synthetic-token-for-worker-one-only"
	sum := sha256.Sum256([]byte(token))
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"workers":{"one":"`+hex.EncodeToString(sum[:])+`"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	api, err := NewAPI(nil, path)
	if err != nil {
		t.Fatal(err)
	}
	instance := "00000000-0000-4000-8000-000000000001"
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
	if err := api.authorize(ctx, "one", instance); err != nil {
		t.Fatal(err)
	}
	if code := status.Code(api.authorize(ctx, "two", instance)); code != codes.Unauthenticated {
		t.Fatalf("wrong worker: %v", code)
	}
	if code := status.Code(api.authorize(context.Background(), "one", instance)); code != codes.Unauthenticated {
		t.Fatalf("missing token: %v", code)
	}
	if code := status.Code(api.authorize(ctx, "one", "invalid")); code != codes.InvalidArgument {
		t.Fatalf("invalid instance: %v", code)
	}
}

func TestWorkerTransportConfiguration(t *testing.T) {
	c := WorkerConfig{Endpoint: "example.com:443", Transport: "tls", ID: "one", TokenFile: "/synthetic/token", OllamaURL: "http://ollama:11434", VisionModel: "vision", TextModel: "text"}
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	for _, transport := range []string{"", "ssh", "typo", "internal"} {
		c.Transport = transport
		if err := c.validate(); err == nil {
			t.Errorf("accepted transport %q", transport)
		}
	}
	c.Transport = "internal"
	c.Endpoint = "stash:50058"
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
}
