package mediaprocessor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	MaxPreparedBytes     = 10 << 20
	DefaultPreparedBytes = 8 << 20
	MinPreparedBytes     = 1 << 20
	MaxVideoDuration     = 5 * time.Minute
)

type Input struct {
	Filename string
	MIMEType string
	Data     []byte
}

type Result struct {
	MIMEType string
	Data     []byte
	Duration time.Duration
}

type Processor interface {
	Prepare(context.Context, Input) (Result, error)
}

type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string {
	if strings.TrimSpace(e.Detail) == "" {
		return e.Code
	}
	return e.Code + ": " + strings.TrimSpace(e.Detail)
}

type FFmpeg struct {
	ffmpegPath       string
	ffprobePath      string
	commandWrapper   string
	preparedMaxBytes int
	maxVideoDuration time.Duration
}

func NewFromEnv() (*FFmpeg, error) {
	ffmpegPath := strings.TrimSpace(os.Getenv("CONTENT_AGENT_FFMPEG_COMMAND"))
	ffprobePath := strings.TrimSpace(os.Getenv("CONTENT_AGENT_FFPROBE_COMMAND"))
	if ffmpegPath == "" || ffprobePath == "" {
		return nil, errors.New("ffmpeg and ffprobe commands are required")
	}
	preparedMaxBytes, err := preparedMaxBytesFromEnv()
	if err != nil {
		return nil, err
	}
	maxVideoDuration, err := maxVideoDurationFromEnv()
	if err != nil {
		return nil, err
	}
	return &FFmpeg{
		ffmpegPath: ffmpegPath, ffprobePath: ffprobePath,
		commandWrapper:   strings.TrimSpace(os.Getenv("CONTENT_AGENT_MEDIA_COMMAND_WRAPPER")),
		preparedMaxBytes: preparedMaxBytes, maxVideoDuration: maxVideoDuration,
	}, nil
}

func AvailableFromEnv() bool {
	_, err := NewFromEnv()
	return err == nil
}

func (p *FFmpeg) Prepare(ctx context.Context, input Input) (Result, error) {
	if p == nil || len(input.Data) == 0 || len(input.Data) > 50<<20 ||
		(input.MIMEType != "video/mp4" && input.MIMEType != "video/quicktime") {
		return Result{}, &Error{Code: "MEDIA_INPUT_INVALID"}
	}
	root, err := os.MkdirTemp("", "content-agent-video-*")
	if err != nil {
		return Result{}, &Error{Code: "MEDIA_PROCESSING_FAILED", Detail: err.Error()}
	}
	defer os.RemoveAll(root)
	extension := strings.ToLower(filepath.Ext(input.Filename))
	if extension != ".mp4" && extension != ".mov" {
		extension = ".mp4"
	}
	inputPath := filepath.Join(root, "input"+extension)
	if err := os.WriteFile(inputPath, input.Data, 0o600); err != nil {
		return Result{}, &Error{Code: "MEDIA_PROCESSING_FAILED", Detail: err.Error()}
	}
	duration, err := p.probe(ctx, inputPath)
	if err != nil {
		return Result{}, err
	}
	maxVideoDuration := p.maxVideoDuration
	if maxVideoDuration == 0 {
		maxVideoDuration = MaxVideoDuration
	}
	if duration <= 0 || duration > maxVideoDuration {
		return Result{}, &Error{Code: "VIDEO_DURATION_EXCEEDED"}
	}
	preparedMIMEType := input.MIMEType
	prepared := append([]byte(nil), input.Data...)
	preparedMaxBytes := p.preparedMaxBytes
	if preparedMaxBytes == 0 {
		preparedMaxBytes = DefaultPreparedBytes
	}
	if len(input.Data) > preparedMaxBytes {
		outputPath := filepath.Join(root, "prepared.mp4")
		targetBytes := preparedMaxBytes * 9 / 10
		targetKbps := int((float64(targetBytes)*8/duration.Seconds())/1000) - 64
		if targetKbps < 128 {
			targetKbps = 128
		}
		command, err := p.command(ctx, p.ffmpegPath, transcodeArgs(inputPath, outputPath, targetKbps)...)
		if err != nil {
			return Result{}, &Error{Code: "MEDIA_PROCESSING_FAILED", Detail: err.Error()}
		}
		var stderr bytes.Buffer
		command.Stderr = &stderr
		if err := command.Run(); err != nil {
			return Result{}, &Error{
				Code:   "MEDIA_PROCESSING_FAILED",
				Detail: commandFailureDetail(err, stderr.String()),
			}
		}
		prepared, err = os.ReadFile(outputPath)
		if err != nil || len(prepared) == 0 || len(prepared) > preparedMaxBytes {
			return Result{}, &Error{Code: "MEDIA_OUTPUT_TOO_LARGE"}
		}
		preparedMIMEType = "video/mp4"
	}
	return Result{MIMEType: preparedMIMEType, Data: prepared, Duration: duration}, nil
}

