package processing

import (
	"context"
	"errors"
	"io"
	"mime"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	pb "stash/gen/processing"
	"stash/internal/media"
)

const maxMediaBytes int64 = 512 * 1024 * 1024
const maxFrameBytes = 16 * 1024 * 1024

// DownloadMedia serves only the object referenced by the caller's active lease.
// Object-store credentials and URLs never leave the coordinator. Large inputs
// are streamed in bounded chunks, independent of the unary RPC message limit.
func (a *API) DownloadMedia(l *pb.LeaseRequest, stream grpc.ServerStreamingServer[pb.MediaChunk]) error {
	ctx, cancel := context.WithTimeout(stream.Context(), 10*time.Minute)
	defer cancel()
	if err := a.authorize(ctx, l.WorkerId, l.InstanceId); err != nil {
		return err
	}
	load := func() ([]byte, error) {
		var payload []byte
		err := a.Queue.DB.QueryRow(ctx, sql("media_owned"), l.JobId, l.WorkerId, l.InstanceId, l.LeaseId).Scan(&payload)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, status.Error(codes.PermissionDenied, "media lease unavailable")
		}
		if err != nil {
			return nil, status.Error(codes.Unavailable, "queue unavailable")
		}
		return payload, nil
	}
	payload, err := load()
	if err != nil {
		return err
	}
	task := new(pb.Task)
	if proto.Unmarshal(payload, task) != nil || task.MediaPath == "" || (task.Kind != "vision" && task.Kind != "frames") {
		return status.Error(codes.InvalidArgument, "task has no media input")
	}
	if a.Media == nil {
		return status.Error(codes.Unavailable, "media store unavailable")
	}
	input, err := a.Media.Get(ctx, task.MediaPath)
	if err != nil {
		return status.Error(codes.Unavailable, "media unavailable")
	}
	defer input.Close()
	// Cancellation interrupts storage reads. The worker also bounds the RPC deadline.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			input.Close()
		case <-done:
		}
	}()
	defer close(done)
	buffer := make([]byte, 64*1024)
	var total int64
	checked := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			return status.Error(codes.DeadlineExceeded, "media download cancelled")
		}
		if time.Since(checked) > 20*time.Second {
			if _, err := load(); err != nil {
				return err
			}
			checked = time.Now()
		}
		n, err := input.Read(buffer)
		total += int64(n)
		if total > maxMediaBytes {
			return status.Error(codes.ResourceExhausted, "media input exceeds limit")
		}
		if n > 0 {
			if err := stream.Send(&pb.MediaChunk{Data: buffer[:n]}); err != nil {
				return err
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return status.Error(codes.Unavailable, "media read unavailable")
		}
	}
}

func prepareMedia(ctx context.Context, base context.Context, client pb.WorkerServiceClient, job *pb.ClaimResponse) ([][]byte, error) {
	download, cancel := context.WithTimeout(base, 10*time.Minute)
	defer cancel()
	// The job context is cancelled when its lease is lost or the worker shuts down.
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	stream, err := client.DownloadMedia(download, job.Lease)
	if err != nil {
		return nil, err
	}
	input, err := os.CreateTemp("", "stash-worker-media-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(input.Name())
	defer input.Close()
	var total int64
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		total += int64(len(chunk.Data))
		if total > maxMediaBytes || len(chunk.Data) > 64*1024 {
			return nil, errors.New("media input exceeds limit")
		}
		if _, err = input.Write(chunk.Data); err != nil {
			return nil, err
		}
	}
	if _, err = input.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	var frames [][]byte
	contentType, _, err := mime.ParseMediaType(job.Task.ContentType)
	if err != nil {
		return nil, errors.New("invalid media type")
	}
	if strings.HasPrefix(contentType, "image/") {
		if total > maxFrameBytes {
			return nil, errors.New("image input exceeds limit")
		}
		data, err := io.ReadAll(input)
		if err != nil {
			return nil, err
		}
		frames = [][]byte{data}
	} else if strings.HasPrefix(contentType, "video/") {
		frames, err = media.ExtractFrames(ctx, input)
		if err != nil {
			return nil, err
		}
	} else {
		return nil, errors.New("unsupported media type")
	}
	if !validFrames(frames) {
		return nil, errors.New("invalid prepared frames")
	}
	return frames, nil
}

func validFrames(frames [][]byte) bool {
	if len(frames) == 0 || len(frames) > 12 {
		return false
	}
	total := 0
	for _, frame := range frames {
		if len(frame) == 0 {
			return false
		}
		total += len(frame)
		if total > maxFrameBytes {
			return false
		}
	}
	return true
}
