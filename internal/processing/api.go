package processing

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	pb "stash/gen/processing"
)

type API struct {
	Queue  *Queue
	Hashes map[string]string
}

var identityPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

func NewAPI(q *Queue, path string) (*API, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("worker credential registry unavailable")
	}
	defer f.Close()
	var document struct {
		Version int               `json:"version"`
		Workers map[string]string `json:"workers"`
	}
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&document); err != nil || document.Version != 1 || len(document.Workers) == 0 || len(document.Workers) > 128 {
		return nil, errors.New("invalid worker credential registry")
	}
	seen := map[string]bool{}
	for id, digest := range document.Workers {
		decoded, err := hex.DecodeString(digest)
		if !identityPattern.MatchString(id) || err != nil || len(decoded) != 32 || seen[digest] {
			return nil, errors.New("invalid worker credential registry")
		}
		seen[digest] = true
	}
	return &API{Queue: q, Hashes: document.Workers}, nil
}
func (a *API) authorize(ctx context.Context, worker, instance string) error {
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return status.Error(codes.Unauthenticated, "invalid worker credential")
	}
	token := strings.TrimPrefix(values[0], "Bearer ")
	if len(token) < 32 || len(token) > 512 {
		return status.Error(codes.Unauthenticated, "invalid worker credential")
	}
	digest := sha256.Sum256([]byte(token))
	expected, ok := a.Hashes[worker]
	if !ok || subtle.ConstantTimeCompare([]byte(hex.EncodeToString(digest[:])), []byte(expected)) != 1 {
		return status.Error(codes.Unauthenticated, "invalid worker credential")
	}
	if _, err := uuid.Parse(instance); err != nil {
		return status.Error(codes.InvalidArgument, "invalid instance identity")
	}
	return nil
}
func (a *API) Report(ctx context.Context, r *pb.ReportRequest) (*pb.WorkerReply, error) {
	if err := a.authorize(ctx, r.WorkerId, r.InstanceId); err != nil {
		return nil, err
	}
	_, err := a.Queue.DB.Exec(ctx, sql("report"), r.WorkerId, r.InstanceId, r.Ready, r.VisionModel, r.TextModel)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "worker report unavailable")
	}
	return &pb.WorkerReply{Accepted: true}, nil
}
func (a *API) Claim(ctx context.Context, r *pb.ClaimRequest) (*pb.ClaimResponse, error) {
	if err := a.authorize(ctx, r.WorkerId, r.InstanceId); err != nil {
		return nil, err
	}
	q := a.Queue
	var ready bool
	if err := q.DB.QueryRow(ctx, sql("ready"), r.WorkerId, r.InstanceId, q.VisionModel, q.TextModel).Scan(&ready); err != nil {
		return nil, status.Error(codes.Unavailable, "queue unavailable")
	}
	if !ready {
		return &pb.ClaimResponse{}, nil
	}
	preferred := false
	for _, id := range q.Preferred {
		if id == r.WorkerId {
			preferred = true
		}
	}
	if !preferred {
		if err := q.DB.QueryRow(ctx, sql("preferred"), q.Preferred, q.VisionModel, q.TextModel).Scan(&ready); err != nil {
			return nil, status.Error(codes.Unavailable, "queue unavailable")
		}
		if ready {
			return &pb.ClaimResponse{}, nil
		}
	}
	if _, err := q.DB.Exec(ctx, sql("reap")); err != nil {
		return nil, status.Error(codes.Unavailable, "queue unavailable")
	}
	lease := uuid.NewString()
	var id string
	var payload []byte
	tx, err := q.DB.Begin(ctx)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "queue unavailable")
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, sql("lock_instance"), r.WorkerId+":"+r.InstanceId); err != nil {
		return nil, status.Error(codes.Unavailable, "queue unavailable")
	}
	err = tx.QueryRow(ctx, sql("claim"), r.WorkerId, r.InstanceId, lease).Scan(&id, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return &pb.ClaimResponse{}, nil
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "queue unavailable")
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, status.Error(codes.Unavailable, "queue unavailable")
	}
	task := new(pb.Task)
	if err := proto.Unmarshal(payload, task); err != nil {
		return nil, status.Error(codes.Internal, "invalid queued task")
	}
	return &pb.ClaimResponse{Lease: &pb.LeaseRequest{WorkerId: r.WorkerId, InstanceId: r.InstanceId, JobId: id, LeaseId: lease}, Task: task}, nil
}
func (a *API) change(ctx context.Context, l *pb.LeaseRequest, query string, extra ...any) (*pb.WorkerReply, error) {
	if l == nil {
		return nil, status.Error(codes.InvalidArgument, "missing lease")
	}
	if err := a.authorize(ctx, l.WorkerId, l.InstanceId); err != nil {
		return nil, err
	}
	args := []any{l.JobId, l.WorkerId, l.InstanceId, l.LeaseId}
	args = append(args, extra...)
	tag, err := a.Queue.DB.Exec(ctx, sql(query), args...)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "queue update unavailable")
	}
	return &pb.WorkerReply{Accepted: tag.RowsAffected() == 1}, nil
}
func (a *API) Heartbeat(ctx context.Context, l *pb.LeaseRequest) (*pb.WorkerReply, error) {
	return a.change(ctx, l, "heartbeat")
}
func (a *API) Complete(ctx context.Context, r *pb.CompleteRequest) (*pb.WorkerReply, error) {
	if r.Lease == nil {
		return nil, status.Error(codes.InvalidArgument, "missing lease")
	}
	if err := a.authorize(ctx, r.Lease.WorkerId, r.Lease.InstanceId); err != nil {
		return nil, err
	}
	if r.Result == nil || len(r.Result.Description) > 1024*1024 {
		return nil, status.Error(codes.InvalidArgument, "invalid result")
	}
	if len(r.Result.Vector) > 0 {
		if r.Result.Model != a.Queue.TextModel || len(r.Result.Vector) != 1024 {
			return nil, status.Error(codes.InvalidArgument, "incompatible embedding")
		}
		for _, value := range r.Result.Vector {
			if math.IsInf(float64(value), 0) || math.IsNaN(float64(value)) {
				return nil, status.Error(codes.InvalidArgument, "invalid embedding")
			}
		}
	} else if r.Result.Model != a.Queue.VisionModel || strings.TrimSpace(r.Result.Description) == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid vision result")
	}
	var payload []byte
	var jobState string
	err := a.Queue.DB.QueryRow(ctx, sql("owned"), r.Lease.JobId, r.Lease.WorkerId, r.Lease.InstanceId, r.Lease.LeaseId).Scan(&payload, &jobState)
	if errors.Is(err, pgx.ErrNoRows) {
		return &pb.WorkerReply{Accepted: false}, nil
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "queue unavailable")
	}
	if jobState != "done" {
		task := new(pb.Task)
		if proto.Unmarshal(payload, task) != nil || task.Model != r.Result.Model || (task.Kind == "text" && len(r.Result.Vector) != 1024) || (task.Kind == "vision" && len(r.Result.Vector) != 0) {
			return nil, status.Error(codes.InvalidArgument, "result task mismatch")
		}
	}
	data, err := proto.Marshal(r.Result)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid result")
	}
	return a.change(ctx, r.Lease, "complete", data)
}
func (a *API) Fail(ctx context.Context, r *pb.FailRequest) (*pb.WorkerReply, error) {
	return a.change(ctx, r.Lease, "fail", r.Permanent)
}
func (a *API) Serve(addr string) (*grpc.Server, error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	server := grpc.NewServer(grpc.MaxRecvMsgSize(20*1024*1024), grpc.MaxSendMsgSize(20*1024*1024), grpc.MaxConcurrentStreams(32), grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		return handler(bounded, req)
	}))
	pb.RegisterWorkerServiceServer(server, a)
	go server.Serve(listener)
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			a.Queue.DB.Exec(ctx, sql("cleanup"))
			cancel()
		}
	}()
	return server, nil
}
