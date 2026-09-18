package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/capability"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestRuntimeHTTPCandidateRoutesEmptyState(t *testing.T) {
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatalf("LoadRegistry() error = %v", err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatalf("runtime.Open() error = %v", err)
	}
	defer store.Close()
	handler := NewWithRuntime(shell.New(registry), store, nil).Handler()

	projectResponse := performJSON(
		t,
		handler,
		http.MethodPost,
		"/api/v1/projects",
		map[string]any{"title": "Candidate Routes"},
		http.StatusCreated,
	)
	projectID := stringAt(t, objectAt(t, projectResponse, "data"), "project_id")

	candidatesResponse := performJSON(
		t,
		handler,
		http.MethodGet,
		"/api/v1/projects/"+projectID+"/script-candidates",
		nil,
		http.StatusOK,
	)
	items, ok := objectAt(t, candidatesResponse, "data")["items"].([]any)
	if !ok || len(items) != 0 {
		t.Fatalf("candidate items = %#v, want empty array", items)
	}

	finalResponse := performJSON(
		t,
		handler,
		http.MethodGet,
		"/api/v1/projects/"+projectID+"/final-script-selection",
		nil,
		http.StatusOK,
	)
	if finalResponse["data"] != nil {
		t.Fatalf("final selection = %#v, want null", finalResponse["data"])
	}

	missingResponse := performJSON(
		t,
		handler,
		http.MethodGet,
		"/api/v1/script-candidates/cand_missing",
		nil,
		http.StatusNotFound,
	)
	if code := stringAt(t, objectAt(t, missingResponse, "error"), "code"); code != "RESOURCE_NOT_FOUND" {
		t.Fatalf("missing candidate code = %q", code)
	}
}

func TestInternalAgentTurnCommitIsAuthenticatedAndIdempotent(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "internal-test-token")
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatalf("LoadRegistry() error = %v", err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatalf("runtime.Open() error = %v", err)
	}
	defer store.Close()
	handler := NewWithRuntime(shell.New(registry), store, nil).Handler()

	projectResponse := performJSON(t, handler, http.MethodPost, "/api/v1/projects", map[string]any{
		"title": "SDK controlled write",
	}, http.StatusCreated)
	project := objectAt(t, projectResponse, "data")
	projectID := stringAt(t, project, "project_id")
	conversationID := stringAt(t, project, "primary_conversation_id")
	body := map[string]any{
		"project_id": projectID, "conversation_id": conversationID,
		"request": map[string]any{
			"content": "生成一份人物设定", "capability_ref": nil,
			"attachment_refs": []any{}, "selection_snapshot": nil,
			"client_context": map[string]any{},
		},
		"decision": map[string]any{
			"reply": "已生成并保存人物设定。", "intent": "create_artifact", "confidence": 1,
			"capability_ref": nil, "target_ref": nil, "clarification": nil, "proposed_action": nil,
			"artifact_draft": map[string]any{
				"artifact_type": "generic_document", "title": "人物设定",
				"payload": map[string]any{"content": "主角沈砚，冷静克制。"},
			},
		},
	}
	performJSONWithHeaders(t, handler, http.MethodPost, "/internal/v1/agent/turn-commits", body,
		map[string]string{"Authorization": "Bearer wrong"}, http.StatusUnauthorized)
	headers := map[string]string{
		"Authorization":   "Bearer internal-test-token",
		"Idempotency-Key": "55555555-5555-4555-8555-555555555555",
	}
	first := performJSONWithHeaders(t, handler, http.MethodPost, "/internal/v1/agent/turn-commits", body, headers, http.StatusCreated)
	second := performJSONWithHeaders(t, handler, http.MethodPost, "/internal/v1/agent/turn-commits", body, headers, http.StatusCreated)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("idempotent commit changed response: first=%#v second=%#v", first, second)
	}
	artifacts, err := store.ListArtifactsByProject(context.Background(), projectID)
	if err != nil {
		t.Fatalf("ListArtifacts() error = %v", err)
	}
	if len(artifacts) != 1 || artifacts[0].Title != "人物设定" {
		t.Fatalf("artifacts = %+v", artifacts)
	}
	multipleBody := map[string]any{
		"project_id": projectID, "conversation_id": conversationID,
		"request": map[string]any{
			"content": "续写两章，每章独立编辑器", "capability_ref": nil,
			"attachment_refs": []any{}, "selection_snapshot": nil,
			"client_context": map[string]any{},
		},
		"decision": map[string]any{
			"reply": "已分别生成两章。", "intent": "create_artifact", "confidence": 1,
			"capability_ref": nil, "target_ref": nil, "clarification": nil,
			"proposed_action": nil, "artifact_draft": nil,
			"artifact_drafts": []map[string]any{
				{"artifact_type": "generic_document", "title": "第1章", "payload": map[string]any{"content": "第一章正文"}},
				{"artifact_type": "generic_document", "title": "第2章", "payload": map[string]any{"content": "第二章正文"}},
			},
		},
	}
	multipleHeaders := map[string]string{
		"Authorization":   "Bearer internal-test-token",
		"Idempotency-Key": "66666666-6666-4666-8666-666666666666",
	}
	multipleFirst := performJSONWithHeaders(t, handler, http.MethodPost, "/internal/v1/agent/turn-commits", multipleBody, multipleHeaders, http.StatusCreated)
	multipleSecond := performJSONWithHeaders(t, handler, http.MethodPost, "/internal/v1/agent/turn-commits", multipleBody, multipleHeaders, http.StatusCreated)
	if !reflect.DeepEqual(multipleFirst, multipleSecond) {
		t.Fatalf("multi-artifact idempotent commit changed response")
	}
	multipleExchange := objectAt(t, multipleFirst, "data")
	createdArtifacts, ok := multipleExchange["generic_artifacts"].([]any)
	if !ok || len(createdArtifacts) != 2 {
		t.Fatalf("generic_artifacts = %#v", multipleExchange["generic_artifacts"])
	}
	artifacts, err = store.ListArtifactsByProject(context.Background(), projectID)
	if err != nil || len(artifacts) != 3 {
		t.Fatalf("artifacts after multi-artifact commit = %+v, error = %v", artifacts, err)
	}
}

