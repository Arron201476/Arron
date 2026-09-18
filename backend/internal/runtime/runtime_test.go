package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
)

func TestRuntimePersistsProjectAcrossRestart(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "content_agent.db")
	registry := loadTestRegistry(t)
	store, err := Open(databasePath, registry)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	project, err := store.CreateProject(ctx, "重启测试")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(databasePath, registry)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer reopened.Close()
	got, err := reopened.GetProject(ctx, project.ProjectID)
	if err != nil {
		t.Fatalf("GetProject() after restart error = %v", err)
	}
	if got.Title != "重启测试" || got.PrimaryConversationID == "" {
		t.Fatalf("project after restart = %+v", got)
	}
}

func TestProjectMessageAndUploadCommandsAreIdempotent(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	projectMeta := CommandMeta{
		Scope:          SharedWorkspaceID,
		CommandType:    "create_project",
		IdempotencyKey: "55555555-5555-4555-8555-555555555555",
		RequestHash:    "project_hash_v1",
	}
	project, err := store.CreateProjectCommand(ctx, "幂等作品", projectMeta)
	if err != nil {
		t.Fatalf("CreateProjectCommand() error = %v", err)
	}
	repeatedProject, err := store.CreateProjectCommand(ctx, "幂等作品", projectMeta)
	if err != nil || repeatedProject.ProjectID != project.ProjectID {
		t.Fatalf("project retry = %+v, error = %v", repeatedProject, err)
	}
	reusedProjectMeta := projectMeta
	reusedProjectMeta.RequestHash = "different_project_hash"
	_, err = store.CreateProjectCommand(ctx, "另一个名称", reusedProjectMeta)
	assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")

	messageMeta := CommandMeta{
		Scope:          project.ProjectID,
		CommandType:    "create_message",
		IdempotencyKey: "66666666-6666-4666-8666-666666666666",
		RequestHash:    "message_hash_v1",
	}
	message, err := store.CreateUserMessageCommand(ctx, project.PrimaryConversationID, "唯一消息", messageMeta)
	if err != nil {
		t.Fatalf("CreateUserMessageCommand() error = %v", err)
	}
	repeatedMessage, err := store.CreateUserMessageCommand(ctx, project.PrimaryConversationID, "唯一消息", messageMeta)
	if err != nil || repeatedMessage.MessageID != message.MessageID {
		t.Fatalf("message retry = %+v, error = %v", repeatedMessage, err)
	}

	content := "first-content-123"
	sessionMeta := CommandMeta{
		Scope:          project.ProjectID,
		CommandType:    "create_upload_session",
		IdempotencyKey: "77777777-7777-4777-8777-777777777777",
		RequestHash:    "upload_session_hash_v1",
	}
	specs := []UploadItemSpec{{
		ClientItemKey:     "source_1",
		Kind:              "text",
		OriginalFilename:  "source.txt",
		DeclaredMIMEType:  "text/plain",
		DeclaredSizeBytes: int64(len([]byte(content))),
	}}
	session, err := store.CreateUploadSessionCommand(ctx, project.ProjectID, specs, sessionMeta)
	if err != nil {
		t.Fatalf("CreateUploadSessionCommand() error = %v", err)
	}
	repeatedSession, err := store.CreateUploadSessionCommand(ctx, project.ProjectID, specs, sessionMeta)
	if err != nil || repeatedSession.UploadSessionID != session.UploadSessionID {
		t.Fatalf("upload session retry = %+v, error = %v", repeatedSession, err)
	}
	itemID := session.Items[0].UploadItemID
	contentMeta := CommandMeta{
		Scope:          project.ProjectID,
		CommandType:    "write_upload_content",
		IdempotencyKey: "99999999-9999-4999-8999-999999999999",
		RequestHash:    "upload_content_stream_v1",
	}
	receivedItem, err := store.WriteUploadContentCommand(ctx, itemID, bytes.NewBufferString(content), contentMeta)
	if err != nil {
		t.Fatalf("WriteUploadContentCommand() error = %v", err)
	}
	repeatedItem, err := store.WriteUploadContentCommand(ctx, itemID, bytes.NewBufferString(content), contentMeta)
	if err != nil || repeatedItem.UploadItemID != receivedItem.UploadItemID ||
		repeatedItem.Status != receivedItem.Status {
		t.Fatalf("upload content retry = %+v, error = %v", repeatedItem, err)
	}
	_, err = store.WriteUploadContentCommand(ctx, itemID, bytes.NewBufferString("other-content-456"), contentMeta)
	assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
	stagingEntries, err := os.ReadDir(filepath.Join(store.dataRoot, "staging"))
	if err != nil {
		t.Fatalf("ReadDir(staging) error = %v", err)
	}
	if len(stagingEntries) != 1 || stagingEntries[0].Name() != itemID+".part" {
		t.Fatalf("staging entries = %v, want only canonical part", stagingEntries)
	}
	completeMeta := CommandMeta{
		Scope:          project.ProjectID,
		CommandType:    "complete_upload_item",
		IdempotencyKey: "88888888-8888-4888-8888-888888888888",
		RequestHash:    "upload_complete_hash_v1",
	}
	result, err := store.CompleteUploadItemCommand(ctx, itemID, completeMeta)
	if err != nil {
		t.Fatalf("CompleteUploadItemCommand() error = %v", err)
	}
	repeatedResult, err := store.CompleteUploadItemCommand(ctx, itemID, completeMeta)
	if err != nil || repeatedResult.Asset.AssetID != result.Asset.AssetID ||
		repeatedResult.Snapshot.AssetSnapshotID != result.Snapshot.AssetSnapshotID {
		t.Fatalf("upload complete retry = %+v, error = %v", repeatedResult, err)
	}
	assets, err := store.ListAssets(ctx, project.ProjectID)
	if err != nil {
		t.Fatalf("ListAssets() error = %v", err)
	}
	if len(assets) != 1 {
		t.Fatalf("asset count = %d, want 1", len(assets))
	}
}

