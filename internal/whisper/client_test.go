package whisper

import (
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	pb "stash/gen/whisper"
)

type transcriptionServer struct {
	pb.UnimplementedTranscriptionServiceServer
	mu           sync.Mutex
	requests     map[string]string
	lostResponse bool
	acknowledged bool
}

func (s *transcriptionServer) Submit(stream pb.TranscriptionService_SubmitServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if first.RequestId == "" || !first.RetainUntilAck || first.Format != "mp4" {
		return status.Error(codes.InvalidArgument, "missing retention identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.requests[first.RequestId]; ok {
		// Real Transcriber also returns before reading the replay's media.
		return stream.SendAndClose(&pb.SubmitResponse{JobId: "job-fixture"})
	}
	var body strings.Builder
	body.Write(first.Data)
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if chunk.RequestId != "" || chunk.RetainUntilAck || chunk.Format != "" {
			return status.Error(codes.InvalidArgument, "metadata after first chunk")
		}
		body.Write(chunk.Data)
	}
	s.requests[first.RequestId] = body.String()
	if s.lostResponse {
		s.lostResponse = false
		return status.Error(codes.Unavailable, "synthetic lost Submit response")
	}
	return stream.SendAndClose(&pb.SubmitResponse{JobId: "job-fixture"})
}

func (s *transcriptionServer) Ack(context.Context, *pb.AckRequest) (*pb.AckResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acknowledged = true
	return &pb.AckResponse{Acknowledged: true}, nil
}

func TestSubmitRetainsReplaysAndAcknowledges(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	remote := &transcriptionServer{requests: make(map[string]string), lostResponse: true}
	pb.RegisterTranscriptionServiceServer(server, remote)
	go server.Serve(lis)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///fixture", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{conn: conn, stub: pb.NewTranscriptionServiceClient(conn)}
	t.Cleanup(func() { c.Close() })
	data := strings.Repeat("synthetic-audio", 200000)
	ctx := t.Context()
	if _, err := c.Submit(ctx, strings.NewReader(data), "mp4", "stash:fixture:1"); status.Code(err) != codes.Unavailable {
		t.Fatalf("expected lost response, got %v", err)
	}
	for range 2 {
		id, err := c.Submit(ctx, strings.NewReader(data), "mp4", "stash:fixture:1")
		if err != nil || id != "job-fixture" {
			t.Fatalf("replay: %s %v", id, err)
		}
	}
	if err := c.Ack(ctx, "job-fixture"); err != nil {
		t.Fatal(err)
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if remote.requests["stash:fixture:1"] != data || len(remote.requests) != 1 || !remote.acknowledged {
		t.Fatal("request payload, deduplication or acknowledgement changed")
	}
}

type closedChunkStream struct{}

func (closedChunkStream) Send(*pb.TranscribeChunk) error { return io.EOF }

func TestReplayEOFLeavesResponseForCloseAndRecv(t *testing.T) {
	if err := sendChunks(closedChunkStream{}, strings.NewReader("fixture"), "mp4", "stash:fixture"); err != nil {
		t.Fatalf("early replay response treated as upload failure: %v", err)
	}
}