func TestInternalAgentTurnCommitRejectsMismatchedDurableTurnContext(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "internal-test-token")
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatalf("LoadRegistry() error = %v", err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatalf("runtime.Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(context.Background(), "Bound turn commit")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	key := "77777777-7777-4777-8777-777777777777"
	turn, err := store.AcceptAgentTurn(
		context.Background(), project.PrimaryConversationID,
		agentcontract.MessageRequest{Content: "原始请求"},
		businessruntime.CommandMeta{
			Scope: project.ProjectID, CommandType: "create_message",
			IdempotencyKey: key, RequestHash: "original-request",
		},
	)
	if err != nil {
		t.Fatalf("AcceptAgentTurn() error = %v", err)
	}
	handler := NewWithRuntime(shell.New(registry), store, nil).Handler()
	response := performJSONWithHeaders(
		t, handler, http.MethodPost, "/internal/v1/agent/turn-commits",
		map[string]any{
			"project_id": project.ProjectID, "conversation_id": project.PrimaryConversationID,
			"agent_turn_id": turn.AgentTurnID,
			"request": map[string]any{
				"content": "被替换的请求", "attachment_refs": []any{}, "client_context": map[string]any{},
			},
			"decision": map[string]any{
				"reply": "不应提交", "intent": "chat", "confidence": 1,
			},
		},
		map[string]string{
			"Authorization": "Bearer internal-test-token", "Idempotency-Key": key,
		},
		http.StatusBadRequest,
	)
	if code := stringAt(t, objectAt(t, response, "error"), "code"); code != "AGENT_TURN_CONTEXT_MISMATCH" {
		t.Fatalf("error code = %q", code)
	}
	stored, err := store.GetAgentTurn(context.Background(), turn.AgentTurnID)
	if err != nil || stored.Status != "accepted" {
		t.Fatalf("turn after mismatch = %+v, error = %v", stored, err)
	}
	messages, err := store.ListMessages(context.Background(), project.PrimaryConversationID)
	if err != nil || len(messages) != 0 {
		t.Fatalf("messages after mismatch = %+v, error = %v", messages, err)
	}
}

func TestInternalAgentTurnCommitDiscoversEachSkillAsIndependentPlugin(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "internal-test-token")
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatalf("LoadRegistry() error = %v", err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatalf("runtime.Open() error = %v", err)
	}
	defer store.Close()
	handler := NewWithRuntime(shell.New(registry), store, nil).Handler()

	plugins := []struct{ id, version string }{
		{"novel_to_script", "1.4.0"},
		{"non_novel_to_script", "1.3.0"},
		{"video_reference_creation", "1.2.0"},
	}
	for index, plugin := range plugins {
		projectResponse := performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/projects", map[string]any{
			"title": plugin.id,
		}, map[string]string{"Idempotency-Key": fmt.Sprintf("60000000-0000-4000-8000-%012d", index+1)}, http.StatusCreated)
		project := objectAt(t, projectResponse, "data")
		projectID := stringAt(t, project, "project_id")
		conversationID := stringAt(t, project, "primary_conversation_id")
		reference := map[string]any{
			"capability_id": plugin.id, "version": plugin.version, "selection_mode": "explicit",
		}
		body := map[string]any{
			"project_id": projectID, "conversation_id": conversationID,
			"request": map[string]any{
				"content": "使用这个 Skill", "capability_ref": reference,
				"attachment_refs": []any{}, "selection_snapshot": nil, "client_context": map[string]any{},
			},
			"decision": map[string]any{
				"reply": "请确认本次配置。", "intent": "propose_capability", "confidence": 1,
				"capability_ref": reference, "target_ref": nil, "clarification": nil, "artifact_draft": nil,
				"proposed_action": map[string]any{
					"action_type": "collect_run_configuration", "capability_ref": reference,
					"input": map[string]any{}, "config": map[string]any{}, "requires_confirmation": false,
				},
			},
		}
		response := performJSONWithHeaders(
			t, handler, http.MethodPost, "/internal/v1/agent/turn-commits", body,
			map[string]string{
				"Authorization":   "Bearer internal-test-token",
				"Idempotency-Key": fmt.Sprintf("70000000-0000-4000-8000-%012d", index+1),
			}, http.StatusCreated,
		)
		exchange := objectAt(t, response, "data")
		action := objectAt(t, exchange, "proposed_action")
		capabilityRef := objectAt(t, action, "capability_ref")
		if got := stringAt(t, capabilityRef, "capability_id"); got != plugin.id {
			t.Fatalf("plugin action capability = %q, want %q", got, plugin.id)
		}
		contextPayload := objectAt(t, exchange, "message_context")
		routing := objectAt(t, contextPayload, "routing_context")
		if stringAt(t, routing, "scope") != "invocation" || routing["invocation_id"] == nil {
			t.Fatalf("plugin routing = %#v", routing)
		}
	}
}

