package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestVideoReferenceExtractionAggregatesSealedEpisodes(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, err := store.CreateProject(ctx, "视频参考创作闭环")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	message, err := store.CreateUserMessage(ctx, project.PrimaryConversationID, "解析这两集视频并参考创作")
	if err != nil {
		t.Fatalf("CreateUserMessage() error = %v", err)
	}
	first := createVideoAsset(t, store, project.ProjectID, "第1集.mp4")
	second := createVideoAsset(t, store, project.ProjectID, "第2集.mp4")
	set, err := store.CreateAssetSet(ctx, CreateAssetSetCommand{
		ProjectID: project.ProjectID, Purpose: "video_reference_source", DisplayName: "参考剧视频",
	})
	if err != nil {
		t.Fatalf("CreateAssetSet() error = %v", err)
	}
	set, err = store.CreateAssetSetVersion(ctx, CreateAssetSetVersionCommand{
		AssetSetID: set.AssetSet.AssetSetID, ExpectedCurrentVersion: set.AssetSet.CurrentVersion,
		Changes: []AssetSetChange{
			{Operation: "add_asset", AssetID: second.Asset.AssetID},
			{Operation: "add_asset", AssetID: first.Asset.AssetID},
		},
	})
	if err != nil {
		t.Fatalf("CreateAssetSetVersion() error = %v", err)
	}
	// Natural episode order is authoritative even when upload order differs.
	orderOne, orderTwo := 1, 2
	set, err = store.CreateAssetSetVersion(ctx, CreateAssetSetVersionCommand{
		AssetSetID: set.AssetSet.AssetSetID, ExpectedCurrentVersion: set.AssetSet.CurrentVersion,
		Changes: []AssetSetChange{
			{Operation: "set_episode_order", AssetID: first.Asset.AssetID, EpisodeOrder: &orderOne},
			{Operation: "set_episode_order", AssetID: second.Asset.AssetID, EpisodeOrder: &orderTwo},
		},
	})
	if err != nil {
		t.Fatalf("reorder AssetSet error = %v", err)
	}
	sealed, err := store.SealAssetSet(ctx, SealAssetSetCommand{
		AssetSetID: set.AssetSet.AssetSetID, ExpectedCurrentVersion: set.AssetSet.CurrentVersion,
		UserConfirmedUploadComplete: true,
	})
	if err != nil {
		t.Fatalf("SealAssetSet() error = %v", err)
	}

	input, _ := json.Marshal(map[string]any{
		"project_id": project.ProjectID, "source_type": "video_reference",
		"asset_set_id": sealed.AssetSet.AssetSetID, "asset_set_version_id": sealed.Version.AssetSetVersionID,
		"collection_state": "sealed", "user_request_message_id": message.MessageID, "user_notes": []string{},
	})
	config := json.RawMessage(`{"config_ref":"extraction","payload":{"fidelity_level":"high","timecode_precision":"second","uncertain_content_policy":"mark"}}`)
	started, err := store.StartRun(ctx, bindStartRunProposal(t, store, StartRunCommand{
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		CapabilityID: "video_reference_creation", CapabilityVersion: "1.2.0", RunKind: "generation",
		Input: input, Config: config, Confirmed: true,
	}))
	if err != nil {
		t.Fatalf("StartRun(video) error = %v", err)
	}
	running := approveSnapshotAndResume(t, store, started)
	if currentStepID(running) != "extract_video_scripts" {
		t.Fatalf("current step = %s, want extract_video_scripts", currentStepID(running))
	}

	for expectedOrder := 1; expectedOrder <= 2; expectedOrder++ {
		claim := claimTaskForExecutor(t, store, "workflow.video_script_extract")
		var cursor struct {
			Video struct {
				AssetID         string `json:"asset_id"`
				AssetSnapshotID string `json:"asset_snapshot_id"`
				Filename        string `json:"file_name"`
				EpisodeOrder    int    `json:"episode_order"`
				EpisodeNo       int    `json:"episode_no"`
			} `json:"video"`
		}
		if err := json.Unmarshal(claim.ContextPack.TaskCursor, &cursor); err != nil ||
			cursor.Video.EpisodeOrder != expectedOrder || cursor.Video.EpisodeNo != expectedOrder ||
			len(claim.ContextPack.AssetContext) != 1 ||
			claim.ContextPack.AssetContext[0].AssetID != cursor.Video.AssetID {
			t.Fatalf("video cursor/context = %s / %+v, error = %v", claim.ContextPack.TaskCursor, claim.ContextPack.AssetContext, err)
		}
		payload := videoScriptProviderPayload(cursor.Video.EpisodeNo, cursor.Video.EpisodeOrder,
			cursor.Video.Filename, cursor.Video.AssetID, cursor.Video.AssetSnapshotID)
		if expectedOrder == 1 {
			pausing, err := store.RequestRunPause(ctx, PauseRunCommand{RunID: running.Run.RunID})
			if err != nil || pausing.Run.Status != "pausing" {
				t.Fatalf("pause active video task = %+v, error = %v", pausing.Run, err)
			}
		}
		result := commitNonNovelResponse(t, store, claim, payload)
		if !strings.Contains(string(result.ArtifactVersion.Payload), "△主角进入房间。") ||
			strings.TrimSpace(string(result.ArtifactVersion.Payload)) == fmt.Sprintf(`{"script_text":"第%d集"}`, expectedOrder) {
			t.Fatalf("video script text was not rebuilt from structured scenes: %s", result.ArtifactVersion.Payload)
		}
		if expectedOrder == 1 && result.CommitStatus != "task_artifact_saved" {
			t.Fatalf("first video commit = %+v", result)
		}
		if expectedOrder == 1 {
			if result.RunSnapshot.Run.Status != "paused" {
				t.Fatalf("video run after active item commit = %+v", result.RunSnapshot.Run)
			}
			running, err = store.ResumeRun(ctx, ResumeRunCommand{RunID: running.Run.RunID})
			if err != nil || running.Run.Status != "running" {
				t.Fatalf("resume video run = %+v, error = %v", running.Run, err)
			}
		}
		if expectedOrder == 2 {
			running = result.RunSnapshot
		}
	}
	if running.CurrentApproval == nil || running.Run.Status != "waiting_approval" {
		t.Fatalf("video batch checkpoint = %+v", running)
	}

	running = approveSnapshotAndResume(t, store, running)
	if currentStepID(running) != "analyze_reference_scripts" {
		t.Fatalf("current step after aggregate = %s", currentStepID(running))
	}
	analysisClaim := claimTaskForExecutor(t, store, "worker.structured_content")
	if analysisClaim.ContextPack.Step.StepID != "analyze_reference_scripts" ||
		len(analysisClaim.ContextPack.UpstreamContext) != 3 {
		t.Fatalf("analysis context = %+v", analysisClaim.ContextPack)
	}
	var aggregateContent json.RawMessage
	videoUnits := 0
	for _, upstream := range analysisClaim.ContextPack.UpstreamContext {
		switch upstream.ArtifactType {
		case "reference_scripts":
			aggregateContent = upstream.Content
		case "video_script_unit":
			videoUnits++
		}
	}
	if len(aggregateContent) == 0 || videoUnits != 2 {
		t.Fatalf("analysis context does not include index and video units: %+v", analysisClaim.ContextPack.UpstreamContext)
	}
	var aggregate struct {
		EpisodeCount                   int    `json:"episode_count"`
		EpisodeOrder                   []int  `json:"episode_order"`
		Completeness                   string `json:"completeness"`
		ContinueWithIncompleteMaterial bool   `json:"continue_with_incomplete_material"`
	}
	if err := json.Unmarshal(aggregateContent, &aggregate); err != nil ||
		aggregate.EpisodeCount != 2 || len(aggregate.EpisodeOrder) != 2 ||
		aggregate.EpisodeOrder[0] != 1 || aggregate.EpisodeOrder[1] != 2 ||
		aggregate.Completeness != "complete" || aggregate.ContinueWithIncompleteMaterial {
		t.Fatalf("reference scripts = %+v, error = %v", aggregate, err)
	}
	analysisCommit := commitNonNovelResponse(t, store, analysisClaim, videoScriptAnalysisProviderPayload())
	if analysisCommit.RunSnapshot.CurrentApproval == nil {
		t.Fatal("script analysis did not reach approval")
	}
	running = approveSnapshotAndResume(t, store, analysisCommit.RunSnapshot)
	optionsClaim := claimTaskForExecutor(t, store, "worker.structured_content")
	if optionsClaim.ContextPack.Step.StepID != "propose_adaptation_options" {
		t.Fatalf("step after analysis = %s", optionsClaim.ContextPack.Step.StepID)
	}
	optionsCommit := commitNonNovelResponse(t, store, optionsClaim, videoAdaptationOptionsProviderPayload())
	approval := optionsCommit.RunSnapshot.CurrentApproval
	if approval == nil || !containsString(approval.Options, "select_adaptation_strategy") {
		t.Fatalf("adaptation approval = %+v", approval)
	}
	resolution, _ := json.Marshal(map[string]any{
		"adaptation_options_artifact_version_id": approval.SubjectRefID,
		"selection": map[string]any{
			"selected_option_ids": []string{"option_1"},
			"combined_methods":    []string{},
			"custom_changes":      []string{"加入新的职业设定"},
		},
		"creation_config": map[string]any{
			"target_episode_count":     12,
			"episode_duration_minutes": 2,
			"user_requirements":        []string{"节奏紧凑"},
		},
	})
	selected, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "resolve_approval",
			IdempotencyKey: "select_adaptation", RequestHash: "select_adaptation_hash",
		},
		ApprovalRequestID: approval.ApprovalRequestID,
		Action:            "select_adaptation_strategy", ExpectedApprovalVersion: approval.Version,
		SubjectSnapshotHash: approval.SubjectSnapshotHash, ResolutionPayload: resolution,
	})
	if err != nil || currentStepID(selected) != "build_adaptation_brief" || selected.Run.Status != "paused" {
		t.Fatalf("ResolveApproval(adaptation) = %+v, error = %v", selected, err)
	}
	running, err = store.ResumeRun(ctx, ResumeRunCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "resume_run",
			IdempotencyKey: "resume_adaptation_brief", RequestHash: "resume_adaptation_brief_hash",
		},
		RunID: selected.Run.RunID,
	})
	if err != nil || running.Run.Status != "running" {
		t.Fatalf("ResumeRun(adaptation brief) = %+v, error = %v", running, err)
	}
	briefClaim := claimTaskForExecutor(t, store, "worker.structured_content")
	if briefClaim.ContextPack.Step.StepID != "build_adaptation_brief" ||
		briefClaim.ContextPack.ConfigSnapshotRef == nil ||
		briefClaim.ContextPack.ConfigSnapshotRef.ConfigRef != "creation" ||
		len(briefClaim.ContextPack.DecisionSnapshots) != 1 ||
		briefClaim.ContextPack.DecisionSnapshots[0].SourceRefID != approval.SubjectRefID {
		t.Fatalf("adaptation brief context = %+v", briefClaim.ContextPack)
	}
}

