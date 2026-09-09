package preview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Video previews are the one rung of the ladder that leaves the process, and
// ffmpeg is the only external dependency CIT is allowed. It is optional: when
// it is not on PATH, video files simply get the generic icon and everything
// else carries on working.

// ErrNoFFmpeg means ffmpeg was not found. Callers treat it as "unsupported",
// never as a failure worth showing the user as broken.
var ErrNoFFmpeg = errors.New("preview: ffmpeg tidak ada di PATH")

const (
	// framePosition is how far into the video to grab. The first frame of an
	// export is very often black or a title card; ten percent in is usually
	// actual content.
	framePosition = 0.10

	// fallbackSeek is used when the duration cannot be read. Two seconds is
	// past most fade-ins and still inside even a very short clip.
	fallbackSeek = 2 * time.Second
)

// videoThumbnail pulls one frame out of a video.
//
// Both ffprobe and ffmpeg run under the caller's context with a deadline. A
// malformed container can send either into a very long analysis pass, and a
// preview is never worth hanging the application over.
func (g *Generator) videoThumbnail(ctx context.Context, path string) (encoded, int, int, error) {
	ffmpeg, err := g.lookFFmpeg()
	if err != nil {
		return encoded{}, 0, 0, err
	}

	ctx, cancel := context.WithTimeout(ctx, g.ffmpegTimeout)
	defer cancel()

	seek := fallbackSeek
	if d, err := g.videoDuration(ctx, path); err == nil && d > 0 {
		seek = time.Duration(float64(d) * framePosition)
	}

	// -nostdin so a prompt can never block; -frames:v 1 for a single frame;
	// mjpeg straight to stdout so nothing touches the filesystem.
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-nostdin",
		"-loglevel", "error",
		"-ss", formatSeconds(seek),
		"-i", path,
		"-frames:v", "1",
		"-f", "image2",
		"-vcodec", "mjpeg",
		"-",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return encoded{}, 0, 0, fmt.Errorf("preview: ffmpeg melewati batas waktu %s: %w",
				g.ffmpegTimeout, ctx.Err())
		}
		return encoded{}, 0, 0, fmt.Errorf("preview: ffmpeg gagal: %w (%s)",
			err, firstLine(stderr.String()))
	}
	if stdout.Len() == 0 {
		return encoded{}, 0, 0, errors.New("preview: ffmpeg tidak menghasilkan bingkai")
	}

	img, _, err := decodeLimited(stdout.Bytes(), g.maxPixels)
	if err != nil {
		return encoded{}, 0, 0, err
	}
	return g.renderThumbnail(img)
}

// videoDuration asks ffprobe how long the file is, in seconds.
func (g *Generator) videoDuration(ctx context.Context, path string) (time.Duration, error) {
	probe, err := g.lookFFprobe()
	if err != nil {
		return 0, err
	}

	cmd := exec.CommandContext(ctx, probe,
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	seconds, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || seconds <= 0 {
		return 0, fmt.Errorf("preview: durasi tidak terbaca")
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

func (g *Generator) lookFFmpeg() (string, error) {
	if g.ffmpegPath != "" {
		return g.ffmpegPath, nil
	}
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return "", ErrNoFFmpeg
	}
	return path, nil
}

func (g *Generator) lookFFprobe() (string, error) {
	if g.ffprobePath != "" {
		return g.ffprobePath, nil
	}
	path, err := exec.LookPath("ffprobe")
	if err != nil {
		return "", ErrNoFFmpeg
	}
	return path, nil
}

func formatSeconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 3, 64)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
