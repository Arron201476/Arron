package httpapi

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

type preflightStreamingAgent struct {
	calls int
}

func (*preflightStreamingAgent) GenericChatAvailable() bool { return true }

func (a *preflightStreamingAgent) StreamTurn(
	context.Context,
	agentcontract.AgentInput,
	string,
	string,
	shell.AgentTurnStreamHandler,
) (bool, error) {
	a.calls++
	return true, nil
}

func TestMessagePreflightRejectsInvalidAttachmentBeforeAgentCall(t *testing.T) {
	registry := capability.NewEmptyRegistry()
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatalf("runtime.Open() error = %v", err)
	}
	defer store.Close()
	agent := &preflightStreamingAgent{}
	server := NewWithRuntime(shell.NewWithAgentService(registry, agent), store, nil)

	projectResponse := performJSON(t, server.Handler(), http.MethodPost, "/api/v1/projects", map[string]any{
		"title": "Preflight",
	}, http.StatusCreated)
	conversationID := stringAt(t, objectAt(t, projectResponse, "data"), "primary_conversation_id")
	response := performJSON(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/conversations/"+conversationID+"/messages",
		map[string]any{
			"content": "看看这个附件",
			"attachment_refs": []map[string]any{{
				"asset_id":          "ast_missing",
				"asset_snapshot_id": "ass_missing",
			}},
		},
		http.StatusNotFound,
	)
	if agent.calls != 0 {
		t.Fatalf("agent was called before attachment preflight: %d", agent.calls)
	}
	if code := stringAt(t, objectAt(t, response, "error"), "code"); code != "ASSET_SNAPSHOT_NOT_FOUND" {
		t.Fatalf("preflight code = %q", code)
	}
}

func TestMessageFailsClosedWhenDurableAgentManagerIsNotStarted(t *testing.T) {
	registry := capability.NewEmptyRegistry()
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, err := store.CreateProject(context.Background(), "SDK required")
	if err != nil {
		t.Fatal(err)
	}
	agent := &preflightStreamingAgent{}
	handler := NewWithRuntime(shell.NewWithAgentService(registry, agent), store, nil).Handler()
	response := performJSONWithHeaders(
		t, handler, http.MethodPost,
		"/api/v1/conversations/"+project.PrimaryConversationID+"/messages",
		map[string]any{"content": "must use durable SDK turn"},
		map[string]string{"Idempotency-Key": "81234567-1234-4234-8234-123456789012"},
		http.StatusServiceUnavailable,
	)
	if code := stringAt(t, objectAt(t, response, "error"), "code"); code != "SDK_AGENT_RUNTIME_UNAVAILABLE" {
		t.Fatalf("error code = %q", code)
	}
	if agent.calls != 0 {
		t.Fatalf("Agent called without durable manager: %d", agent.calls)
	}
}
