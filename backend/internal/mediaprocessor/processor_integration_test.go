package mediaprocessor

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestFFmpegPrepareIntegration(t *testing.T) {
	largePath := os.Getenv("CONTENT_AGENT_MEDIA_TEST_LARGE_VIDEO")
	longPath := os.Getenv("CONTENT_AGENT_MEDIA_TEST_LONG_VIDEO")
	if largePath == "" || longPath == "" {
		t.Skip("media integration fixtures are not configured")
	}
	processor, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv() error = %v", err)
	}
	large, err := os.ReadFile(largePath)
	if err != nil {
		t.Fatalf("read large fixture: %v", err)
	}
	if len(large) <= MaxPreparedBytes || len(large) > 50<<20 {
		t.Fatalf("large fixture size = %d", len(large))
	}
	prepared, err := processor.Prepare(context.Background(), Input{
		Filename: "large-source.mp4", MIMEType: "video/mp4", Data: large,
	})
	if err != nil {
		t.Fatalf("Prepare(large) error = %v", err)
	}
	if prepared.MIMEType != "video/mp4" || len(prepared.Data) == 0 ||
		len(prepared.Data) > MaxPreparedBytes || prepared.Duration <= 0 ||
		prepared.Duration > MaxVideoDuration {
		t.Fatalf("prepared result = mime %s, bytes %d, duration %s", prepared.MIMEType, len(prepared.Data), prepared.Duration)
	}

	tooLong, err := os.ReadFile(longPath)
	if err != nil {
		t.Fatalf("read long fixture: %v", err)
	}
	_, err = processor.Prepare(context.Background(), Input{
		Filename: "too-long.mp4", MIMEType: "video/mp4", Data: tooLong,
	})
	var mediaError *Error
	if !errors.As(err, &mediaError) || mediaError.Code != "VIDEO_DURATION_EXCEEDED" {
		t.Fatalf("Prepare(too long) error = %v", err)
	}

	movPath := os.Getenv("CONTENT_AGENT_MEDIA_TEST_MOV_VIDEO")
	if movPath == "" {
		return
	}
	mov, err := os.ReadFile(movPath)
	if err != nil {
		t.Fatalf("read MOV fixture: %v", err)
	}
	preparedMOV, err := processor.Prepare(context.Background(), Input{
		Filename: "sample.mov", MIMEType: "video/quicktime", Data: mov,
	})
	if err != nil {
		t.Fatalf("Prepare(MOV) error = %v", err)
	}
	if len(preparedMOV.Data) == 0 || preparedMOV.Duration <= 0 || preparedMOV.Duration > MaxVideoDuration {
		t.Fatalf("prepared MOV result = mime %s, bytes %d, duration %s", preparedMOV.MIMEType, len(preparedMOV.Data), preparedMOV.Duration)
	}
}