func TestPrototypeCheckpointVersionAndApprovalFlow(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "content_agent.db")
	store, err := Open(databasePath, loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, err := store.CreateProject(ctx, "首个闭环")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	message, err := store.CreateUserMessage(ctx, project.PrimaryConversationID, "请把小说转成剧本")
	if err != nil {
		t.Fatalf("CreateUserMessage() error = %v", err)
	}
	sourceAsset := createTextAsset(t, store, project.ProjectID, "novel.txt", "第一章 少年下山。")
	input := mustJSON(t, map[string]any{
		"project_id":  project.ProjectID,
		"source_type": "novel",
		"assets": []map[string]any{{
			"asset_id":          sourceAsset.Asset.AssetID,
			"asset_snapshot_id": sourceAsset.Snapshot.AssetSnapshotID,
			"role":              "primary_source",
			"order":             1,
		}},
		"user_request_message_id": message.MessageID,
		"user_notes":              []string{},
	})
	config := mustJSON(t, map[string]any{
		"config_ref": "creation",
		"payload": map[string]any{
			"target_episode_count":            12,
			"episode_duration_minutes":        2,
			"preserve_existing_episode_marks": true,
			"expansion_policy":                "confirm_if_needed",
			"user_requirements":               []string{},
		},
	})
	start := StartRunCommand{
		CommandMeta: CommandMeta{
			Scope:          project.ProjectID,
			CommandType:    "start_run",
			IdempotencyKey: "11111111-1111-4111-8111-111111111111",
			RequestHash:    "start_hash_v1",
		},
		ProjectID:         project.ProjectID,
		ConversationID:    project.PrimaryConversationID,
		CapabilityID:      "novel_to_script",
		CapabilityVersion: "1.4.0",
		RunKind:           "generation",
		Input:             input,
		Config:            config,
		Confirmed:         true,
	}
	start = bindStartRunProposal(t, store, start)
	snapshot, err := store.StartRun(ctx, start)
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	if snapshot.Run.Status != "waiting_approval" || len(snapshot.Steps) != 1 || len(snapshot.Artifacts) != 1 {
		t.Fatalf("initial snapshot = %+v", snapshot)
	}
	if snapshot.CurrentApproval == nil || snapshot.CurrentApproval.Status != "pending" {
		t.Fatalf("initial approval = %+v", snapshot.CurrentApproval)
	}
	repeatedStart, err := store.StartRun(ctx, start)
	if err != nil {
		t.Fatalf("StartRun() idempotent retry error = %v", err)
	}
	if repeatedStart.Run.RunID != snapshot.Run.RunID || repeatedStart.EventCursor != snapshot.EventCursor {
		t.Fatalf("idempotent StartRun changed response: first=%+v retry=%+v", snapshot, repeatedStart)
	}
	reusedStart := start
	reusedStart.RequestHash = "different_start_hash"
	_, err = store.StartRun(ctx, reusedStart)
	assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")

	otherProject, err := store.CreateProject(ctx, "其他作品")
	if err != nil {
		t.Fatalf("CreateProject(other) error = %v", err)
	}
	otherMessage, err := store.CreateUserMessage(ctx, otherProject.PrimaryConversationID, "跨作品材料测试")
	if err != nil {
		t.Fatalf("CreateUserMessage(other) error = %v", err)
	}
	crossProjectInput := mustJSON(t, map[string]any{
		"project_id":  otherProject.ProjectID,
		"source_type": "novel",
		"assets": []map[string]any{{
			"asset_id":          sourceAsset.Asset.AssetID,
			"asset_snapshot_id": sourceAsset.Snapshot.AssetSnapshotID,
			"role":              "primary_source",
			"order":             1,
		}},
		"user_request_message_id": otherMessage.MessageID,
		"user_notes":              []string{},
	})
	crossProjectStart := bindStartRunProposal(t, store, StartRunCommand{
		ProjectID:         otherProject.ProjectID,
		ConversationID:    otherProject.PrimaryConversationID,
		CapabilityID:      "novel_to_script",
		CapabilityVersion: "1.4.0",
		RunKind:           "generation",
		Input:             crossProjectInput,
		Config:            config,
		Confirmed:         true,
	})
	_, err = store.StartRun(ctx, crossProjectStart)
	assertDomainCode(t, err, "RESOURCE_PROJECT_MISMATCH")

	conflictingStart := start
	conflictingStart.CommandMeta = CommandMeta{}
	_, err = store.StartRun(ctx, conflictingStart)
	assertDomainCode(t, err, "CONFIRMATION_STALE")
	if _, err := store.CreateUserMessage(ctx, project.PrimaryConversationID, "运行中继续聊天"); err != nil {
		t.Fatalf("CreateUserMessage() during active write run error = %v", err)
	}

	artifact := snapshot.Artifacts[0]
	firstVersion, err := store.GetArtifactVersion(ctx, artifact.CurrentVersionID)
	if err != nil {
		t.Fatalf("GetArtifactVersion(v1) error = %v", err)
	}
	editedPayload := mustJSON(t, map[string]any{
		"input_snapshot_id": snapshot.Run.CurrentInputSnapshotVersionID,
		"source_kind":       "novel",
		"assets": []map[string]any{{
			"asset_id":          sourceAsset.Asset.AssetID,
			"asset_snapshot_id": sourceAsset.Snapshot.AssetSnapshotID,
			"role":              "primary_source",
			"order":             1,
		}},
		"user_request_message_id": message.MessageID,
		"confirmed_strategy_refs": []string{"manual_reviewed"},
	})
	versionResult, err := store.CreateArtifactVersion(ctx, CreateVersionCommand{
		CommandMeta: CommandMeta{
			Scope:          project.ProjectID,
			CommandType:    "create_artifact_version",
			IdempotencyKey: "22222222-2222-4222-8222-222222222222",
			RequestHash:    "artifact_hash_v1",
		},
		ArtifactID:    artifact.ArtifactID,
		BaseVersionID: firstVersion.ArtifactVersionID,
		BaseVersion:   firstVersion.Version,
		NewPayload:    editedPayload,
	})
	if err != nil {
		t.Fatalf("CreateArtifactVersion() error = %v", err)
	}
	if versionResult.ArtifactVersion.Version != 2 || versionResult.ArtifactVersion.Status != "pending_approval" {
		t.Fatalf("new version = %+v", versionResult.ArtifactVersion)
	}
	repeatedVersion, err := store.CreateArtifactVersion(ctx, CreateVersionCommand{
		CommandMeta: CommandMeta{
			Scope:          project.ProjectID,
			CommandType:    "create_artifact_version",
			IdempotencyKey: "22222222-2222-4222-8222-222222222222",
			RequestHash:    "artifact_hash_v1",
		},
		ArtifactID:    artifact.ArtifactID,
		BaseVersionID: firstVersion.ArtifactVersionID,
		BaseVersion:   firstVersion.Version,
		NewPayload:    editedPayload,
	})
	if err != nil {
		t.Fatalf("CreateArtifactVersion() idempotent retry error = %v", err)
	}
	if repeatedVersion.ArtifactVersion.ArtifactVersionID != versionResult.ArtifactVersion.ArtifactVersionID {
		t.Fatalf("idempotent artifact retry created a new version: first=%+v retry=%+v", versionResult, repeatedVersion)
	}
	oldVersion, err := store.GetArtifactVersion(ctx, firstVersion.ArtifactVersionID)
	if err != nil {
		t.Fatalf("GetArtifactVersion(v1 after edit) error = %v", err)
	}
	if oldVersion.Status != "superseded" {
		t.Fatalf("old version status = %s, want superseded", oldVersion.Status)
	}
	oldApproval, err := store.GetApproval(ctx, snapshot.CurrentApproval.ApprovalRequestID)
	if err != nil {
		t.Fatalf("GetApproval(old) error = %v", err)
	}
	if oldApproval.Status != "expired" {
		t.Fatalf("old approval status = %s, want expired", oldApproval.Status)
	}

	_, err = store.CreateArtifactVersion(ctx, CreateVersionCommand{
		ArtifactID:    artifact.ArtifactID,
		BaseVersionID: firstVersion.ArtifactVersionID,
		BaseVersion:   firstVersion.Version,
		NewPayload:    editedPayload,
	})
	assertDomainCode(t, err, "ARTIFACT_VERSION_CONFLICT")

	_, err = store.ResolveApproval(ctx, ResolveApprovalCommand{
		ApprovalRequestID:       versionResult.Approval.ApprovalRequestID,
		Action:                  "approve",
		ExpectedApprovalVersion: 1,
		SubjectSnapshotHash:     "stale_hash",
	})
	assertDomainCode(t, err, "APPROVAL_SUBJECT_CHANGED")

	approved, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
		CommandMeta: CommandMeta{
			Scope:          project.ProjectID,
			CommandType:    "resolve_approval",
			IdempotencyKey: "33333333-3333-4333-8333-333333333333",
			RequestHash:    "approval_hash_v1",
		},
		ApprovalRequestID:       versionResult.Approval.ApprovalRequestID,
		Action:                  "approve",
		ExpectedApprovalVersion: 1,
		SubjectSnapshotHash:     versionResult.Approval.SubjectSnapshotHash,
	})
	if err != nil {
		t.Fatalf("ResolveApproval() error = %v", err)
	}
	if approved.Run.Status != "paused" || len(approved.Steps) != 2 {
		t.Fatalf("approved snapshot = %+v", approved)
	}
	if approved.Steps[0].Status != "completed" ||
		approved.Steps[1].StepID != "build_source_manifest" ||
		approved.Steps[1].Status != "pending" {
		t.Fatalf("steps after approval = %+v", approved.Steps)
	}
	confirmed, err := store.GetArtifactVersion(ctx, versionResult.ArtifactVersion.ArtifactVersionID)
	if err != nil {
		t.Fatalf("GetArtifactVersion(confirmed) error = %v", err)
	}
	if confirmed.Status != "confirmed" || confirmed.ConfirmedAt == nil {
		t.Fatalf("confirmed version = %+v", confirmed)
	}
	repeatedApproval, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
		CommandMeta: CommandMeta{
			Scope:          project.ProjectID,
			CommandType:    "resolve_approval",
			IdempotencyKey: "33333333-3333-4333-8333-333333333333",
			RequestHash:    "approval_hash_v1",
		},
		ApprovalRequestID:       versionResult.Approval.ApprovalRequestID,
		Action:                  "approve",
		ExpectedApprovalVersion: 1,
		SubjectSnapshotHash:     versionResult.Approval.SubjectSnapshotHash,
	})
	if err != nil {
		t.Fatalf("ResolveApproval() idempotent retry error = %v", err)
	}
	if repeatedApproval.Run.RunID != approved.Run.RunID || repeatedApproval.EventCursor != approved.EventCursor {
		t.Fatalf("idempotent approval retry changed response: first=%+v retry=%+v", approved, repeatedApproval)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened, err := Open(databasePath, loadTestRegistry(t))
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer reopened.Close()
	recovered, err := reopened.GetRunSnapshot(ctx, approved.Run.RunID)
	if err != nil {
		t.Fatalf("GetRunSnapshot() after restart error = %v", err)
	}
	if recovered.Run.Status != "paused" || len(recovered.Steps) != 2 || recovered.CurrentApproval != nil {
		t.Fatalf("recovered snapshot = %+v", recovered)
	}
	restartedRetry, err := reopened.ResolveApproval(ctx, ResolveApprovalCommand{
		CommandMeta: CommandMeta{
			Scope:          project.ProjectID,
			CommandType:    "resolve_approval",
			IdempotencyKey: "33333333-3333-4333-8333-333333333333",
			RequestHash:    "approval_hash_v1",
		},
		ApprovalRequestID:       versionResult.Approval.ApprovalRequestID,
		Action:                  "approve",
		ExpectedApprovalVersion: 1,
		SubjectSnapshotHash:     versionResult.Approval.SubjectSnapshotHash,
	})
	if err != nil {
		t.Fatalf("ResolveApproval() retry after restart error = %v", err)
	}
	if restartedRetry.EventCursor != approved.EventCursor {
		t.Fatalf("restart idempotency response changed: first=%+v retry=%+v", approved.EventCursor, restartedRetry.EventCursor)
	}
}

