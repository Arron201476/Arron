package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	businessruntime "content-agent/backend/internal/runtime"
)

const browserVideoPreviewTimeout = 30 * time.Minute

var browserVideoPreviewLocks sync.Map

func openBrowserVideoPreview(content businessruntime.AssetContent) (*os.File, error) {
	if content.Asset == nil || content.File == nil || content.Asset.Kind != "video" {
		return nil, errors.New("browser-compatible preview requires a video asset")
	}
	ffmpegPath := strings.TrimSpace(os.Getenv("CONTENT_AGENT_FFMPEG_COMMAND"))
	if ffmpegPath == "" {
		return nil, errors.New("CONTENT_AGENT_FFMPEG_COMMAND is not configured")
	}
	stat, err := content.File.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat video source: %w", err)
	}
	previewPath := browserVideoPreviewPath(content.Asset.AssetID, stat.Size(), stat.ModTime())
	lockValue, _ := browserVideoPreviewLocks.LoadOrStore(previewPath, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	if preview, err := openNonEmptyFile(previewPath); err == nil {
		return preview, nil
	}
	if err := os.MkdirAll(filepath.Dir(previewPath), 0o750); err != nil {
		return nil, fmt.Errorf("create browser video preview cache: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(previewPath), ".video-preview-*.mp4")
	if err != nil {
		return nil, fmt.Errorf("create browser video preview: %w", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return nil, fmt.Errorf("close browser video preview: %w", err)
	}
	defer os.Remove(temporaryPath)

	ctx, cancel := context.WithTimeout(context.Background(), browserVideoPreviewTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, ffmpegPath, browserVideoPreviewArgs(content.File.Name(), temporaryPath)...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		detail := strings.Join(strings.Fields(stderr.String()), " ")
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			detail = "preview generation timed out"
		} else if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("generate browser video preview: %s", detail)
	}
	if generated, err := os.Stat(temporaryPath); err != nil || generated.Size() == 0 {
		return nil, errors.New("generate browser video preview: ffmpeg produced an empty file")
	}
	if err := os.Rename(temporaryPath, previewPath); err != nil {
		return nil, fmt.Errorf("cache browser video preview: %w", err)
	}
	return os.Open(previewPath)
}

func browserVideoPreviewPath(assetID string, size int64, modifiedAt time.Time) string {
	cacheRoot := strings.TrimSpace(os.Getenv("CONTENT_AGENT_VIDEO_PREVIEW_CACHE_DIR"))
	if cacheRoot == "" {
		cacheRoot = filepath.Join(os.TempDir(), "content-agent-browser-video-previews")
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", assetID, size, modifiedAt.UnixNano())))
	return filepath.Join(cacheRoot, hex.EncodeToString(digest[:16])+".mp4")
}

func browserVideoPreviewArgs(inputPath, outputPath string) []string {
	return []string{
		"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-i", inputPath,
		"-map", "0:v:0", "-map", "0:a?",
		"-vf", "scale=w='min(1280,iw)':h='min(1280,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2,fps=24",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "27", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "96k", "-movflags", "+faststart", outputPath,
	}
}

func openNonEmptyFile(path string) (*os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	stat, err := file.Stat()
	if err != nil || stat.Size() == 0 {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("cached browser video preview is empty")
	}
	return file, nil
}