func TestRuntimeHTTPProjectDeleteAndAssetParseRetry(t *testing.T) {
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatalf("LoadRegistry() error = %v", err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatalf("runtime.Open() error = %v", err)
	}
	defer store.Close()
	handler := NewWithRuntime(shell.New(registry), store, nil).Handler()

	projectResponse := performJSON(t, handler, http.MethodPost, "/api/v1/projects", map[string]any{
		"title": "删除与解析重试",
	}, http.StatusCreated)
	projectID := stringAt(t, objectAt(t, projectResponse, "data"), "project_id")
	sourceBytes := []byte("待解析的故事大纲")
	uploadResponse := performJSON(t, handler, http.MethodPost, "/api/v1/projects/"+projectID+"/upload-sessions", map[string]any{
		"items": []map[string]any{{
			"client_item_key": "retry_text", "kind": "text", "original_filename": "outline.txt",
			"declared_mime_type": "text/plain", "declared_size_bytes": len(sourceBytes),
		}},
	}, http.StatusCreated)
	uploadItemID := stringAt(t, arrayAt(t, objectAt(t, uploadResponse, "data"), "items")[0].(map[string]any), "upload_item_id")
	performBytes(t, handler, http.MethodPut, "/api/v1/upload-items/"+uploadItemID+"/content", sourceBytes, http.StatusOK)
	completeResponse := performJSON(t, handler, http.MethodPost, "/api/v1/upload-items/"+uploadItemID+"/complete", nil, http.StatusCreated)
	assetID := stringAt(t, objectAt(t, objectAt(t, completeResponse, "data"), "asset"), "asset_id")
	parseResponse := performJSON(t, handler, http.MethodPost, "/api/v1/assets/"+assetID+"/parse-retries", nil, http.StatusOK)
	if status := stringAt(t, objectAt(t, parseResponse, "data"), "status"); status != "completed" {
		t.Fatalf("parse retry status = %q", status)
	}
	previewResponse := performJSON(t, handler, http.MethodPost, "/api/v1/projects/"+projectID+"/delete-previews", nil, http.StatusCreated)
	preview := objectAt(t, previewResponse, "data")
	confirmBody := map[string]any{"preview_hash": stringAt(t, preview, "snapshot_hash"), "confirmed": true}
	confirmResponse := performJSON(t, handler, http.MethodPost, "/api/v1/projects/"+projectID+"/delete-confirmations", confirmBody, http.StatusAccepted)
	if deletedID := stringAt(t, objectAt(t, confirmResponse, "data"), "project_id"); deletedID != projectID {
		t.Fatalf("deleted project id = %q", deletedID)
	}
	repeated := performJSON(t, handler, http.MethodPost, "/api/v1/projects/"+projectID+"/delete-confirmations", confirmBody, http.StatusAccepted)
	if repeatedID := stringAt(t, objectAt(t, repeated, "data"), "project_id"); repeatedID != projectID {
		t.Fatalf("idempotent deleted project id = %q", repeatedID)
	}
	projectsResponse := performJSON(t, handler, http.MethodGet, "/api/v1/projects", nil, http.StatusOK)
	if items := arrayAt(t, objectAt(t, projectsResponse, "data"), "items"); len(items) != 0 {
		t.Fatalf("projects after delete = %#v", items)
	}
}

