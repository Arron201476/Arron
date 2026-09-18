package httpapi

import (
	"context"
	"maps"
	"net/http"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func assertStatefulToolActivityHTTP(t *testing.T, handler http.Handler, projectID, conversationID, attemptID, token string) {
	t.Helper()
	headers := map[string]string{"X-Agent-Project-ID": projectID, "X-Agent-Execution-Attempt-ID": attemptID, "X-Agent-Attempt-Token": token}
	performInternalJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/projects/"+projectID, nil, headers, http.StatusOK)
	invalid := maps.Clone(headers)
	invalid["X-Agent-Attempt-Token"] = "wrong"
	performInternalJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/projects/"+projectID, nil, invalid, http.StatusForbidden)
	invalid = maps.Clone(headers)
	invalid["X-Agent-Task-Attempt-ID"] = "another-mode"
	performInternalJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/projects/"+projectID, nil, invalid, http.StatusForbidden)
	performJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/projects/"+projectID, nil, headers, http.StatusForbidden)
	body := map[string]any{"project_id": projectID, "conversation_id": conversationID,
		"execution_attempt_id": attemptID, "attempt_token": token,
		"sdk_tool_call_id": "sdk-stateful-http", "tool_id": "runtime:inspect_project", "arguments": map[string]any{}}
	wrong := maps.Clone(body)
	wrong["execution_attempt_id"] = "another-attempt"
	performInternalJSONWithHeaders(t, handler, http.MethodPost, "/internal/v1/agent-tool-calls", wrong, headers, http.StatusForbidden)
	performInternalJSONWithHeaders(t, handler, http.MethodPost, "/internal/v1/agent-tool-calls", body, nil, http.StatusForbidden)
	call := objectAt(t, performInternalJSONWithHeaders(t, handler, http.MethodPost, "/internal/v1/agent-tool-calls", body, headers, http.StatusCreated), "data")
	callID := stringAt(t, call, "agent_tool_call_id")
	performInternalJSONWithHeaders(t, handler, http.MethodPost, "/internal/v1/agent-tool-calls/"+callID+"/start", map[string]any{"expected_sdk_tool_call_id": "sdk-stateful-http"}, headers, http.StatusOK)
	performInternalJSONWithHeaders(t, handler, http.MethodPost, "/internal/v1/agent-tool-calls/unknown/complete", map[string]any{"result": map[string]any{}}, headers, http.StatusForbidden)
	performInternalJSONWithHeaders(t, handler, http.MethodPost, "/internal/v1/agent-tool-calls/"+callID+"/complete", map[string]any{"result": map[string]any{"project_id": projectID}}, headers, http.StatusOK)
}

func assertExecutionHeartbeatHTTP(t *testing.T, handler http.Handler, attemptID, token, inputHash string) {
	t.Helper()
	path := "/internal/v1/executor/attempts/" + attemptID + "/heartbeat"
	body := map[string]any{"input_snapshot_hash": inputHash, "lease_seconds": 90}
	headers := map[string]string{"X-Attempt-Token": token}
	performJSONWithHeaders(t, handler, http.MethodPost, path, body, headers, http.StatusUnauthorized)
	performInternalJSONWithHeaders(t, handler, http.MethodPost, path, body, map[string]string{"X-Attempt-Token": "wrong"}, http.StatusBadRequest)
	wrong := maps.Clone(body)
	wrong["input_snapshot_hash"] = "wrong"
	performInternalJSONWithHeaders(t, handler, http.MethodPost, path, wrong, headers, http.StatusBadRequest)
	result := objectAt(t, performInternalJSONWithHeaders(t, handler, http.MethodPost, path, body, headers, http.StatusOK), "data")
	if stringAt(t, result, "attempt_id") != attemptID || stringAt(t, result, "input_snapshot_hash") != inputHash || stringAt(t, result, "status") != "running" {
		t.Fatalf("heartbeat changed identity: %+v", result)
	}
}

func assertExecutionApprovalHTTP(t *testing.T, handler http.Handler, projectID, conversationID, attemptID, token, inputHash string) string {
	t.Helper()
	activity := map[string]string{"X-Agent-Project-ID": projectID, "X-Agent-Execution-Attempt-ID": attemptID, "X-Agent-Attempt-Token": token}
	call := objectAt(t, performInternalJSONWithHeaders(t, handler, http.MethodPost, "/internal/v1/agent-tool-calls", map[string]any{
		"project_id": projectID, "conversation_id": conversationID, "execution_attempt_id": attemptID, "attempt_token": token,
		"sdk_tool_call_id": "stateful-http-approval", "tool_id": "runtime:install_workspace_skill",
		"arguments": map[string]any{"root_path": "draft/skill", "snapshot_hash": "fixture-hash", "scope": "project", "installation_id": "", "expected_active_version_id": ""},
	}, activity, http.StatusCreated), "data")
	approval := objectAt(t, call, "approval")
	path := "/internal/v1/executor/attempts/" + attemptID + "/approval-checkpoint"
	checkpoint := map[string]any{"input_snapshot_hash": inputHash, "schema_version": "1.16", "run_state": map[string]any{"$schemaVersion": "1.16", "private": "stateful-private"},
		"worker_state": map[string]any{"phase": "generate"}, "pending_sdk_tool_call_ids": []string{"stateful-http-approval"}}
	performJSONWithHeaders(t, handler, http.MethodPost, path, checkpoint, map[string]string{"X-Attempt-Token": token}, http.StatusUnauthorized)
	performInternalJSONWithHeaders(t, handler, http.MethodPost, path, checkpoint, map[string]string{"X-Attempt-Token": "wrong"}, http.StatusBadRequest)
	paused := objectAt(t, performInternalJSONWithHeaders(t, handler, http.MethodPost, path, checkpoint, map[string]string{"X-Attempt-Token": token}, http.StatusOK), "data")
	if stringAt(t, paused, "status") != "waiting_approval" || paused["run_state"] != nil || paused["worker_state"] != nil {
		t.Fatalf("private checkpoint projected: %+v", paused)
	}
	performInternalJSONWithHeaders(t, handler, http.MethodPost, path, checkpoint, map[string]string{"X-Attempt-Token": token}, http.StatusOK)
	heartbeatPath := "/internal/v1/executor/attempts/" + attemptID + "/heartbeat"
	heartbeatBody := map[string]any{"input_snapshot_hash": inputHash, "lease_seconds": 90}
	waiting := objectAt(t, performInternalJSONWithHeaders(t, handler, http.MethodPost, heartbeatPath, heartbeatBody,
		map[string]string{"X-Attempt-Token": token}, http.StatusOK), "data")
	if stringAt(t, waiting, "status") != "waiting_approval" || waiting["lease_until"] != paused["lease_until"] || waiting["run_state"] != nil || waiting["worker_state"] != nil {
		t.Fatalf("waiting heartbeat changed receipt or exposed checkpoint: %+v", waiting)
	}
	performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/agent-tool-approvals/"+stringAt(t, approval, "agent_tool_approval_id")+"/resolutions",
		map[string]any{"expected_version": approval["version"], "subject_snapshot_hash": approval["subject_snapshot_hash"], "action": "reject"},
		map[string]string{"Idempotency-Key": "fdef909a-c8bd-40bb-90ec-c7a3e0ceee34"}, http.StatusOK)
	resumed := objectAt(t, performInternalJSON(t, handler, http.MethodPost, "/internal/v1/executor/task-claims", map[string]any{
		"worker_id": "http_resumed", "executor_ids": []string{"worker.structured_content"}, "provider_id": "http_provider", "model_id": "http_model", "lease_seconds": 60,
	}, http.StatusOK), "data")
	newToken := stringAt(t, resumed, "attempt_token")
	if newToken == token || stringAt(t, objectAt(t, resumed, "attempt"), "attempt_id") != attemptID || objectAt(t, resumed, "resume")["run_state"] == nil {
		t.Fatalf("HTTP resume lost checkpoint identity: %+v", resumed)
	}
	if stringAt(t, objectAt(t, resumed, "resume"), "schema_version") != "1.16" {
		t.Fatalf("HTTP resume lost native SDK schema: %+v", resumed)
	}
	performInternalJSONWithHeaders(t, handler, http.MethodPost, path, checkpoint, map[string]string{"X-Attempt-Token": token}, http.StatusBadRequest)
	performInternalJSONWithHeaders(t, handler, http.MethodPost, heartbeatPath, heartbeatBody, map[string]string{"X-Attempt-Token": token}, http.StatusBadRequest)
	assertExecutionHeartbeatHTTP(t, handler, attemptID, newToken, inputHash)
	return assertExecutionNativePauseHTTP(t, handler, stringAt(t, objectAt(t, resumed, "attempt"), "run_id"), attemptID, newToken, inputHash)
}

