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
	StatusAccepted    = pb.JobStatus_ACCEPTED
	StatusDownloading = pb.JobStatus_DOWNLOADING
	StatusQueued      = pb.JobStatus_QUEUED
	StatusRunning     = pb.JobStatus_RUNNING
	StatusDone        = pb.JobStatus_DONE
	StatusFailed      = pb.JobStatus_FAILED
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

func (c *Client) Submit(ctx context.Context, r io.Reader, format, requestID string) (jobID string, err error) {
	if requestID == "" {
		return "", fmt.Errorf("transcription request ID required")
	}
	ctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()

	stream, err := c.stub.Submit(ctx)
	if err != nil {
		return "", wrapErr(err)
	}
	if err := sendChunks(stream, r, format, requestID); err != nil {
		return "", err
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		return "", wrapErr(err)
	}
	if resp.JobId == "" {
		return "", fmt.Errorf("transcriber returned an empty job ID")
	}
	return resp.JobId, nil
}

func (c *Client) Ack(ctx context.Context, jobID string) error {
	ctx, cancel := context.WithTimeout(ctx, pollTimeout)
	defer cancel()
	resp, err := c.stub.Ack(ctx, &pb.AckRequest{JobId: jobID})
	if err != nil {
		return wrapErr(err)
	}
	if !resp.Acknowledged {
		return fmt.Errorf("transcriber did not acknowledge receipt")
	}
	return nil
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
}, r io.Reader, format, requestID string) error {
	// A replay may finish immediately after this header. Send can then return
	// EOF; CloseAndRecv still carries the original job ID (or the RPC error).
	if err := stream.Send(&pb.TranscribeChunk{
		Format: format, RequestId: requestID, RetainUntilAck: true,
	}); err != nil {
		if err == io.EOF {
			return nil
		}
		return wrapErr(err)
	}
	buf := make([]byte, chunkSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := &pb.TranscribeChunk{Data: buf[:n]}
			if sendErr := stream.Send(chunk); sendErr != nil {
				if sendErr == io.EOF {
					return nil
				}
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
