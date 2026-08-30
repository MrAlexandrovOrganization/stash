// Package clip implements the embed.Embedder port against the external
// clip-embedder gRPC microservice (open_clip). The service layer depends only
// on the embed.Embedder interface, never on this transport.
package clip

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	pb "stash/gen/embed"
)

// Client is a gRPC client for the clip-embedder Embedder service.
type Client struct {
	conn *grpc.ClientConn
	stub pb.EmbedderServiceClient
}

// NewClient dials the clip-embedder service at addr (host:port).
func NewClient(addr string) (*Client, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("clip embedder: grpc dial %q: %w", addr, err)
	}
	return &Client{conn: conn, stub: pb.NewEmbedderServiceClient(conn)}, nil
}

// Close releases the underlying connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

func (c *Client) EmbedText(ctx context.Context, text string) ([]float32, error) {
	resp, err := c.stub.EmbedText(ctx, &pb.EmbedTextRequest{Text: text})
	if err != nil {
		return nil, wrapErr(err)
	}
	return resp.Vector, nil
}

func (c *Client) EmbedImage(ctx context.Context, data []byte) ([]float32, error) {
	resp, err := c.stub.EmbedImage(ctx, &pb.EmbedImageRequest{Image: data})
	if err != nil {
		return nil, wrapErr(err)
	}
	return resp.Vector, nil
}

func wrapErr(err error) error {
	st, _ := status.FromError(err)
	if st.Code() == codes.Unavailable {
		return fmt.Errorf("clip embedder unavailable: %w", err)
	}
	return err
}