func maxVideoDurationFromEnv() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("CONTENT_AGENT_VIDEO_MAX_DURATION_SECONDS"))
	if raw == "" {
		return MaxVideoDuration, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 || value > int64((time.Duration(1<<63-1)/time.Second)) {
		return 0, errors.New("video duration limit must be a positive number of seconds")
	}
	return time.Duration(value) * time.Second, nil
}

func preparedMaxBytesFromEnv() (int, error) {
	raw := strings.TrimSpace(os.Getenv("CONTENT_AGENT_VIDEO_PREPARED_MAX_BYTES"))
	if raw == "" {
		return DefaultPreparedBytes, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < MinPreparedBytes || value > MaxPreparedBytes {
		return 0, errors.New("prepared video byte limit must be between 1 MiB and 10 MiB")
	}
	return value, nil
}

func transcodeArgs(inputPath, outputPath string, targetKbps int) []string {
	bitrate := strconv.Itoa(targetKbps) + "k"
	return []string{
		"-nostdin", "-y", "-i", inputPath,
		"-vf", "scale=w='min(1280,iw)':h='min(1280,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2",
		"-c:v", "libx264", "-preset", "veryfast", "-b:v", bitrate,
		"-maxrate", bitrate, "-bufsize", strconv.Itoa(targetKbps*2) + "k",
		"-c:a", "aac", "-b:a", "64k", "-movflags", "+faststart", outputPath,
	}
}

func (p *FFmpeg) probe(ctx context.Context, path string) (time.Duration, error) {
	command, err := p.command(ctx, p.ffprobePath, probeArgs(path)...)
	if err != nil {
		return 0, &Error{Code: "MEDIA_PROBE_FAILED", Detail: err.Error()}
	}
	output, err := command.Output()
	if err != nil {
		stderr := ""
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			stderr = string(exitError.Stderr)
		}
		return 0, &Error{Code: "MEDIA_PROBE_FAILED", Detail: commandFailureDetail(err, stderr)}
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
	if err != nil || seconds <= 0 {
		return 0, &Error{
			Code:   "MEDIA_PROBE_FAILED",
			Detail: "ffprobe returned an invalid duration: " + strings.TrimSpace(string(output)),
		}
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

func (p *FFmpeg) command(ctx context.Context, executable string, arguments ...string) (*exec.Cmd, error) {
	if strings.TrimSpace(p.commandWrapper) == "" {
		return exec.CommandContext(ctx, executable, arguments...), nil
	}
	encodedArguments, err := json.Marshal(arguments)
	if err != nil {
		return nil, fmt.Errorf("encode media command arguments: %w", err)
	}
	command := exec.CommandContext(
		ctx,
		"powershell.exe",
		"-NoProfile",
		"-NonInteractive",
		"-File",
		p.commandWrapper,
		"-ExecutableBase64",
		base64.StdEncoding.EncodeToString([]byte(executable)),
		"-ArgumentsBase64",
		base64.StdEncoding.EncodeToString(encodedArguments),
	)
	return command, nil
}

func commandFailureDetail(err error, stderr string) string {
	stderr = strings.Join(strings.Fields(stderr), " ")
	if stderr != "" {
		return stderr
	}
	if err != nil {
		return err.Error()
	}
	return "external media command failed"
}

func probeArgs(path string) []string {
	return []string{
		"-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path,
	}
}

func ErrorCode(err error) string {
	var mediaError *Error
	if errors.As(err, &mediaError) {
		return mediaError.Code
	}
	return "MEDIA_PROCESSING_FAILED"
}
