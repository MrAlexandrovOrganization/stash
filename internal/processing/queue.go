// Package processing owns the durable inference queue; workers have no database credentials.
package processing

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"
	pb "stash/gen/processing"
	"stash/internal/vision"
)

//go:embed sql/*.sql
var queries embed.FS

func sql(name string) string {
	b, err := queries.ReadFile("sql/" + name + ".sql")
	if err != nil {
		panic(err)
	}
	return string(b)
}

type Queue struct {
	DB                     *pgxpool.Pool
	VisionModel, TextModel string
	NumCtx                 int
	Preferred              []string
}
type TerminalError struct{}

func (TerminalError) Error() string { return "processing failed after retries" }

func (q *Queue) wait(ctx context.Context, task *pb.Task) (*pb.Result, error) {
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(task)
	if err != nil {
		return nil, err
	}
	if len(payload) > 16*1024*1024 {
		return nil, fmt.Errorf("processing input too large")
	}
	salt, _ := ctx.Value(forceKey{}).(string)
	hash := sha256.Sum256(append([]byte(fmt.Sprint(salt)+":"), payload...))
	key := hex.EncodeToString(hash[:])
	// Serialize admissions across coordinators; existing replays remain accepted when full.
	tx, err := q.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, sql("lock_admission")); err != nil {
		return nil, err
	}
	var count int
	if err = tx.QueryRow(ctx, sql("count")).Scan(&count); err != nil {
		return nil, err
	}
	if count >= 512 {
		var exists bool
		if err = tx.QueryRow(ctx, sql("exists"), key).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, fmt.Errorf("processing queue full")
		}
	}
	if err = tx.QueryRow(ctx, sql("submit"), key, payload).Scan(&key); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		var state string
		var data []byte
		if err = q.DB.QueryRow(ctx, sql("get"), key).Scan(&state, &data); err != nil {
			return nil, err
		}
		if state == "done" {
			result := new(pb.Result)
			if err := proto.Unmarshal(data, result); err != nil {
				return nil, err
			}
			return result, nil
		}
		if state == "failed" {
			return nil, TerminalError{}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

type forceKey struct{}

func RequestIdentity(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, forceKey{}, key)
}

type Vision struct{ Queue *Queue }

var _ vision.Provider = (*Vision)(nil)

func (v *Vision) Describe(ctx context.Context, frames [][]byte) (string, error) {
	result, err := v.Queue.wait(ctx, &pb.Task{Kind: "vision", Model: v.Queue.VisionModel, Frames: frames, NumCtx: int32(v.Queue.NumCtx)})
	if err != nil {
		return "", err
	}
	return result.Description, nil
}

type Text struct{ Queue *Queue }

func (t *Text) EmbedText(ctx context.Context, text string) ([]float32, error) {
	result, err := t.Queue.wait(ctx, &pb.Task{Kind: "text", Model: t.Queue.TextModel, Text: text})
	if err != nil {
		return nil, err
	}
	return result.Vector, nil
}
func (t *Text) EmbedImage(context.Context, []byte) ([]float32, error) {
	return nil, errors.New("text provider does not support images")
}
