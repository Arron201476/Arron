package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestAgentTaskHTTPWorkerLifecycle(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "internal-test-token")
	root := testRoot(t)
	registry, err := capability.LoadRegistry(capability.LoadOptions{
		ProjectRoot: root,
		SkillRoots: []capability.SkillRoot{{
			Scope: capability.SkillScopeWorkspace,
			Path:  filepath.Join(root, "fixtures", "skills", "background"), Priority: 200,
		}},
	})
	if err != nil {
		t.Fatalf("LoadRegistry() error = %v", err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatalf("runtime.Open() error = %v", err)
	}
	defer store.Close()
	handler := NewWithRuntime(shell.New(registry), store, nil).Handler()
	project, err := store.CreateProject(context.Background(), "后台任务 HTTP")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	reference := &agentcontract.CapabilityRef{
		CapabilityID: "story_research_digest", Version: "1.0.0", SelectionMode: "explicit",
	}
	exchange, err := store.CreateMessageExchange(context.Background(), businessruntime.CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID,
		Request: agentcontract.MessageRequest{
			Content: "后台整理故事资料", CapabilityRef: reference,
		},
		Decision: agentcontract.AgentDecision{
			Reply: "已加入后台任务。", Intent: "propose_capability", Confidence: 1,
			CapabilityRef: reference,
			ProposedAction: &agentcontract.ProposedActionDraft{
				ActionType: "start_background_task", CapabilityRef: reference,
				Input: json.RawMessage(`{"topic":"悬疑"}`), Config: json.RawMessage(`{}`),
				RequiresConfirmation: false,
			},
		},
	})
	if err != nil || exchange.TaskRef == nil {
		t.Fatalf("CreateMessageExchange() = %+v, error = %v", exchange, err)
	}

	listed := performJSON(t, handler, http.MethodGet,
		"/api/v1/projects/"+project.ProjectID+"/agent-tasks", nil, http.StatusOK)
	items, ok := objectAt(t, listed, "data")["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("listed tasks = %#v", objectAt(t, listed, "data")["items"])
	}
	projection := performJSON(t, handler, http.MethodGet,
		"/api/v1/projects/"+project.ProjectID+"/workspace-projection", nil, http.StatusOK)
	projectedTasks := arrayAt(t, objectAt(t, objectAt(t, projection, "data"), "snapshot"), "agent_tasks")
	if len(projectedTasks) != 1 ||
		stringAt(t, projectedTasks[0].(map[string]any), "agent_task_id") != exchange.TaskRef.AgentTaskID {
		t.Fatalf("projected agent tasks = %#v", projectedTasks)
	}
	claimPath := "/internal/v1/agent-tasks/claims"
	claimBody := map[string]any{
		"worker_id": "sdk-background-http", "provider_id": "openai",
		"model_id": "test-model", "lease_seconds": 60,
	}
	performJSONWithHeaders(t, handler, http.MethodPost, claimPath, claimBody,
		map[string]string{"Authorization": "Bearer wrong"}, http.StatusUnauthorized)
	claimResponse := performJSONWithHeaders(t, handler, http.MethodPost, claimPath, claimBody,
		map[string]string{"Authorization": "Bearer internal-test-token"}, http.StatusOK)
	claim := objectAt(t, claimResponse, "data")
	attempt := claim["attempt"].(map[string]any)
	attemptID := stringAt(t, attempt, "agent_task_attempt_id")
	attemptToken := stringAt(t, claim, "attempt_token")
	internalHeaders := map[string]string{
		"Authorization":   "Bearer internal-test-token",
		"X-Attempt-Token": attemptToken,
	}
	progress := performJSONWithHeaders(t, handler, http.MethodPost,
		"/internal/v1/agent-task-attempts/"+attemptID+"/progress",
		map[string]any{"current": 1, "total": 2, "message": "整理事实"},
		internalHeaders, http.StatusOK)
	if got := objectAt(t, progress, "data")["progress_current"]; got != float64(1) {
		t.Fatalf("progress_current = %#v", got)
	}
	completed := performJSONWithHeaders(t, handler, http.MethodPost,
		"/internal/v1/agent-task-attempts/"+attemptID+"/complete",
		map[string]any{
			"result": map[string]any{"summary": "完成"},
			"artifact_draft": map[string]any{
				"artifact_type": "generic_document", "title": "调研摘要",
				"payload": map[string]any{"content": "事实与建议"},
			},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
		}, internalHeaders, http.StatusOK)
	completedTask := objectAt(t, completed, "data")
	if stringAt(t, completedTask, "status") != "completed" ||
		stringAt(t, completedTask, "agent_task_id") != exchange.TaskRef.AgentTaskID {
		t.Fatalf("completed task = %#v", completedTask)
	}

	loaded := performJSON(t, handler, http.MethodGet,
		"/api/v1/agent-tasks/"+exchange.TaskRef.AgentTaskID, nil, http.StatusOK)
	if stringAt(t, objectAt(t, loaded, "data"), "status") != "completed" {
		t.Fatalf("loaded task = %#v", loaded)
	}
	attempts := performJSON(t, handler, http.MethodGet,
		"/api/v1/agent-tasks/"+exchange.TaskRef.AgentTaskID+"/attempts", nil, http.StatusOK)
	attemptItems, ok := objectAt(t, attempts, "data")["items"].([]any)
	if !ok || len(attemptItems) != 1 || attemptItems[0].(map[string]any)["status"] != "completed" {
		t.Fatalf("attempts = %#v", objectAt(t, attempts, "data")["items"])
	}
}
