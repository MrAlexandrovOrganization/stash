package whisper

import (
	"context"
	"fmt"
	"io"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	pb "stash/gen/whisper"
)

const (
	chunkSize     = 1 * 1024 * 1024
	uploadTimeout = 5 * time.Minute
	pollTimeout   = 10 * time.Second
)

type JobStatus = pb.JobStatus

const (
	StatusPending = pb.JobStatus_PENDING
	StatusRunning = pb.JobStatus_RUNNING
	StatusDone    = pb.JobStatus_DONE
	StatusFailed  = pb.JobStatus_FAILED
)

type JobResult struct {
	Status JobStatus
	Text   string
	Error  string
}

type Client struct {
	conn *grpc.ClientConn
	stub pb.TranscriptionServiceClient
}

func NewClient(host, port string) (*Client, error) {
	conn, err := grpc.NewClient(
		fmt.Sprintf("%s:%s", host, port),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("grpc dial: %w", err)
	}
	return &Client{conn: conn, stub: pb.NewTranscriptionServiceClient(conn)}, nil
}

func (c *Client) Close() error {
	return c.conn.Close()
}

func (c *Client) Submit(ctx context.Context, r io.Reader, format string) (jobID string, err error) {
	ctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()

	stream, err := c.stub.Submit(ctx)
	if err != nil {
		return "", wrapErr(err)
	}
	if err := sendChunks(stream, r, format); err != nil {
		return "", err
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		return "", wrapErr(err)
	}
	return resp.JobId, nil
}

func (c *Client) GetStatus(ctx context.Context, jobID string) (*JobResult, error) {
	ctx, cancel := context.WithTimeout(ctx, pollTimeout)
	defer cancel()

	resp, err := c.stub.GetStatus(ctx, &pb.StatusRequest{JobId: jobID})
	if err != nil {
		return nil, wrapErr(err)
	}
	return &JobResult{
		Status: resp.Status,
		Text:   resp.Text,
		Error:  resp.Error,
	}, nil
}

func sendChunks(stream interface {
	Send(*pb.TranscribeChunk) error
}, r io.Reader, format string) error {
	buf := make([]byte, chunkSize)
	first := true
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := &pb.TranscribeChunk{Data: buf[:n]}
			if first {
				chunk.Format = format
				first = false
			}
			if sendErr := stream.Send(chunk); sendErr != nil {
				return wrapErr(sendErr)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read chunk: %w", err)
		}
	}
	return nil
}

func wrapErr(err error) error {
	st, _ := status.FromError(err)
	if st.Code() == codes.Unavailable || st.Code() == codes.DeadlineExceeded {
		return fmt.Errorf("whisper unavailable: %w", err)
	}
	return err
}
