package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/agenttool"
)

func TestAgentToolCallPersistsRedactedAuditAndCompletion(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	project, err := store.CreateProject(context.Background(), "tool audit")
	if err != nil {
		t.Fatal(err)
	}

	call, err := store.BeginAgentToolCall(context.Background(), BeginAgentToolCallCommand{
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		AgentTurnID: "turn_read", SDKToolCallID: "call_read_1",
		ToolID:            "mcp:fixture/read_fact",
		ConfigurationHash: toolConfigurationHashForTest(t, store, "mcp:fixture/read_fact"),
		Arguments: json.RawMessage(`{
			"query":"hero", "authorization":"Bearer should-not-persist",
			"nested":{"api_key":"also-secret"}
		}`),
	})
	if err != nil {
		t.Fatalf("BeginAgentToolCall() error = %v", err)
	}
	if call.Status != "running" || call.ApprovalStatus != "not_required" || call.Approval != nil {
		t.Fatalf("read call = %+v", call)
	}
	summary := string(call.ArgumentsSummary)
	if strings.Contains(summary, "should-not-persist") || strings.Contains(summary, "also-secret") ||
		strings.Count(summary, "[redacted]") != 2 {
		t.Fatalf("arguments summary was not redacted: %s", summary)
	}
	var storedSummary string
	if err := store.db.QueryRow(`
		SELECT arguments_summary_json FROM agent_tool_calls WHERE agent_tool_call_id = ?`,
		call.AgentToolCallID,
	).Scan(&storedSummary); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(storedSummary, "should-not-persist") || strings.Contains(storedSummary, "also-secret") {
		t.Fatalf("database persisted a secret: %s", storedSummary)
	}

	completed, err := store.CompleteAgentToolCall(context.Background(), CompleteAgentToolCallCommand{
		AgentToolCallID: call.AgentToolCallID,
		Result:          json.RawMessage(`{"fact":"The lead lives in Shanghai."}`),
		ResultSizeBytes: 34,
		TraceRef:        "trace_fixture_read",
	})
	if err != nil {
		t.Fatalf("CompleteAgentToolCall() error = %v", err)
	}
	if completed.Status != "completed" || completed.ResultHash == nil ||
		completed.ResultSizeBytes == nil || completed.CompletedAt == nil {
		t.Fatalf("completed call = %+v", completed)
	}
	items, err := store.ListAgentToolCalls(context.Background(), project.ProjectID)
	if err != nil || len(items) != 1 || items[0].Status != "completed" {
		t.Fatalf("ListAgentToolCalls() = %+v, error = %v", items, err)
	}
	var eventCount int
	if err := store.db.QueryRow(`
		SELECT COUNT(*) FROM events
		WHERE project_id = ? AND event_type IN ('agent.tool_call.started','agent.tool_call.completed')`,
		project.ProjectID,
	).Scan(&eventCount); err != nil || eventCount != 2 {
		t.Fatalf("tool event count = %d, error = %v", eventCount, err)
	}
}

func TestSensitiveAgentToolCannotRunBeforeApproval(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	project, err := store.CreateProject(context.Background(), "tool approval")
	if err != nil {
		t.Fatal(err)
	}

	call, err := store.BeginAgentToolCall(context.Background(), BeginAgentToolCallCommand{
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		SDKToolCallID: "call_write_1", ToolID: "mcp:fixture/save_fact",
		ConfigurationHash: toolConfigurationHashForTest(t, store, "mcp:fixture/save_fact"),
		Arguments:         json.RawMessage(`{"key":"lead.city","value":"Shanghai"}`),
	})
	if err != nil {
		t.Fatalf("BeginAgentToolCall() error = %v", err)
	}
	if call.Status != "pending_approval" || call.ApprovalStatus != "pending" || call.Approval == nil {
		t.Fatalf("sensitive call = %+v", call)
	}
	_, err = store.StartAgentToolCall(context.Background(), StartAgentToolCallCommand{
		AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID,
		ConfigurationHash: toolConfigurationHashForTest(t, store, call.ToolID),
	})
	assertDomainCode(t, err, "AGENT_TOOL_APPROVAL_REQUIRED")
	_, err = store.CompleteAgentToolCall(context.Background(), CompleteAgentToolCallCommand{
		AgentToolCallID: call.AgentToolCallID, Result: json.RawMessage(`{"ok":true}`),
	})
	assertDomainCode(t, err, "AGENT_TOOL_CALL_STATE_CONFLICT")

	approval, err := store.ResolveAgentToolApproval(context.Background(), ResolveAgentToolApprovalCommand{
		AgentToolApprovalID: call.Approval.AgentToolApprovalID,
		ExpectedVersion:     call.Approval.Version,
		SubjectSnapshotHash: call.Approval.SubjectSnapshotHash,
		Action:              "approve",
		ActorRef:            "test_user",
	})
	if err != nil {
		t.Fatalf("ResolveAgentToolApproval() error = %v", err)
	}
	if approval.Status != "approved" || approval.Version != 2 || approval.ActorRef == nil {
		t.Fatalf("approval = %+v", approval)
	}
	approved, err := store.GetAgentToolCall(context.Background(), call.AgentToolCallID)
	if err != nil || approved.Status != "approved" || approved.StartedAt != nil {
		t.Fatalf("approved call = %+v, error = %v", approved, err)
	}
	started, err := store.StartAgentToolCall(context.Background(), StartAgentToolCallCommand{
		AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID,
		ConfigurationHash: toolConfigurationHashForTest(t, store, call.ToolID),
	})
	if err != nil || started.Status != "running" || started.StartedAt == nil {
		t.Fatalf("started call = %+v, error = %v", started, err)
	}
	completed, err := store.CompleteAgentToolCall(context.Background(), CompleteAgentToolCallCommand{
		AgentToolCallID: call.AgentToolCallID, Result: json.RawMessage(`{"ok":true}`),
	})
	if err != nil || completed.Status != "completed" {
		t.Fatalf("completed call = %+v, error = %v", completed, err)
	}
}