func TestRuntimeHTTPCheckpointFlow(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", defaultTestInternalServiceToken)
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatalf("LoadRegistry() error = %v", err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatalf("runtime.Open() error = %v", err)
	}
	defer store.Close()
	server := NewWithRuntime(
		shell.NewWithAgentService(registry, &scriptedSDKAgent{
			store: store, decide: checkpointAgentDecision,
		}),
		store,
		nil,
	)
	startTestAgentTurnManager(t, server)
	handler := server.Handler()

	projectResponse := performJSON(t, handler, http.MethodPost, "/api/v1/projects", map[string]any{
		"title": "HTTP 闭环",
	}, http.StatusCreated)
	project := objectAt(t, projectResponse, "data")
	projectID := stringAt(t, project, "project_id")
	conversationID := stringAt(t, project, "primary_conversation_id")

	sourceBytes := []byte("第一章 少年下山。")
	uploadResponse := performJSON(t, handler, http.MethodPost, "/api/v1/projects/"+projectID+"/upload-sessions", map[string]any{
		"items": []map[string]any{{
			"client_item_key":     "source_1",
			"kind":                "text",
			"original_filename":   "novel.txt",
			"declared_mime_type":  "text/plain",
			"declared_size_bytes": len(sourceBytes),
		}},
	}, http.StatusCreated)
	uploadItems := arrayAt(t, objectAt(t, uploadResponse, "data"), "items")
	uploadItemID := stringAt(t, uploadItems[0].(map[string]any), "upload_item_id")
	firstUploadContent := performBytes(t, handler, http.MethodPut, "/api/v1/upload-items/"+uploadItemID+"/content", sourceBytes, http.StatusOK)
	repeatedUploadContent := performBytes(t, handler, http.MethodPut, "/api/v1/upload-items/"+uploadItemID+"/content", sourceBytes, http.StatusOK)
	if stringAt(t, objectAt(t, firstUploadContent, "data"), "upload_item_id") !=
		stringAt(t, objectAt(t, repeatedUploadContent, "data"), "upload_item_id") {
		t.Fatal("stream upload retry did not return the original response")
	}
	completeResponse := performJSON(t, handler, http.MethodPost, "/api/v1/upload-items/"+uploadItemID+"/complete", nil, http.StatusCreated)
	assetResult := objectAt(t, completeResponse, "data")
	sourceAsset := objectAt(t, assetResult, "asset")
	sourceSnapshot := objectAt(t, assetResult, "asset_snapshot")
	assetID := stringAt(t, sourceAsset, "asset_id")
	assetSnapshotID := stringAt(t, sourceSnapshot, "asset_snapshot_id")
	contentRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/assets/"+assetID+"/content",
		nil,
	)
	contentResponse := httptest.NewRecorder()
	handler.ServeHTTP(contentResponse, contentRequest)
	if contentResponse.Code != http.StatusOK ||
		!bytes.Equal(contentResponse.Body.Bytes(), sourceBytes) ||
		contentResponse.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf(
			"asset content status=%d type=%q body=%q",
			contentResponse.Code,
			contentResponse.Header().Get("Content-Type"),
			contentResponse.Body.Bytes(),
		)
	}
	rangeRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/assets/"+assetID+"/content",
		nil,
	)
	rangeRequest.Header.Set("Range", "bytes=0-4")
	rangeResponse := httptest.NewRecorder()
	handler.ServeHTTP(rangeResponse, rangeRequest)
	if rangeResponse.Code != http.StatusPartialContent ||
		!bytes.Equal(rangeResponse.Body.Bytes(), sourceBytes[:5]) ||
		rangeResponse.Header().Get("Content-Range") != fmt.Sprintf("bytes 0-4/%d", len(sourceBytes)) {
		t.Fatalf(
			"asset range status=%d content_range=%q body=%x",
			rangeResponse.Code,
			rangeResponse.Header().Get("Content-Range"),
			rangeResponse.Body.Bytes(),
		)
	}

	messageResponse := performJSON(t, handler, http.MethodPost, "/api/v1/conversations/"+conversationID+"/messages", map[string]any{
		"content": "请开始小说转剧本",
		"capability_ref": map[string]any{
			"capability_id": "novel_to_script",
			"version":       "1.4.0",
		},
		"attachment_refs": []map[string]any{{
			"asset_id":          assetID,
			"asset_snapshot_id": assetSnapshotID,
		}},
	}, http.StatusAccepted)
	turnID := stringAt(t, objectAt(t, messageResponse, "data"), "agent_turn_id")
	messageData := testExchangeObject(t, waitForCommittedTestExchange(
		t, store, projectID, "44444444-4444-4444-8444-444444444444", turnID,
	))
	message := objectAt(t, messageData, "user_message")
	messageID := stringAt(t, message, "message_id")
	proposedAction := objectAt(t, messageData, "proposed_action")
	pendingResponse := performJSON(
		t,
		handler,
		http.MethodGet,
		"/api/v1/projects/"+projectID+"/proposed-actions",
		nil,
		http.StatusOK,
	)
	if items := arrayAt(t, objectAt(t, pendingResponse, "data"), "items"); len(items) != 1 {
		t.Fatalf("pending proposed action count = %d, want 1", len(items))
	}
	snapshotResponse := performJSON(
		t,
		handler,
		http.MethodGet,
		"/api/v1/projects/"+projectID+"/snapshot",
		nil,
		http.StatusOK,
	)
	snapshot := objectAt(t, snapshotResponse, "data")
	if snapshotProject := objectAt(t, snapshot, "project"); stringAt(t, snapshotProject, "project_id") != projectID {
		t.Fatalf("snapshot project = %#v", snapshotProject)
	}
	if actions := arrayAt(t, snapshot, "pending_proposed_actions"); len(actions) != 1 {
		t.Fatalf("snapshot pending proposed actions = %#v", actions)
	}
	if snapshot["active_run"] != nil {
		t.Fatalf("snapshot active run before start = %#v, want nil", snapshot["active_run"])
	}

	startBody := map[string]any{
		"capability_id":      "novel_to_script",
		"capability_version": "1.4.0",
		"run_kind":           "generation",
		"conversation_id":    conversationID,
		"input":              proposedAction["input"],
		"config":             proposedAction["config"],
		"confirmation": map[string]any{
			"confirmed":               true,
			"proposed_action_id":      stringAt(t, proposedAction, "proposed_action_id"),
			"action_version":          int(numberAt(t, proposedAction, "version")),
			"confirmation_message_id": stringAt(t, proposedAction, "confirmation_message_id"),
			"snapshot_hash":           stringAt(t, proposedAction, "snapshot_hash"),
		},
	}
	tamperedBody := maps.Clone(startBody)
	tamperedBody["config"] = map[string]any{
		"config_ref": "creation",
		"payload": map[string]any{
			"target_episode_count": 99,
		},
	}
	tamperedResponse := performJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/projects/"+projectID+"/runs",
		tamperedBody,
		map[string]string{"Idempotency-Key": "11111111-1111-4111-8111-111111111111"},
		http.StatusConflict,
	)
	if code := stringAt(t, objectAt(t, tamperedResponse, "error"), "code"); code != "CONFIRMATION_SNAPSHOT_MISMATCH" {
		t.Fatalf("tampered start code = %q", code)
	}
	startResponse := performJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/projects/"+projectID+"/runs",
		startBody,
		map[string]string{"Idempotency-Key": "22222222-2222-4222-8222-222222222222"},
		http.StatusCreated,
	)
	startData := objectAt(t, startResponse, "data")
	run := objectAt(t, startData, "run")
	if status := stringAt(t, run, "status"); status != "waiting_approval" {
		t.Fatalf("run status = %q, want waiting_approval", status)
	}
	assertActionIDs(t, startData, "cancel_run")
	startedSnapshotResponse := performJSON(
		t,
		handler,
		http.MethodGet,
		"/api/v1/projects/"+projectID+"/snapshot",
		nil,
		http.StatusOK,
	)
	startedSnapshot := objectAt(t, startedSnapshotResponse, "data")
	if pending := arrayAt(t, startedSnapshot, "pending_proposed_actions"); len(pending) != 0 {
		t.Fatalf("snapshot pending actions after start = %#v", pending)
	}
	history := arrayAt(t, startedSnapshot, "proposed_actions")
	if len(history) != 1 {
		t.Fatalf("snapshot proposed action history after start = %#v", history)
	}
	historyAction, ok := history[0].(map[string]any)
	if !ok || stringAt(t, historyAction, "status") != "consumed" {
		t.Fatalf("snapshot proposed action history after start = %#v", history)
	}
	if consumedRunID := stringAt(t, historyAction, "consumed_run_id"); consumedRunID != stringAt(t, run, "run_id") {
		t.Fatalf("snapshot consumed run id = %q, want %q", consumedRunID, stringAt(t, run, "run_id"))
	}
	replayResponse := performJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/projects/"+projectID+"/runs",
		startBody,
		map[string]string{"Idempotency-Key": "33333333-3333-4333-8333-333333333333"},
		http.StatusConflict,
	)
	if code := stringAt(t, objectAt(t, replayResponse, "error"), "code"); code != "CONFIRMATION_STALE" {
		t.Fatalf("replayed start code = %q", code)
	}
	artifacts := arrayAt(t, startData, "artifact_summary")
	artifact := artifacts[0].(map[string]any)
	artifactID := stringAt(t, artifact, "artifact_id")
	versionID := stringAt(t, artifact, "current_version_id")

	versionResponse := performJSON(t, handler, http.MethodGet, "/api/v1/artifact-versions/"+versionID, nil, http.StatusOK)
	version := objectAt(t, versionResponse, "data")
	lineageResponse := performJSON(
		t,
		handler,
		http.MethodGet,
		"/api/v1/artifact-versions/"+versionID+"/lineage?max_depth=1",
		nil,
		http.StatusOK,
	)
	lineage := objectAt(t, lineageResponse, "data")
	upstream := arrayAt(t, lineage, "upstream")
	if len(upstream) != 1 ||
		stringAt(t, upstream[0].(map[string]any), "upstream_ref_id") != assetSnapshotID ||
		stringAt(t, upstream[0].(map[string]any), "relation") != "references_source" {
		t.Fatalf("source lineage = %+v", lineage)
	}
	editResponse := performJSON(t, handler, http.MethodPost, "/api/v1/artifacts/"+artifactID+"/versions", map[string]any{
		"base_version_id": versionID,
		"base_version":    int(numberAt(t, version, "version")),
		"change_mode":     "full_payload",
		"new_payload": map[string]any{
			"input_snapshot_id": stringAt(t, run, "current_input_snapshot_version_id"),
			"source_kind":       "novel",
			"assets": []map[string]any{{
				"asset_id":          assetID,
				"asset_snapshot_id": assetSnapshotID,
				"role":              "primary_source",
				"order":             1,
			}},
			"user_request_message_id": messageID,
			"confirmed_strategy_refs": []string{"http_reviewed"},
		},
	}, http.StatusCreated)
	editData := objectAt(t, editResponse, "data")
	editedSourceVersion := objectAt(t, editData, "artifact_version")
	approval := objectAt(t, editData, "approval")
	approvalID := stringAt(t, approval, "approval_request_id")
	assertApprovalRevisionHTTPAdmission(t, server, approval, stringAt(t, editedSourceVersion, "artifact_version_id"))

	resolveResponse := performJSON(t, handler, http.MethodPost, "/api/v1/approvals/"+approvalID+"/resolutions", map[string]any{
		"action":                    "approve",
		"expected_approval_version": 1,
		"subject_snapshot_hash":     stringAt(t, approval, "subject_snapshot_hash"),
	}, http.StatusOK)
	resolvedData := objectAt(t, resolveResponse, "data")
	resolvedRun := objectAt(t, resolvedData, "run")
	if status := stringAt(t, resolvedRun, "status"); status != "paused" {
		t.Fatalf("resolved run status = %q, want paused", status)
	}
	assertActionIDs(t, resolvedData, "resume_run", "cancel_run")
	runID := stringAt(t, resolvedRun, "run_id")
	resumeResponse := performJSON(t, handler, http.MethodPost, "/api/v1/runs/"+runID+"/resume", nil, http.StatusOK)
	if status := stringAt(t, objectAt(t, objectAt(t, resumeResponse, "data"), "run"), "status"); status != "running" {
		t.Fatalf("resumed run status = %q, want running", status)
	}
	resumedData := objectAt(t, resumeResponse, "data")
	assertActionIDs(t, resumedData, "pause_run", "cancel_run")
	if resumedTasks := arrayAt(t, resumedData, "task_items"); len(resumedTasks) != 1 {
		t.Fatalf("resume snapshot task_items = %#v, want one queued task", resumedTasks)
	}
	resumedSteps := arrayAt(t, resumedData, "steps")
	currentStep := resumedSteps[len(resumedSteps)-1].(map[string]any)
	stepRunID := stringAt(t, currentStep, "step_run_id")
	taskResponse := performJSON(t, handler, http.MethodGet, "/api/v1/steps/"+stepRunID+"/tasks", nil, http.StatusOK)
	tasks := arrayAt(t, objectAt(t, taskResponse, "data"), "items")
	if len(tasks) != 1 || stringAt(t, tasks[0].(map[string]any), "status") != "pending" {
		t.Fatalf("queued tasks = %+v", tasks)
	}
	directSnapshotResponse := performJSON(t, handler, http.MethodGet, "/api/v1/runs/"+runID+"/snapshot", nil, http.StatusOK)
	if directTasks := arrayAt(t, objectAt(t, directSnapshotResponse, "data"), "task_items"); len(directTasks) != 1 {
		t.Fatalf("direct run snapshot task_items = %#v, want one queued task", directTasks)
	}
	claimResponse := performInternalJSON(t, handler, http.MethodPost, "/internal/v1/executor/task-claims", map[string]any{
		"worker_id":     "http_worker",
		"executor_ids":  []string{"worker.structured_content"},
		"provider_id":   "http_provider",
		"model_id":      "http_model",
		"lease_seconds": 60,
	}, http.StatusOK)
	claim := objectAt(t, claimResponse, "data")
	attempt := objectAt(t, claim, "attempt")
	contextPack := objectAt(t, claim, "context_pack")
	if stringAt(t, contextPack, "context_hash") != stringAt(t, attempt, "input_snapshot_hash") ||
		stringAt(t, objectAt(t, contextPack, "budget"), "model_id") != "http_model" {
		t.Fatalf("HTTP context pack identity = %+v, attempt = %+v", contextPack, attempt)
	}
	assetContext := arrayAt(t, contextPack, "asset_context")
	if len(assetContext) != 1 {
		t.Fatalf("HTTP context pack asset inputs = %+v", contextPack)
	}
	assetContextItem := assetContext[0].(map[string]any)
	if assetContextItem["content"] != "" ||
		stringAt(t, objectAt(t, contextPack, "output_contract"), "artifact_type") != "task_checkpoint:source_analysis" ||
		len(arrayAt(t, contextPack, "rules")) != 3 {
		t.Fatalf("HTTP context pack inputs = %+v", contextPack)
	}
	sourceAttemptID := stringAt(t, attempt, "attempt_id")
	sourceAttemptToken := stringAt(t, claim, "attempt_token")
	assertExecutionHeartbeatHTTP(t, handler, sourceAttemptID, sourceAttemptToken, stringAt(t, attempt, "input_snapshot_hash"))
	toolRegistry, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	store.SetAgentToolRegistry(toolRegistry)
	assertStatefulToolActivityHTTP(t, handler, projectID, conversationID, sourceAttemptID, sourceAttemptToken)
	sourceAttemptToken = assertExecutionApprovalHTTP(t, handler, projectID, conversationID, sourceAttemptID, sourceAttemptToken, stringAt(t, attempt, "input_snapshot_hash"))
	sourceResultResponse := performInternalJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/internal/v1/executor/attempts/"+sourceAttemptID+"/results",
		map[string]any{
			"input_snapshot_hash": stringAt(t, attempt, "input_snapshot_hash"),
			"response_payload":    validHTTPSourceAnalysisResponse(t, contextPack),
			"usage":               map[string]any{"input_tokens": 8, "output_tokens": 4},
			"trace_ref":           "trace_http_source_analysis",
		},
		map[string]string{"X-Attempt-Token": sourceAttemptToken},
		http.StatusAccepted,
	)
	sourceReceived := objectAt(t, sourceResultResponse, "data")
	sourceCommitResponse := performInternalJSON(
		t,
		handler,
		http.MethodPost,
		"/internal/v1/executor/attempts/"+sourceAttemptID+"/commit",
		map[string]any{
			"expected_response_hash": stringAt(t, sourceReceived, "response_hash"),
		},
		http.StatusCreated,
	)
	if status := stringAt(t, objectAt(t, sourceCommitResponse, "data"), "commit_status"); status != "task_checkpointed" {
		t.Fatalf("source analysis commit status = %q, want task_checkpointed", status)
	}

	claimResponse = performInternalJSON(t, handler, http.MethodPost, "/internal/v1/executor/task-claims", map[string]any{
		"worker_id":     "http_worker",
		"executor_ids":  []string{"worker.structured_content"},
		"provider_id":   "http_provider",
		"model_id":      "http_model",
		"lease_seconds": 60,
	}, http.StatusOK)
	claim = objectAt(t, claimResponse, "data")
	attempt = objectAt(t, claim, "attempt")
	contextPack = objectAt(t, claim, "context_pack")
	if stringAt(t, objectAt(t, contextPack, "output_contract"), "artifact_type") != "story_bible" ||
		!strings.Contains(stringAt(t, objectAt(t, contextPack, "prompt"), "content"), "来源分析聚合") {
		t.Fatalf("HTTP story aggregate context = %+v", contextPack)
	}
	attemptID := stringAt(t, attempt, "attempt_id")
	attemptToken := stringAt(t, claim, "attempt_token")
	resultResponse := performInternalJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/internal/v1/executor/attempts/"+attemptID+"/results",
		map[string]any{
			"input_snapshot_hash": stringAt(t, attempt, "input_snapshot_hash"),
			"response_payload":    validHTTPStoryBibleResponse(),
			"usage":               map[string]any{"input_tokens": 10, "output_tokens": 5},
			"trace_ref":           "trace_http_1",
		},
		map[string]string{"X-Attempt-Token": attemptToken},
		http.StatusAccepted,
	)
	if status := stringAt(t, objectAt(t, resultResponse, "data"), "status"); status != "result_received" {
		t.Fatalf("attempt status = %q, want result_received", status)
	}
	receivedAttempt := objectAt(t, resultResponse, "data")
	commitResponse := performInternalJSON(
		t,
		handler,
		http.MethodPost,
		"/internal/v1/executor/attempts/"+attemptID+"/commit",
		map[string]any{
			"expected_response_hash": stringAt(t, receivedAttempt, "response_hash"),
		},
		http.StatusCreated,
	)
	commitData := objectAt(t, commitResponse, "data")
	if status := stringAt(t, objectAt(t, commitData, "artifact_version"), "status"); status != "pending_approval" {
		t.Fatalf("committed artifact version status = %q, want pending_approval", status)
	}
	if status := stringAt(t, objectAt(t, objectAt(t, commitData, "run_snapshot"), "run"), "status"); status != "waiting_approval" {
		t.Fatalf("committed run status = %q, want waiting_approval", status)
	}
	impactEditResponse := performJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/artifacts/"+artifactID+"/versions",
		map[string]any{
			"base_version_id": stringAt(t, editedSourceVersion, "artifact_version_id"),
			"base_version":    int(numberAt(t, editedSourceVersion, "version")),
			"change_mode":     "full_payload",
			"new_payload": map[string]any{
				"input_snapshot_id": stringAt(t, run, "current_input_snapshot_version_id"),
				"source_kind":       "novel",
				"assets": []map[string]any{{
					"asset_id":          assetID,
					"asset_snapshot_id": assetSnapshotID,
					"role":              "primary_source",
					"order":             1,
				}},
				"user_request_message_id": messageID,
				"confirmed_strategy_refs": []string{"http_reviewed", "impact_reviewed"},
			},
		},
		map[string]string{"Idempotency-Key": "55555555-5555-4555-8555-555555555555"},
		http.StatusCreated,
	)
	impactEditData := objectAt(t, impactEditResponse, "data")
	impactReview := objectAt(t, impactEditData, "impact_review")
	if len(arrayAt(t, impactReview, "affected_items")) != 1 ||
		stringAt(t, impactReview, "status") != "regeneration_planned" {
		t.Fatalf("HTTP impact review = %+v", impactReview)
	}
	impactReviewID := stringAt(t, impactReview, "impact_review_id")
	storedImpactResponse := performJSON(
		t,
		handler,
		http.MethodGet,
		"/api/v1/impact-reviews/"+impactReviewID,
		nil,
		http.StatusOK,
	)
	storedImpact := objectAt(t, storedImpactResponse, "data")
	if stringAt(t, storedImpact, "snapshot_hash") != stringAt(t, impactReview, "snapshot_hash") {
		t.Fatalf("stored HTTP impact review = %+v", storedImpact)
	}
	propagation := objectAt(t, impactEditData, "propagation")
	if stringAt(t, objectAt(t, propagation, "dependency_decision"), "action") != "regenerate_downstream" ||
		propagation["regeneration_plan"] == nil ||
		stringAt(t, objectAt(t, objectAt(t, propagation, "run_snapshot"), "run"), "status") != "paused" {
		t.Fatalf("HTTP automatic propagation = %+v", propagation)
	}
	cancelResponse := performJSON(t, handler, http.MethodPost, "/api/v1/runs/"+runID+"/cancel", map[string]any{
		"confirmation": map[string]any{"confirmed": true},
	}, http.StatusOK)
	cancelData := objectAt(t, cancelResponse, "data")
	if status := stringAt(t, objectAt(t, cancelData, "run"), "status"); status != "cancelled" {
		t.Fatalf("cancelled run status = %q, want cancelled", status)
	}
	if len(arrayAt(t, cancelData, "artifact_summary")) == 0 {
		t.Fatal("cancel removed existing artifacts")
	}
	expiredImpactResponse := performJSON(
		t,
		handler,
		http.MethodGet,
		"/api/v1/impact-reviews/"+impactReviewID,
		nil,
		http.StatusOK,
	)
	if status := stringAt(t, objectAt(t, expiredImpactResponse, "data"), "status"); status != "regeneration_planned" {
		t.Fatalf("resolved impact review after cancel status = %q, want regeneration_planned", status)
	}
	deletePreviewResponse := performJSON(
		t,
		handler,
		http.MethodPost,
		"/api/v1/assets/"+assetID+"/delete-previews",
		nil,
		http.StatusCreated,
	)
	deletePreview := objectAt(t, deletePreviewResponse, "data")
	if !deletePreview["existing_artifacts_will_be_preserved"].(bool) ||
		len(arrayAt(t, deletePreview, "artifact_impacts")) == 0 {
		t.Fatalf("delete preview = %+v", deletePreview)
	}
	deleteResponse := performJSON(
		t,
		handler,
		http.MethodPost,
		"/api/v1/assets/"+assetID+"/delete-confirmations",
		map[string]any{
			"preview_hash": stringAt(t, deletePreview, "snapshot_hash"),
			"confirmed":    true,
		},
		http.StatusAccepted,
	)
	if status := stringAt(t, objectAt(t, objectAt(t, deleteResponse, "data"), "asset"), "status"); status != "deleted" {
		t.Fatalf("deleted asset status = %q", status)
	}
	deletedContentRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/assets/"+assetID+"/content",
		nil,
	)
	deletedContentResponse := httptest.NewRecorder()
	handler.ServeHTTP(deletedContentResponse, deletedContentRequest)
	if deletedContentResponse.Code != http.StatusGone {
		t.Fatalf(
			"deleted asset content status=%d body=%s",
			deletedContentResponse.Code,
			deletedContentResponse.Body.String(),
		)
	}
	var deletedContentPayload map[string]any
	if err := json.Unmarshal(deletedContentResponse.Body.Bytes(), &deletedContentPayload); err != nil {
		t.Fatalf("decode deleted content response: %v", err)
	}
	if code := stringAt(t, objectAt(t, deletedContentPayload, "error"), "code"); code != "ASSET_SOURCE_DELETED" {
		t.Fatalf("deleted content error code = %q", code)
	}
}