func TestRunLifecycleCommands(t *testing.T) {
	t.Run("pause waiting approval and cancel", func(t *testing.T) {
		ctx := context.Background()
		databasePath := filepath.Join(t.TempDir(), "content_agent.db")
		store, err := Open(databasePath, loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}

		project, sourceAsset, initial := startNovelRunForLifecycle(t, store, "暂停与结束")
		pauseCommand := PauseRunCommand{
			CommandMeta: CommandMeta{
				Scope:          project.ProjectID,
				CommandType:    "pause_run",
				IdempotencyKey: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
				RequestHash:    "pause_waiting_approval_v1",
			},
			RunID: initial.Run.RunID,
		}
		paused, err := store.RequestRunPause(ctx, pauseCommand)
		if err != nil {
			t.Fatalf("RequestRunPause() error = %v", err)
		}
		if paused.Run.Status != "paused" || paused.CurrentApproval == nil ||
			paused.Steps[0].Status != "waiting_approval" {
			t.Fatalf("paused waiting-approval snapshot = %+v", paused)
		}
		repeatedPause, err := store.RequestRunPause(ctx, pauseCommand)
		if err != nil || repeatedPause.EventCursor != paused.EventCursor {
			t.Fatalf("pause retry = %+v, error = %v", repeatedPause, err)
		}
		_, err = store.ResumeRun(ctx, ResumeRunCommand{RunID: paused.Run.RunID})
		assertDomainCode(t, err, "RUN_STATE_CONFLICT")
		_, err = store.CancelRun(ctx, CancelRunCommand{RunID: paused.Run.RunID})
		assertDomainCode(t, err, "REQUIRED_CONFIRMATION_MISSING")

		cancelCommand := CancelRunCommand{
			CommandMeta: CommandMeta{
				Scope:          project.ProjectID,
				CommandType:    "cancel_run",
				IdempotencyKey: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
				RequestHash:    "cancel_paused_run_v1",
			},
			RunID:     paused.Run.RunID,
			Confirmed: true,
			ActorRef:  "test_user",
		}
		cancelled, err := store.CancelRun(ctx, cancelCommand)
		if err != nil {
			t.Fatalf("CancelRun() error = %v", err)
		}
		if cancelled.Run.Status != "cancelled" || cancelled.Run.EndedAt == nil ||
			cancelled.CurrentApproval != nil || len(cancelled.Artifacts) != 1 ||
			cancelled.Steps[0].Status != "cancelled" {
			t.Fatalf("cancelled snapshot = %+v", cancelled)
		}
		approval, err := store.GetApproval(ctx, initial.CurrentApproval.ApprovalRequestID)
		if err != nil || approval.Status != "cancelled" {
			t.Fatalf("cancelled approval = %+v, error = %v", approval, err)
		}
		version, err := store.GetArtifactVersion(ctx, initial.CurrentApproval.SubjectRefID)
		if err != nil || version.Status != "invalidated" {
			t.Fatalf("invalidated pending version = %+v, error = %v", version, err)
		}
		gotProject, err := store.GetProject(ctx, project.ProjectID)
		if err != nil || gotProject.Status != "ready" || gotProject.ActiveWriteRunID != nil {
			t.Fatalf("project after cancel = %+v, error = %v", gotProject, err)
		}
		repeatedCancel, err := store.CancelRun(ctx, cancelCommand)
		if err != nil || repeatedCancel.EventCursor != cancelled.EventCursor {
			t.Fatalf("cancel retry = %+v, error = %v", repeatedCancel, err)
		}

		newMessage, err := store.CreateUserMessage(ctx, project.PrimaryConversationID, "重新开始")
		if err != nil {
			t.Fatalf("CreateUserMessage() after cancel error = %v", err)
		}
		replacement, err := store.StartRun(ctx, bindStartRunProposal(t, store, novelStartCommand(
			project,
			newMessage.MessageID,
			sourceAsset,
		)))
		if err != nil || replacement.Run.Status != "waiting_approval" {
			t.Fatalf("replacement run = %+v, error = %v", replacement, err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		reopened, err := Open(databasePath, loadTestRegistry(t))
		if err != nil {
			t.Fatalf("reopen error = %v", err)
		}
		defer reopened.Close()
		restartedRetry, err := reopened.CancelRun(ctx, cancelCommand)
		if err != nil || restartedRetry.EventCursor != cancelled.EventCursor {
			t.Fatalf("cancel retry after restart = %+v, error = %v", restartedRetry, err)
		}
	})

	t.Run("resume pause at safe boundary and resume cursor", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer store.Close()

		project, _, initial := startNovelRunForLifecycle(t, store, "游标恢复")
		approved, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
			ApprovalRequestID:       initial.CurrentApproval.ApprovalRequestID,
			Action:                  "approve",
			ExpectedApprovalVersion: initial.CurrentApproval.Version,
			SubjectSnapshotHash:     initial.CurrentApproval.SubjectSnapshotHash,
		})
		if err != nil {
			t.Fatalf("ResolveApproval() error = %v", err)
		}
		if approved.Run.Status != "paused" || approved.CurrentApproval != nil ||
			approved.Steps[1].Status != "pending" {
			t.Fatalf("approved boundary snapshot = %+v", approved)
		}

		resumeCommand := ResumeRunCommand{
			CommandMeta: CommandMeta{
				Scope:          project.ProjectID,
				CommandType:    "resume_run",
				IdempotencyKey: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
				RequestHash:    "resume_pending_step_v1",
			},
			RunID: approved.Run.RunID,
		}
		running, err := store.ResumeRun(ctx, resumeCommand)
		if err != nil {
			t.Fatalf("ResumeRun() error = %v", err)
		}
		if running.Run.Status != "running" || len(running.Steps) != 3 ||
			running.Steps[1].StepID != "build_source_manifest" ||
			running.Steps[1].Status != "completed" ||
			running.Steps[2].StepID != "build_story_bible" ||
			running.Steps[2].Status != "running" ||
			running.Steps[2].AttemptCount != 1 {
			t.Fatalf("running snapshot = %+v", running)
		}
		repeatedResume, err := store.ResumeRun(ctx, resumeCommand)
		if err != nil || repeatedResume.EventCursor != running.EventCursor {
			t.Fatalf("resume retry = %+v, error = %v", repeatedResume, err)
		}
		_, err = store.ResumeRun(ctx, ResumeRunCommand{RunID: running.Run.RunID})
		assertDomainCode(t, err, "RUN_STATE_CONFLICT")
		claim := claimStructuredTask(t, store)

		pauseCommand := PauseRunCommand{
			CommandMeta: CommandMeta{
				Scope:          project.ProjectID,
				CommandType:    "pause_run",
				IdempotencyKey: "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
				RequestHash:    "pause_running_step_v1",
			},
			RunID: running.Run.RunID,
		}
		pausing, err := store.RequestRunPause(ctx, pauseCommand)
		if err != nil {
			t.Fatalf("RequestRunPause(running) error = %v", err)
		}
		if pausing.Run.Status != "pausing" || pausing.Steps[2].Status != "running" {
			t.Fatalf("pausing snapshot = %+v", pausing)
		}

		cursor := json.RawMessage(`{"next_item":2,"provider_attempt":"attempt_1"}`)
		completePauseCommand := CompleteRunPauseCommand{
			CommandMeta: CommandMeta{
				Scope:          project.ProjectID,
				CommandType:    "complete_run_pause",
				IdempotencyKey: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
				RequestHash:    "safe_cursor_v1",
			},
			RunID:      running.Run.RunID,
			TaskCursor: cursor,
		}
		safelyPaused, err := store.CompleteRunPauseAtSafeBoundary(ctx, completePauseCommand)
		if err != nil {
			t.Fatalf("CompleteRunPauseAtSafeBoundary() error = %v", err)
		}
		if safelyPaused.Run.Status != "paused" || safelyPaused.Steps[2].Status != "paused" ||
			string(safelyPaused.Steps[2].TaskCursor) != string(cursor) {
			t.Fatalf("safe paused snapshot = %+v", safelyPaused)
		}
		abandoned, err := store.GetExecutionAttempt(ctx, claim.Attempt.AttemptID)
		if err != nil || abandoned.Status != "abandoned" {
			t.Fatalf("safe-boundary attempt = %+v, error = %v", abandoned, err)
		}
		repeatedComplete, err := store.CompleteRunPauseAtSafeBoundary(ctx, completePauseCommand)
		if err != nil || repeatedComplete.EventCursor != safelyPaused.EventCursor {
			t.Fatalf("complete pause retry = %+v, error = %v", repeatedComplete, err)
		}

		resumedAgain, err := store.ResumeRun(ctx, ResumeRunCommand{
			CommandMeta: CommandMeta{
				Scope:          project.ProjectID,
				CommandType:    "resume_run",
				IdempotencyKey: "ffffffff-ffff-4fff-8fff-ffffffffffff",
				RequestHash:    "resume_saved_cursor_v1",
			},
			RunID: running.Run.RunID,
		})
		if err != nil {
			t.Fatalf("ResumeRun(saved cursor) error = %v", err)
		}
		if resumedAgain.Run.Status != "running" ||
			string(resumedAgain.Steps[2].TaskCursor) != string(cursor) {
			t.Fatalf("resumed cursor snapshot = %+v", resumedAgain)
		}
	})

	t.Run("pause idle step immediately and resume", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer store.Close()

		project, _, initial := startNovelRunForLifecycle(t, store, "空闲暂停")
		running := approveAndResumeLifecycleRun(t, store, project, initial)
		paused, err := store.RequestRunPause(ctx, PauseRunCommand{RunID: running.Run.RunID})
		if err != nil {
			t.Fatalf("RequestRunPause(idle) error = %v", err)
		}
		if paused.Run.Status != "paused" || paused.Steps[len(paused.Steps)-1].Status != "paused" {
			t.Fatalf("idle pause must converge immediately: %+v", paused)
		}
		if !hasAvailableAction(paused.AvailableActions, "resume_run") {
			t.Fatalf("paused actions = %+v", paused.AvailableActions)
		}
		events, err := store.ListRunEvents(ctx, running.Run.RunID, 0, 500)
		if err != nil || len(events.Items) < 2 {
			t.Fatalf("ListRunEvents(idle pause) = %+v, error = %v", events, err)
		}
		last := events.Items[len(events.Items)-2:]
		if last[0].EventType != "run.pause_requested" || last[1].EventType != "run.paused" {
			t.Fatalf("idle pause event order = %+v", last)
		}
		resumed, err := store.ResumeRun(ctx, ResumeRunCommand{RunID: running.Run.RunID})
		if err != nil || resumed.Run.Status != "running" {
			t.Fatalf("ResumeRun(idle pause) = %+v, error = %v", resumed, err)
		}
	})

	t.Run("withdraw pause while execution is active", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer store.Close()

		project, _, initial := startNovelRunForLifecycle(t, store, "撤销暂停")
		running := approveAndResumeLifecycleRun(t, store, project, initial)
		claim := claimStructuredTask(t, store)
		pausing, err := store.RequestRunPause(ctx, PauseRunCommand{RunID: running.Run.RunID})
		if err != nil || pausing.Run.Status != "pausing" ||
			!hasAvailableAction(pausing.AvailableActions, "resume_run") {
			t.Fatalf("RequestRunPause(active) = %+v, error = %v", pausing, err)
		}
		resumed, err := store.ResumeRun(ctx, ResumeRunCommand{RunID: running.Run.RunID})
		if err != nil || resumed.Run.Status != "running" {
			t.Fatalf("ResumeRun(pausing) = %+v, error = %v", resumed, err)
		}
		attempt, err := store.GetExecutionAttempt(ctx, claim.Attempt.AttemptID)
		if err != nil || attempt.Status != "running" {
			t.Fatalf("withdrawn pause attempt = %+v, error = %v", attempt, err)
		}
	})

	t.Run("expired execution converges pausing run", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer store.Close()
		currentTime := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
		store.now = func() time.Time { return currentTime }

		project, _, initial := startNovelRunForLifecycle(t, store, "暂停租约恢复")
		running := approveAndResumeLifecycleRun(t, store, project, initial)
		claim := claimStructuredTaskWithLease(t, store, 30)
		pausing, err := store.RequestRunPause(ctx, PauseRunCommand{RunID: running.Run.RunID})
		if err != nil || pausing.Run.Status != "pausing" {
			t.Fatalf("RequestRunPause(active) = %+v, error = %v", pausing, err)
		}
		currentTime = currentTime.Add(31 * time.Second)
		claimed, err := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
			WorkerID: "worker_recovery", ExecutorIDs: []string{"worker.structured_content"},
			ProviderID: "provider_test", LeaseSeconds: 30,
		})
		if err != nil || claimed != nil {
			t.Fatalf("ClaimExecutionTask(converge pause) = %+v, error = %v", claimed, err)
		}
		converged, err := store.GetRunSnapshot(ctx, running.Run.RunID)
		if err != nil || converged.Run.Status != "paused" ||
			converged.Steps[len(converged.Steps)-1].Status != "paused" {
			t.Fatalf("converged pause = %+v, error = %v", converged, err)
		}
		attempt, err := store.GetExecutionAttempt(ctx, claim.Attempt.AttemptID)
		if err != nil || attempt.Status != "abandoned" {
			t.Fatalf("expired pausing attempt = %+v, error = %v", attempt, err)
		}
	})
}

