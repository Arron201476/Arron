package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestProjectSkillDraftHTTPValidationDownloadConfirmationAndSDKApproval(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "draft-service")
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "draft.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tools, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	store.SetAgentToolRegistry(tools)
	auth, err := NewStaticTokenAuthenticator([]StaticTokenPrincipal{
		{Token: "draft-owner", UserID: "draft_owner", WorkspaceID: "draft_workspace", Role: identity.RoleOwner},
		{Token: "draft-viewer", UserID: "draft_viewer", WorkspaceID: "draft_workspace", Role: identity.RoleViewer},
		{Token: "draft-foreign", UserID: "draft_foreign", WorkspaceID: "foreign_workspace", Role: identity.RoleOwner},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithRuntimeRevisionAndTools(shell.New(registry), store, nil, tools, nil)
	if err := server.ConfigureAuthentication(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	owner := identity.Principal{Kind: identity.KindUser, UserID: "draft_owner", WorkspaceID: "draft_workspace", Role: identity.RoleOwner}
	ctx := identity.WithPrincipal(context.Background(), owner)
	project, err := store.CreateProject(ctx, "Skill draft HTTP")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Write and install my Skill"}, businessruntime.CommandMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil {
		t.Fatal(err)
	}
	service := map[string]string{"Authorization": "Bearer draft-service", "X-Agent-Project-ID": project.ProjectID, "X-Agent-Turn-ID": turn.AgentTurnID}
	root := "skills/http-draft"
	text := "---\nname: http-draft\ndescription: Review story outlines when requested.\n---\n# Review\nReport narrative gaps.\n"
	patch := map[string]any{"path": root + "/SKILL.md", "expected_version": 0, "operation": "create_file", "diff": "+draft"}
	call := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, "/internal/v1/agent-tool-calls", map[string]any{
		"project_id": project.ProjectID, "conversation_id": project.PrimaryConversationID, "agent_turn_id": turn.AgentTurnID,
		"sdk_tool_call_id": "sdk-draft-file", "tool_id": "runtime:apply_workspace_patch", "arguments": patch,
	}, service, http.StatusCreated), "data")
	performJSONWithHeaders(t, handler, http.MethodPost, "/internal/v1/agent-tool-calls/"+stringAt(t, call, "agent_tool_call_id")+"/project-file-patches", map[string]any{"sdk_tool_call_id": "sdk-draft-file", "patch": patch, "content": text}, service, http.StatusOK)
	base := "/api/v1/projects/" + project.ProjectID + "/skill-drafts"
	previewURL := base + "/preview?" + url.Values{"root_path": {root}}.Encode()
	preview := objectAt(t, performJSONWithHeaders(t, handler, http.MethodGet, previewURL, nil, bearer("draft-viewer", ""), http.StatusOK), "data")
	if preview["status"] != "valid" {
		t.Fatalf("preview: %+v", preview)
	}
	performJSONWithHeaders(t, handler, http.MethodGet, previewURL, nil, bearer("draft-foreign", ""), http.StatusNotFound)
	archiveURL := base + "/archive?" + url.Values{"root_path": {root}, "snapshot_hash": {stringAt(t, preview, "snapshot_hash")}}.Encode()
	request := httptest.NewRequest(http.MethodGet, archiveURL, nil)
	request.Header.Set("Authorization", "Bearer draft-viewer")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	kind, params, headerErr := mime.ParseMediaType(response.Header().Get("Content-Disposition"))
	archive, archiveErr := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if response.Code != http.StatusOK || headerErr != nil || archiveErr != nil || kind != "attachment" || params["filename"] != "http-draft.zip" || len(archive.File) != 1 || archive.File[0].Name != "SKILL.md" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download: status=%d headers=%v zip=%v header=%v", response.Code, response.Header(), archiveErr, headerErr)
	}
	performJSONWithHeaders(t, handler, http.MethodGet, base+"/archive?root_path="+url.QueryEscape(root)+"&snapshot_hash=stale", nil, bearer("draft-owner", ""), http.StatusConflict)
	body := map[string]any{"root_path": root, "snapshot_hash": preview["snapshot_hash"], "scope": "user", "installation_id": "", "expected_active_version_id": "", "confirmed": false}
	key := "22222222-2222-4222-8222-222222222222"
	performJSONWithHeaders(t, handler, http.MethodPost, base+"/installations", body, bearer("draft-owner", key), http.StatusBadRequest)
	body["confirmed"] = true
	performJSONWithHeaders(t, handler, http.MethodPost, base+"/installations", body, bearer("draft-viewer", key), http.StatusForbidden)
	missingKey := bearer("draft-owner", "")
	missingKey["Idempotency-Key"] = ""
	performJSONWithHeaders(t, handler, http.MethodPost, base+"/installations", body, missingKey, http.StatusBadRequest)
	saved := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, base+"/installations", body, bearer("draft-owner", key), http.StatusOK), "data")
	replay := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, base+"/installations", body, bearer("draft-owner", key), http.StatusOK), "data")
	if objectAt(t, replay, "receipt")["receipt_id"] != objectAt(t, saved, "receipt")["receipt_id"] {
		t.Fatal("idempotent HTTP install returned a different receipt")
	}
	args := map[string]any{"root_path": root, "snapshot_hash": preview["snapshot_hash"], "scope": "project", "installation_id": "", "expected_active_version_id": ""}
	installCall := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, "/internal/v1/agent-tool-calls", map[string]any{
		"project_id": project.ProjectID, "conversation_id": project.PrimaryConversationID, "agent_turn_id": turn.AgentTurnID,
		"sdk_tool_call_id": "sdk-draft-install", "tool_id": "runtime:install_workspace_skill", "arguments": args,
	}, service, http.StatusCreated), "data")
	installPath := "/internal/v1/agent-tool-calls/" + stringAt(t, installCall, "agent_tool_call_id")
	installBody := map[string]any{"sdk_tool_call_id": "sdk-draft-install", "arguments": args}
	performJSONWithHeaders(t, handler, http.MethodPost, installPath+"/skill-installations", installBody, bearer("draft-owner", ""), http.StatusUnauthorized)
	performJSONWithHeaders(t, handler, http.MethodPost, installPath+"/skill-installations", installBody, service, http.StatusConflict)
	approval := objectAt(t, installCall, "approval")
	_, err = store.ResolveAgentToolApproval(ctx, businessruntime.ResolveAgentToolApprovalCommand{AgentToolApprovalID: stringAt(t, approval, "agent_tool_approval_id"), ExpectedVersion: int(approval["version"].(float64)), SubjectSnapshotHash: stringAt(t, approval, "subject_snapshot_hash"), Action: "approve", ActorRef: owner.UserID})
	if err != nil {
		t.Fatal(err)
	}
	performJSONWithHeaders(t, handler, http.MethodPost, installPath+"/start", map[string]any{"expected_sdk_tool_call_id": "sdk-draft-install"}, service, http.StatusOK)
	installed := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, installPath+"/skill-installations", installBody, service, http.StatusOK), "data")
	if objectAt(t, installed, "installation")["registry_status"] != "available" {
		t.Fatalf("SDK catalog not updated: %+v", installed)
	}
	if _, err := store.CancelAgentTurn(ctx, turn.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	performJSONWithHeaders(t, handler, http.MethodPost, installPath+"/skill-installations", installBody, service, http.StatusForbidden)
}