func videoScriptAnalysisProviderPayload() json.RawMessage {
	evidenceRefs := videoSourceRefs("asset_test", "snapshot_test")
	section := map[string]any{"summary": "测试分析", "items": []any{map[string]any{"claim": "测试判断", "evidence_refs": evidenceRefs}}}
	return mustJSONNoTest(map[string]any{
		"material_completeness": map[string]any{"status": "complete", "episode_count": 2, "missing_episode_nos": []int{}, "limitations": []string{}},
		"basic_information":     map[string]any{"material_type": "漫剧", "format": "短剧", "genre": []string{"逆袭"}, "audience": "短剧观众", "worldview_tags": []string{}, "special_mechanism_tags": []string{}, "logline": "主角反击", "plot_summary": "主角经历冲突并反击", "worldview": "现代"},
		"overall_judgment":      section, "opening_strategy": section, "hook_system": section,
		"payoff_and_emotion": section, "plot_and_emotion_curve": section,
		"characters_and_relationships": section, "differentiation": section,
		"transferable_methods": section, "marketing_material_directions": section,
		"episode_index":        []map[string]any{{"episode_no": 1, "one_sentence_story": "主角遇到冲突", "key_conflict": "身份冲突", "ending_hook": "真相将揭晓", "conversion_point": "主角反击", "evidence_refs": videoSourceRefs("asset_test", "snapshot_test")}},
		"risks":                []any{map[string]any{"risk": "节奏风险", "impact": "影响留存", "direction": "压缩重复信息", "evidence_refs": evidenceRefs}},
		"optimization_actions": []any{map[string]any{"priority": "P1", "problem": "节奏重复", "change": "压缩重复信息", "expected_effect": "提升留存", "is_suggestion": true}},
		"final_conclusion":     map[string]any{"keep": []string{"开场冲突"}, "strengthen": []string{"人物差异"}, "recommendation": "保留结构并替换具体内容"},
	})
}