func validHTTPStoryBibleResponse() map[string]any {
	return map[string]any{
		"story_bible": map[string]any{
			"story_overview": map[string]any{
				"one_sentence_logline": "少年下山后卷入旧案。",
				"core_conflict":        "少年追查真相与幕后势力冲突。",
				"main_emotional_drive": "守护同伴并查明身世。",
				"genre_tags":           []string{"逆袭"},
			},
			"source_structure": []map[string]any{{
				"source_unit_id":             "SRC-C001-B001",
				"source_range":               "第一章",
				"summary":                    "少年下山。",
				"key_events":                 []string{"少年离开师门"},
				"character_changes":          []string{"主角进入陌生环境"},
				"conflict_stage":             "建立冲突",
				"hook_or_suspense_potential": "high",
				"source_evidence":            []any{},
			}},
			"characters":               []any{},
			"relationships":            []any{},
			"world_rules":              []string{},
			"major_plotline":           []any{},
			"climax_map":               map[string]any{},
			"foreshadowing_and_payoff": []any{},
			"must_keep_facts":          []string{"少年下山"},
			"short_drama_assets":       map[string]any{},
			"adaptation_risks":         []string{},
		},
		"source_trace": map[string]any{
			"from_source_text": []string{"原文明确写出少年下山。"},
			"model_inference":  []string{},
		},
		"next_action": map[string]any{"type": "continue"},
	}
}

