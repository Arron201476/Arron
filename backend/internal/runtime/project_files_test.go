package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/identity"
)

func openProjectFilesTestStore(t *testing.T, database string) *Store {
	t.Helper()
	store, err := Open(database, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	store.SetAgentToolRegistry(registry)
	return store
}

func beginProjectFileCall(t *testing.T, store *Store, project Project, patch ProjectFilePatch) AgentToolCall {
	t.Helper()
	arguments, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, ok := store.agentTools.Get("runtime:apply_workspace_patch")
	if !ok {
		t.Fatal("workspace patch tool not registered")
	}
	call, err := store.BeginAgentToolCall(context.Background(), BeginAgentToolCallCommand{
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		SDKToolCallID: store.newID("sdk_file"), ToolID: "runtime:apply_workspace_patch", Arguments: arguments,
		ConfigurationHash: descriptor.ConfigurationHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	return call
}

func TestProjectFilesVersionHistoryIdempotencyAndRestart(t *testing.T) {
	ctx := context.Background()
	database := filepath.Join(t.TempDir(), "runtime.db")
	store := openProjectFilesTestStore(t, database)
	defer func() { store.Close() }()
	project, err := store.CreateProject(ctx, "Working files")
	if err != nil {
		t.Fatal(err)
	}
	patch := ProjectFilePatch{Path: "draft/SKILL.md", Operation: "create_file", Diff: "+Hello"}
	call := beginProjectFileCall(t, store, project, patch)
	first, err := store.ApplyProjectFilePatch(ctx, call.AgentToolCallID, call.SDKToolCallID, patch, "Hello")
	if err != nil || first.Version != 1 || first.SizeBytes != 5 {
		t.Fatalf("create: %+v, %v", first, err)
	}
	duplicate, err := store.ApplyProjectFilePatch(ctx, call.AgentToolCallID, call.SDKToolCallID, patch, "Hello")
	if err != nil || duplicate != first {
		t.Fatalf("duplicate: %+v, %v", duplicate, err)
	}
	_, err = store.ApplyProjectFilePatch(ctx, call.AgentToolCallID, call.SDKToolCallID, patch, "different")
	assertDomainCode(t, err, "AGENT_TOOL_CALL_ID_CONFLICT")

	update := ProjectFilePatch{Path: patch.Path, ExpectedVersion: 1, Operation: "update_file", Diff: "@@\n-Hello\n+World"}
	updateCall := beginProjectFileCall(t, store, project, update)
	competingCall := beginProjectFileCall(t, store, project, update)
	second, err := store.ApplyProjectFilePatch(ctx, updateCall.AgentToolCallID, updateCall.SDKToolCallID, update, "World")
	if err != nil || second.Version != 2 {
		t.Fatalf("update: %+v, %v", second, err)
	}
	_, err = store.ApplyProjectFilePatch(ctx, competingCall.AgentToolCallID, competingCall.SDKToolCallID, update, "World")
	assertDomainCode(t, err, "PROJECT_FILE_VERSION_CONFLICT")
	read, err := store.ReadProjectFile(ctx, project.ProjectID, patch.Path, 1, 1, 2)
	if err != nil || read.Content != "el" || read.NextOffset != 3 || !read.Truncated {
		t.Fatalf("history: %+v, %v", read, err)
	}

	remove := ProjectFilePatch{Path: patch.Path, ExpectedVersion: 2, Operation: "delete_file"}
	removeCall := beginProjectFileCall(t, store, project, remove)
	deleted, err := store.ApplyProjectFilePatch(ctx, removeCall.AgentToolCallID, removeCall.SDKToolCallID, remove, "")
	if err != nil || !deleted.Deleted || deleted.Version != 3 {
		t.Fatalf("delete: %+v, %v", deleted, err)
	}
	_, err = store.ReadProjectFile(ctx, project.ProjectID, patch.Path, 0, 0, 100)
	assertDomainCode(t, err, "PROJECT_FILE_DELETED")
	files, err := store.ListProjectFiles(ctx, project.ProjectID)
	if err != nil || len(files) != 1 || !files[0].Deleted {
		t.Fatalf("list tombstone: %+v, %v", files, err)
	}
	patch.ExpectedVersion = 3
	patch.Diff = "+Restored"
	call = beginProjectFileCall(t, store, project, patch)
	restored, err := store.ApplyProjectFilePatch(ctx, call.AgentToolCallID, call.SDKToolCallID, patch, "Restored")
	if err != nil || restored.Version != 4 {
		t.Fatalf("recreate: %+v, %v", restored, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openProjectFilesTestStore(t, database)
	read, err = store.ReadProjectFile(ctx, project.ProjectID, patch.Path, 0, 0, 100)
	if err != nil || read.Content != "Restored" || read.File.Version != 4 {
		t.Fatalf("restart: %+v, %v", read, err)
	}
	read, err = store.ReadProjectFile(ctx, project.ProjectID, patch.Path, 1, 0, 100)
	if err != nil || read.Content != "Hello" {
		t.Fatalf("old version after restart: %+v, %v", read, err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM events WHERE project_id = ? AND event_type = 'project.file.updated'`, project.ProjectID).Scan(&count); err != nil || count != 4 {
		t.Fatalf("audit count %d, %v", count, err)
	}
}

func TestProjectFilesScopeProvenanceCancellationAndQuota(t *testing.T) {
	ctx := context.Background()
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer store.Close()
	project, err := store.CreateProject(ctx, "Scoped files")
	if err != nil {
		t.Fatal(err)
	}
	patch := ProjectFilePatch{Path: "notes.txt", Operation: "create_file", Diff: "+text"}
	call := beginProjectFileCall(t, store, project, patch)
	viewer := identity.DefaultLocalPrincipal()
	viewer.Role = identity.RoleViewer
	_, err = store.ApplyProjectFilePatch(identity.WithPrincipal(ctx, viewer), call.AgentToolCallID, call.SDKToolCallID, patch, "text")
	assertDomainCode(t, err, "ROLE_FORBIDDEN")
	other := identity.DefaultLocalPrincipal()
	other.WorkspaceID = "workspace_other"
	otherContext := identity.WithPrincipal(ctx, other)
	_, err = store.ApplyProjectFilePatch(otherContext, call.AgentToolCallID, call.SDKToolCallID, patch, "text")
	assertDomainCode(t, err, "PROJECT_NOT_FOUND")
	_, err = store.ListProjectFiles(otherContext, project.ProjectID)
	assertDomainCode(t, err, "PROJECT_NOT_FOUND")
	_, err = store.ReadProjectFile(otherContext, project.ProjectID, patch.Path, 0, 0, 100)
	assertDomainCode(t, err, "PROJECT_NOT_FOUND")
	_, err = store.ListProjectFiles(WithAgentActivity(ctx, AgentActivityIdentity{ProjectID: "prj_other"}), project.ProjectID)
	assertDomainCode(t, err, "PROJECT_NOT_FOUND")
	_, err = store.ApplyProjectFilePatch(ctx, call.AgentToolCallID, "forged", patch, "text")
	assertDomainCode(t, err, "AGENT_TOOL_CALL_STATE_CONFLICT")
	forged := patch
	forged.Path = "other.txt"
	_, err = store.ApplyProjectFilePatch(ctx, call.AgentToolCallID, call.SDKToolCallID, forged, "text")
	assertDomainCode(t, err, "AGENT_TOOL_CALL_STATE_CONFLICT")
	if _, err := store.ApplyProjectFilePatch(ctx, call.AgentToolCallID, call.SDKToolCallID, patch, "text"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE workspace_quotas SET max_storage_bytes = 7 WHERE workspace_id = ?`, identity.DefaultWorkspaceID); err != nil {
		t.Fatal(err)
	}
	patch.ExpectedVersion = 1
	patch.Operation = "update_file"
	patch.Diff = "@@\n-text\n+more"
	call = beginProjectFileCall(t, store, project, patch)
	_, err = store.ApplyProjectFilePatch(ctx, call.AgentToolCallID, call.SDKToolCallID, patch, "more")
	assertDomainCode(t, err, "WORKSPACE_QUOTA_EXCEEDED")
	if _, err := store.CancelAgentToolCall(ctx, CancelAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, Reason: "stop"}); err != nil {
		t.Fatal(err)
	}
	_, err = store.ApplyProjectFilePatch(ctx, call.AgentToolCallID, call.SDKToolCallID, patch, "x")
	assertDomainCode(t, err, "AGENT_TOOL_CALL_STATE_CONFLICT")
	read, err := store.ReadProjectFile(identity.WithPrincipal(ctx, viewer), project.ProjectID, patch.Path, 0, 0, 100)
	if err != nil || read.Content != "text" {
		t.Fatalf("failed writes mutated file: %+v, %v", read, err)
	}
}

func TestProjectFilesRecheckCurrentUserInsideTransaction(t *testing.T) {
	for _, scenario := range []struct {
		name, query, writeCode string
		readAllowed            bool
	}{
		{"role", `UPDATE workspace_memberships SET role='viewer' WHERE workspace_id=? AND user_id=?`, "ROLE_FORBIDDEN", true},
		{"membership", `UPDATE workspace_memberships SET status='disabled' WHERE workspace_id=? AND user_id=?`, "WORKSPACE_ACCESS_DENIED", false},
		{"user", `UPDATE users SET status='disabled' WHERE user_id=?`, "WORKSPACE_ACCESS_DENIED", false},
		{"workspace", `UPDATE workspaces SET status='deleting' WHERE workspace_id=?`, "WORKSPACE_ACCESS_DENIED", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "revoked.db"))
			defer store.Close()
			user := identity.Principal{Kind: identity.KindUser, UserID: "file_author", WorkspaceID: identity.DefaultWorkspaceID, Role: identity.RoleEditor}
			if err := store.BootstrapPrincipal(context.Background(), user); err != nil {
				t.Fatal(err)
			}
			ctx := identity.WithPrincipal(context.Background(), user)
			project, err := store.CreateProject(ctx, "Revoked file access")
			if err != nil {
				t.Fatal(err)
			}
			patch := ProjectFilePatch{Path: "private.txt", Operation: "create_file", Diff: "+original"}
			call := beginProjectFileCall(t, store, project, patch)
			if _, err := store.ApplyProjectFilePatch(ctx, call.AgentToolCallID, call.SDKToolCallID, patch, "original"); err != nil {
				t.Fatal(err)
			}
			patch.ExpectedVersion, patch.Operation, patch.Diff = 1, "update_file", "@@\n-original\n+changed"
			call = beginProjectFileCall(t, store, project, patch)
			args := []any{user.WorkspaceID, user.UserID}
			if scenario.name == "user" {
				args = []any{user.UserID}
			} else if scenario.name == "workspace" {
				args = []any{user.WorkspaceID}
			}
			if _, err := store.db.Exec(scenario.query, args...); err != nil {
				t.Fatal(err)
			}
			files, listErr := store.ListProjectFiles(ctx, project.ProjectID)
			page, readErr := store.ReadProjectFile(ctx, project.ProjectID, patch.Path, 1, 0, 100)
			_, body, downloadErr := store.ReadProjectFileBytes(ctx, project.ProjectID, patch.Path, 1)
			if scenario.readAllowed {
				if listErr != nil || readErr != nil || downloadErr != nil || len(files) != 1 || page.Content != "original" || string(body) != "original" {
					t.Fatalf("viewer should retain read access: list=%v read=%v download=%v", listErr, readErr, downloadErr)
				}
			} else {
				for _, err := range []error{listErr, readErr, downloadErr} {
					assertDomainCode(t, err, "WORKSPACE_ACCESS_DENIED")
				}
			}
			_, err = store.ApplyProjectFilePatch(ctx, call.AgentToolCallID, call.SDKToolCallID, patch, "changed")
			assertDomainCode(t, err, scenario.writeCode)
			var versions, events int
			if err := store.db.QueryRow(`SELECT
				(SELECT COUNT(*) FROM project_file_versions WHERE project_id=?),
				(SELECT COUNT(*) FROM events WHERE project_id=? AND event_type='project.file.updated')`, project.ProjectID, project.ProjectID).Scan(&versions, &events); err != nil {
				t.Fatal(err)
			}
			if versions != 1 || events != 1 {
				t.Fatalf("revoked patch changed history: versions=%d events=%d", versions, events)
			}
		})
	}
}

func TestProjectFilesRejectInvalidPathsContentAndCaseCollisions(t *testing.T) {
	for _, name := range []string{"", "/etc/passwd", "../x", "a/../b", "a//b", "a/./b", `C:\x`, `a\b`, "x:", "CON.txt", "a/LPT1", "x.", "x ", "a\n/b", "a?", strings.Repeat("x", 241)} {
		t.Run(name, func(t *testing.T) { assertDomainCode(t, validateProjectFilePath(name), "PROJECT_FILE_PATH_INVALID") })
	}
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer store.Close()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, "Validation")
	if err != nil {
		t.Fatal(err)
	}
	patch := ProjectFilePatch{Path: "docs/notes.txt", Operation: "create_file", Diff: "+\u4f60\u597d"}
	call := beginProjectFileCall(t, store, project, patch)
	for _, content := range []string{"x\x00", string([]byte{0xff}), strings.Repeat("x", maxProjectFileBytes+1)} {
		_, err := store.ApplyProjectFilePatch(ctx, call.AgentToolCallID, call.SDKToolCallID, patch, content)
		assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
	}
	if _, err := store.ApplyProjectFilePatch(ctx, call.AgentToolCallID, call.SDKToolCallID, patch, "\u4f60\u597d"); err != nil {
		t.Fatal(err)
	}
	read, err := store.ReadProjectFile(ctx, project.ProjectID, patch.Path, 1, 1, 1)
	if err != nil || read.Content != "\u597d" || read.Truncated || read.File.SizeBytes != 6 {
		t.Fatalf("UTF-8 page: %+v, %v", read, err)
	}
	patch.Path = "docs/NOTES.txt"
	call = beginProjectFileCall(t, store, project, patch)
	_, err = store.ApplyProjectFilePatch(ctx, call.AgentToolCallID, call.SDKToolCallID, patch, "other")
	assertDomainCode(t, err, "PROJECT_FILE_PATH_CONFLICT")
	for _, name := range []string{"docs", "docs/notes.txt/child.txt"} {
		patch.Path = name
		call = beginProjectFileCall(t, store, project, patch)
		_, err = store.ApplyProjectFilePatch(ctx, call.AgentToolCallID, call.SDKToolCallID, patch, "collision")
		assertDomainCode(t, err, "PROJECT_FILE_PATH_CONFLICT")
	}
	for _, args := range [][3]int{{-1, 0, 1}, {0, -1, 1}, {0, 0, 0}, {0, 0, maxProjectFileBytes + 1}, {0, 3, 1}} {
		_, err = store.ReadProjectFile(ctx, project.ProjectID, "docs/notes.txt", args[0], args[1], args[2])
		assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
	}
}

func TestProjectFilesStaleTurnAndDeletePreview(t *testing.T) {
	ctx := context.Background()
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer store.Close()
	project, err := store.CreateProject(ctx, "Delete files")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "write"}, CommandMeta{})
	if err != nil {
		t.Fatal(err)
	}
	patch := ProjectFilePatch{Path: "new.txt", Operation: "create_file", Diff: "+new"}
	call := beginProjectFileCall(t, store, project, patch)
	if _, err := store.db.Exec(`UPDATE agent_tool_calls SET agent_turn_id = ? WHERE agent_tool_call_id = ?`, turn.AgentTurnID, call.AgentToolCallID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE agent_turns SET status = 'cancelled' WHERE agent_turn_id = ?`, turn.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	_, err = store.ApplyProjectFilePatch(ctx, call.AgentToolCallID, call.SDKToolCallID, patch, "new")
	assertDomainCode(t, err, "AGENT_ACTIVITY_STALE")
	preview, err := store.CreateProjectDeletePreview(ctx, CreateProjectDeletePreviewCommand{ProjectID: project.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	call = beginProjectFileCall(t, store, project, patch)
	if _, err := store.ApplyProjectFilePatch(ctx, call.AgentToolCallID, call.SDKToolCallID, patch, "new"); err != nil {
		t.Fatal(err)
	}
	_, err = store.ConfirmProjectDelete(ctx, ConfirmProjectDeleteCommand{ProjectID: project.ProjectID, PreviewHash: preview.SnapshotHash, Confirmed: true})
	assertDomainCode(t, err, "DELETE_PREVIEW_EXPIRED")
	preview, err = store.CreateProjectDeletePreview(ctx, CreateProjectDeletePreviewCommand{ProjectID: project.ProjectID})
	if err != nil || preview.Impact.WorkingFileVersionCount != 1 {
		t.Fatalf("preview files: %+v, %v", preview, err)
	}
	if _, err := store.ConfirmProjectDelete(ctx, ConfirmProjectDeleteCommand{ProjectID: project.ProjectID, PreviewHash: preview.SnapshotHash, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM project_file_versions WHERE project_id = ?`, project.ProjectID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("files retained after project deletion: %d, %v", count, err)
	}
	_, err = store.ListProjectFiles(ctx, project.ProjectID)
	assertDomainCode(t, err, "PROJECT_NOT_FOUND")
}