func assertExecutionNativePauseHTTP(t *testing.T, handler http.Handler, runID, attemptID, token, inputHash string) string {
	t.Helper()
	base := "/internal/v1/executor/attempts/" + attemptID
	headers := map[string]string{"X-Attempt-Token": token}
	heartbeat := map[string]any{"input_snapshot_hash": inputHash, "lease_seconds": 90}
	performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/runs/"+runID+"/pause", map[string]any{}, map[string]string{"Idempotency-Key": "47f78239-3371-4dc0-a739-10d3b486def1"}, http.StatusAccepted)
	receipt := objectAt(t, performInternalJSONWithHeaders(t, handler, http.MethodPost, base+"/heartbeat", heartbeat, headers, http.StatusOK), "data")
	if receipt["pause_requested"] != true {
		t.Fatalf("missing native pause signal: %+v", receipt)
	}
	checkpoint := map[string]any{"input_snapshot_hash": inputHash, "schema_version": "1.16", "run_state": map[string]any{"$schemaVersion": "1.16", "private": "native-private"},
		"worker_state": map[string]any{"phase": "generate"}, "pending_sdk_tool_call_ids": []string{}}
	performJSONWithHeaders(t, handler, http.MethodPost, base+"/pause-checkpoint", checkpoint, headers, http.StatusUnauthorized)
	performInternalJSONWithHeaders(t, handler, http.MethodPost, base+"/pause-checkpoint", checkpoint, map[string]string{"X-Attempt-Token": "wrong"}, http.StatusBadRequest)
	performInternalJSONWithHeaders(t, handler, http.MethodPost, base+"/approval-checkpoint", checkpoint, headers, http.StatusBadRequest)
	paused := objectAt(t, performInternalJSONWithHeaders(t, handler, http.MethodPost, base+"/pause-checkpoint", checkpoint, headers, http.StatusOK), "data")
	if paused["status"] != "paused" || paused["run_state"] != nil || paused["worker_state"] != nil {
		t.Fatalf("invalid public pause receipt: %+v", paused)
	}
	performInternalJSONWithHeaders(t, handler, http.MethodPost, base+"/pause-checkpoint", checkpoint, headers, http.StatusOK)
	performInternalJSONWithHeaders(t, handler, http.MethodPost, base+"/heartbeat", heartbeat, headers, http.StatusOK)
	performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/runs/"+runID+"/resume", map[string]any{}, map[string]string{"Idempotency-Key": "a7f78239-3371-4dc0-a739-10d3b486def1"}, http.StatusOK)
	resumed := objectAt(t, performInternalJSON(t, handler, http.MethodPost, "/internal/v1/executor/task-claims", map[string]any{
		"worker_id": "http_native_resumed", "executor_ids": []string{"worker.structured_content"}, "provider_id": "http_provider", "model_id": "http_model", "lease_seconds": 60,
	}, http.StatusOK), "data")
	newToken := stringAt(t, resumed, "attempt_token")
	if newToken == token || stringAt(t, objectAt(t, resumed, "attempt"), "attempt_id") != attemptID || len(arrayAt(t, objectAt(t, resumed, "resume"), "approval_decisions")) != 0 {
		t.Fatalf("native resume changed identity: %+v", resumed)
	}
	performInternalJSONWithHeaders(t, handler, http.MethodPost, base+"/pause-checkpoint", checkpoint, headers, http.StatusBadRequest)
	assertExecutionHeartbeatHTTP(t, handler, attemptID, newToken, inputHash)
	return newToken
}