func TestExecutorAttemptGuards(t *testing.T) {
	t.Run("accept once without committing artifact", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer store.Close()

		project, _, initial := startNovelRunForLifecycle(t, store, "执行结果接收")
		running := approveAndResumeLifecycleRun(t, store, project, initial)
		tasks, err := store.ListTaskItems(ctx, *running.Run.CurrentStepRunID)
		if err != nil || len(tasks) != 1 || tasks[0].Status != "pending" {
			t.Fatalf("queued tasks = %+v, error = %v", tasks, err)
		}
		noClaim, err := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
			WorkerID:     "worker_wrong",
			ExecutorIDs:  []string{"worker.video_multimodal"},
			ProviderID:   "provider_test",
			LeaseSeconds: 60,
		})
		if err != nil || noClaim != nil {
			t.Fatalf("wrong executor claim = %+v, error = %v", noClaim, err)
		}
		claim, err := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
			WorkerID:     "worker_1",
			ExecutorIDs:  []string{"worker.structured_content"},
			ProviderID:   "provider_test",
			LeaseSeconds: 60,
		})
		if err != nil {
			t.Fatalf("ClaimExecutionTask() error = %v", err)
		}
		if claim == nil || claim.AttemptToken == "" || claim.Task.Status != "running" ||
			claim.Attempt.Status != "running" || claim.Attempt.InputSnapshotHash == "" {
			t.Fatalf("claim = %+v", claim)
		}
		var storedTokenHash string
		if err := store.db.QueryRowContext(ctx, `
			SELECT token_hash FROM execution_attempts WHERE attempt_id = ?`,
			claim.Attempt.AttemptID,
		).Scan(&storedTokenHash); err != nil {
			t.Fatalf("read token hash: %v", err)
		}
		if storedTokenHash == claim.AttemptToken {
			t.Fatal("attempt token was stored in plaintext")
		}
		secondClaim, err := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
			WorkerID:     "worker_2",
			ExecutorIDs:  []string{"worker.structured_content"},
			ProviderID:   "provider_test",
			LeaseSeconds: 60,
		})
		if err != nil || secondClaim != nil {
			t.Fatalf("duplicate claim = %+v, error = %v", secondClaim, err)
		}

		payload := json.RawMessage(`{"story_bible":{"title":"测试故事"}}`)
		_, err = store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{
			AttemptID:         claim.Attempt.AttemptID,
			AttemptToken:      "wrong-token",
			InputSnapshotHash: claim.Attempt.InputSnapshotHash,
			ResponsePayload:   payload,
		})
		assertDomainCode(t, err, "ATTEMPT_TOKEN_INVALID")
		_, err = store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{
			AttemptID:         claim.Attempt.AttemptID,
			AttemptToken:      claim.AttemptToken,
			InputSnapshotHash: "wrong-input-hash",
			ResponsePayload:   payload,
		})
		assertDomainCode(t, err, "ATTEMPT_INPUT_CHANGED")
		received, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{
			AttemptID:         claim.Attempt.AttemptID,
			AttemptToken:      claim.AttemptToken,
			InputSnapshotHash: claim.Attempt.InputSnapshotHash,
			ResponsePayload:   payload,
			Usage:             json.RawMessage(`{"input_tokens":100,"output_tokens":20}`),
			TraceRef:          "trace_test_1",
		})
		if err != nil || received.Status != "result_received" || received.ResponseHash == nil {
			t.Fatalf("received attempt = %+v, error = %v", received, err)
		}
		repeated, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{
			AttemptID:         claim.Attempt.AttemptID,
			AttemptToken:      claim.AttemptToken,
			InputSnapshotHash: claim.Attempt.InputSnapshotHash,
			ResponsePayload:   payload,
		})
		if err != nil || repeated.AttemptID != received.AttemptID ||
			repeated.ResponseHash == nil || *repeated.ResponseHash != *received.ResponseHash {
			t.Fatalf("repeated result = %+v, error = %v", repeated, err)
		}
		_, err = store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{
			AttemptID:         claim.Attempt.AttemptID,
			AttemptToken:      claim.AttemptToken,
			InputSnapshotHash: claim.Attempt.InputSnapshotHash,
			ResponsePayload:   json.RawMessage(`{"story_bible":{"title":"不同结果"}}`),
		})
		assertDomainCode(t, err, "ATTEMPT_RESULT_CONFLICT")
		artifacts, err := store.ListArtifactsByRun(ctx, running.Run.RunID)
		if err != nil || len(artifacts) != 2 ||
			artifacts[0].ArtifactType != "source_input" ||
			artifacts[1].ArtifactType != "source_manifest" {
			t.Fatalf("artifacts before adapter commit = %+v, error = %v", artifacts, err)
		}
		tasks, err = store.ListTaskItems(ctx, *running.Run.CurrentStepRunID)
		if err != nil || tasks[0].Status != "running" {
			t.Fatalf("task advanced before adapter commit = %+v, error = %v", tasks, err)
		}
	})

	t.Run("pause and cancel reject late results", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer store.Close()

		project, _, initial := startNovelRunForLifecycle(t, store, "迟到结果")
		running := approveAndResumeLifecycleRun(t, store, project, initial)
		claim := claimStructuredTask(t, store)
		_, err = store.RequestRunPause(ctx, PauseRunCommand{RunID: running.Run.RunID})
		if err != nil {
			t.Fatalf("RequestRunPause() error = %v", err)
		}
		lateCommand := SubmitExecutionResultCommand{
			AttemptID:         claim.Attempt.AttemptID,
			AttemptToken:      claim.AttemptToken,
			InputSnapshotHash: claim.Attempt.InputSnapshotHash,
			ResponsePayload:   json.RawMessage(`{"late":true}`),
		}
		paused, err := store.CompleteRunPauseAtSafeBoundary(ctx, CompleteRunPauseCommand{
			RunID:      running.Run.RunID,
			TaskCursor: json.RawMessage(`{"next_item":1}`),
		})
		if err != nil || paused.Run.Status != "paused" {
			t.Fatalf("safe pause = %+v, error = %v", paused, err)
		}
		_, err = store.SubmitExecutionResult(ctx, lateCommand)
		assertDomainCode(t, err, "ATTEMPT_STALE")
		abandoned, err := store.GetExecutionAttempt(ctx, claim.Attempt.AttemptID)
		if err != nil || abandoned.Status != "abandoned" {
			t.Fatalf("abandoned attempt = %+v, error = %v", abandoned, err)
		}

		resumed, err := store.ResumeRun(ctx, ResumeRunCommand{RunID: running.Run.RunID})
		if err != nil || resumed.Run.Status != "running" {
			t.Fatalf("ResumeRun() error = %v, snapshot = %+v", err, resumed)
		}
		secondClaim := claimStructuredTask(t, store)
		_, err = store.CancelRun(ctx, CancelRunCommand{
			RunID:     running.Run.RunID,
			Confirmed: true,
		})
		if err != nil {
			t.Fatalf("CancelRun() error = %v", err)
		}
		_, err = store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{
			AttemptID:         secondClaim.Attempt.AttemptID,
			AttemptToken:      secondClaim.AttemptToken,
			InputSnapshotHash: secondClaim.Attempt.InputSnapshotHash,
			ResponsePayload:   json.RawMessage(`{"late_after_cancel":true}`),
		})
		assertDomainCode(t, err, "ATTEMPT_STALE")
		cancelledAttempt, err := store.GetExecutionAttempt(ctx, secondClaim.Attempt.AttemptID)
		if err != nil || cancelledAttempt.Status != "cancelled" {
			t.Fatalf("cancelled attempt = %+v, error = %v", cancelledAttempt, err)
		}
	})

	t.Run("expired lease creates a new attempt", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer store.Close()
		currentTime := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
		store.now = func() time.Time { return currentTime }

		project, _, initial := startNovelRunForLifecycle(t, store, "Lease 过期")
		_ = approveAndResumeLifecycleRun(t, store, project, initial)
		firstClaim := claimStructuredTaskWithLease(t, store, 30)
		currentTime = currentTime.Add(31 * time.Second)
		secondClaim := claimStructuredTaskWithLease(t, store, 30)
		if secondClaim.Attempt.AttemptNo != 2 ||
			secondClaim.Attempt.AttemptID == firstClaim.Attempt.AttemptID {
			t.Fatalf("second claim = %+v, first = %+v", secondClaim, firstClaim)
		}
		if secondClaim.ContextPack.ContextPackID == firstClaim.ContextPack.ContextPackID ||
			secondClaim.ContextPack.ContextHash != firstClaim.ContextPack.ContextHash ||
			secondClaim.Attempt.InputSnapshotHash != firstClaim.Attempt.InputSnapshotHash {
			t.Fatalf(
				"retry context changed: first=%+v second=%+v",
				firstClaim.ContextPack,
				secondClaim.ContextPack,
			)
		}
		expired, err := store.GetExecutionAttempt(ctx, firstClaim.Attempt.AttemptID)
		if err != nil || expired.Status != "expired" ||
			expired.ErrorCode == nil || *expired.ErrorCode != "ATTEMPT_LEASE_EXPIRED" {
			t.Fatalf("expired attempt = %+v, error = %v", expired, err)
		}
		var retryEventCount int
		if err := store.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM events
			WHERE run_id = ? AND event_type = 'task.retry_scheduled'`,
			secondClaim.Task.RunID,
		).Scan(&retryEventCount); err != nil || retryEventCount != 1 {
			t.Fatalf("retry event count = %d, error = %v", retryEventCount, err)
		}
		_, err = store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{
			AttemptID:         firstClaim.Attempt.AttemptID,
			AttemptToken:      firstClaim.AttemptToken,
			InputSnapshotHash: firstClaim.Attempt.InputSnapshotHash,
			ResponsePayload:   json.RawMessage(`{"too_late":true}`),
		})
		assertDomainCode(t, err, "ATTEMPT_STALE")
	})
}

func TestStepExecutionContextPack(t *testing.T) {
	t.Run("contains only declared immutable execution inputs", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer store.Close()

		project, sourceAsset, initial := startNovelRunForLifecycle(t, store, "Context Pack")
		running := approveAndResumeLifecycleRun(t, store, project, initial)
		sourceClaim, err := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
			WorkerID:     "worker_context",
			ExecutorIDs:  []string{"worker.structured_content"},
			ProviderID:   "provider_context",
			ModelID:      "model_context",
			LeaseSeconds: 60,
		})
		if err != nil {
			t.Fatalf("ClaimExecutionTask(source analysis) error = %v", err)
		}
		sourcePack := sourceClaim.ContextPack
		if sourcePack.Step.StepID != "build_story_bible" ||
			sourcePack.Intent.Operation != "generate_task_checkpoint" ||
			sourcePack.OutputContract.ArtifactType != "task_checkpoint:source_analysis" ||
			sourcePack.Prompt.Ref != "../../design/prompts/novel-to-script/source-analysis.v1.md" ||
			len(sourcePack.AssetContext) != 1 ||
			sourcePack.AssetContext[0].Content != "" ||
			!strings.Contains(string(sourcePack.TaskCursor), "SRC-C001-B001") {
			t.Fatalf("source analysis context pack = %+v", sourcePack)
		}
		commitSourceAnalysisClaim(t, store, sourceClaim)

		claim, err := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
			WorkerID:     "worker_context",
			ExecutorIDs:  []string{"worker.structured_content"},
			ProviderID:   "provider_context",
			ModelID:      "model_context",
			LeaseSeconds: 60,
		})
		if err != nil {
			t.Fatalf("ClaimExecutionTask(story aggregate) error = %v", err)
		}
		pack := claim.ContextPack
		if pack.ContextPackID == "" || pack.ContextPackVersion != "1.0.0" ||
			pack.PackType != "step_execution" ||
			pack.ProjectID != project.ProjectID ||
			pack.Run.RunID != running.Run.RunID ||
			pack.Step.StepID != "build_story_bible" ||
			pack.Intent.ArtifactType != "story_bible" ||
			pack.ContextHash != claim.Attempt.InputSnapshotHash {
			t.Fatalf("context pack identity = %+v, attempt = %+v", pack, claim.Attempt)
		}
		if len(pack.AssetContext) != 1 ||
			pack.AssetContext[0].AssetID != sourceAsset.Asset.AssetID ||
			pack.AssetContext[0].AssetSnapshotID != sourceAsset.Snapshot.AssetSnapshotID ||
			pack.AssetContext[0].Content != "" {
			t.Fatalf("asset context = %+v", pack.AssetContext)
		}
		if len(pack.UpstreamContext) != 2 ||
			pack.UpstreamContext[0].ArtifactType != "source_input" ||
			pack.UpstreamContext[0].Status != "confirmed" ||
			pack.UpstreamContext[1].ArtifactType != "source_manifest" ||
			pack.UpstreamContext[1].Status != "confirmed" ||
			len(pack.Rules) != 3 ||
			pack.Prompt.Ref != "../../design/prompts/novel-to-script/story-bible.v1.md" ||
			!strings.Contains(pack.Prompt.Content, "来源分析聚合 / 故事圣经") ||
			!strings.Contains(string(pack.TaskCursor), `"source_analysis"`) ||
			!jsonObject(pack.OutputContract.Schema) {
			t.Fatalf("worker inputs = %+v", pack)
		}
		var manifest sourceManifestPayload
		if err := json.Unmarshal(pack.UpstreamContext[1].Content, &manifest); err != nil {
			t.Fatalf("decode source manifest: %v", err)
		}
		if manifest.ProjectID != project.ProjectID ||
			manifest.RunInputSnapshotVersionID != running.Run.CurrentInputSnapshotVersionID ||
			manifest.SourceKind != "novel" ||
			len(manifest.Units) != 1 ||
			manifest.Units[0].SourceUnitID != "SRC-C001-B001" ||
			manifest.Units[0].ContentHash == "" ||
			len(manifest.CoveredSourceUnitIDs) != 1 ||
			manifest.CoveredSourceUnitIDs[0] != manifest.Units[0].SourceUnitID ||
			manifest.TruncationRisk ||
			len(manifest.MissingOrUnreadScope) != 0 {
			t.Fatalf("source manifest = %+v", manifest)
		}
		if pack.Prompt.ContentHash != sha256Hex([]byte(pack.Prompt.Content)) {
			t.Fatalf("prompt content hash = %s", pack.Prompt.ContentHash)
		}
		for _, rule := range pack.Rules {
			if !strings.HasPrefix(rule.Ref, "../../design/rules/") ||
				strings.Contains(rule.Ref, "novel2script_agent_project") ||
				rule.ContentHash != sha256Hex([]byte(rule.Content)) {
				t.Fatalf("rule document = %+v", rule)
			}
		}
		if pack.Budget.ProviderID != "provider_context" ||
			pack.Budget.ModelID == nil || *pack.Budget.ModelID != "model_context" ||
			pack.Budget.RequiredCharacters <= 0 ||
			pack.Budget.TruncationApplied {
			t.Fatalf("context budget = %+v", pack.Budget)
		}
		hashInput := pack
		hashInput.ContextPackID = ""
		hashInput.ContextHash = ""
		hashInput.CreatedAt = time.Time{}
		encoded, err := json.Marshal(hashInput)
		if err != nil {
			t.Fatalf("marshal hash input: %v", err)
		}
		if calculated := sha256Hex(encoded); calculated != pack.ContextHash {
			t.Fatalf("context hash = %s, calculated = %s", pack.ContextHash, calculated)
		}

		var storedHash, storedPayload string
		if err := store.db.QueryRowContext(ctx, `
			SELECT context_hash, payload_json
			FROM context_packs WHERE attempt_id = ?`,
			claim.Attempt.AttemptID,
		).Scan(&storedHash, &storedPayload); err != nil {
			t.Fatalf("read stored context pack: %v", err)
		}
		if storedHash != pack.ContextHash ||
			!strings.Contains(storedPayload, "第一章 少年下山。") ||
			strings.Contains(storedPayload, "attempt_token") {
			t.Fatalf("stored context pack hash/content mismatch")
		}
	})

	t.Run("uses bound approval snapshot instead of current version", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer store.Close()

		project, sourceAsset, initial := startNovelRunForLifecycle(t, store, "固定上游版本")
		_ = approveAndResumeLifecycleRun(t, store, project, initial)
		newAsset := createTextAsset(t, store, project.ProjectID, "replacement.txt", "新版本内容，不应进入当前 Task。")
		sourceArtifact := initial.Artifacts[0]
		baseVersion, err := store.GetArtifactVersion(ctx, sourceArtifact.CurrentVersionID)
		if err != nil {
			t.Fatalf("GetArtifactVersion() error = %v", err)
		}
		newPayload := mustJSON(t, map[string]any{
			"input_snapshot_id": initial.Run.CurrentInputSnapshotVersionID,
			"source_kind":       "novel",
			"assets": []map[string]any{{
				"asset_id":          newAsset.Asset.AssetID,
				"asset_snapshot_id": newAsset.Snapshot.AssetSnapshotID,
				"role":              "primary_source",
				"order":             1,
			}},
			"user_request_message_id": initial.CurrentApproval.SubjectRefID,
			"confirmed_strategy_refs": []string{},
		})
		if _, err := store.CreateArtifactVersion(ctx, CreateVersionCommand{
			ArtifactID:    sourceArtifact.ArtifactID,
			BaseVersionID: baseVersion.ArtifactVersionID,
			BaseVersion:   baseVersion.Version,
			NewPayload:    newPayload,
		}); err != nil {
			t.Fatalf("CreateArtifactVersion() error = %v", err)
		}
		claim := claimStructuredTask(t, store)
		if len(claim.ContextPack.AssetContext) != 1 ||
			claim.ContextPack.AssetContext[0].AssetID != sourceAsset.Asset.AssetID ||
			claim.ContextPack.AssetContext[0].Content != "" ||
			!strings.Contains(string(claim.ContextPack.TaskCursor), "第一章 少年下山。") ||
			strings.Contains(string(claim.ContextPack.TaskCursor), "新版本内容") {
			t.Fatalf("context drifted to current version: %+v", claim.ContextPack.AssetContext)
		}
		if claim.ContextPack.UpstreamContext[0].ArtifactVersionID != baseVersion.ArtifactVersionID {
			t.Fatalf("upstream version = %+v, want %s", claim.ContextPack.UpstreamContext, baseVersion.ArtifactVersionID)
		}
	})

	t.Run("rejects cross project upstream reference", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer store.Close()

		projectA, _, initialA := startNovelRunForLifecycle(t, store, "作品 A")
		runningA := approveAndResumeLifecycleRun(t, store, projectA, initialA)
		_, _, initialB := startNovelRunForLifecycle(t, store, "作品 B")
		approvedB, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
			ApprovalRequestID:       initialB.CurrentApproval.ApprovalRequestID,
			Action:                  "approve",
			ExpectedApprovalVersion: initialB.CurrentApproval.Version,
			SubjectSnapshotHash:     initialB.CurrentApproval.SubjectSnapshotHash,
		})
		if err != nil {
			t.Fatalf("ResolveApproval(B) error = %v", err)
		}
		_ = approvedB
		tasks, err := store.ListTaskItems(ctx, *runningA.Run.CurrentStepRunID)
		if err != nil || len(tasks) != 1 {
			t.Fatalf("tasks A = %+v, error = %v", tasks, err)
		}
		tampered := mustJSON(t, map[string]any{
			"run_input_snapshot_version_id": runningA.Run.CurrentInputSnapshotVersionID,
			"step_input_versions": []map[string]any{
				{
					"artifact_id":         initialB.Artifacts[0].ArtifactID,
					"artifact_version_id": initialB.Artifacts[0].CurrentVersionID,
					"version":             1,
					"status":              "confirmed",
				},
				{
					"artifact_id":         initialB.Artifacts[0].ArtifactID,
					"artifact_version_id": initialB.Artifacts[0].CurrentVersionID,
					"version":             1,
					"status":              "confirmed",
				},
			},
		})
		if _, err := store.db.ExecContext(ctx, `
			UPDATE task_items SET input_snapshot_json = ? WHERE task_item_id = ?`,
			string(tampered),
			tasks[0].TaskItemID,
		); err != nil {
			t.Fatalf("tamper task snapshot: %v", err)
		}
		_, err = store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
			WorkerID:     "worker_cross_project",
			ExecutorIDs:  []string{"worker.structured_content"},
			ProviderID:   "provider_test",
			LeaseSeconds: 60,
		})
		assertDomainCode(t, err, "CONTEXT_PROJECT_MISMATCH")
		var attemptCount, contextCount int
		if err := store.db.QueryRowContext(ctx, `
			SELECT
				(SELECT COUNT(*) FROM execution_attempts WHERE task_item_id = ?),
				(SELECT COUNT(*) FROM context_packs WHERE task_item_id = ?)`,
			tasks[0].TaskItemID,
			tasks[0].TaskItemID,
		).Scan(&attemptCount, &contextCount); err != nil {
			t.Fatalf("read rejected claim counts: %v", err)
		}
		if attemptCount != 0 || contextCount != 0 {
			t.Fatalf("rejected claim created attempt=%d context=%d", attemptCount, contextCount)
		}
	})

	t.Run("rejects required content over budget without truncation", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer store.Close()

		project, _, initial := startNovelRunWithContent(
			t,
			store,
			"长文本预算",
			strings.Repeat("甲", maxContextInputCharacters),
		)
		running := approveAndResumeLifecycleRun(t, store, project, initial)
		_, err = store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
			WorkerID:     "worker_budget",
			ExecutorIDs:  []string{"worker.structured_content"},
			ProviderID:   "provider_test",
			LeaseSeconds: 60,
		})
		assertDomainCode(t, err, "CONTEXT_REQUIRED_INPUT_EXCEEDS_BUDGET")
		tasks, taskErr := store.ListTaskItems(ctx, *running.Run.CurrentStepRunID)
		if taskErr != nil || len(tasks) != 1 || tasks[0].Status != "pending" {
			t.Fatalf("budget rejected task = %+v, error = %v", tasks, taskErr)
		}
		var attemptCount int
		if err := store.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM execution_attempts WHERE task_item_id = ?`,
			tasks[0].TaskItemID,
		).Scan(&attemptCount); err != nil || attemptCount != 0 {
			t.Fatalf("budget rejection attempts = %d, error = %v", attemptCount, err)
		}
	})
}

