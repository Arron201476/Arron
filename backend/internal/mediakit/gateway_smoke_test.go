package mediakit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This opt-in test exercises the real MediaKit upload, OCR, and polling flow.
func TestGatewaySmoke(t *testing.T) {
	if os.Getenv("CONTENT_AGENT_MEDIAKIT_GATEWAY_SMOKE") != "1" {
		t.Skip("set CONTENT_AGENT_MEDIAKIT_GATEWAY_SMOKE=1 to run the real gateway smoke test")
	}
	fixture := os.Getenv("CONTENT_AGENT_MEDIAKIT_VIDEO_FIXTURE")
	if fixture == "" {
		t.Fatal("CONTENT_AGENT_MEDIAKIT_VIDEO_FIXTURE is required")
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	config, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	client, err := New(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	result, err := client.Extract(ctx, Input{MIMEType: fixtureMIMEType(fixture), Data: data})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Subtitles) == 0 {
		t.Fatal("gateway returned no subtitle segments")
	}
	if output := os.Getenv("CONTENT_AGENT_MEDIAKIT_SMOKE_OUTPUT"); output != "" {
		encoded, marshalErr := json.MarshalIndent(result, "", "  ")
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if writeErr := os.WriteFile(output, encoded, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	t.Logf("task=%s duration=%.3fs subtitle_segments=%d", result.TaskID, result.Duration, len(result.Subtitles))
}

func fixtureMIMEType(path string) string {
	if strings.EqualFold(filepath.Ext(path), ".mov") {
		return "video/quicktime"
	}
	return "video/mp4"
}
