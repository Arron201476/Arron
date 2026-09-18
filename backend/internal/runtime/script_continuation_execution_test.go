package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestScriptContinuationRequiresOneOutlineBeforeWriting(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, err := store.CreateProject(ctx, "剧本续写两阶段")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	message, err := store.CreateUserMessage(ctx, project.PrimaryConversationID, "先给五个方向，再续写")
	if err != nil {
		t.Fatalf("CreateUserMessage() error = %v", err)
	}
	source := createTextAsset(t, store, project.ProjectID, "script.txt", "场1 客厅 夜\n林岚：你终于来了。")
	input := mustJSON(t, map[string]any{
		"project_id": project.ProjectID, "source_type": "script",
		"assets": []map[string]any{{
			"asset_id": source.Asset.AssetID, "asset_snapshot_id": source.Snapshot.AssetSnapshotID,
			"role": "primary_source", "order": 1,
		}},
		"user_request_message_id": message.MessageID, "user_notes": []string{},
	})
	config := json.RawMessage(`{"config_ref":"continuation","payload":{"target_length_chars":15000}}`)
	started, err := store.StartRun(ctx, bindStartRunProposal(t, store, StartRunCommand{
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		CapabilityID: "script_continuation", CapabilityVersion: "1.0.0", RunKind: "generation",
		Input: input, Config: config, Confirmed: true,
	}))
	if err != nil {
		t.Fatalf("StartRun(script continuation) error = %v", err)
	}
	running := approveSnapshotAndResume(t, store, started)
	if currentStepID(running) != "generate_continuation_options" {
		t.Fatalf("current step = %s", currentStepID(running))
	}
	claim := claimTaskForExecutor(t, store, "worker.structured_content")
	optionsCommit := commitNonNovelResponse(t, store, claim, continuationOptionsPayload())
	approval := optionsCommit.RunSnapshot.CurrentApproval
	if approval == nil || !containsString(approval.Options, "select_single_option") {
		t.Fatalf("continuation approval = %+v", approval)
	}

	invalid := mustJSON(t, map[string]any{
		"options_artifact_version_id": approval.SubjectRefID,
		"selected_option_id":          "option_9",
	})
	_, err = store.ResolveApproval(ctx, ResolveApprovalCommand{
		ApprovalRequestID: approval.ApprovalRequestID, Action: "select_single_option",
		ExpectedApprovalVersion: approval.Version, SubjectSnapshotHash: approval.SubjectSnapshotHash,
		ResolutionPayload: invalid,
	})
	assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")

	resolution := mustJSON(t, map[string]any{
		"options_artifact_version_id": approval.SubjectRefID,
		"selected_option_id":          "option_3",
	})
	selected, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
		ApprovalRequestID: approval.ApprovalRequestID, Action: "select_single_option",
		ExpectedApprovalVersion: approval.Version, SubjectSnapshotHash: approval.SubjectSnapshotHash,
		ResolutionPayload: resolution,
	})
	if err != nil || selected.Run.Status != "paused" || currentStepID(selected) != "generate_continuation_script" {
		t.Fatalf("ResolveApproval(continuation) = %+v, error = %v", selected, err)
	}
	running, err = store.ResumeRun(ctx, ResumeRunCommand{RunID: selected.Run.RunID})
	if err != nil {
		t.Fatalf("ResumeRun(continuation script) error = %v", err)
	}
	scriptClaim := claimTaskForExecutor(t, store, "worker.structured_content")
	if scriptClaim.ContextPack.Step.StepID != "generate_continuation_script" ||
		len(scriptClaim.ContextPack.DecisionSnapshots) != 1 ||
		scriptClaim.ContextPack.DecisionSnapshots[0].DecisionType != "single_option_selection" {
		t.Fatalf("continuation script context = %+v", scriptClaim.ContextPack)
	}
	var decision map[string]any
	if err := json.Unmarshal(scriptClaim.ContextPack.DecisionSnapshots[0].Payload, &decision); err != nil || decision["selected_option_id"] != "option_3" {
		t.Fatalf("continuation decision = %+v, error = %v", decision, err)
	}
	selectedOption, ok := decision["selected_option"].(map[string]any)
	if !ok || selectedOption["option_id"] != "option_3" || selectedOption["outline"] == "" {
		t.Fatalf("selected continuation option = %+v", selectedOption)
	}
	scriptCommit := commitNonNovelResponse(t, store, scriptClaim, mustJSON(t, map[string]any{
		"title": "第三方向续写", "selected_option_id": "option_3", "script_text": continuationScriptText(15000, "续"),
	}))
	if scriptCommit.RunSnapshot.CurrentApproval == nil {
		t.Fatal("continuation script did not reach final approval")
	}
	finalApproval := scriptCommit.RunSnapshot.CurrentApproval
	completed, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
		ApprovalRequestID: finalApproval.ApprovalRequestID, Action: "approve",
		ExpectedApprovalVersion: finalApproval.Version, SubjectSnapshotHash: finalApproval.SubjectSnapshotHash,
	})
	if err != nil || completed.Run.Status != "completed" {
		t.Fatalf("final continuation approval = %+v, error = %v", completed, err)
	}

	artifacts, err := store.ListArtifactsByRun(ctx, completed.Run.RunID)
	if err != nil {
		t.Fatalf("ListArtifactsByRun() error = %v", err)
	}
	var continuation Artifact
	for _, artifact := range artifacts {
		if artifact.ArtifactType == "continuation_script" {
			continuation = artifact
			break
		}
	}
	if continuation.ArtifactID == "" {
		t.Fatal("completed continuation artifact is missing")
	}
	baseVersion, err := store.GetArtifactVersion(ctx, continuation.CurrentVersionID)
	if err != nil {
		t.Fatalf("GetArtifactVersion() error = %v", err)
	}
	for _, format := range []string{"txt", "md", "docx", "json"} {
		_, data, err := store.DownloadArtifactVersion(ctx, baseVersion.ArtifactVersionID, format)
		if err != nil || len(data) == 0 {
			t.Fatalf("completed continuation download %s: %v", format, err)
		}
		if (format == "txt" || format == "md") && string(data) != continuationScriptText(15000, "续") {
			t.Fatalf("completed continuation %s lost source text", format)
		}
	}
	edited, err := store.CreateArtifactVersion(ctx, CreateVersionCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "create_artifact_version",
			IdempotencyKey: "7b178caf-3d2a-46a9-94d9-2dfe5c7f00ab", RequestHash: "continuation_edit_v2",
		},
		ArtifactID: continuation.ArtifactID, BaseVersionID: baseVersion.ArtifactVersionID,
		BaseVersion: baseVersion.Version, ChangeMode: "whole_artifact", ActorRef: "continuation_editor",
		NewPayload: mustJSON(t, map[string]any{
			"title": "第三方向续写修订版", "selected_option_id": "option_3", "script_text": continuationScriptText(15000, "改"),
		}),
	})
	if err != nil || edited.Approval.Status != "pending" {
		t.Fatalf("CreateArtifactVersion(completed continuation) = %+v, error = %v", edited, err)
	}
	approvedEdit, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "resolve_approval",
			IdempotencyKey: "124b1b43-e5db-4ab2-bc6a-f3c8974909d0", RequestHash: "approve_continuation_edit_v2",
		},
		ApprovalRequestID: edited.Approval.ApprovalRequestID, Action: "approve",
		ExpectedApprovalVersion: edited.Approval.Version, SubjectSnapshotHash: edited.Approval.SubjectSnapshotHash,
	})
	if err != nil || approvedEdit.Run.Status != "completed" {
		t.Fatalf("ResolveApproval(completed continuation edit) = %+v, error = %v", approvedEdit, err)
	}
	confirmedEdit, err := store.GetArtifactVersion(ctx, edited.ArtifactVersion.ArtifactVersionID)
	if err != nil || confirmedEdit.Status != "confirmed" {
		t.Fatalf("edited continuation version = %+v, error = %v", confirmedEdit, err)
	}
	readyProject, err := store.GetProject(ctx, project.ProjectID)
	if err != nil || readyProject.Status != "ready" || readyProject.ActiveWriteRunID != nil {
		t.Fatalf("project after completed continuation edit approval = %+v, error = %v", readyProject, err)
	}
	for versionID, character := range map[string]string{baseVersion.ArtifactVersionID: "续", confirmedEdit.ArtifactVersionID: "改"} {
		_, data, err := store.DownloadArtifactVersion(ctx, versionID, "txt")
		if err != nil || string(data) != continuationScriptText(15000, character) {
			t.Fatalf("version-specific continuation download %s: %v", versionID, err)
		}
	}
}

func continuationScriptText(target int, character string) string {
	minimum := (target*9 + 9) / 10
	return "场2 客厅 夜\n林岚：这次轮到我选择。\n" + strings.Repeat(character, minimum)
}

func continuationOptionsPayload() json.RawMessage {
	options := make([]map[string]any, 0, 5)
	for index := 1; index <= 5; index++ {
		options = append(options, map[string]any{
			"option_id": "option_" + string(rune('0'+index)), "title": "方向", "genre_tag": "反转",
			"summary": "核心走向", "outline": "起承转合完整，包含两场冲突并走向明确结局。",
		})
	}
	return mustJSONNoTest(map[string]any{
		"core_settings": map[string]any{
			"worldview": "现代都市", "story_synopsis": "两人在客厅对峙。", "continuation_anchor": "林岚说你终于来了之后",
			"script_style_profile": map[string]any{"format_pattern": "场次加对白", "dialogue_style": "短促", "scene_rhythm": "快节奏"},
			"character_settings":   []map[string]any{{"name": "林岚", "personality_and_identity": "主角", "current_status": "正在对峙", "core_motive": "查明真相"}},
		},
		"options": options, "selection_instructions": "请选择且只能选择一个方向。",
	})
}
