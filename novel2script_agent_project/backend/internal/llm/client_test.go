package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompleteRetriesTransientProviderFailures(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"test","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	client := NewOpenAICompatibleClient(Config{BaseURL: server.URL, APIKey: "key", ControlModel: "test", RequestTimeout: 5 * time.Second})
	response, err := client.Complete(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || response.Content != "ok" {
		t.Fatalf("expected third attempt success, attempts=%d response=%#v", attempts, response)
	}
}

func TestCompleteDoesNotRetryClientError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer server.Close()
	client := NewOpenAICompatibleClient(Config{BaseURL: server.URL, APIKey: "key", ControlModel: "test", RequestTimeout: 5 * time.Second})
	_, err := client.Complete(context.Background(), ChatRequest{})
	if err == nil || attempts != 1 {
		t.Fatalf("expected one failed attempt, attempts=%d err=%v", attempts, err)
	}
}

func TestMetadataTraceDoesNotPersistPromptOrResponse(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("N2S_TRACE_DIR", dir)
	t.Setenv("N2S_LLM_TRACE_LEVEL", "metadata")
	writeTrace(
		ChatRequest{Model: "test-model", Messages: []Message{{Role: RoleUser, Content: "PRIVATE_NOVEL_TEXT"}}, TraceContext: map[string]string{"run_id": "run_123", "operation": "patch_artifact"}},
		ChatResponse{Model: "test-model", Content: "PRIVATE_SCRIPT_TEXT"},
		errors.New("PRIVATE_PROVIDER_ERROR"), time.Now().UTC(), time.Second, map[string]string{"x-request-id": "request-safe"},
	)
	data, err := os.ReadFile(filepath.Join(dir, "llm_trace.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, secret := range []string{"PRIVATE_NOVEL_TEXT", "PRIVATE_SCRIPT_TEXT", "PRIVATE_PROVIDER_ERROR"} {
		if strings.Contains(text, secret) {
			t.Fatalf("metadata trace leaked %s: %s", secret, text)
		}
	}
	for _, expected := range []string{`"trace_level":"metadata"`, `"content_chars":18`, `"x-request-id":"request-safe"`, `"run_id":"run_123"`, `"operation":"patch_artifact"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("metadata trace missing %s: %s", expected, text)
		}
	}
}

func TestTraceCanBeExplicitlyDisabled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("N2S_TRACE_DIR", dir)
	t.Setenv("N2S_LLM_TRACE_LEVEL", "off")
	writeTrace(ChatRequest{Model: "test"}, ChatResponse{}, nil, time.Now().UTC(), time.Millisecond, nil)
	if _, err := os.Stat(filepath.Join(dir, "llm_trace.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected disabled trace not to create a file, got %v", err)
	}
}

func TestFullTraceRequiresExplicitLevel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("N2S_TRACE_DIR", dir)
	t.Setenv("N2S_LLM_TRACE_LEVEL", "full")
	writeTrace(ChatRequest{Model: "test", Messages: []Message{{Role: RoleUser, Content: "DEBUG_ONLY_PROMPT"}}}, ChatResponse{Content: "DEBUG_ONLY_RESPONSE"}, nil, time.Now().UTC(), time.Millisecond, nil)
	data, err := os.ReadFile(filepath.Join(dir, "llm_trace.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "DEBUG_ONLY_PROMPT") || !strings.Contains(string(data), "DEBUG_ONLY_RESPONSE") {
		t.Fatalf("explicit full trace did not contain debug payload: %s", data)
	}
}