func TestProjectFilesMigrateVersion38AndWorkspaceDeletion(t *testing.T) {
	database := filepath.Join(t.TempDir(), "runtime.db")
	store := openProjectFilesTestStore(t, database)
	if _, err := store.db.Exec(`DROP TABLE project_file_versions`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA user_version = 38`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openProjectFilesTestStore(t, database)
	defer store.Close()
	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("migration: %d, %v", version, err)
	}
	var backup string
	if err := store.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version = 38 AND to_version = ?`, schemaVersion).Scan(&backup); err != nil || backup == "" {
		t.Fatalf("migration backup: %q, %v", backup, err)
	}
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	project, err := store.CreateProject(ctx, "Workspace deletion")
	if err != nil {
		t.Fatal(err)
	}
	patch := ProjectFilePatch{Path: "notes.txt", Operation: "create_file", Diff: "+notes"}
	call := beginProjectFileCall(t, store, project, patch)
	if _, err := store.ApplyProjectFilePatch(ctx, call.AgentToolCallID, call.SDKToolCallID, patch, "notes"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteWorkspace(ctx, DeleteWorkspaceCommand{WorkspaceID: identity.DefaultWorkspaceID, UserID: identity.DefaultUserID, Confirmation: identity.DefaultWorkspaceID}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM project_file_versions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("workspace file cleanup: %d, %v", count, err)
	}
}

func TestProjectFilesBackgroundCancellationPreventsLateWrites(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "runtime.db"), loadBackgroundTaskRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	task := createBackgroundTaskForTest(t, store)
	claim, err := store.ClaimAgentTask(ctx, ClaimAgentTaskCommand{WorkerID: "file-worker", ProviderID: "sdk", ModelID: "fixture", LeaseSeconds: 60})
	if err != nil || claim == nil {
		t.Fatalf("claim: %+v, %v", claim, err)
	}
	patch := ProjectFilePatch{Path: "background.txt", Operation: "create_file", Diff: "+background"}
	arguments, _ := json.Marshal(patch)
	call, err := store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{
		ProjectID: task.ProjectID, ConversationID: task.ConversationID,
		SkillInvocationID: task.SkillInvocationID, AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID,
		AttemptToken: claim.AttemptToken, SDKToolCallID: "sdk-background-file", ToolID: "runtime:apply_workspace_patch", Arguments: arguments,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelAgentTask(ctx, CancelAgentTaskCommand{AgentTaskID: task.AgentTaskID, ActorRef: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyProjectFilePatch(ctx, call.AgentToolCallID, call.SDKToolCallID, patch, "background"); err == nil {
		t.Fatal("cancelled background task wrote a file")
	}
	files, err := store.ListProjectFiles(ctx, task.ProjectID)
	if err != nil || len(files) != 0 {
		t.Fatalf("late background output: %+v, %v", files, err)
	}
}

func TestProjectFilesBackgroundRechecksWorkerIdentityAfterRequestAuthentication(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "worker.db"), loadBackgroundTaskRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	task := createBackgroundTaskForTest(t, store)
	claim, err := store.ClaimAgentTask(ctx, ClaimAgentTaskCommand{WorkerID: "file-worker", ProviderID: "sdk", ModelID: "fixture", LeaseSeconds: 60})
	if err != nil || claim == nil {
		t.Fatalf("claim: %+v, %v", claim, err)
	}
	patch := ProjectFilePatch{Path: "worker.txt", Operation: "create_file", Diff: "+current-worker"}
	arguments, _ := json.Marshal(patch)
	call, err := store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{
		ProjectID: task.ProjectID, ConversationID: task.ConversationID,
		SkillInvocationID: task.SkillInvocationID, AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID,
		AttemptToken: claim.AttemptToken, SDKToolCallID: "sdk-worker-file", ToolID: "runtime:apply_workspace_patch", Arguments: arguments,
	})
	if err != nil {
		t.Fatal(err)
	}
	activity := AgentActivityIdentity{ProjectID: task.ProjectID, AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID, AttemptToken: claim.AttemptToken}
	user, err := store.ResolveAgentActivityPrincipal(ctx, activity)
	if err != nil {
		t.Fatal(err)
	}
	service := identity.WithDelegatedUser(identity.WithPrincipal(ctx, identity.Principal{Kind: identity.KindService}), user)
	if _, err := store.db.Exec(`UPDATE agent_task_attempts SET token_hash=? WHERE agent_task_attempt_id=?`, sha256Hex([]byte("new-worker-token")), claim.Attempt.AgentTaskAttemptID); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name, code string
		ctx        context.Context
	}{
		{"rotated", "ATTEMPT_TOKEN_INVALID", WithAgentActivity(service, activity)},
		{"missing-activity", "AGENT_ACTIVITY_REQUIRED", service},
		{"wrong-attempt", "AGENT_ACTIVITY_SCOPE_MISMATCH", WithAgentActivity(service, AgentActivityIdentity{ProjectID: task.ProjectID, AgentTaskAttemptID: "different", AttemptToken: "new-worker-token"})},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			_, err := store.ApplyProjectFilePatch(scenario.ctx, call.AgentToolCallID, call.SDKToolCallID, patch, "current-worker")
			assertDomainCode(t, err, scenario.code)
			files, err := store.ListProjectFiles(ctx, task.ProjectID)
			if err != nil || len(files) != 0 {
				t.Fatalf("stale worker wrote files: %+v %v", files, err)
			}
		})
	}
	activity.AttemptToken = "new-worker-token"
	file, err := store.ApplyProjectFilePatch(WithAgentActivity(service, activity), call.AgentToolCallID, call.SDKToolCallID, patch, "current-worker")
	if err != nil || file.Version != 1 {
		t.Fatalf("current worker did not retain the valid call: %+v %v", file, err)
	}
}