func videoAdaptationOptionsProviderPayload() json.RawMessage {
	option := func(id, title string) map[string]any {
		return map[string]any{
			"option_id": id, "title": title, "one_sentence_strategy": "保留节奏结构并替换人物和事件",
			"patterns_to_keep": []string{"开场冲突"}, "content_to_replace": []string{"人物身份"},
			"new_element_suggestions": []string{"新职业"}, "character_and_relationship_changes": []string{"改为同事关系"},
			"worldview_changes": []string{"改为现代职场"}, "event_chain_changes": []string{"替换核心事件"},
			"pacing_hook_transfer": []string{"保留尾钩密度"}, "difference_requirements": []string{"不得复用原台词"},
			"risks": []string{"需避免情节近似"}, "suitable_when": "需要快速迁移节奏时",
			"evidence_refs": videoSourceRefs("asset_test", "snapshot_test"),
		}
	}
	return mustJSONNoTest(map[string]any{
		"options":                []map[string]any{option("option_1", "结构迁移"), option("option_2", "关系重构")},
		"selection_instructions": "可选择一个方案，也可组合两个方案。",
	})
}

func TestVideoReferenceStepRegenerationReplacesExistingEpisodeAndPlansWholeBatch(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, err := store.CreateProject(ctx, "视频整批重解析")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	message, err := store.CreateUserMessage(ctx, project.PrimaryConversationID, "解析两集视频")
	if err != nil {
		t.Fatalf("CreateUserMessage() error = %v", err)
	}
	first := createVideoAsset(t, store, project.ProjectID, "第1集.mp4")
	second := createVideoAsset(t, store, project.ProjectID, "第2集.mp4")
	set, err := store.CreateAssetSet(ctx, CreateAssetSetCommand{
		ProjectID: project.ProjectID, Purpose: "video_reference_source", DisplayName: "参考剧视频",
	})
	if err != nil {
		t.Fatalf("CreateAssetSet() error = %v", err)
	}
	set, err = store.CreateAssetSetVersion(ctx, CreateAssetSetVersionCommand{
		AssetSetID: set.AssetSet.AssetSetID, ExpectedCurrentVersion: set.AssetSet.CurrentVersion,
		Changes: []AssetSetChange{{Operation: "add_asset", AssetID: first.Asset.AssetID}, {Operation: "add_asset", AssetID: second.Asset.AssetID}},
	})
	if err != nil {
		t.Fatalf("CreateAssetSetVersion() error = %v", err)
	}
	one, two := 1, 2
	set, err = store.CreateAssetSetVersion(ctx, CreateAssetSetVersionCommand{
		AssetSetID: set.AssetSet.AssetSetID, ExpectedCurrentVersion: set.AssetSet.CurrentVersion,
		Changes: []AssetSetChange{
			{Operation: "set_episode_order", AssetID: first.Asset.AssetID, EpisodeOrder: &one},
			{Operation: "set_episode_order", AssetID: second.Asset.AssetID, EpisodeOrder: &two},
		},
	})
	if err != nil {
		t.Fatalf("set episode order error = %v", err)
	}
	sealed, err := store.SealAssetSet(ctx, SealAssetSetCommand{
		AssetSetID: set.AssetSet.AssetSetID, ExpectedCurrentVersion: set.AssetSet.CurrentVersion,
		UserConfirmedUploadComplete: true,
	})
	if err != nil {
		t.Fatalf("SealAssetSet() error = %v", err)
	}
	input, _ := json.Marshal(map[string]any{
		"project_id": project.ProjectID, "source_type": "video_reference",
		"asset_set_id": sealed.AssetSet.AssetSetID, "asset_set_version_id": sealed.Version.AssetSetVersionID,
		"collection_state": "sealed", "user_request_message_id": message.MessageID, "user_notes": []string{},
	})
	started, err := store.StartRun(ctx, bindStartRunProposal(t, store, StartRunCommand{
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		CapabilityID: "video_reference_creation", CapabilityVersion: "1.2.0", RunKind: "generation",
		Input:     input,
		Config:    json.RawMessage(`{"config_ref":"extraction","payload":{"fidelity_level":"high","timecode_precision":"second","uncertain_content_policy":"mark"}}`),
		Confirmed: true,
	}))
	if err != nil {
		t.Fatalf("StartRun(video) error = %v", err)
	}
	running := approveSnapshotAndResume(t, store, started)
	firstClaim := claimTaskForExecutor(t, store, "workflow.video_script_extract")
	firstCommit := commitNonNovelResponse(t, store, firstClaim, videoScriptProviderPayload(1, 1, "第1集.mp4", first.Asset.AssetID, first.Asset.CurrentSnapshotID))
	if firstCommit.CommitStatus != "task_artifact_saved" {
		t.Fatalf("first commit = %+v", firstCommit)
	}
	paused, err := store.RequestRunPause(ctx, PauseRunCommand{
		CommandMeta: CommandMeta{Scope: project.ProjectID, CommandType: "pause_run", IdempotencyKey: "11111111-1111-4111-8111-111111111111", RequestHash: "pause_after_first"},
		RunID:       running.Run.RunID,
	})
	if err != nil || paused.Run.Status != "paused" {
		t.Fatalf("pause after first = %+v, error = %v", paused.Run, err)
	}
	regenerated, err := store.RequestTargetedRegeneration(ctx, RequestTargetedRegenerationCommand{
		CommandMeta: CommandMeta{Scope: project.ProjectID, CommandType: "request_targeted_regeneration", IdempotencyKey: "22222222-2222-4222-8222-222222222222", RequestHash: "regenerate_all_video"},
		ProjectID:   project.ProjectID, ArtifactID: firstCommit.Artifact.ArtifactID,
		RegenerationScope: "step", Instruction: "都重新解析",
	})
	if err != nil {
		t.Fatalf("RequestTargetedRegeneration(all) error = %v", err)
	}
	if got := regenerated.RegenerationPlan.Groups[0].TaskKeys; len(got) != 2 || got[0] != "episode:1" || got[1] != "episode:2" {
		t.Fatalf("whole-step task keys = %#v", got)
	}
	running, err = store.ResumeRun(ctx, ResumeRunCommand{
		CommandMeta: CommandMeta{Scope: project.ProjectID, CommandType: "resume_run", IdempotencyKey: "33333333-3333-4333-8333-333333333333", RequestHash: "resume_regenerate_all_video"},
		RunID:       running.Run.RunID,
	})
	if err != nil {
		t.Fatalf("ResumeRun(regeneration) error = %v", err)
	}
	var batchApproval *Approval
	for episodeNo := 1; episodeNo <= 2; episodeNo++ {
		claim := claimTaskForExecutor(t, store, "workflow.video_script_extract")
		var cursor struct {
			Video struct {
				AssetID         string `json:"asset_id"`
				AssetSnapshotID string `json:"asset_snapshot_id"`
				Filename        string `json:"file_name"`
				EpisodeOrder    int    `json:"episode_order"`
				EpisodeNo       int    `json:"episode_no"`
			} `json:"video"`
		}
		if err := json.Unmarshal(claim.ContextPack.TaskCursor, &cursor); err != nil || cursor.Video.EpisodeOrder != episodeNo {
			t.Fatalf("regeneration cursor = %s, error = %v", claim.ContextPack.TaskCursor, err)
		}
		commit := commitNonNovelResponse(t, store, claim, videoScriptProviderPayload(
			cursor.Video.EpisodeNo, cursor.Video.EpisodeOrder, cursor.Video.Filename,
			cursor.Video.AssetID, cursor.Video.AssetSnapshotID,
		))
		if episodeNo == 2 && (commit.RunSnapshot.Run.Status != "waiting_approval" || commit.RunSnapshot.CurrentApproval == nil) {
			t.Fatalf("regenerated batch did not reach approval: %+v", commit.RunSnapshot)
		}
		if episodeNo == 2 {
			batchApproval = commit.RunSnapshot.CurrentApproval
		}
	}
	var firstVersion, artifactCount int
	if err := store.db.QueryRowContext(ctx, `
		SELECT av.version FROM artifacts a JOIN artifact_versions av ON av.artifact_version_id = a.current_version_id
		WHERE a.run_id = ? AND a.artifact_type = 'video_script_unit' AND a.scope_key = 'episode:1'`, running.Run.RunID).Scan(&firstVersion); err != nil {
		t.Fatalf("load regenerated episode 1 error = %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM artifacts WHERE run_id = ? AND artifact_type = 'video_script_unit'`, running.Run.RunID).Scan(&artifactCount); err != nil {
		t.Fatalf("count regenerated episodes error = %v", err)
	}
	if firstVersion <= 1 || artifactCount != 2 {
		t.Fatalf("regenerated artifacts: episode1 version=%d count=%d", firstVersion, artifactCount)
	}
	if batchApproval == nil {
		t.Fatal("regenerated batch approval is missing")
	}
	if _, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
		CommandMeta:       CommandMeta{Scope: project.ProjectID, CommandType: "resolve_approval", IdempotencyKey: "44444444-4444-4444-8444-444444444444", RequestHash: "approve_regenerated_video_batch"},
		ApprovalRequestID: batchApproval.ApprovalRequestID, ExpectedApprovalVersion: batchApproval.Version,
		SubjectSnapshotHash: batchApproval.SubjectSnapshotHash, Action: "approve",
	}); err != nil {
		t.Fatalf("approve regenerated batch error = %v", err)
	}
	var planStatus string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM regeneration_plans WHERE regeneration_plan_id = ?`, regenerated.RegenerationPlan.RegenerationPlanID).Scan(&planStatus); err != nil {
		t.Fatalf("load regeneration plan status error = %v", err)
	}
	if planStatus != "completed" {
		t.Fatalf("regeneration plan status = %q, want completed", planStatus)
	}
}

func TestVideoReferenceSequentialBatchFailureStopsLaterItemsAndRetriesFromFailure(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	project, err := store.CreateProject(ctx, "视频并发失败隔离")
	if err != nil {
		t.Fatal(err)
	}
	message, err := store.CreateUserMessage(ctx, project.PrimaryConversationID, "解析三集视频")
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.CreateAssetSet(ctx, CreateAssetSetCommand{
		ProjectID: project.ProjectID, Purpose: "video_reference_source", DisplayName: "参考剧视频",
	})
	if err != nil {
		t.Fatal(err)
	}
	changes := make([]AssetSetChange, 0, 3)
	for episodeNo := 1; episodeNo <= 3; episodeNo++ {
		asset := createVideoAsset(t, store, project.ProjectID, fmt.Sprintf("第%d集.mp4", episodeNo))
		changes = append(changes, AssetSetChange{Operation: "add_asset", AssetID: asset.Asset.AssetID})
	}
	set, err = store.CreateAssetSetVersion(ctx, CreateAssetSetVersionCommand{
		AssetSetID: set.AssetSet.AssetSetID, ExpectedCurrentVersion: set.AssetSet.CurrentVersion, Changes: changes,
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := store.SealAssetSet(ctx, SealAssetSetCommand{
		AssetSetID: set.AssetSet.AssetSetID, ExpectedCurrentVersion: set.AssetSet.CurrentVersion,
		UserConfirmedUploadComplete: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{
		"project_id": project.ProjectID, "source_type": "video_reference",
		"asset_set_id": sealed.AssetSet.AssetSetID, "asset_set_version_id": sealed.Version.AssetSetVersionID,
		"collection_state": "sealed", "user_request_message_id": message.MessageID, "user_notes": []string{},
	})
	started, err := store.StartRun(ctx, bindStartRunProposal(t, store, StartRunCommand{
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		CapabilityID: "video_reference_creation", CapabilityVersion: "1.2.0", RunKind: "generation",
		Input:     input,
		Config:    json.RawMessage(`{"config_ref":"extraction","payload":{"fidelity_level":"high","timecode_precision":"second","uncertain_content_policy":"mark"}}`),
		Confirmed: true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	running := approveSnapshotAndResume(t, store, started)
	stepRunID := *running.Run.CurrentStepRunID

	first := claimTaskForExecutor(t, store, "workflow.video_script_extract")
	commitVideoClaim(t, store, first)
	second := claimTaskForExecutor(t, store, "workflow.video_script_extract")
	failed, err := store.FailExecutionAttempt(ctx, FailExecutionAttemptCommand{
		AttemptID: second.Attempt.AttemptID, AttemptToken: second.AttemptToken,
		InputSnapshotHash: second.Attempt.InputSnapshotHash, ErrorCode: "PROVIDER_TEMPORARY_FAILURE",
	})
	if err != nil || !failed.Retryable {
		t.Fatalf("first automatic failure = %+v, error = %v", failed, err)
	}
	secondRetry := claimTaskForExecutor(t, store, "workflow.video_script_extract")
	failed, err = store.FailExecutionAttempt(ctx, FailExecutionAttemptCommand{
		AttemptID: secondRetry.Attempt.AttemptID, AttemptToken: secondRetry.AttemptToken,
		InputSnapshotHash: secondRetry.Attempt.InputSnapshotHash, ErrorCode: "PROVIDER_TEMPORARY_FAILURE",
	})
	if err != nil || failed.Retryable || failed.Task.Status != "failed" {
		t.Fatalf("exhausted batch item = %+v, error = %v", failed, err)
	}
	runAfterFailure, err := store.GetRun(ctx, running.Run.RunID)
	if err != nil || runAfterFailure.Status != "failed" {
		t.Fatalf("sequential batch must fail after exhausted item, run=%+v error=%v", runAfterFailure, err)
	}

	retried, err := store.RetryFailedStep(ctx, RetryFailedStepCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "retry_failed_step",
			IdempotencyKey: "video_failed_item_retry", RequestHash: "video_failed_item_retry_hash",
		},
		StepRunID: stepRunID,
	})
	if err != nil || retried.Run.Status != "running" {
		t.Fatalf("RetryFailedStep() = %+v, error = %v", retried.Run, err)
	}
	tasks, err := store.ListTaskItems(ctx, stepRunID)
	if err != nil || len(tasks) != 3 || tasks[0].Status != "succeeded" ||
		tasks[0].Failure != nil || tasks[0].FailureDetail != nil ||
		tasks[1].Status != "pending" || tasks[1].Failure != nil || tasks[1].FailureDetail != nil ||
		tasks[2].Status != "pending" || tasks[2].Failure != nil || tasks[2].FailureDetail != nil {
		t.Fatalf("retry must preserve successful items: %+v, error=%v", tasks, err)
	}
	secondAfterRetry := claimTaskForExecutor(t, store, "workflow.video_script_extract")
	commitVideoClaim(t, store, secondAfterRetry)
	thirdAfterRetry := claimTaskForExecutor(t, store, "workflow.video_script_extract")
	completed := commitVideoClaim(t, store, thirdAfterRetry)
	if completed.RunSnapshot.Run.Status != "waiting_approval" || completed.Approval.ApprovalRequestID == "" {
		t.Fatalf("retried batch did not reach approval: %+v", completed)
	}
}

func prepareRunningPartialVideoBatch(t *testing.T, store *Store) (Project, RunSnapshot) {
	t.Helper()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, "部分视频结果继续")
	if err != nil {
		t.Fatal(err)
	}
	message, err := store.CreateUserMessage(ctx, project.PrimaryConversationID, "解析两集视频")
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.CreateAssetSet(ctx, CreateAssetSetCommand{
		ProjectID: project.ProjectID, Purpose: "video_reference_source", DisplayName: "参考剧视频",
	})
	if err != nil {
		t.Fatal(err)
	}
	changes := make([]AssetSetChange, 0, 2)
	for episodeNo := 1; episodeNo <= 2; episodeNo++ {
		asset := createVideoAsset(t, store, project.ProjectID, fmt.Sprintf("第%d集.mp4", episodeNo))
		changes = append(changes, AssetSetChange{Operation: "add_asset", AssetID: asset.Asset.AssetID})
	}
	set, err = store.CreateAssetSetVersion(ctx, CreateAssetSetVersionCommand{
		AssetSetID: set.AssetSet.AssetSetID, ExpectedCurrentVersion: set.AssetSet.CurrentVersion, Changes: changes,
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := store.SealAssetSet(ctx, SealAssetSetCommand{
		AssetSetID: set.AssetSet.AssetSetID, ExpectedCurrentVersion: set.AssetSet.CurrentVersion,
		UserConfirmedUploadComplete: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{
		"project_id": project.ProjectID, "source_type": "video_reference",
		"asset_set_id": sealed.AssetSet.AssetSetID, "asset_set_version_id": sealed.Version.AssetSetVersionID,
		"collection_state": "sealed", "user_request_message_id": message.MessageID, "user_notes": []string{},
	})
	started, err := store.StartRun(ctx, bindStartRunProposal(t, store, StartRunCommand{
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		CapabilityID: "video_reference_creation", CapabilityVersion: "1.2.0", RunKind: "generation",
		Input:     input,
		Config:    json.RawMessage(`{"config_ref":"extraction","payload":{"fidelity_level":"high","timecode_precision":"second","uncertain_content_policy":"mark"}}`),
		Confirmed: true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	running := approveSnapshotAndResume(t, store, started)
	return project, running
}

func preparePartialVideoBatch(t *testing.T, store *Store) (Project, RunSnapshot, *TaskClaim) {
	t.Helper()
	ctx := context.Background()
	project, running := prepareRunningPartialVideoBatch(t, store)
	first := claimTaskForExecutor(t, store, "workflow.video_script_extract")
	commitVideoClaim(t, store, first)
	second := claimTaskForExecutor(t, store, "workflow.video_script_extract")
	failed, err := store.FailExecutionAttempt(ctx, FailExecutionAttemptCommand{
		AttemptID: second.Attempt.AttemptID, AttemptToken: second.AttemptToken,
		InputSnapshotHash: second.Attempt.InputSnapshotHash, ErrorCode: "PROVIDER_TEMPORARY_FAILURE",
	})
	if err != nil || !failed.Retryable {
		t.Fatalf("first automatic failure = %+v, error = %v", failed, err)
	}
	secondRetry := claimTaskForExecutor(t, store, "workflow.video_script_extract")
	failed, err = store.FailExecutionAttempt(ctx, FailExecutionAttemptCommand{
		AttemptID: secondRetry.Attempt.AttemptID, AttemptToken: secondRetry.AttemptToken,
		InputSnapshotHash: secondRetry.Attempt.InputSnapshotHash, ErrorCode: "PROVIDER_TEMPORARY_FAILURE",
	})
	if err != nil || failed.Task.Status != "failed" {
		t.Fatalf("exhausted batch item = %+v, error = %v", failed, err)
	}
	settled, err := store.GetRunSnapshot(ctx, running.Run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Run.Status != "failed" || !containsAvailableAction(settled.AvailableActions, "continue_with_partial_results") {
		t.Fatalf("partial batch actions = %+v, run=%+v", settled.AvailableActions, settled.Run)
	}
	return project, settled, first
}

func TestVideoReferencePartialBatchCanContinueThroughGenericRuntimePolicy(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, settled, first := preparePartialVideoBatch(t, store)
	stepRunID := *settled.Run.CurrentStepRunID
	for _, status := range []string{"pending", "running", "repair_pending", "waiting_approval", "paused"} {
		if _, err := store.db.Exec(`INSERT INTO task_items(task_item_id, step_run_id, run_id, item_key, item_order,
			status, attempt_count, input_snapshot_json, cursor_json, created_at, updated_at)
			SELECT 'unsettled-control-fixture', step_run_id, run_id, 'episode:3', 3, ?, 0,
			input_snapshot_json, cursor_json, created_at, updated_at FROM task_items WHERE task_item_id=?`, status, first.Task.TaskItemID); err != nil {
			t.Fatal(err)
		}
		before := publicControlState(t, store, project.ProjectID)
		_, err := store.ContinueWithPartialResults(ctx, ContinueWithPartialResultsCommand{StepRunID: stepRunID, Confirmed: true})
		assertDomainCode(t, err, "RUN_STATE_CONFLICT")
		if after := publicControlState(t, store, project.ProjectID); after != before {
			t.Fatalf("partial continuation skipped %s work: %s -> %s", status, before, after)
		}
		if _, err := store.db.Exec(`DELETE FROM task_items WHERE task_item_id='unsettled-control-fixture'`); err != nil {
			t.Fatal(err)
		}
	}

	waiting, err := store.ContinueWithPartialResults(ctx, ContinueWithPartialResultsCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "continue_with_partial_results",
			IdempotencyKey: "continue_partial_1", RequestHash: "continue_partial_hash_1",
		},
		StepRunID: stepRunID, Confirmed: true, ActorRef: "test_user",
	})
	if err != nil || waiting.Run.Status != "waiting_approval" || waiting.CurrentApproval == nil {
		t.Fatalf("ContinueWithPartialResults() = %+v, error=%v", waiting, err)
	}
	if waiting.CurrentApproval.Title != "确认按现有结果继续" ||
		!strings.Contains(waiting.CurrentApproval.Reason, "episode:2") {
		t.Fatalf("partial approval = %+v", waiting.CurrentApproval)
	}
	resolved := approveSnapshotAndResume(t, store, waiting)
	if resolved.Run.Status == "failed" {
		t.Fatalf("partial approval resolution = %+v", resolved.Run)
	}
	var referencePayload string
	if err := store.db.QueryRowContext(ctx, `
		SELECT av.payload_json
		FROM artifacts a JOIN artifact_versions av ON av.artifact_version_id = a.current_version_id
		WHERE a.run_id = ? AND a.artifact_type = 'reference_scripts'`, resolved.Run.RunID).Scan(&referencePayload); err != nil {
		t.Fatal(err)
	}
	var reference struct {
		Completeness       string `json:"completeness"`
		FailedEpisodeNos   []int  `json:"failed_episode_nos"`
		ContinueIncomplete bool   `json:"continue_with_incomplete_material"`
	}
	if err := json.Unmarshal([]byte(referencePayload), &reference); err != nil {
		t.Fatal(err)
	}
	if reference.Completeness != "incomplete" || !reference.ContinueIncomplete ||
		len(reference.FailedEpisodeNos) != 1 || reference.FailedEpisodeNos[0] != 2 {
		t.Fatalf("partial reference completeness = %+v", reference)
	}
}

func commitVideoClaim(t *testing.T, store *Store, claim *TaskClaim) ArtifactCommitResult {
	t.Helper()
	var cursor struct {
		Video struct {
			AssetID         string `json:"asset_id"`
			AssetSnapshotID string `json:"asset_snapshot_id"`
			Filename        string `json:"file_name"`
			EpisodeOrder    int    `json:"episode_order"`
			EpisodeNo       int    `json:"episode_no"`
		} `json:"video"`
	}
	if err := json.Unmarshal(claim.ContextPack.TaskCursor, &cursor); err != nil {
		t.Fatal(err)
	}
	return commitNonNovelResponse(t, store, claim, videoScriptProviderPayload(
		cursor.Video.EpisodeNo, cursor.Video.EpisodeOrder, cursor.Video.Filename,
		cursor.Video.AssetID, cursor.Video.AssetSnapshotID,
	))
}

func videoScriptProviderPayload(episodeNo, episodeOrder int, filename, assetID, snapshotID string) json.RawMessage {
	return mustJSONNoTest(map[string]any{
		"video_script_unit": map[string]any{
			"episode_no": episodeNo, "episode_order": episodeOrder, "source_file_name": filename,
			"plot_summary": "主角遭遇冲突并作出反击，结尾留下下一集悬念。", "title": "测试集",
			"script_text": fmt.Sprintf("第%d集", episodeNo),
			"scenes": []map[string]any{{
				"scene_id": "scene_1_1", "heading": "场 1-1 INT. 测试地 - 日", "location": "测试地",
				"interior_exterior": "内", "time_of_day": "日", "characters": []string{"主角"},
				"blocks": []map[string]any{{
					"line_id": "line_1_1_1", "block_type": "action", "text": "△主角进入房间。",
					"source_refs": videoSourceRefs(assetID, snapshotID), "uncertainty": "none",
				}},
				"source_refs": videoSourceRefs(assetID, snapshotID),
			}},
			"source_refs": videoSourceRefs(assetID, snapshotID), "uncertainty_flags": []any{},
			"extraction_completeness": map[string]any{
				"status": "complete_first_pass", "known_gaps": []string{}, "review_notes": []string{},
			},
			"source_availability": "active",
		},
	})
}

func videoSourceRefs(assetID, snapshotID string) []map[string]any {
	return []map[string]any{{
		"source_type": "video_time_range", "asset_id": assetID, "asset_snapshot_id": snapshotID,
		"time_range": map[string]any{"start_ms": 0, "end_ms": 1000},
	}}
}
