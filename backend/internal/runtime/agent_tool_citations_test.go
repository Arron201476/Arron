package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/agenttool"
)

func TestHostedCitationsSurviveStringResultTruncationAndRestart(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "citations.db")
	store, err := Open(database, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	registry, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion, HostedTools: []agenttool.HostedToolConfig{{ID: "web", Type: "web_search", Description: "Search", Enabled: true, Access: agenttool.AccessRead}}})
	if err != nil {
		t.Fatal(err)
	}
	store.SetAgentToolRegistry(registry)
	project, err := store.CreateProject(ctx, "Citations")
	if err != nil {
		t.Fatal(err)
	}
	call, err := store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID, SDKToolCallID: "sdk-web", ToolID: "hosted:web", Arguments: json.RawMessage(`{"instruction":"search"}`), ConfigurationHash: toolConfigurationHashForTest(t, store, "hosted:web")})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"text": strings.Repeat("result ", 500), "citations": []map[string]any{
		{"type": "url_citation", "title": "Source", "url": "https://example.org/article?q=topic"},
		{"type": "url_citation", "title": "Duplicate", "url": "https://example.org/article?q=topic"},
		{"type": "file_citation", "filename": "source.pdf", "file_id": "file-source", "url": "javascript:alert(1)"},
		{"type": "url_citation", "url": "https://example.org/private?api_token=fixture-secret"},
		{"type": "url_citation", "url": "javascript:alert(1)"},
	}})
	encoded, _ := json.Marshal(string(body))
	completed, err := store.CompleteAgentToolCall(ctx, CompleteAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, Result: encoded})
	if err != nil || len(completed.Citations) != 2 || completed.OmittedCitations != 2 {
		t.Fatalf("citations: %+v %v", completed, err)
	}
	if completed.Citations[0].URL != "https://example.org/article?q=topic" || completed.Citations[1].URL != "" || strings.Contains(string(completed.ResultSummary), "fixture-secret") || len(completed.ResultSummary) > maxAgentToolSummaryBytes {
		t.Fatalf("unsafe/truncated citations: %+v", completed)
	}
	duplicate, err := store.CompleteAgentToolCall(ctx, CompleteAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, Result: encoded})
	if err != nil || len(duplicate.Citations) != 2 {
		t.Fatalf("repeat: %+v %v", duplicate, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(database, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetAgentToolCall(ctx, call.AgentToolCallID)
	if err != nil || len(loaded.Citations) != 2 || loaded.ResultHash == nil || *loaded.ResultHash != *completed.ResultHash {
		t.Fatalf("restart: %+v %v", loaded, err)
	}
}

func TestHostedCitationSafetyAndBudget(t *testing.T) {
	if safeCitationURL("https://example.org/#access_token=secret") {
		t.Fatal("credential fragment accepted")
	}
	for _, value := range []string{"", "//example.org/path", "file:///private", "data:text/plain,secret", "https://user:pass@example.org/", "https://example.org/?ACCESS_KEY=secret", "https://example.org/a\\b", "https://example.org/\u200b", "https://example.org/" + strings.Repeat("a", 2048)} {
		if safeCitationURL(value) {
			t.Errorf("unsafe URL accepted: %q", value)
		}
	}
	var citations []AgentToolCitation
	for i := 0; i < 60; i++ {
		citations = append(citations, AgentToolCitation{Type: "url_citation", URL: fmt.Sprintf("https://example.org/%d/%s", i, strings.Repeat("x", 1500)), Title: strings.Repeat("source", 100)})
	}
	raw, _ := json.Marshal(map[string]any{"citations": citations})
	summary := hostedCitationSummary(raw, json.RawMessage(`{"text":"`+strings.Repeat("x", 15000)+`"}`))
	call := AgentToolCall{ToolKind: "hosted", ResultSummary: summary}
	attachToolCitations(&call)
	if len(summary) > maxAgentToolSummaryBytes || len(call.Citations) < 1 || len(call.Citations) > 32 || call.OmittedCitations != 60-len(call.Citations) {
		t.Fatalf("budget: %d %+v", len(summary), call)
	}
	other := AgentToolCall{ToolKind: "mcp", ResultSummary: summary}
	attachToolCitations(&other)
	if len(other.Citations) != 0 {
		t.Fatal("ordinary output spoofed hosted citations")
	}
}
