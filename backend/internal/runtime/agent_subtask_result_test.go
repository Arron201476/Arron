package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"content-agent/backend/internal/identity"
)

func beginSubtaskFixture(t *testing.T, store *Store, project Project, turnID, sdkID string) AgentToolCall {
	t.Helper()
	call, err := store.BeginAgentToolCall(context.Background(), BeginAgentToolCallCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID, AgentTurnID: turnID,
		SDKToolCallID: sdkID, ToolID: delegateSubtaskTool, Arguments: json.RawMessage(`{"title":"Review","task":"Analyze","materials":"private supplied material"}`)})
	if err != nil {
		t.Fatal(err)
	}
	return call
}

func subtaskReadFixture(t *testing.T, store *Store, parent AgentToolCall, name string, toolID string, args json.RawMessage) AgentToolCall {
	t.Helper()
	call, err := store.BeginAgentToolCall(context.Background(), BeginAgentToolCallCommand{ProjectID: parent.ProjectID, ConversationID: parent.ConversationID, AgentTurnID: *parent.AgentTurnID,
		SDKToolCallID: "subtask:" + sha256Hex([]byte(parent.SDKToolCallID))[:24] + ":" + sha256Hex([]byte(name))[:24], ToolID: toolID, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if call.ParentToolCallID != parent.AgentToolCallID {
		t.Fatalf("read lost parent: %+v", call)
	}
	return call
}

func encodedSubtaskResult(t *testing.T, result AgentSubtaskResult) json.RawMessage {
	t.Helper()
	content, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := json.Marshal(string(content))
	if err != nil {
		t.Fatal(err)
	}
	return wrapped
}

func TestSubtaskResultsVerifyReadsPersistFullTextAndSurviveRebuild(t *testing.T) {
	ctx := context.Background()
	store, project, turn, path := pauseTurnFixture(t)
	defer func() { store.Close() }()
	claimInputTurn(t, store, turn.AgentTurnID)
	parent := beginSubtaskFixture(t, store, project, turn.AgentTurnID, "parent")
	if strings.Contains(string(parent.ArgumentsSummary), "private") {
		t.Fatal("material leaked into shared audit")
	}
	artifact := createDeliveryArtifact(t, store, project, "generic_document", `{"content_markdown":"Source text"}`)
	ref := SubtaskArtifactReference{artifact.ArtifactID, artifact.CurrentVersionID}
	args, _ := json.Marshal(map[string]string{"artifact_version_id": ref.ArtifactVersionID})
	read := subtaskReadFixture(t, store, parent, "read", "runtime:inspect_artifact_version", args)
	result := AgentSubtaskResult{SchemaVersion: "agent_subtask.v1", Text: strings.Repeat("Conclusion. ", 2500), ReadCallIDs: []string{read.AgentToolCallID}, InspectedArtifacts: []SubtaskArtifactReference{ref}}
	complete := func(value AgentSubtaskResult) (AgentToolCall, error) {
		return store.CompleteAgentToolCall(ctx, CompleteAgentToolCallCommand{AgentToolCallID: parent.AgentToolCallID, Result: encodedSubtaskResult(t, value)})
	}
	_, err := complete(result)
	assertDomainCode(t, err, "AGENT_SUBTASK_RESULT_INVALID")
	_, err = store.GetAgentSubtaskResult(ctx, parent.AgentToolCallID)
	assertDomainCode(t, err, "AGENT_SUBTASK_RESULT_NOT_FOUND")
	version, err := store.GetArtifactVersion(ctx, ref.ArtifactVersionID)
	if err != nil {
		t.Fatal(err)
	}
	readResult, _ := json.Marshal(map[string]any{"data": version})
	readResult, _ = json.Marshal(string(readResult))
	if _, err := store.CompleteAgentToolCall(ctx, CompleteAgentToolCallCommand{AgentToolCallID: read.AgentToolCallID, Result: readResult}); err != nil {
		t.Fatal(err)
	}
	bad := result
	bad.ReadCallIDs = []string{"foreign-call"}
	_, err = complete(bad)
	assertDomainCode(t, err, "AGENT_SUBTASK_RESULT_INVALID")
	bad = result
	bad.InspectedArtifacts = []SubtaskArtifactReference{{ref.ArtifactID, "unread-version"}}
	_, err = complete(bad)
	assertDomainCode(t, err, "AGENT_SUBTASK_RESULT_INVALID")
	bad = result
	bad.ReadCallIDs = []string{}
	bad.InspectedArtifacts = []SubtaskArtifactReference{}
	_, err = complete(bad)
	assertDomainCode(t, err, "AGENT_SUBTASK_RESULT_INVALID")
	completed, err := complete(result)
	if err != nil || completed.Status != "completed" || strings.Contains(string(completed.ResultSummary), "Conclusion") {
		t.Fatalf("completion %+v %v", completed, err)
	}
	if _, err := complete(result); err != nil {
		t.Fatal("lost completion acknowledgement was not idempotent", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	view, err := store.GetAgentSubtaskResult(ctx, parent.AgentToolCallID)
	if err != nil || view.Text != result.Text || view.ProjectID != project.ProjectID || view.AgentToolCallID != parent.AgentToolCallID || len(view.InspectedArtifacts) != 1 {
		t.Fatalf("full result lost: %+v %v", view, err)
	}
	foreign := identity.WithPrincipal(ctx, identity.Principal{Kind: identity.KindUser, UserID: "foreign", WorkspaceID: "foreign", Role: identity.RoleOwner})
	_, err = store.GetAgentSubtaskResult(foreign, parent.AgentToolCallID)
	assertDomainCode(t, err, "PROJECT_NOT_FOUND")
	if _, err := store.db.Exec(`UPDATE agent_subtask_results SET result_json='{}' WHERE agent_tool_call_id=?`, parent.AgentToolCallID); err != nil {
		t.Fatal(err)
	}
	_, err = store.GetAgentSubtaskResult(ctx, parent.AgentToolCallID)
	assertDomainCode(t, err, "AGENT_SUBTASK_RESULT_INVALID")
}

func TestSubtaskReadBoundaryRejectsWrongParentWritesAndExcessiveCalls(t *testing.T) {
	ctx := context.Background()
	store, project, turn, _ := pauseTurnFixture(t)
	defer store.Close()
	claimInputTurn(t, store, turn.AgentTurnID)
	parent := beginSubtaskFixture(t, store, project, turn.AgentTurnID, "parent")
	command := BeginAgentToolCallCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID, AgentTurnID: turn.AgentTurnID,
		SDKToolCallID: "subtask:" + strings.Repeat("0", 24) + ":" + strings.Repeat("1", 24), ToolID: "runtime:inspect_project", Arguments: json.RawMessage(`{}`)}
	_, err := store.BeginAgentToolCall(ctx, command)
	assertDomainCode(t, err, "AGENT_ACTIVITY_SCOPE_MISMATCH")
	command.SDKToolCallID = "subtask:" + sha256Hex([]byte(parent.SDKToolCallID))[:24] + ":" + strings.Repeat("1", 24)
	command.ToolID = "runtime:apply_workspace_patch"
	_, err = store.BeginAgentToolCall(ctx, command)
	assertDomainCode(t, err, "AGENT_ACTIVITY_SCOPE_MISMATCH")
	var first AgentToolCall
	for index := range 64 {
		read := subtaskReadFixture(t, store, parent, fmt.Sprint(index), "runtime:inspect_project", json.RawMessage(`{}`))
		if index == 0 {
			first = read
		}
	}
	command.ToolID = "runtime:inspect_project"
	_, err = store.BeginAgentToolCall(ctx, command)
	assertDomainCode(t, err, "AGENT_SUBTASK_LIMIT")
	command.SDKToolCallID = first.SDKToolCallID
	if replay, err := store.BeginAgentToolCall(ctx, command); err != nil || replay.AgentToolCallID != first.AgentToolCallID {
		t.Fatal("read replay consumed quota", err)
	}
	command.AgentTurnID = "different-turn"
	_, err = store.BeginAgentToolCall(ctx, command)
	assertDomainCode(t, err, "AGENT_ACTIVITY_SCOPE_MISMATCH")
	if _, err := store.CancelAgentToolCall(ctx, CancelAgentToolCallCommand{AgentToolCallID: parent.AgentToolCallID, Reason: "stop"}); err != nil {
		t.Fatal(err)
	}
	_, err = store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: first.AgentToolCallID, ExpectedSDKToolCallID: first.SDKToolCallID})
	assertDomainCode(t, err, "AGENT_TOOL_CALL_STATE_CONFLICT")
	_, err = store.CompleteAgentToolCall(ctx, CompleteAgentToolCallCommand{AgentToolCallID: first.AgentToolCallID, Result: json.RawMessage(`{}`)})
	assertDomainCode(t, err, "AGENT_TOOL_CALL_STATE_CONFLICT")
}

func TestSubtaskResultQuotaDeletionAndMigrationBackup(t *testing.T) {
	for _, deletion := range []string{"project", "workspace"} {
		t.Run(deletion, func(t *testing.T) {
			ctx := context.Background()
			store, project, turn, path := pauseTurnFixture(t)
			defer func() { store.Close() }()
			claimInputTurn(t, store, turn.AgentTurnID)
			parent := beginSubtaskFixture(t, store, project, turn.AgentTurnID, "parent")
			if _, err := store.db.Exec(`DROP TABLE agent_subtask_results; DROP TABLE agent_subtask_reads; PRAGMA user_version=52;`); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			var err error
			store, err = Open(path, loadTestRegistry(t))
			if err != nil {
				t.Fatal(err)
			}
			var backup string
			if err := store.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version=52 AND to_version=? AND status='completed'`, schemaVersion).Scan(&backup); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(backup); err != nil {
				t.Fatal(err)
			}
			before, err := store.CreateProjectDeletePreview(ctx, CreateProjectDeletePreviewCommand{ProjectID: project.ProjectID})
			if err != nil {
				t.Fatal(err)
			}
			result := AgentSubtaskResult{SchemaVersion: "agent_subtask.v1", Text: "Draft", ReadCallIDs: []string{}, InspectedArtifacts: []SubtaskArtifactReference{}}
			command := CompleteAgentToolCallCommand{AgentToolCallID: parent.AgentToolCallID, Result: encodedSubtaskResult(t, result)}
			if _, err := store.db.Exec(`UPDATE workspace_quotas SET max_storage_bytes=1 WHERE workspace_id=?`, project.WorkspaceID); err != nil {
				t.Fatal(err)
			}
			_, err = store.CompleteAgentToolCall(ctx, command)
			assertDomainCode(t, err, "WORKSPACE_QUOTA_EXCEEDED")
			if _, err := store.db.Exec(`UPDATE workspace_quotas SET max_storage_bytes=100000 WHERE workspace_id=?`, project.WorkspaceID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.CompleteAgentToolCall(ctx, command); err != nil {
				t.Fatal(err)
			}
			if err := store.CheckWorkspaceStorageQuota(ctx, project.WorkspaceID, 100000); err == nil {
				t.Fatal("stored result did not count toward quota")
			}
			if deletion == "project" {
				latest, err := store.CreateProjectDeletePreview(ctx, CreateProjectDeletePreviewCommand{ProjectID: project.ProjectID})
				if err != nil || before.SnapshotHash == latest.SnapshotHash || latest.Impact.SubtaskResultCount != 1 {
					t.Fatal("result omitted from delete preview", err)
				}
				_, err = store.ConfirmProjectDelete(ctx, ConfirmProjectDeleteCommand{ProjectID: project.ProjectID, PreviewHash: latest.SnapshotHash, Confirmed: true})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				_, err = store.DeleteWorkspace(ctx, DeleteWorkspaceCommand{WorkspaceID: project.WorkspaceID, UserID: identity.DefaultUserID, Confirmation: project.WorkspaceID})
				if err != nil {
					t.Fatal(err)
				}
			}
			var count int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM agent_subtask_results`).Scan(&count); err != nil || count != 0 {
				t.Fatal("deleted result retained", err)
			}
			_, err = store.GetAgentSubtaskResult(ctx, parent.AgentToolCallID)
			assertDomainCode(t, err, "PROJECT_NOT_FOUND")
		})
	}
}