func TestAgentToolApprovalRejectsAndSDKCallIDCannotBeReused(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	project, err := store.CreateProject(context.Background(), "tool rejection")
	if err != nil {
		t.Fatal(err)
	}
	command := BeginAgentToolCallCommand{
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		SDKToolCallID: "call_reused", ToolID: "mcp:fixture/save_fact",
		ConfigurationHash: toolConfigurationHashForTest(t, store, "mcp:fixture/save_fact"),
		Arguments:         json.RawMessage(`{"value":"first"}`),
	}
	call, err := store.BeginAgentToolCall(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := store.BeginAgentToolCall(context.Background(), command)
	if err != nil || duplicate.AgentToolCallID != call.AgentToolCallID {
		t.Fatalf("idempotent begin = %+v, error = %v", duplicate, err)
	}
	command.Arguments = json.RawMessage(`{"value":"changed"}`)
	_, err = store.BeginAgentToolCall(context.Background(), command)
	assertDomainCode(t, err, "AGENT_TOOL_CALL_ID_CONFLICT")

	_, err = store.ResolveAgentToolApproval(context.Background(), ResolveAgentToolApprovalCommand{
		AgentToolApprovalID: call.Approval.AgentToolApprovalID,
		ExpectedVersion:     call.Approval.Version,
		SubjectSnapshotHash: "wrong",
		Action:              "reject",
	})
	assertDomainCode(t, err, "AGENT_TOOL_APPROVAL_SUBJECT_CHANGED")
	rejected, err := store.ResolveAgentToolApproval(context.Background(), ResolveAgentToolApprovalCommand{
		AgentToolApprovalID: call.Approval.AgentToolApprovalID,
		ExpectedVersion:     call.Approval.Version,
		SubjectSnapshotHash: call.Approval.SubjectSnapshotHash,
		Action:              "reject",
	})
	if err != nil || rejected.Status != "rejected" {
		t.Fatalf("rejected approval = %+v, error = %v", rejected, err)
	}
	current, err := store.GetAgentToolCall(context.Background(), call.AgentToolCallID)
	if err != nil || current.Status != "rejected" || current.CompletedAt == nil {
		t.Fatalf("rejected call = %+v, error = %v", current, err)
	}
	_, err = store.StartAgentToolCall(context.Background(), StartAgentToolCallCommand{
		AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID,
		ConfigurationHash: toolConfigurationHashForTest(t, store, call.ToolID),
	})
	assertDomainCode(t, err, "AGENT_TOOL_CALL_STATE_CONFLICT")
}

func TestAgentToolRegistryIsAuthoritative(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, err := store.CreateProject(context.Background(), "registry gate")
	if err != nil {
		t.Fatal(err)
	}
	command := BeginAgentToolCallCommand{
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		SDKToolCallID: "unknown_1", ToolID: "mcp:untrusted/write",
		Arguments: json.RawMessage(`{}`),
	}
	_, err = store.BeginAgentToolCall(context.Background(), command)
	assertDomainCode(t, err, "AGENT_TOOL_REGISTRY_UNAVAILABLE")
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	_, err = store.BeginAgentToolCall(context.Background(), command)
	assertDomainCode(t, err, "AGENT_TOOL_NOT_FOUND")

	var domain *DomainError
	if !errors.As(err, &domain) || domain.Message == "" {
		t.Fatalf("registry error = %v", err)
	}
}

func agentToolRegistryForTest(t *testing.T) *agenttool.Registry {
	t.Helper()
	registry, err := agenttool.Compile(agenttool.Config{
		SchemaVersion: agenttool.SchemaVersion,
		MCPServers: []agenttool.MCPServerConfig{{
			ID: "fixture", Description: "Fixture MCP server",
			Transport: "streamable_http", URL: "http://127.0.0.1:9321/mcp",
			Enabled: true, MaxResultBytes: 64 * 1024,
			AllowedTools: []agenttool.MCPToolConfig{
				{Name: "read_fact", Description: "Read a fact", Access: agenttool.AccessRead},
				{Name: "save_fact", Description: "Save a fact", Access: agenttool.AccessWrite},
			},
		}},
	})
	if err != nil {
		t.Fatalf("compile Agent tool registry: %v", err)
	}
	return registry
}

func toolConfigurationHashForTest(t *testing.T, store *Store, id string) string {
	t.Helper()
	descriptor, ok := store.agentTools.Get(id)
	if !ok || descriptor.ConfigurationHash == "" {
		t.Fatalf("missing configuration hash for %s", id)
	}
	return descriptor.ConfigurationHash
}