func TestActivityHeadersBindSDKReadsWritesAndCleanup(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "activity-service")
	registry := capability.NewEmptyRegistry()
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "activity.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tools, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	store.SetAgentToolRegistry(tools)
	user := identity.Principal{Kind: identity.KindUser, UserID: "sdk-user", WorkspaceID: "sdk-workspace", Role: identity.RoleEditor}
	if err := store.BootstrapPrincipal(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithPrincipal(context.Background(), user)
	project, err := store.CreateProject(ctx, "SDK project")
	if err != nil {
		t.Fatal(err)
	}
	otherProject, err := store.CreateProject(ctx, "Other project")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Read"}, businessruntime.CommandMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil {
		t.Fatal(err)
	}
	handler := NewWithRuntimeRevisionAndTools(shell.New(registry), store, nil, tools, nil).Handler()
	headers := map[string]string{"Authorization": "Bearer activity-service", "X-Agent-Project-ID": project.ProjectID, "X-Agent-Turn-ID": turn.AgentTurnID}
	me := performJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/auth/me", nil, headers, http.StatusOK)
	if stringAt(t, objectAt(t, me, "data"), "user_id") != user.UserID {
		t.Fatalf("SDK lost user: %+v", me)
	}
	performJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/projects/"+project.ProjectID, nil, headers, http.StatusOK)
	performJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/projects/"+otherProject.ProjectID, nil, headers, http.StatusNotFound)
	performJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/auth/me", nil, map[string]string{"X-Agent-Project-ID": project.ProjectID, "X-Agent-Turn-ID": turn.AgentTurnID}, http.StatusForbidden)
	performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/projects", map[string]any{"title": "No service writes"}, headers, http.StatusForbidden)
	body := map[string]any{
		"project_id": project.ProjectID, "conversation_id": project.PrimaryConversationID,
		"agent_turn_id": "other-turn", "sdk_tool_call_id": "sdk-call", "tool_id": "runtime:inspect_project", "arguments": map[string]any{},
	}
	performJSONWithHeaders(t, handler, http.MethodPost, "/internal/v1/agent-tool-calls", body, headers, http.StatusForbidden)
	body["agent_turn_id"] = turn.AgentTurnID
	call := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, "/internal/v1/agent-tool-calls", body, headers, http.StatusCreated), "data")
	callID := stringAt(t, call, "agent_tool_call_id")
	if _, err := store.BeginAgentTurnCommit(ctx, turn.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	exchange, err := store.CreateMessageExchange(ctx, businessruntime.CreateMessageExchangeCommand{
		ConversationID: project.PrimaryConversationID, Request: turn.Request,
		Decision: agentcontract.AgentDecision{Intent: "chat", Reply: "Read complete", Confidence: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteAgentTurnCommit(ctx, turn.AgentTurnID, exchange); err != nil {
		t.Fatal(err)
	}
	performJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/projects/"+project.ProjectID, nil, headers, http.StatusForbidden)
	body["sdk_tool_call_id"] = "new-call"
	performJSONWithHeaders(t, handler, http.MethodPost, "/internal/v1/agent-tool-calls", body, headers, http.StatusForbidden)
	performJSONWithHeaders(t, handler, http.MethodPost, "/internal/v1/agent-tool-calls/"+callID+"/start", map[string]any{"expected_sdk_tool_call_id": "sdk-call"}, headers, http.StatusForbidden)
	performJSONWithHeaders(t, handler, http.MethodPost, "/internal/v1/agent-tool-calls/"+callID+"/complete", map[string]any{"result": map[string]any{"ok": true}}, headers, http.StatusOK)
	performJSONWithHeaders(t, handler, http.MethodPost, "/internal/v1/agent-tool-calls/unknown/complete", map[string]any{"result": map[string]any{"ok": true}}, headers, http.StatusForbidden)
}