func validHTTPSourceAnalysisResponse(
	t *testing.T,
	contextPack map[string]any,
) map[string]any {
	t.Helper()
	cursor := objectAt(t, contextPack, "task_cursor")
	batch := objectAt(t, cursor, "batch")
	sourceUnits := arrayAt(t, batch, "source_units")
	units := make([]map[string]any, 0, len(sourceUnits))
	covered := make([]string, 0, len(sourceUnits))
	for _, raw := range sourceUnits {
		sourceUnit := raw.(map[string]any)
		sourceUnitID := stringAt(t, sourceUnit, "source_unit_id")
		units = append(units, map[string]any{
			"source_unit_id":             sourceUnitID,
			"summary":                    stringAt(t, sourceUnit, "text"),
			"key_events":                 []string{"来源单元事件"},
			"character_changes":          []string{},
			"conflict_stage":             "来源发展",
			"hook_or_suspense_potential": "medium",
			"source_refs": []map[string]any{{
				"source_type":       "asset_text_range",
				"asset_id":          stringAt(t, sourceUnit, "asset_id"),
				"asset_snapshot_id": stringAt(t, sourceUnit, "asset_snapshot_id"),
				"source_unit_id":    sourceUnitID,
				"range_label":       sourceUnitID,
			}},
			"claims": []any{},
		})
		covered = append(covered, sourceUnitID)
	}
	return map[string]any{
		"source_kind": "novel",
		"units":       units,
		"coverage_check": map[string]any{
			"covered_source_unit_ids": covered,
			"missing_source_unit_ids": []string{},
			"order_issues":            []string{},
		},
		"source_trace": map[string]any{
			"grounded": []any{},
			"inferred": []any{},
			"claims":   []any{},
		},
	}
}

