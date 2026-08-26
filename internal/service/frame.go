package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// videoFrameMin is the floor on sampled frames so even short clips get enough
// context for the vision model to describe what happens in them.
const videoFrameMin = 4

// videoFrameMax caps the number of frames sent to the vision model, keeping
// the request size and the model's context window in check (a 3B model like
// qwen2.5vl:3b processes each frame, so more frames = more compute/time).
const videoFrameMax = 12

// videoFrameInterval is the target spacing (seconds) between sampled frames.
// The smaller it is, the more of the video the model actually "sees".
const videoFrameInterval = 3.0

// videoFrameCountForDuration turns a clip length into a frame budget that
// covers the whole video: roughly one frame every videoFrameInterval seconds,
// bounded by [videoFrameMin, videoFrameMax].
func videoFrameCountForDuration(dur float64) int {
	if dur <= 0 {
		return videoFrameMin
	}
	n := int(math.Ceil(dur / videoFrameInterval))
	if n < videoFrameMin {
		return videoFrameMin
	}
	if n > videoFrameMax {
		return videoFrameMax
	}
	return n
}

// extractFrames returns JPEG frames sampled across the whole media clip so a
// vision model can describe the entire video (not just how it starts). The
// frame count scales with the clip length — short clips get videoFrameMin
// frames, longer ones up to videoFrameMax — so the model sees the arc of the
// whole video.
//
// The input is spooled to a temp file first: mp4 demuxing requires a seekable
// stream, which a pipe does not provide. Only the small output frames are
// held in memory.
func extractFrames(ctx context.Context, r io.Reader) ([][]byte, error) {
	in, err := os.CreateTemp("", "stash-frame-in-*")
	if err != nil {
		return nil, fmt.Errorf("temp file: %w", err)
	}
	inName := in.Name()
	defer os.Remove(inName)

	if _, err := io.Copy(in, r); err != nil {
		in.Close()
		return nil, fmt.Errorf("spool input: %w", err)
	}
	if err := in.Close(); err != nil {
		return nil, fmt.Errorf("spool input: %w", err)
	}

	dur := probeDuration(ctx, inName)
	n := videoFrameCountForDuration(dur)
	stamps := sampleTimestamps(ctx, inName, dur, n)

	var frames [][]byte
	var errs []string
	for _, ts := range stamps {
		data, err := grabFrame(ctx, inName, ts)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		frames = append(frames, data)
	}
	if len(frames) == 0 {
		if len(errs) > 0 {
			return nil, errors.New(strings.Join(errs, "; "))
		}
		return nil, errors.New("no frames extracted")
	}
	return frames, nil
}

// sampleTimestamps picks n timestamps (seconds) spread across the clip
// (10%..90% of the duration) to cover the whole video while skipping black
// lead-in/lead-out frames. Falls back to [0] when the duration is unknown.
func sampleTimestamps(ctx context.Context, path string, dur float64, n int) []string {
	if dur <= 0 {
		return []string{"0"}
	}
	stamps := make([]string, 0, n)
	for i := 0; i < n; i++ {
		// 10%..90% avoids black lead-in/lead-out frames common in clips.
		frac := float64(i+1) / float64(n+1)
		stamps = append(stamps, strconv.FormatFloat(dur*frac, 'f', 1, 64))
	}
	return stamps
}

// probeDuration returns the media duration in seconds, or 0 if it cannot be
// determined (e.g. ffprobe missing or a non-seekable stream).
func probeDuration(ctx context.Context, path string) float64 {
	out, err := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "csv=p=0", path,
	).Output()
	if err != nil {
		return 0
	}
	dur, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || dur <= 0 {
		return 0
	}
	return dur
}

// grabFrame seeks to ts seconds in path and writes one JPEG frame.
func grabFrame(ctx context.Context, path, ts string) ([]byte, error) {
	out, err := os.CreateTemp("", "stash-frame-out-*.jpg")
	if err != nil {
		return nil, fmt.Errorf("temp file: %w", err)
	}
	outName := out.Name()
	out.Close()
	defer os.Remove(outName)

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error",
		"-ss", ts,
		"-i", path,
		"-frames:v", "1",
		"-f", "mjpeg", "-y", outName,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg @%ss: %w: %s", ts, err, strings.TrimSpace(stderr.String()))
	}

	data, err := os.ReadFile(outName)
	if err != nil {
		return nil, fmt.Errorf("read frame: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("ffmpeg produced no frame @%ss", ts)
	}
	return data, nil
}