func TestExecutionAttemptFailurePolicy(t *testing.T) {
	t.Run("retryable failure schedules one automatic retry", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer store.Close()

		project, _, initial := startNovelRunForLifecycle(t, store, "自动重试")
		running := approveAndResumeLifecycleRun(t, store, project, initial)
		firstClaim := claimStructuredTask(t, store)
		technicalDetail := "provider request exceeded the rate limit"
		providerOutput := "raw provider response"
		requestID := "request-test-1"
		command := FailExecutionAttemptCommand{
			AttemptID:         firstClaim.Attempt.AttemptID,
			AttemptToken:      firstClaim.AttemptToken,
			InputSnapshotHash: firstClaim.Attempt.InputSnapshotHash,
			ErrorCode:         "MODEL_RATE_LIMIT",
			FailureDetail: &ExecutionFailureDetail{
				ErrorCode:         "MODEL_RATE_LIMIT",
				Stage:             "provider_call",
				Summary:           "模型服务当前请求过多。",
				TechnicalDetail:   &technicalDetail,
				ProviderOutput:    &providerOutput,
				ProviderRequestID: &requestID,
				Retryable:         true,
			},
		}
		failed, err := store.FailExecutionAttempt(ctx, command)
		if err != nil {
			t.Fatalf("FailExecutionAttempt() error = %v", err)
		}
		if !failed.Retryable || failed.Attempt.Status != "failed" ||
			failed.Task.Status != "pending" ||
			failed.Task.CurrentAttemptID != nil ||
			failed.Task.Failure == nil || *failed.Task.Failure != "MODEL_RATE_LIMIT" {
			t.Fatalf("first failure result = %+v", failed)
		}
		tasks, err := store.ListTaskItems(ctx, *running.Run.CurrentStepRunID)
		var failedTask *TaskItem
		for index := range tasks {
			if tasks[index].TaskItemID == firstClaim.Task.TaskItemID {
				failedTask = &tasks[index]
				break
			}
		}
		if err != nil || failedTask == nil || failedTask.FailureDetail == nil ||
			failedTask.FailureDetail.Summary != "模型服务当前请求过多。" ||
			failedTask.FailureDetail.ProviderRequestID == nil ||
			*failedTask.FailureDetail.ProviderRequestID != requestID ||
			failedTask.FailureDetail.ProviderOutput == nil ||
			*failedTask.FailureDetail.ProviderOutput != providerOutput ||
			!failedTask.FailureDetail.Retryable {
			t.Fatalf("persisted failure detail = %+v, error = %v", tasks, err)
		}
		repeated, err := store.FailExecutionAttempt(ctx, command)
		if err != nil || !repeated.Retryable ||
			repeated.Attempt.AttemptID != failed.Attempt.AttemptID {
			t.Fatalf("repeated failure = %+v, error = %v", repeated, err)
		}
		secondClaim := claimStructuredTask(t, store)
		if secondClaim.Attempt.AttemptNo != 2 {
			t.Fatalf("second attempt = %+v", secondClaim.Attempt)
		}
		exhausted, err := store.FailExecutionAttempt(ctx, FailExecutionAttemptCommand{
			AttemptID:         secondClaim.Attempt.AttemptID,
			AttemptToken:      secondClaim.AttemptToken,
			InputSnapshotHash: secondClaim.Attempt.InputSnapshotHash,
			ErrorCode:         "MODEL_RATE_LIMIT",
		})
		if err != nil {
			t.Fatalf("FailExecutionAttempt(exhausted) error = %v", err)
		}
		if exhausted.Retryable || exhausted.Task.Status != "failed" {
			t.Fatalf("exhausted failure = %+v", exhausted)
		}
		run, err := store.GetRun(ctx, running.Run.RunID)
		if err != nil || run.Status != "failed" {
			t.Fatalf("run after exhausted retry = %+v, error = %v", run, err)
		}
		steps, err := store.ListStepRuns(ctx, running.Run.RunID)
		if err != nil || steps[len(steps)-1].Status != "failed" {
			t.Fatalf("steps after exhausted retry = %+v, error = %v", steps, err)
		}
		var retryEvents, runFailedEvents int
		if err := store.db.QueryRowContext(ctx, `
			SELECT
				SUM(CASE WHEN event_type = 'task.retry_scheduled' THEN 1 ELSE 0 END),
				SUM(CASE WHEN event_type = 'run.failed' THEN 1 ELSE 0 END)
			FROM events WHERE run_id = ?`,
			running.Run.RunID,
		).Scan(&retryEvents, &runFailedEvents); err != nil {
			t.Fatalf("read failure events: %v", err)
		}
		if retryEvents != 1 || runFailedEvents != 1 {
			t.Fatalf("failure events retry=%d run_failed=%d", retryEvents, runFailedEvents)
		}
		failedSnapshot, err := store.GetRunSnapshot(ctx, running.Run.RunID)
		if err != nil || !containsAvailableAction(failedSnapshot.AvailableActions, "retry_failed_step") {
			t.Fatalf("failed available actions = %+v, error = %v", failedSnapshot.AvailableActions, err)
		}
		retried, err := store.RetryFailedStep(ctx, RetryFailedStepCommand{
			CommandMeta: CommandMeta{
				Scope:          project.ProjectID,
				CommandType:    "retry_failed_step",
				IdempotencyKey: "retry_failed_step_1",
				RequestHash:    "retry_failed_step_hash_1",
			},
			StepRunID: steps[len(steps)-1].StepRunID,
		})
		if err != nil || retried.Run.Status != "running" ||
			!containsAvailableAction(retried.AvailableActions, "pause_run") {
			t.Fatalf("RetryFailedStep() = %+v, error = %v", retried, err)
		}
		retriedTasks, err := store.ListTaskItems(ctx, steps[len(steps)-1].StepRunID)
		if err != nil {
			t.Fatalf("retried tasks = %+v, error = %v", retriedTasks, err)
		}
		var retriedFailedTask *TaskItem
		for index := range retriedTasks {
			if retriedTasks[index].TaskItemID == secondClaim.Task.TaskItemID {
				retriedFailedTask = &retriedTasks[index]
				break
			}
		}
		if retriedFailedTask == nil || retriedFailedTask.Status != "pending" ||
			retriedFailedTask.Failure != nil || retriedFailedTask.FailureDetail != nil {
			t.Fatalf("retried failed task = %+v, all tasks = %+v", retriedFailedTask, retriedTasks)
		}
	})

	t.Run("non retryable failure fails run immediately", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer store.Close()

		project, _, initial := startNovelRunForLifecycle(t, store, "不可重试")
		running := approveAndResumeLifecycleRun(t, store, project, initial)
		claim := claimStructuredTask(t, store)
		result, err := store.FailExecutionAttempt(ctx, FailExecutionAttemptCommand{
			AttemptID:         claim.Attempt.AttemptID,
			AttemptToken:      claim.AttemptToken,
			InputSnapshotHash: claim.Attempt.InputSnapshotHash,
			ErrorCode:         "PROVIDER_AUTH_FAILED",
		})
		if err != nil {
			t.Fatalf("FailExecutionAttempt() error = %v", err)
		}
		if result.Retryable || result.Task.Status != "failed" ||
			result.Attempt.ErrorCode == nil ||
			*result.Attempt.ErrorCode != "PROVIDER_AUTH_FAILED" {
			t.Fatalf("non-retryable failure = %+v", result)
		}
		run, err := store.GetRun(ctx, running.Run.RunID)
		if err != nil || run.Status != "failed" {
			t.Fatalf("failed run = %+v, error = %v", run, err)
		}
		failedProject, err := store.GetProject(ctx, project.ProjectID)
		if err != nil {
			t.Fatalf("GetProject(failed run) error = %v", err)
		}
		if failedProject.ActiveWriteRunID == nil ||
			*failedProject.ActiveWriteRunID != run.RunID ||
			failedProject.Status != "failed" {
			t.Fatalf("failed run did not retain project lock: %+v", failedProject)
		}
		failedSnapshot, err := store.GetRunSnapshot(ctx, run.RunID)
		if err != nil || containsAvailableAction(failedSnapshot.AvailableActions, "retry_failed_step") {
			t.Fatalf("non-retryable available actions = %+v, error = %v", failedSnapshot.AvailableActions, err)
		}
		exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
			ConversationID: project.PrimaryConversationID,
			Request:        agentcontract.MessageRequest{Content: "重新开始"},
			Decision: agentcontract.AgentDecision{
				Reply:      "当前任务结束后才能开始新的生成。",
				Intent:     "chat",
				Confidence: 1,
			},
		})
		if err != nil {
			t.Fatalf("CreateMessageExchange() during failed run error = %v", err)
		}
		if exchange.AgentMessage.Content == "" {
			t.Fatal("CreateMessageExchange() returned an empty assistant message")
		}
	})
}