func performBytes(t *testing.T, handler http.Handler, method, path string, body []byte, wantStatus int) map[string]any {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("Idempotency-Key", "44444444-4444-4444-8444-444444444444")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("%s %s status = %d, want %d; body = %s", method, path, response.Code, wantStatus, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v; body = %s", err, response.Body.String())
	}
	return payload
}

func checkpointAgentDecision(
	input agentcontract.AgentInput,
) (agentcontract.AgentDecision, error) {
	assets := make([]map[string]any, 0, len(input.Request.AttachmentRefs))
	for index, attachment := range input.Request.AttachmentRefs {
		assets = append(assets, map[string]any{
			"asset_id":          attachment.AssetID,
			"asset_snapshot_id": attachment.AssetSnapshotID,
			"role":              "primary_source",
			"order":             index + 1,
		})
	}
	runInput, _ := json.Marshal(map[string]any{
		"project_id":  input.ProjectID,
		"source_type": "novel",
		"assets":      assets,
		"user_notes":  []string{},
	})
	config, _ := json.Marshal(map[string]any{
		"config_ref": "creation",
		"payload": map[string]any{
			"target_episode_count":            12,
			"episode_duration_minutes":        2,
			"preserve_existing_episode_marks": true,
			"expansion_policy":                "confirm_if_needed",
			"user_requirements":               []string{},
		},
	})
	return agentcontract.AgentDecision{
		Reply:         "配置已就绪，请确认开始。",
		Intent:        "propose_capability",
		Confidence:    1,
		CapabilityRef: input.Request.CapabilityRef,
		ProposedAction: &agentcontract.ProposedActionDraft{
			ActionType:           "start_run",
			CapabilityRef:        input.Request.CapabilityRef,
			Input:                runInput,
			Config:               config,
			RequiresConfirmation: true,
		},
	}, nil
}

