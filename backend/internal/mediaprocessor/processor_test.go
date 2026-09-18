package mediaprocessor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewFromEnvRequiresBothCommands(t *testing.T) {
	t.Setenv("CONTENT_AGENT_FFMPEG_COMMAND", "")
	t.Setenv("CONTENT_AGENT_FFPROBE_COMMAND", "")
	if AvailableFromEnv() {
		t.Fatal("media processor must be unavailable without both commands")
	}
	t.Setenv("CONTENT_AGENT_FFMPEG_COMMAND", "ffmpeg-test")
	t.Setenv("CONTENT_AGENT_FFPROBE_COMMAND", "ffprobe-test")
	processor, err := NewFromEnv()
	if err != nil || processor.ffmpegPath != "ffmpeg-test" || processor.ffprobePath != "ffprobe-test" {
		t.Fatalf("NewFromEnv() = %+v, error = %v", processor, err)
	}
}

func TestWrappedMediaCommandKeepsExecutableAndArgumentsSeparate(t *testing.T) {
	processor := &FFmpeg{commandWrapper: `C:\project\scripts\invoke-media-command.ps1`}
	command, err := processor.command(context.Background(), `C:\tools\ffprobe.exe`, "-v", "error", `C:\video files\1.mov`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(filepath.Base(command.Path), "powershell.exe") || command.Args[4] != processor.commandWrapper {
		t.Fatalf("wrapped command = %#v", command.Args)
	}
	values := map[string]string{}
	for index := 5; index+1 < len(command.Args); index += 2 {
		values[command.Args[index]] = command.Args[index+1]
	}
	executable, err := base64.StdEncoding.DecodeString(values["-ExecutableBase64"])
	if err != nil || string(executable) != `C:\tools\ffprobe.exe` {
		t.Fatalf("wrapped executable = %q, error = %v", executable, err)
	}
	decoded, err := base64.StdEncoding.DecodeString(values["-ArgumentsBase64"])
	if err != nil {
		t.Fatal(err)
	}
	var arguments []string
	if err := json.Unmarshal(decoded, &arguments); err != nil {
		t.Fatal(err)
	}
	if strings.Join(arguments, "|") != `-v|error|C:\video files\1.mov` {
		t.Fatalf("wrapped arguments = %#v", arguments)
	}
}

func TestWindowsMediaCommandWrapperIntegration(t *testing.T) {
	videoPath := strings.TrimSpace(os.Getenv("CONTENT_AGENT_MEDIA_INTEGRATION_VIDEO"))
	if videoPath == "" {
		t.Skip("set CONTENT_AGENT_MEDIA_INTEGRATION_VIDEO to run the local media integration test")
	}
	staleArguments, err := json.Marshal([]string{
		strings.TrimSpace(os.Getenv("CONTENT_AGENT_FFPROBE_COMMAND")),
		videoPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTENT_AGENT_MEDIA_WRAPPED_EXECUTABLE", "stale-ffprobe.exe")
	t.Setenv(
		"CONTENT_AGENT_MEDIA_WRAPPED_ARGUMENTS",
		base64.StdEncoding.EncodeToString(staleArguments),
	)
	processor, err := NewFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	duration, err := processor.probe(context.Background(), videoPath)
	if err != nil || duration <= 0 {
		t.Fatalf("wrapped ffprobe duration = %s, error = %v", duration, err)
	}
	command, err := processor.command(context.Background(), processor.ffmpegPath, "-version")
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "ffmpeg version") {
		t.Fatalf("wrapped ffmpeg output = %q, error = %v", output, err)
	}
	video, err := os.ReadFile(videoPath)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := processor.Prepare(context.Background(), Input{
		Filename: "input.mp4",
		MIMEType: "video/mp4",
		Data:     video,
	})
	if err != nil {
		t.Fatalf("wrapped media preparation failed: %v", err)
	}
	if prepared.Duration <= 0 || len(prepared.Data) == 0 || len(prepared.Data) > DefaultPreparedBytes {
		t.Fatalf("wrapped media preparation = duration %s, bytes %d", prepared.Duration, len(prepared.Data))
	}
}

func TestNewFromEnvUsesConfigurablePreparedByteLimit(t *testing.T) {
	t.Setenv("CONTENT_AGENT_FFMPEG_COMMAND", "ffmpeg-test")
	t.Setenv("CONTENT_AGENT_FFPROBE_COMMAND", "ffprobe-test")
	t.Setenv("CONTENT_AGENT_VIDEO_PREPARED_MAX_BYTES", "7340032")
	processor, err := NewFromEnv()
	if err != nil || processor.preparedMaxBytes != 7<<20 {
		t.Fatalf("NewFromEnv() = %+v, error = %v", processor, err)
	}
	t.Setenv("CONTENT_AGENT_VIDEO_PREPARED_MAX_BYTES", "10485761")
	if _, err := NewFromEnv(); err == nil {
		t.Fatal("prepared byte limit above the hard gateway boundary must be rejected")
	}
}

func TestNewFromEnvUsesFiveMinuteDefaultAndConfigurableDurationLimit(t *testing.T) {
	t.Setenv("CONTENT_AGENT_FFMPEG_COMMAND", "ffmpeg-test")
	t.Setenv("CONTENT_AGENT_FFPROBE_COMMAND", "ffprobe-test")
	t.Setenv("CONTENT_AGENT_VIDEO_MAX_DURATION_SECONDS", "")
	processor, err := NewFromEnv()
	if err != nil || processor.maxVideoDuration != 5*time.Minute {
		t.Fatalf("NewFromEnv() = %+v, error = %v", processor, err)
	}
	t.Setenv("CONTENT_AGENT_VIDEO_MAX_DURATION_SECONDS", "420")
	processor, err = NewFromEnv()
	if err != nil || processor.maxVideoDuration != 7*time.Minute {
		t.Fatalf("NewFromEnv(custom duration) = %+v, error = %v", processor, err)
	}
	t.Setenv("CONTENT_AGENT_VIDEO_MAX_DURATION_SECONDS", "0")
	if _, err := NewFromEnv(); err == nil {
		t.Fatal("non-positive video duration limit must be rejected")
	}
}

func TestPrepareRejectsInvalidVideoBeforeExternalCommands(t *testing.T) {
	processor := &FFmpeg{ffmpegPath: "not-used", ffprobePath: "not-used"}
	for _, input := range []Input{
		{},
		{Filename: "bad.webm", MIMEType: "video/webm", Data: []byte("video")},
	} {
		_, err := processor.Prepare(context.Background(), input)
		if ErrorCode(err) != "MEDIA_INPUT_INVALID" {
			t.Fatalf("Prepare(%+v) error = %v", input, err)
		}
	}
}

func TestFFmpegArgumentsAreShellIndependentAndBounded(t *testing.T) {
	args := transcodeArgs("input.mov", "output.mp4", 768)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "scale=w='min(1280,iw)':h='min(1280,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2") ||
		!strings.Contains(joined, "-b:v 768k") ||
		args[len(args)-1] != "output.mp4" {
		t.Fatalf("transcode args = %#v", args)
	}
	probe := probeArgs("episode.mp4")
	if probe[len(probe)-1] != "episode.mp4" || !strings.Contains(strings.Join(probe, " "), "format=duration") {
		t.Fatalf("probe args = %#v", probe)
	}
}

func TestErrorCodeDoesNotExposeInternalError(t *testing.T) {
	if ErrorCode(&Error{Code: "VIDEO_DURATION_EXCEEDED"}) != "VIDEO_DURATION_EXCEEDED" ||
		ErrorCode(context.Canceled) != "MEDIA_PROCESSING_FAILED" {
		t.Fatal("media error classification changed")
	}
}

func TestMediaErrorKeepsStableCodeAndDiagnosticDetail(t *testing.T) {
	err := &Error{Code: "MEDIA_PROBE_FAILED", Detail: "moov atom not found"}
	if ErrorCode(err) != "MEDIA_PROBE_FAILED" ||
		err.Error() != "MEDIA_PROBE_FAILED: moov atom not found" {
		t.Fatalf("media error = %v, code = %s", err, ErrorCode(err))
	}
	if detail := commandFailureDetail(context.DeadlineExceeded, "\n invalid input \n"); detail != "invalid input" {
		t.Fatalf("command failure detail = %q", detail)
	}
}