func containsAvailableAction(actions []AvailableAction, actionID string) bool {
	for _, action := range actions {
		if action.ActionID == actionID && action.Enabled {
			return true
		}
	}
	return false
}

func TestExecutionResultArtifactCommit(t *testing.T) {
	t.Run("adapt validate and commit exactly once", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer store.Close()

		project, _, initial := startNovelRunForLifecycle(t, store, "正式产物提交")
		running := approveAndResumeLifecycleRun(t, store, project, initial)
		claim := claimStructuredTask(t, store)
		payload := validStoryBibleProviderResponse()
		received, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{
			AttemptID:         claim.Attempt.AttemptID,
			AttemptToken:      claim.AttemptToken,
			InputSnapshotHash: claim.Attempt.InputSnapshotHash,
			ResponsePayload:   payload,
		})
		if err != nil || received.ResponseHash == nil {
			t.Fatalf("SubmitExecutionResult() = %+v, error = %v", received, err)
		}
		committed, err := store.CommitExecutionResult(ctx, CommitExecutionResultCommand{
			AttemptID:            claim.Attempt.AttemptID,
			ExpectedResponseHash: *received.ResponseHash,
		})
		if err != nil {
			t.Fatalf("CommitExecutionResult() error = %v", err)
		}
		if committed.Artifact.ArtifactType != "story_bible" ||
			committed.ArtifactVersion.Status != "pending_approval" ||
			committed.ArtifactVersion.CreatedByKind != "model" ||
			committed.ArtifactVersion.ActorRef != claim.Attempt.AttemptID {
			t.Fatalf("committed artifact = %+v, version = %+v", committed.Artifact, committed.ArtifactVersion)
		}
		if committed.Approval.Status != "pending" ||
			committed.Approval.SubjectRefID != committed.ArtifactVersion.ArtifactVersionID ||
			committed.RunSnapshot.Run.Status != "waiting_approval" ||
			committed.RunSnapshot.Steps[len(committed.RunSnapshot.Steps)-1].Status != "waiting_approval" {
			t.Fatalf("commit state = %+v", committed)
		}
		var adapted map[string]any
		if err := json.Unmarshal(committed.ArtifactVersion.Payload, &adapted); err != nil {
			t.Fatalf("decode adapted payload: %v", err)
		}
		if _, exists := adapted["next_action"]; exists {
			t.Fatal("model next_action leaked into artifact payload")
		}
		if _, exists := adapted["status"]; exists {
			t.Fatal("model status leaked into artifact payload")
		}
		structure := adapted["source_structure"].([]any)
		firstUnit := structure[0].(map[string]any)
		if _, exists := firstUnit["source_evidence"]; exists {
			t.Fatal("legacy source_evidence was not removed")
		}
		if _, exists := firstUnit["source_refs"]; !exists {
			t.Fatal("legacy source_evidence was not normalized to source_refs")
		}
		trace := adapted["source_trace"].(map[string]any)
		if len(trace["grounded"].([]any)) != 1 || len(trace["inferred"].([]any)) != 1 {
			t.Fatalf("normalized source trace = %+v", trace)
		}
		lineage, err := store.GetArtifactVersionLineage(
			ctx,
			committed.ArtifactVersion.ArtifactVersionID,
			2,
		)
		if err != nil {
			t.Fatalf("GetArtifactVersionLineage() error = %v", err)
		}
		directUpstream := map[string]bool{}
		assetEdges := 0
		configEdges := 0
		for _, edge := range lineage.Upstream {
			if edge.Depth == 1 && edge.UpstreamKind == "artifact_version" {
				directUpstream[edge.UpstreamRefID] = true
			}
			if edge.Depth == 1 && edge.UpstreamKind == "config_snapshot" &&
				edge.Relation == "configured_by" {
				configEdges++
			}
			if edge.Depth == 2 && edge.UpstreamKind == "asset_snapshot" {
				assetEdges++
			}
		}
		if len(lineage.Upstream) != 5 ||
			len(directUpstream) != 2 ||
			!directUpstream[claim.ContextPack.UpstreamContext[0].ArtifactVersionID] ||
			!directUpstream[claim.ContextPack.UpstreamContext[1].ArtifactVersionID] ||
			configEdges != 1 ||
			assetEdges != 2 {
			t.Fatalf("committed lineage = %+v", lineage)
		}

		tasks, err := store.ListTaskItems(ctx, *running.Run.CurrentStepRunID)
		if err != nil || len(tasks) != 2 ||
			tasks[0].ItemKey != "preparation:source_analysis" ||
			tasks[0].Status != "succeeded" ||
			tasks[0].OutputArtifactVersionID != nil ||
			tasks[1].ItemKey != "aggregate:story_bible_aggregate" ||
			tasks[1].Status != "succeeded" ||
			tasks[1].OutputArtifactVersionID == nil ||
			*tasks[1].OutputArtifactVersionID != committed.ArtifactVersion.ArtifactVersionID {
			t.Fatalf("committed task = %+v, error = %v", tasks, err)
		}
		attempt, err := store.GetExecutionAttempt(ctx, claim.Attempt.AttemptID)
		if err != nil || attempt.Status != "succeeded" {
			t.Fatalf("committed attempt = %+v, error = %v", attempt, err)
		}

		repeated, err := store.CommitExecutionResult(ctx, CommitExecutionResultCommand{
			AttemptID:            claim.Attempt.AttemptID,
			ExpectedResponseHash: *received.ResponseHash,
		})
		if err != nil {
			t.Fatalf("repeat CommitExecutionResult() error = %v", err)
		}
		if repeated.ArtifactVersion.ArtifactVersionID != committed.ArtifactVersion.ArtifactVersionID ||
			repeated.RunSnapshot.EventCursor != committed.RunSnapshot.EventCursor {
			t.Fatalf("repeated commit = %+v, initial = %+v", repeated, committed)
		}
		artifacts, err := store.ListArtifactsByRun(ctx, running.Run.RunID)
		if err != nil || len(artifacts) != 3 {
			t.Fatalf("artifacts after repeated commit = %+v, error = %v", artifacts, err)
		}
	})

	t.Run("context pack integrity failure rolls back formal artifact", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer store.Close()

		project, _, initial := startNovelRunForLifecycle(t, store, "上下文完整性回滚")
		running := approveAndResumeLifecycleRun(t, store, project, initial)
		claim := claimStructuredTask(t, store)
		received, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{
			AttemptID:         claim.Attempt.AttemptID,
			AttemptToken:      claim.AttemptToken,
			InputSnapshotHash: claim.Attempt.InputSnapshotHash,
			ResponsePayload:   validStoryBibleProviderResponse(),
		})
		if err != nil || received.ResponseHash == nil {
			t.Fatalf("SubmitExecutionResult() = %+v, error = %v", received, err)
		}
		if _, err := store.db.ExecContext(ctx, `
			UPDATE context_packs SET payload_json = '{}'
			WHERE attempt_id = ?`,
			claim.Attempt.AttemptID,
		); err != nil {
			t.Fatalf("tamper context pack: %v", err)
		}
		_, err = store.CommitExecutionResult(ctx, CommitExecutionResultCommand{
			AttemptID:            claim.Attempt.AttemptID,
			ExpectedResponseHash: *received.ResponseHash,
		})
		assertDomainCode(t, err, "CONTEXT_PACK_HASH_MISMATCH")
		artifacts, listErr := store.ListArtifactsByRun(ctx, running.Run.RunID)
		if listErr != nil || len(artifacts) != 2 ||
			artifacts[0].ArtifactType != "source_input" ||
			artifacts[1].ArtifactType != "source_manifest" {
			t.Fatalf("artifacts after context rejection = %+v, error = %v", artifacts, listErr)
		}
		var dependencyCount int
		if err := store.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM artifact_dependencies WHERE run_id = ?`,
			running.Run.RunID,
		).Scan(&dependencyCount); err != nil {
			t.Fatalf("count dependencies: %v", err)
		}
		if dependencyCount != 2 {
			t.Fatalf("dependency count after rollback = %d, want source and manifest asset edges", dependencyCount)
		}
	})

	t.Run("invalid output creates no formal artifact", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer store.Close()

		project, _, initial := startNovelRunForLifecycle(t, store, "非法产物拒绝")
		running := approveAndResumeLifecycleRun(t, store, project, initial)
		claim := claimStructuredTask(t, store)
		received, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{
			AttemptID:         claim.Attempt.AttemptID,
			AttemptToken:      claim.AttemptToken,
			InputSnapshotHash: claim.Attempt.InputSnapshotHash,
			ResponsePayload:   json.RawMessage(`{"story_bible":{"title":"字段不足","status":"confirmed"}}`),
		})
		if err != nil || received.ResponseHash == nil {
			t.Fatalf("SubmitExecutionResult() = %+v, error = %v", received, err)
		}
		_, err = store.CommitExecutionResult(ctx, CommitExecutionResultCommand{
			AttemptID:            claim.Attempt.AttemptID,
			ExpectedResponseHash: *received.ResponseHash,
		})
		assertDomainCode(t, err, "OUTPUT_SCHEMA_VALIDATION_FAILED")

		artifacts, err := store.ListArtifactsByRun(ctx, running.Run.RunID)
		if err != nil || len(artifacts) != 2 ||
			artifacts[0].ArtifactType != "source_input" ||
			artifacts[1].ArtifactType != "source_manifest" {
			t.Fatalf("formal artifacts after invalid output = %+v, error = %v", artifacts, err)
		}
		attempt, err := store.GetExecutionAttempt(ctx, claim.Attempt.AttemptID)
		if err != nil || attempt.Status != "result_received" {
			t.Fatalf("attempt after invalid output = %+v, error = %v", attempt, err)
		}
		tasks, err := store.ListTaskItems(ctx, *running.Run.CurrentStepRunID)
		if err != nil || len(tasks) != 2 ||
			tasks[0].ItemKey != "preparation:source_analysis" ||
			tasks[0].Status != "succeeded" ||
			tasks[0].OutputArtifactVersionID != nil ||
			tasks[1].ItemKey != "aggregate:story_bible_aggregate" ||
			tasks[1].Status != "running" ||
			tasks[1].OutputArtifactVersionID != nil {
			t.Fatalf("task after invalid output = %+v, error = %v", tasks, err)
		}
		run, err := store.GetRun(ctx, running.Run.RunID)
		if err != nil || run.Status != "running" {
			t.Fatalf("run after invalid output = %+v, error = %v", run, err)
		}
	})

	t.Run("pause accepts received result at safe boundary", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer store.Close()

		project, _, initial := startNovelRunForLifecycle(t, store, "暂停后拒绝提交")
		running := approveAndResumeLifecycleRun(t, store, project, initial)
		claim := claimStructuredTask(t, store)
		received, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{
			AttemptID:         claim.Attempt.AttemptID,
			AttemptToken:      claim.AttemptToken,
			InputSnapshotHash: claim.Attempt.InputSnapshotHash,
			ResponsePayload:   validStoryBibleProviderResponse(),
		})
		if err != nil || received.ResponseHash == nil {
			t.Fatalf("SubmitExecutionResult() = %+v, error = %v", received, err)
		}
		if _, err := store.RequestRunPause(ctx, PauseRunCommand{RunID: running.Run.RunID}); err != nil {
			t.Fatalf("RequestRunPause() error = %v", err)
		}
		committed, err := store.CommitExecutionResult(ctx, CommitExecutionResultCommand{
			AttemptID:            claim.Attempt.AttemptID,
			ExpectedResponseHash: *received.ResponseHash,
		})
		if err != nil || committed.RunSnapshot.Run.Status == "pausing" ||
			committed.RunSnapshot.Run.Status == "running" {
			t.Fatalf("CommitExecutionResult(after pause) = %+v, error = %v", committed, err)
		}
	})
}

func TestResultReceivedAttemptCanBeFailedAfterCommitValidation(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, _, initial := startNovelRunForLifecycle(t, store, "Commit validation failure")
	approveAndResumeLifecycleRun(t, store, project, initial)
	claim := claimStructuredTask(t, store)
	received, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{
		AttemptID:         claim.Attempt.AttemptID,
		AttemptToken:      claim.AttemptToken,
		InputSnapshotHash: claim.Attempt.InputSnapshotHash,
		ResponsePayload:   validStoryBibleProviderResponse(),
	})
	if err != nil || received.Status != "result_received" {
		t.Fatalf("SubmitExecutionResult() = %+v, error = %v", received, err)
	}

	failed, err := store.FailExecutionAttempt(ctx, FailExecutionAttemptCommand{
		AttemptID:         claim.Attempt.AttemptID,
		AttemptToken:      claim.AttemptToken,
		InputSnapshotHash: claim.Attempt.InputSnapshotHash,
		ErrorCode:         "OUTPUT_REPAIR_FAILED",
	})
	if err != nil {
		t.Fatalf("FailExecutionAttempt(result_received) error = %v", err)
	}
	if failed.Attempt.Status != "failed" || failed.Attempt.ErrorCode == nil ||
		*failed.Attempt.ErrorCode != "OUTPUT_REPAIR_FAILED" {
		t.Fatalf("failed attempt = %+v", failed.Attempt)
	}
}

func validStoryBibleProviderResponse() json.RawMessage {
	return mustJSONNoTest(map[string]any{
		"story_bible": map[string]any{
			"story_overview": map[string]any{
				"one_sentence_logline": "少年下山后卷入旧案。",
				"core_conflict":        "少年追查真相与幕后势力冲突。",
				"main_emotional_drive": "守护同伴并查明身世。",
				"genre_tags":           []string{"逆袭", "悬疑"},
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
			"model_inference":  []string{"旧案可作为后续悬念。"},
		},
		"next_action": map[string]any{"type": "continue"},
		"status":      "confirmed",
	})
}

func mustJSONNoTest(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func approveAndResumeLifecycleRun(
	t *testing.T,
	store *Store,
	project Project,
	initial RunSnapshot,
) RunSnapshot {
	t.Helper()
	ctx := context.Background()
	approved, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
		ApprovalRequestID:       initial.CurrentApproval.ApprovalRequestID,
		Action:                  "approve",
		ExpectedApprovalVersion: initial.CurrentApproval.Version,
		SubjectSnapshotHash:     initial.CurrentApproval.SubjectSnapshotHash,
	})
	if err != nil {
		t.Fatalf("ResolveApproval() error = %v", err)
	}
	running, err := store.ResumeRun(ctx, ResumeRunCommand{
		RunID: approved.Run.RunID,
	})
	if err != nil {
		t.Fatalf("ResumeRun() error = %v", err)
	}
	return running
}

func claimStructuredTask(t *testing.T, store *Store) *TaskClaim {
	t.Helper()
	return claimStructuredTaskWithLease(t, store, 60)
}

func hasAvailableAction(actions []AvailableAction, actionID string) bool {
	for _, action := range actions {
		if action.ActionID == actionID && action.Enabled {
			return true
		}
	}
	return false
}

func claimStructuredTaskWithLease(t *testing.T, store *Store, leaseSeconds int) *TaskClaim {
	t.Helper()
	claim := claimStructuredTaskOnce(t, store, leaseSeconds)
	if claim.ContextPack.OutputContract.ArtifactType != "task_checkpoint:source_analysis" {
		return claim
	}
	commitSourceAnalysisClaim(t, store, claim)
	return claimStructuredTaskOnce(t, store, leaseSeconds)
}

func commitSourceAnalysisClaim(t *testing.T, store *Store, claim *TaskClaim) {
	t.Helper()
	response := validSourceAnalysisProviderResponse(t, claim)
	received, err := store.SubmitExecutionResult(context.Background(), SubmitExecutionResultCommand{
		AttemptID:         claim.Attempt.AttemptID,
		AttemptToken:      claim.AttemptToken,
		InputSnapshotHash: claim.Attempt.InputSnapshotHash,
		ResponsePayload:   response,
	})
	if err != nil || received.ResponseHash == nil {
		t.Fatalf("SubmitExecutionResult(source analysis) = %+v, error = %v", received, err)
	}
	committed, err := store.CommitExecutionResult(context.Background(), CommitExecutionResultCommand{
		AttemptID:            claim.Attempt.AttemptID,
		ExpectedResponseHash: *received.ResponseHash,
	})
	if err != nil || committed.CommitStatus != "task_checkpointed" {
		t.Fatalf("CommitExecutionResult(source analysis) = %+v, error = %v", committed, err)
	}
}

func claimStructuredTaskOnce(t *testing.T, store *Store, leaseSeconds int) *TaskClaim {
	t.Helper()
	claim, err := store.ClaimExecutionTask(context.Background(), ClaimExecutionTaskCommand{
		WorkerID:     "worker_test",
		ExecutorIDs:  []string{"worker.structured_content"},
		ProviderID:   "provider_test",
		LeaseSeconds: leaseSeconds,
	})
	if err != nil {
		t.Fatalf("ClaimExecutionTask() error = %v", err)
	}
	if claim == nil {
		t.Fatal("ClaimExecutionTask() returned no task")
	}
	return claim
}

func TestClaimExecutionTaskSkipsCandidateWithoutSealedConfig(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	badProject, _, badInitial := startNovelRunWithContent(
		t, store, "缺配置作品", "第一章\n旧城夜雨，顾川决定追查失踪案。",
	)
	badRunning := approveAndResumeLifecycleRun(t, store, badProject, badInitial)
	if _, err := store.db.ExecContext(
		ctx,
		"DELETE FROM run_config_snapshots WHERE run_id = ?",
		badRunning.Run.RunID,
	); err != nil {
		t.Fatalf("delete bad run config snapshots: %v", err)
	}

	goodProject, _, goodInitial := startNovelRunWithContent(
		t, store, "健康作品", "第一章\n旧城夜雨，林夏决定追查失踪案。",
	)
	goodRunning := approveAndResumeLifecycleRun(t, store, goodProject, goodInitial)

	claim, err := store.ClaimExecutionTask(ctx, ClaimExecutionTaskCommand{
		WorkerID:     "worker_test",
		ExecutorIDs:  []string{"worker.structured_content"},
		ProviderID:   "provider_test",
		LeaseSeconds: 60,
	})
	if err != nil {
		t.Fatalf("ClaimExecutionTask() error = %v", err)
	}
	if claim == nil {
		t.Fatal("ClaimExecutionTask() returned no healthy task")
	}
	if claim.Task.RunID != goodRunning.Run.RunID {
		t.Fatalf("claimed run = %s, want healthy run %s", claim.Task.RunID, goodRunning.Run.RunID)
	}
}

func validSourceAnalysisProviderResponse(
	t *testing.T,
	claim *TaskClaim,
) json.RawMessage {
	t.Helper()
	var cursor struct {
		Batch struct {
			SourceUnits []episodeSplitSourceUnit `json:"source_units"`
		} `json:"batch"`
	}
	if err := json.Unmarshal(claim.ContextPack.TaskCursor, &cursor); err != nil ||
		len(cursor.Batch.SourceUnits) == 0 {
		t.Fatalf("source analysis cursor = %s, error = %v", claim.ContextPack.TaskCursor, err)
	}
	units := make([]map[string]any, 0, len(cursor.Batch.SourceUnits))
	covered := make([]string, 0, len(cursor.Batch.SourceUnits))
	for _, unit := range cursor.Batch.SourceUnits {
		units = append(units, map[string]any{
			"source_unit_id":             unit.SourceUnitID,
			"summary":                    unit.Text,
			"key_events":                 []string{"来源单元事件"},
			"character_changes":          []string{},
			"conflict_stage":             "来源发展",
			"hook_or_suspense_potential": "medium",
			"source_refs": []map[string]any{{
				"source_type":       "asset_text_range",
				"asset_id":          unit.AssetID,
				"asset_snapshot_id": unit.AssetSnapshotID,
				"source_unit_id":    unit.SourceUnitID,
				"range_label":       unit.SourceUnitID,
			}},
			"claims": []any{},
		})
		covered = append(covered, unit.SourceUnitID)
	}
	return mustJSONNoTest(map[string]any{
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
	})
}

func startNovelRunForLifecycle(t *testing.T, store *Store, title string) (Project, AssetResult, RunSnapshot) {
	return startNovelRunWithContent(t, store, title, "第一章 少年下山。")
}

func TestStartRunReleasesFailedRunWriteSlot(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, _, failed := startNovelRunForLifecycle(t, store, "replace failed run")
	now := formatTime(time.Now().UTC())
	if _, err := store.db.Exec(`UPDATE runs SET status = 'failed', ended_at = ?, updated_at = ? WHERE run_id = ?`, now, now, failed.Run.RunID); err != nil {
		t.Fatalf("mark run failed: %v", err)
	}
	message, err := store.CreateUserMessage(ctx, project.PrimaryConversationID, "重新开始")
	if err != nil {
		t.Fatalf("CreateUserMessage() error = %v", err)
	}
	source := createTextAsset(t, store, project.ProjectID, "replacement.txt", "第二版小说材料。")
	replacement, err := store.StartRun(ctx, bindStartRunProposal(
		t, store, novelStartCommand(project, message.MessageID, source),
	))
	if err != nil {
		t.Fatalf("StartRun(replacement) error = %v", err)
	}
	if replacement.Run.RunID == failed.Run.RunID {
		t.Fatalf("replacement reused failed run: %q", replacement.Run.RunID)
	}
	var oldStatus string
	var oldWriteIntent int
	if err := store.db.QueryRow(`SELECT status, write_intent FROM runs WHERE run_id = ?`, failed.Run.RunID).Scan(&oldStatus, &oldWriteIntent); err != nil {
		t.Fatalf("read failed run: %v", err)
	}
	if oldStatus != "failed" || oldWriteIntent != 0 {
		t.Fatalf("failed run status=%q write_intent=%d", oldStatus, oldWriteIntent)
	}
	updated, err := store.GetProject(ctx, project.ProjectID)
	if err != nil || updated.ActiveWriteRunID == nil || *updated.ActiveWriteRunID != replacement.Run.RunID {
		t.Fatalf("project active run = %+v, error=%v", updated.ActiveWriteRunID, err)
	}
}

func startNovelRunWithContent(
	t *testing.T,
	store *Store,
	title string,
	content string,
) (Project, AssetResult, RunSnapshot) {
	t.Helper()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, title)
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	message, err := store.CreateUserMessage(ctx, project.PrimaryConversationID, "开始生成剧本")
	if err != nil {
		t.Fatalf("CreateUserMessage() error = %v", err)
	}
	sourceAsset := createTextAsset(t, store, project.ProjectID, "source.txt", content)
	snapshot, err := store.StartRun(ctx, bindStartRunProposal(
		t,
		store,
		novelStartCommand(project, message.MessageID, sourceAsset),
	))
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	return project, sourceAsset, snapshot
}

func novelStartCommand(project Project, messageID string, sourceAsset AssetResult) StartRunCommand {
	input, _ := json.Marshal(map[string]any{
		"project_id":  project.ProjectID,
		"source_type": "novel",
		"assets": []map[string]any{{
			"asset_id":          sourceAsset.Asset.AssetID,
			"asset_snapshot_id": sourceAsset.Snapshot.AssetSnapshotID,
			"role":              "primary_source",
			"order":             1,
		}},
		"user_request_message_id": messageID,
		"user_notes":              []string{},
	})
	config, _ := json.Marshal(map[string]any{
		"config_ref": "creation",
		"payload": map[string]any{
			"target_episode_count":     12,
			"episode_duration_minutes": 2,
		},
	})
	return StartRunCommand{
		ProjectID:         project.ProjectID,
		ConversationID:    project.PrimaryConversationID,
		CapabilityID:      "novel_to_script",
		CapabilityVersion: "1.4.0",
		RunKind:           "generation",
		Input:             input,
		Config:            config,
		Confirmed:         true,
	}
}

func bindStartRunProposal(t *testing.T, store *Store, command StartRunCommand) StartRunCommand {
	t.Helper()
	reference := &agentcontract.CapabilityRef{
		CapabilityID: command.CapabilityID,
		Version:      command.CapabilityVersion,
	}
	exchange, err := store.CreateMessageExchange(
		context.Background(),
		CreateMessageExchangeCommand{
			ConversationID: command.ConversationID,
			Request: agentcontract.MessageRequest{
				Content:       "确认启动生成",
				CapabilityRef: reference,
			},
			Decision: agentcontract.AgentDecision{
				Reply:         "配置已就绪，请确认开始。",
				Intent:        "propose_capability",
				Confidence:    1,
				CapabilityRef: reference,
				ProposedAction: &agentcontract.ProposedActionDraft{
					ActionType:           "start_run",
					CapabilityRef:        reference,
					Input:                command.Input,
					Config:               command.Config,
					RequiresConfirmation: true,
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("CreateMessageExchange(start proposal) error = %v", err)
	}
	if exchange.Action == nil {
		t.Fatal("CreateMessageExchange(start proposal) returned no action")
	}
	command.Input = exchange.Action.Input
	command.Config = exchange.Action.Config
	command.ProposedActionID = exchange.Action.ProposedActionID
	command.ProposedActionVersion = exchange.Action.Version
	command.ConfirmationMessageID = exchange.Action.ConfirmationMessageID
	command.ConfirmationSnapshotHash = exchange.Action.SnapshotHash
	return command
}

func createTextAsset(t *testing.T, store *Store, projectID, filename, content string) AssetResult {
	t.Helper()
	session, err := store.CreateUploadSession(context.Background(), projectID, []UploadItemSpec{{
		ClientItemKey:     "item_1",
		Kind:              "text",
		OriginalFilename:  filename,
		DeclaredMIMEType:  "text/plain",
		DeclaredSizeBytes: int64(len([]byte(content))),
	}})
	if err != nil {
		t.Fatalf("CreateUploadSession() error = %v", err)
	}
	if len(session.Items) != 1 {
		t.Fatalf("upload items = %d, want 1", len(session.Items))
	}
	item, err := store.WriteUploadContent(context.Background(), session.Items[0].UploadItemID, bytes.NewBufferString(content))
	if err != nil {
		t.Fatalf("WriteUploadContent() error = %v", err)
	}
	if item.Status != "validating" {
		t.Fatalf("upload item status = %s, want validating", item.Status)
	}
	result, err := store.CompleteUploadItem(context.Background(), item.UploadItemID)
	if err != nil {
		t.Fatalf("CompleteUploadItem() error = %v", err)
	}
	if result.Asset.Status != "available" || result.Snapshot.Status != "available" {
		t.Fatalf("asset result = %+v", result)
	}
	return result
}

func loadTestRegistry(t *testing.T) *capability.Registry {
	t.Helper()
	_, file, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	if override := os.Getenv("CONTENT_AGENT_TEST_PROJECT_ROOT"); override != "" {
		root = override
	}
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: root})
	if err != nil {
		t.Fatalf("LoadRegistry() error = %v", err)
	}
	return registry
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return data
}

func assertDomainCode(t *testing.T, err error, code string) {
	t.Helper()
	var domain *DomainError
	if !errors.As(err, &domain) {
		t.Fatalf("error = %v, want DomainError %s", err, code)
	}
	if domain.Code != code {
		t.Fatalf("error code = %s, want %s", domain.Code, code)
	}
}