func performJSON(t *testing.T, handler http.Handler, method, path string, body any, wantStatus int) map[string]any {
	t.Helper()
	return performJSONWithHeaders(t, handler, method, path, body, nil, wantStatus)
}

const defaultTestInternalServiceToken = "internal-test-token"

func performInternalJSON(
	t *testing.T,
	handler http.Handler,
	method string,
	path string,
	body any,
	wantStatus int,
) map[string]any {
	t.Helper()
	return performInternalJSONWithHeaders(t, handler, method, path, body, nil, wantStatus)
}

func performInternalJSONWithHeaders(
	t *testing.T,
	handler http.Handler,
	method string,
	path string,
	body any,
	headers map[string]string,
	wantStatus int,
) map[string]any {
	t.Helper()
	merged := maps.Clone(headers)
	if merged == nil {
		merged = map[string]string{}
	}
	merged["Authorization"] = "Bearer " + defaultTestInternalServiceToken
	return performJSONWithHeaders(t, handler, method, path, body, merged, wantStatus)
}

func performJSONWithHeaders(
	t *testing.T,
	handler http.Handler,
	method string,
	path string,
	body any,
	headers map[string]string,
	wantStatus int,
) map[string]any {
	t.Helper()
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		request.Header.Set("Idempotency-Key", "44444444-4444-4444-8444-444444444444")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("%s %s status = %d, want %d; body = %s", method, path, response.Code, wantStatus, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v; body = %s", err, response.Body.String())
	}
	return payload
}

func objectAt(t *testing.T, source map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := source[key].(map[string]any)
	if !ok {
		t.Fatalf("%q = %#v, want object", key, source[key])
	}
	return value
}

func arrayAt(t *testing.T, source map[string]any, key string) []any {
	t.Helper()
	value, ok := source[key].([]any)
	if !ok {
		t.Fatalf("%q = %#v, want array", key, source[key])
	}
	return value
}

func stringAt(t *testing.T, source map[string]any, key string) string {
	t.Helper()
	value, ok := source[key].(string)
	if !ok || value == "" {
		t.Fatalf("%q = %#v, want non-empty string", key, source[key])
	}
	return value
}

func numberAt(t *testing.T, source map[string]any, key string) float64 {
	t.Helper()
	value, ok := source[key].(float64)
	if !ok {
		t.Fatalf("%q = %#v, want number", key, source[key])
	}
	return value
}

func assertActionIDs(t *testing.T, source map[string]any, want ...string) {
	t.Helper()
	raw, ok := source["available_actions"].([]any)
	if !ok {
		t.Fatalf("available_actions = %#v, want array", source["available_actions"])
	}
	got := make([]string, 0, len(raw))
	for _, item := range raw {
		action, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("available action = %#v, want object", item)
		}
		got = append(got, stringAt(t, action, "action_id"))
	}
	if !slices.Equal(got, want) {
		t.Fatalf("available action IDs = %v, want %v", got, want)
	}
}

func TestRegenerationScopePrefersExplicitBatchLanguageOverCurrentFocus(t *testing.T) {
	for _, test := range []struct {
		content string
		want    string
	}{
		{content: "重新跑第1集", want: "artifact"},
		{content: "都重新解析", want: "step"},
		{content: "全部重新识别", want: "step"},
		{content: "把这批视频重新解析", want: "step"},
	} {
		if got := regenerationScope(test.content); got != test.want {
			t.Fatalf("regenerationScope(%q) = %q, want %q", test.content, got, test.want)
		}
	}
}

func TestTerminalRevisionReply(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{status: "accepted", want: ""},
		{status: "proposed", want: "修改稿已生成，等待你确认后保存。"},
		{status: "cancelled", want: "本次未生成有效修改，原版本未受影响。请补充更具体的修改要求后重试。"},
		{status: "failed", want: "修改稿生成失败，原版本未受影响。可重试本次修改。"},
		{status: "running", want: ""},
	}
	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			if got := terminalRevisionReply(businessruntime.RevisionRequest{Status: test.status}); got != test.want {
				t.Fatalf("terminalRevisionReply(%q) = %q, want %q", test.status, got, test.want)
			}
		})
	}
}
