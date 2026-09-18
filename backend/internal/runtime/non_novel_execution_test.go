package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

func TestNonNovelFixturesBuildStableSourceManifests(t *testing.T) {
	fixtures := []string{
		"complete-outline.txt",
		"brief-synopsis.txt",
		"fragmented-notes.txt",
	}
	for _, filename := range fixtures {
		t.Run(filename, func(t *testing.T) {
			content := fixedNonNovelFixtureContent(t, filename)
			store, err := Open(
				filepath.Join(t.TempDir(), "content_agent.db"),
				loadTestRegistry(t),
			)
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			defer store.Close()

			first := buildNonNovelSourceManifestForTest(
				t,
				store,
				"非小说清单一",
				filename,
				content,
			)
			second := buildNonNovelSourceManifestForTest(
				t,
				store,
				"非小说清单二",
				filename,
				content,
			)
			if first.SourceKind != "non_novel" ||
				len(first.Units) == 0 ||
				len(first.Units) != len(second.Units) {
				t.Fatalf("source manifests = first %+v, second %+v", first, second)
			}
			for index := range first.Units {
				left := first.Units[index]
				right := second.Units[index]
				if left.SourceUnitID != right.SourceUnitID ||
					left.SourceUnitID == "" ||
					left.UnitKind != "document_section" ||
					left.Order != index+1 ||
					left.Order != right.Order ||
					left.ContentHash == "" ||
					left.ContentHash != right.ContentHash ||
					first.CoveredSourceUnitIDs[index] != left.SourceUnitID ||
					second.CoveredSourceUnitIDs[index] != right.SourceUnitID {
					t.Fatalf(
						"unstable non-novel source unit at index %d: first=%+v second=%+v",
						index,
						left,
						right,
					)
				}
			}
		})
	}
}

func TestValidateNonNovelMaterialBankClaims(t *testing.T) {
	manifest := sourceManifestPayload{
		SourceKind: "non_novel",
		Units: []sourceManifestUnit{{
			SourceUnitID:    "SRC-U001-B001",
			AssetID:         "ast_source",
			AssetSnapshotID: "ass_source",
		}},
	}
	manifestPayload := mustJSONNoTest(manifest)
	upstream := []ContextUpstreamArtifact{{
		ArtifactType: "source_manifest",
		Content:      manifestPayload,
	}}
	fact := map[string]any{
		"claim_id":  "FACT-001",
		"category":  "character",
		"statement": "女主是失业记者。",
		"status":    "FACT",
		"source_refs": []map[string]any{{
			"source_type":       "asset_text_range",
			"asset_id":          "ast_source",
			"asset_snapshot_id": "ass_source",
			"source_unit_id":    "SRC-U001-B001",
			"range_label":       "SRC-U001-B001",
		}},
		"confidence": "high",
		"locked":     true,
	}
	payloadForClaims := func(claims ...map[string]any) json.RawMessage {
		return mustJSONNoTest(map[string]any{
			"source_trace": map[string]any{"claims": claims},
		})
	}

	t.Run("accepts exact fact and unlocked proposal", func(t *testing.T) {
		proposal := map[string]any{
			"claim_id":    "PROP-001",
			"category":    "event",
			"statement":   "可以增加一次公开质证。",
			"status":      "PROPOSAL",
			"source_refs": []any{},
			"confidence":  "medium",
			"locked":      false,
		}
		if err := validateMaterialBankClaims(
			upstream,
			payloadForClaims(fact, proposal),
		); err != nil {
			t.Fatalf("validateMaterialBankClaims() error = %v", err)
		}
	})

	t.Run("rejects invented source unit", func(t *testing.T) {
		invalidFact := cloneJSONMap(fact)
		invalidFact["source_refs"] = []map[string]any{{
			"source_type":       "asset_text_range",
			"asset_id":          "ast_source",
			"asset_snapshot_id": "ass_source",
			"source_unit_id":    "SRC-U999-B001",
			"range_label":       "SRC-U999-B001",
		}}
		assertDomainCode(
			t,
			validateMaterialBankClaims(
				upstream,
				payloadForClaims(invalidFact),
			),
			"CONTENT_CLAIM_INVALID",
		)
	})

	t.Run("rejects unapproved confirmed change", func(t *testing.T) {
		confirmedChange := cloneJSONMap(fact)
		confirmedChange["claim_id"] = "CHANGE-001"
		confirmedChange["status"] = "CONFIRMED_CHANGE"
		assertDomainCode(
			t,
			validateMaterialBankClaims(
				upstream,
				payloadForClaims(confirmedChange),
			),
			"CONTENT_CLAIM_INVALID",
		)
	})
}

func TestNonNovelFixturesReachScriptReview(t *testing.T) {
	fixtures := []struct {
		filename          string
		expectVolumeCheck bool
	}{
		{filename: "complete-outline.txt", expectVolumeCheck: false},
		{filename: "brief-synopsis.txt", expectVolumeCheck: true},
		{filename: "fragmented-notes.txt", expectVolumeCheck: true},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.filename, func(t *testing.T) {
			content := fixedNonNovelFixtureContent(t, fixture.filename)
			store, err := Open(
				filepath.Join(t.TempDir(), "content_agent.db"),
				loadTestRegistry(t),
			)
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			defer store.Close()

			project, _, initial := startNonNovelRunWithContent(
				t,
				store,
				"非小说全链-"+fixture.filename,
				fixture.filename,
				content,
				3,
			)
			running := approveAndResumeLifecycleRun(t, store, project, initial)
			if running.Run.Status != "running" ||
				currentStepID(running) != "build_material_bank" {
				t.Fatalf("material bank run = %+v", running)
			}

			materialClaim := claimStructuredTask(t, store)
			if materialClaim.ContextPack.Step.StepID != "build_material_bank" ||
				len(materialClaim.ContextPack.UpstreamContext) != 2 {
				t.Fatalf("material bank claim = %+v", materialClaim.ContextPack)
			}
			materialCommit := commitNonNovelResponse(
				t,
				store,
				materialClaim,
				validNonNovelMaterialBankResponse(
					t,
					materialClaim,
					!fixture.expectVolumeCheck,
				),
			)
			if fixture.filename == "complete-outline.txt" {
				var editedPayload map[string]any
				if err := json.Unmarshal(
					materialCommit.ArtifactVersion.Payload,
					&editedPayload,
				); err != nil {
					t.Fatalf("decode material bank payload: %v", err)
				}
				editedPayload["most_promising_direction"] =
					"以失物认领推进旧案，并保留发布会直播高潮。"
				edited, err := store.CreateArtifactVersion(
					context.Background(),
					CreateVersionCommand{
						ArtifactID:    materialCommit.Artifact.ArtifactID,
						BaseVersionID: materialCommit.ArtifactVersion.ArtifactVersionID,
						BaseVersion:   materialCommit.ArtifactVersion.Version,
						ChangeMode:    "whole_artifact",
						NewPayload:    mustJSONNoTest(editedPayload),
						ActorRef:      "non_novel_editor",
					},
				)
				if err != nil {
					t.Fatalf("CreateArtifactVersion(material bank) error = %v", err)
				}
				if edited.ArtifactVersion.Version != 2 ||
					edited.Approval.SubjectSnapshotHash ==
						materialCommit.Approval.SubjectSnapshotHash {
					t.Fatalf("edited material bank = %+v", edited)
				}
				approved, err := store.ResolveApproval(
					context.Background(),
					ResolveApprovalCommand{
						ApprovalRequestID:       edited.Approval.ApprovalRequestID,
						Action:                  "approve",
						ExpectedApprovalVersion: edited.Approval.Version,
						SubjectSnapshotHash:     edited.Approval.SubjectSnapshotHash,
					},
				)
				if err != nil {
					t.Fatalf("ResolveApproval(edited material bank) error = %v", err)
				}
				running, err = store.ResumeRun(
					context.Background(),
					ResumeRunCommand{RunID: approved.Run.RunID},
				)
				if err != nil {
					t.Fatalf("ResumeRun(edited material bank) error = %v", err)
				}
			} else {
				running = approveSnapshotAndResume(t, store, materialCommit.RunSnapshot)
			}

			if fixture.expectVolumeCheck {
				if running.Run.Status != "waiting_approval" ||
					running.CurrentApproval == nil ||
					running.CurrentApproval.SubjectKind != "transition" ||
					currentStepID(running) != "review_volume_fit" {
					t.Fatalf("volume fit snapshot = %+v", running)
				}
				running = approveSnapshotAndResume(t, store, running)
			}
			if running.Run.Status != "running" ||
				currentStepID(running) != "build_story_seed" {
				t.Fatalf("story seed run = %+v", running)
			}
			if fixture.filename == "complete-outline.txt" {
				paused, err := store.RequestRunPause(
					context.Background(),
					PauseRunCommand{RunID: running.Run.RunID},
				)
				if err != nil ||
					paused.Run.Status != "paused" ||
					paused.Steps[len(paused.Steps)-1].Status != "paused" {
					t.Fatalf("RequestRunPause(idle story seed) = %+v, error = %v", paused, err)
				}
				running, err = store.ResumeRun(
					context.Background(),
					ResumeRunCommand{RunID: paused.Run.RunID},
				)
				if err != nil ||
					running.Run.Status != "running" ||
					currentStepID(running) != "build_story_seed" {
					t.Fatalf("ResumeRun(story seed) = %+v, error = %v", running, err)
				}
			}

			storyClaim := claimStructuredTask(t, store)
			if fixture.expectVolumeCheck && (len(storyClaim.ContextPack.DecisionSnapshots) != 1 ||
				storyClaim.ContextPack.DecisionSnapshots[0].DecisionType != "volume_fit") {
				t.Fatalf("story seed volume decision = %+v", storyClaim.ContextPack.DecisionSnapshots)
			}
			storyCommit := commitNonNovelResponse(
				t,
				store,
				storyClaim,
				validNonNovelStorySeedResponse(t, storyClaim),
			)
			running = approveSnapshotAndResume(t, store, storyCommit.RunSnapshot)
			if running.Run.Status != "running" ||
				currentStepID(running) != "build_series_blueprint" {
				t.Fatalf("series blueprint run = %+v", running)
			}

			blueprintClaim := claimStructuredTask(t, store)
			blueprintCommit := commitNonNovelResponse(
				t,
				store,
				blueprintClaim,
				validNonNovelSeriesBlueprintResponse(t, blueprintClaim, 3),
			)
			running = approveSnapshotAndResume(t, store, blueprintCommit.RunSnapshot)
			if running.Run.Status != "running" ||
				currentStepID(running) != "build_episode_cards" {
				t.Fatalf("episode cards run = %+v", running)
			}

			cardsClaim := claimTaskForExecutor(
				t,
				store,
				"workflow.episode_cards",
			)
			cardsCommit := commitNonNovelResponse(
				t,
				store,
				cardsClaim,
				validNonNovelEpisodeCardsResponse(t, cardsClaim),
			)
			running = approveSnapshotAndResume(t, store, cardsCommit.RunSnapshot)
			if running.Run.Status != "running" ||
				currentStepID(running) != "generate_script_units" {
				t.Fatalf("script generation run = %+v", running)
			}

			var scriptFinal ArtifactCommitResult
			for episodeNo := 1; episodeNo <= 3; episodeNo++ {
				scriptClaim := claimTaskForExecutor(
					t,
					store,
					"workflow.shared_script_generation",
				)
				if scriptClaim.Task.ItemKey != fmt.Sprintf("episode:%d", episodeNo) {
					t.Fatalf("script claim %d = %+v", episodeNo, scriptClaim.Task)
				}
				scriptFinal = commitScriptUnitResult(
					t,
					store,
					scriptClaim,
					episodeNo,
				)
			}
			if scriptFinal.CommitStatus != "waiting_approval" ||
				scriptFinal.Approval.SubjectKind != "artifact_version_set" {
				t.Fatalf("script final = %+v", scriptFinal)
			}
			approvedScripts, err := store.ResolveApproval(
				context.Background(),
				ResolveApprovalCommand{
					ApprovalRequestID:       scriptFinal.Approval.ApprovalRequestID,
					Action:                  "approve",
					ExpectedApprovalVersion: scriptFinal.Approval.Version,
					SubjectSnapshotHash:     scriptFinal.Approval.SubjectSnapshotHash,
				},
			)
			if err != nil {
				t.Fatalf("ResolveApproval(scripts) error = %v", err)
			}
			if approvedScripts.Run.Status != "running" ||
				currentStepID(approvedScripts) != "review_script_set" {
				t.Fatalf("quality review snapshot = %+v", approvedScripts)
			}
			reviewBatchClaim := claimTaskForExecutor(
				t,
				store,
				"workflow.shared_script_quality_review",
			)
			if reviewBatchClaim.ContextPack.Intent.Operation != qualityReviewOperation {
				t.Fatalf("quality review batch operation = %q", reviewBatchClaim.ContextPack.Intent.Operation)
			}
			commitNonNovelResponse(
				t,
				store,
				reviewBatchClaim,
				validQualityReviewBatchResponse(t, reviewBatchClaim),
			)
			reviewGlobalClaim := claimTaskForExecutor(
				t,
				store,
				"workflow.shared_script_quality_review",
			)
			completed := commitNonNovelResponse(
				t,
				store,
				reviewGlobalClaim,
				validQualityReviewPassedResponse(t, reviewGlobalClaim),
			).RunSnapshot
			if completed.Run.Status != "completed" ||
				currentStepID(completed) != "aggregate_scripts" {
				t.Fatalf("quality review completion = %+v", completed)
			}
			var scriptsCount, candidateCount int
			if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM artifacts WHERE run_id = ? AND artifact_type = 'scripts'`,
				initial.Run.RunID).Scan(&scriptsCount); err != nil || scriptsCount != 1 {
				t.Fatalf("scripts count = %d, error = %v", scriptsCount, err)
			}
			if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM script_candidates WHERE source_run_id = ?`,
				initial.Run.RunID).Scan(&candidateCount); err != nil || candidateCount != 1 {
				t.Fatalf("candidate count = %d, error = %v", candidateCount, err)
			}
		})
	}
}

func validQualityReviewBatchResponse(t *testing.T, claim *TaskClaim) json.RawMessage {
	t.Helper()
	cursor, err := parseQualityReviewCursor(claim.ContextPack.TaskCursor)
	if err != nil {
		t.Fatalf("parse quality batch cursor: %v", err)
	}
	summaries := make([]map[string]any, 0, cursor.Review.EpisodeEnd-cursor.Review.EpisodeStart+1)
	for episodeNo := cursor.Review.EpisodeStart; episodeNo <= cursor.Review.EpisodeEnd; episodeNo++ {
		summaries = append(summaries, map[string]any{
			"episode_no": episodeNo, "status": "passed", "summary": "未发现阻断问题。",
		})
	}
	return mustJSONNoTest(map[string]any{
		"input_snapshot_hash": cursor.Review.InputSnapshotHash,
		"episode_start":       cursor.Review.EpisodeStart,
		"episode_end":         cursor.Review.EpisodeEnd,
		"issues":              []any{},
		"episode_summaries":   summaries,
	})
}

func validQualityReviewPassedResponse(t *testing.T, claim *TaskClaim) json.RawMessage {
	t.Helper()
	cursor, err := parseQualityReviewCursor(claim.ContextPack.TaskCursor)
	if err != nil {
		t.Fatalf("parse quality global cursor: %v", err)
	}
	return mustJSONNoTest(map[string]any{
		"review_version":       1,
		"input_snapshot_hash":  cursor.Review.InputSnapshotHash,
		"scope":                "full_script",
		"issue_counts":         map[string]any{"blocker": 0, "high": 0, "medium": 0, "low": 0},
		"issues":               []any{},
		"recommended_route":    nil,
		"affected_episode_nos": []int{},
		"summary":              "审核通过。",
		"compliance_status":    "not_requested",
	})
}

func validQualityReviewActionRequiredResponse(t *testing.T, claim *TaskClaim) json.RawMessage {
	t.Helper()
	cursor, err := parseQualityReviewCursor(claim.ContextPack.TaskCursor)
	if err != nil {
		t.Fatalf("parse quality global cursor: %v", err)
	}
	return mustJSONNoTest(map[string]any{
		"review_version":      1,
		"input_snapshot_hash": cursor.Review.InputSnapshotHash,
		"scope":               "full_script",
		"issue_counts":        map[string]any{"blocker": 0, "high": 0, "medium": 1, "low": 0},
		"issues": []any{map[string]any{
			"issue_id": "QR-MEDIUM-1", "severity": "medium", "episode_nos": []int{1, 2},
			"category": "continuity", "evidence": "第1集结尾与第2集开头状态不一致",
			"impact": "影响上下集承接", "must_preserve": []string{"主角已拿到账本"},
			"revision_target": "补齐承接动作", "recommended_route": "script_generation",
		}},
		"recommended_route":    "script_generation",
		"affected_episode_nos": []int{1, 2},
		"summary":              "存在一项需要确认的连续性问题。",
		"compliance_status":    "not_requested",
	})
}

func TestNonNovelSourceEditRegeneratesMaterialBankThenStorySeed(t *testing.T) {
	ctx := context.Background()
	store, err := Open(
		filepath.Join(t.TempDir(), "content_agent.db"),
		loadTestRegistry(t),
	)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	content := fixedNonNovelFixtureContent(t, "complete-outline.txt")
	project, _, initial := startNonNovelRunWithContent(
		t,
		store,
		"非小说重生成",
		"complete-outline.txt",
		content,
		3,
	)
	running := approveAndResumeLifecycleRun(t, store, project, initial)
	materialClaim := claimStructuredTask(t, store)
	materialCommit := commitNonNovelResponse(
		t,
		store,
		materialClaim,
		validNonNovelMaterialBankResponse(t, materialClaim, true),
	)
	running = approveSnapshotAndResume(t, store, materialCommit.RunSnapshot)
	storyClaim := claimStructuredTask(t, store)
	storyCommit := commitNonNovelResponse(
		t,
		store,
		storyClaim,
		validNonNovelStorySeedResponse(t, storyClaim),
	)

	sourceArtifact := initial.Artifacts[0]
	sourceVersion, err := store.GetArtifactVersion(ctx, sourceArtifact.CurrentVersionID)
	if err != nil {
		t.Fatalf("GetArtifactVersion(source) error = %v", err)
	}
	edited, err := store.CreateArtifactVersion(ctx, CreateVersionCommand{
		ArtifactID:    sourceArtifact.ArtifactID,
		BaseVersionID: sourceVersion.ArtifactVersionID,
		BaseVersion:   sourceVersion.Version,
		ChangeMode:    "whole_artifact",
		NewPayload:    sourceVersion.Payload,
		ActorRef:      "non_novel_editor",
	})
	if err != nil {
		t.Fatalf("CreateArtifactVersion(source) error = %v", err)
	}
	if edited.ImpactReview == nil {
		t.Fatalf("source edit has no impact review: %+v", edited)
	}
	resolution, err := store.ResolveImpactReview(ctx, ResolveImpactReviewCommand{
		CommandMeta: CommandMeta{
			Scope:          project.ProjectID,
			CommandType:    "resolve_impact_review",
			IdempotencyKey: "c12bb4b6-3947-4a95-9b31-f09172f920bb",
			RequestHash:    "non_novel_regenerate_v1",
		},
		ImpactReviewID: edited.ImpactReview.ImpactReviewID,
		Action:         "regenerate_downstream",
		SnapshotHash:   edited.ImpactReview.SnapshotHash,
	})
	if err != nil {
		t.Fatalf("ResolveImpactReview(regenerate) error = %v", err)
	}
	if resolution.RegenerationPlan == nil ||
		len(resolution.RegenerationPlan.Groups) != 2 ||
		resolution.RegenerationPlan.Groups[0].StepID != "build_material_bank" ||
		resolution.RegenerationPlan.Groups[0].Status != "ready" ||
		resolution.RegenerationPlan.Groups[1].StepID != "build_story_seed" ||
		resolution.RegenerationPlan.Groups[1].Status != "blocked" {
		t.Fatalf("non-novel regeneration plan = %+v", resolution.RegenerationPlan)
	}

	running, err = store.ResumeRun(
		ctx,
		ResumeRunCommand{RunID: initial.Run.RunID},
	)
	if err != nil || running.Run.Status != "running" {
		t.Fatalf("ResumeRun(material regeneration) = %+v, error = %v", running, err)
	}
	replacementMaterialClaim := claimStructuredTask(t, store)
	if replacementMaterialClaim.ContextPack.Step.StepID != "build_material_bank" {
		t.Fatalf("replacement material claim = %+v", replacementMaterialClaim.ContextPack)
	}
	replacementMaterial := commitNonNovelResponse(
		t,
		store,
		replacementMaterialClaim,
		validNonNovelMaterialBankResponse(t, replacementMaterialClaim, true),
	)
	if replacementMaterial.Artifact.ArtifactID != materialCommit.Artifact.ArtifactID ||
		replacementMaterial.ArtifactVersion.Version != 2 ||
		replacementMaterial.ArtifactVersion.CreationReason != "regeneration" ||
		replacementMaterial.ArtifactVersion.BaseVersionID == nil ||
		*replacementMaterial.ArtifactVersion.BaseVersionID !=
			materialCommit.ArtifactVersion.ArtifactVersionID {
		t.Fatalf("replacement material bank = %+v", replacementMaterial)
	}
	approvedMaterial, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
		ApprovalRequestID:       replacementMaterial.Approval.ApprovalRequestID,
		Action:                  "approve",
		ExpectedApprovalVersion: replacementMaterial.Approval.Version,
		SubjectSnapshotHash:     replacementMaterial.Approval.SubjectSnapshotHash,
	})
	if err != nil {
		t.Fatalf("ResolveApproval(replacement material) error = %v", err)
	}
	if approvedMaterial.Run.Status != "paused" ||
		currentStepID(approvedMaterial) != "build_story_seed" {
		t.Fatalf("run after material regeneration = %+v", approvedMaterial)
	}

	running, err = store.ResumeRun(
		ctx,
		ResumeRunCommand{RunID: initial.Run.RunID},
	)
	if err != nil || running.Run.Status != "running" {
		t.Fatalf("ResumeRun(story regeneration) = %+v, error = %v", running, err)
	}
	replacementStoryClaim := claimStructuredTask(t, store)
	replacementStory := commitNonNovelResponse(
		t,
		store,
		replacementStoryClaim,
		validNonNovelStorySeedResponse(t, replacementStoryClaim),
	)
	if replacementStory.Artifact.ArtifactID != storyCommit.Artifact.ArtifactID ||
		replacementStory.ArtifactVersion.Version != 2 ||
		replacementStory.ArtifactVersion.CreationReason != "regeneration" ||
		replacementStory.ArtifactVersion.BaseVersionID == nil ||
		*replacementStory.ArtifactVersion.BaseVersionID !=
			storyCommit.ArtifactVersion.ArtifactVersionID {
		t.Fatalf("replacement story seed = %+v", replacementStory)
	}
	approvedStory, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
		ApprovalRequestID:       replacementStory.Approval.ApprovalRequestID,
		Action:                  "approve",
		ExpectedApprovalVersion: replacementStory.Approval.Version,
		SubjectSnapshotHash:     replacementStory.Approval.SubjectSnapshotHash,
	})
	if err != nil {
		t.Fatalf("ResolveApproval(replacement story) error = %v", err)
	}
	plan, err := store.GetRegenerationPlan(
		ctx,
		resolution.RegenerationPlan.RegenerationPlanID,
	)
	if err != nil ||
		plan.Status != "completed" ||
		plan.Groups[0].Status != "completed" ||
		plan.Groups[1].Status != "completed" ||
		approvedStory.Run.Status != "paused" ||
		currentStepID(approvedStory) != "build_series_blueprint" {
		t.Fatalf(
			"completed non-novel regeneration = plan %+v, run %+v, error = %v",
			plan,
			approvedStory,
			err,
		)
	}
}

func validNonNovelMaterialBankResponse(
	t *testing.T,
	claim *TaskClaim,
	sufficient bool,
) json.RawMessage {
	t.Helper()
	trace := nonNovelTraceFromManifest(t, claim)
	return mustJSONNoTest(map[string]any{
		"material_bank": map[string]any{
			"input_type_tags": []string{"story_outline"},
			"user_supplied_facts": map[string]any{
				"characters":     []any{map[string]any{"name": "许知夏"}},
				"relationships":  []any{},
				"events":         []any{map[string]any{"event": "调查失物"}},
				"world_rules":    []string{},
				"scenes":         []any{},
				"dialogue_lines": []string{},
				"selling_points": []string{"失物串联旧案"},
			},
			"conflict_materials":      []any{},
			"emotional_drives":        []any{},
			"payoff_candidates":       []any{},
			"hook_candidates":         []any{},
			"visual_scene_candidates": []any{},
			"discard_or_later":        []string{},
			"inferred_candidates": []any{map[string]any{
				"content":               "可增加公开质证桥段",
				"reason":                "服务高潮",
				"requires_confirmation": true,
			}},
			"gaps_and_questions":       []string{},
			"most_promising_direction": "围绕失物调查旧案",
			"volume_fit_notes": map[string]any{
				"material_sufficiency": map[bool]string{
					true:  "sufficient",
					false: "insufficient",
				}[sufficient],
				"can_support_target": sufficient,
				"risks":              []string{},
				"questions":          []string{},
			},
			"source_trace": trace,
		},
	})
}

func validNonNovelStorySeedResponse(
	t *testing.T,
	claim *TaskClaim,
) json.RawMessage {
	t.Helper()
	trace := nonNovelTraceFromUpstream(t, claim)
	return mustJSONNoTest(map[string]any{
		"story_seed": map[string]any{
			"logline":      "失业记者通过失物追查母亲失踪与工程旧案。",
			"core_premise": "每件失物都指向同一场被掩盖的事故。",
			"genre_tags":   []string{"都市悬疑"},
			"protagonist": map[string]any{
				"name_or_role": "许知夏",
				"goal":         "找到母亲并保住旧物店",
				"pressure":     "地产商强制收购",
				"inner_need":   "学会信任同伴",
			},
			"main_characters":     []any{},
			"relationship_engine": []any{},
			"central_conflict":    "许知夏调查旧案，杜衡持续销毁证据。",
			"world_rules":         []string{},
			"main_plotline": map[string]any{
				"opening_situation":      "许知夏失业返乡接手旧物店。",
				"escalation_path":        []string{"发现旧手机", "寻找失物主人", "直播公开证据"},
				"major_turn":             "确认母亲仍然活着。",
				"final_payoff_direction": "发布会公开事故真相。",
			},
			"payoff_chain":        []any{},
			"hook_engine":         []any{},
			"generated_additions": []any{},
			"volume_plan_notes": map[string]any{
				"can_support_target": true,
				"expansion_strategy": "只补因果桥段，不新增关键设定和主线。",
				"padding_risks":      []string{},
			},
			"development_notes": []string{},
			"risks":             []string{},
			"source_trace":      trace,
		},
	})
}

func validNonNovelSeriesBlueprintResponse(
	t *testing.T,
	claim *TaskClaim,
	targetEpisodeCount int,
) json.RawMessage {
	t.Helper()
	trace := nonNovelTraceFromUpstream(t, claim)
	return mustJSONNoTest(map[string]any{
		"series_blueprint": map[string]any{
			"resolved_episode_count":    targetEpisodeCount,
			"recommended_episode_count": targetEpisodeCount,
			"episode_count_reason":      "按已确认体量推进主线。",
			"series_promise":            "逐件失物揭开旧案。",
			"phase_plan": []any{map[string]any{
				"phase_id":       "P1",
				"episode_range":  fmt.Sprintf("1-%d", targetEpisodeCount),
				"phase_function": "完成调查与公开真相",
				"main_conflict":  "调查者与地产商争夺证据",
				"payoff_focus":   "旧案真相",
				"hook_strategy":  "每集新增一件关键失物",
			}},
			"payoff_distribution":      []any{},
			"hook_distribution":        []any{},
			"first_major_climax_plan":  map[string]any{"episode": targetEpisodeCount},
			"pacing_density_plan":      []any{},
			"character_progression":    []any{},
			"relationship_progression": []any{},
			"continuity_rules":         []string{"周既明不是反派"},
			"generated_additions":      []any{},
			"fit_risks":                []string{},
			"source_trace":             trace,
		},
	})
}

func validNonNovelEpisodeCardsResponse(
	t *testing.T,
	claim *TaskClaim,
) json.RawMessage {
	t.Helper()
	var cursor struct {
		Batch struct {
			EpisodeStart int `json:"episode_start"`
			EpisodeEnd   int `json:"episode_end"`
		} `json:"batch"`
	}
	if err := json.Unmarshal(claim.ContextPack.TaskCursor, &cursor); err != nil ||
		cursor.Batch.EpisodeStart <= 0 ||
		cursor.Batch.EpisodeEnd < cursor.Batch.EpisodeStart {
		t.Fatalf("episode cards cursor = %s, error = %v", claim.ContextPack.TaskCursor, err)
	}
	episodes := make(
		[]map[string]any,
		0,
		cursor.Batch.EpisodeEnd-cursor.Batch.EpisodeStart+1,
	)
	for episodeNo := cursor.Batch.EpisodeStart; episodeNo <= cursor.Batch.EpisodeEnd; episodeNo++ {
		episodes = append(episodes, map[string]any{
			"episode_no":         episodeNo,
			"episode_function":   "推进失物调查。",
			"opening_state":      "许知夏继续寻找失物主人。",
			"main_conflict":      "杜衡阻止证据公开。",
			"key_events":         []string{"找到新线索"},
			"payoff_or_reversal": "获得阶段证据。",
			"character_turn":     "许知夏更信任周既明。",
			"ending_hook": map[string]any{
				"hook_text":     "新的失物出现。",
				"hook_type":     "悬念",
				"hook_strength": "high",
			},
			"card_point_function": "推动追看。",
			"pacing_plan":         map[string]any{"tempo": "fast"},
			"visual_strategy":     map[string]any{"focus": "lost_item"},
			"scene_outline":       []any{map[string]any{"scene": "旧物店"}},
			"source_basis": map[string]any{
				"from_user_material":    []string{"失物调查旧案"},
				"from_story_seed":       []string{"寻找母亲"},
				"from_series_blueprint": []string{"逐件失物推进"},
				"generated_additions":   []string{},
			},
			"risk_notes": []string{},
		})
	}
	return mustJSONNoTest(map[string]any{
		"episode_cards": map[string]any{
			"episodes": episodes,
			"continuity_delta": map[string]any{
				"new_facts":               []string{},
				"character_state_changes": []string{},
				"relationship_changes":    []string{},
				"hooks_opened":            []string{},
				"hooks_resolved":          []string{},
			},
		},
	})
}

func nonNovelTraceFromManifest(
	t *testing.T,
	claim *TaskClaim,
) map[string]any {
	t.Helper()
	for _, upstream := range claim.ContextPack.UpstreamContext {
		if upstream.ArtifactType != "source_manifest" {
			continue
		}
		var manifest sourceManifestPayload
		if err := json.Unmarshal(upstream.Content, &manifest); err != nil ||
			len(manifest.Units) == 0 {
			t.Fatalf("source manifest = %s, error = %v", upstream.Content, err)
		}
		unit := manifest.Units[0]
		reference := map[string]any{
			"source_type":       "asset_text_range",
			"asset_id":          unit.AssetID,
			"asset_snapshot_id": unit.AssetSnapshotID,
			"source_unit_id":    unit.SourceUnitID,
			"range_label":       unit.SourceUnitID,
		}
		return map[string]any{
			"grounded": []any{map[string]any{
				"claim":       "许知夏是失业记者。",
				"source_refs": []any{reference},
			}},
			"inferred": []any{},
			"claims": []any{
				map[string]any{
					"claim_id":     "FACT-001",
					"category":     "character",
					"statement":    "许知夏是失业记者。",
					"status":       "FACT",
					"source_refs":  []any{reference},
					"confidence":   "high",
					"locked":       true,
					"approval_ref": nil,
					"notes":        nil,
				},
				map[string]any{
					"claim_id":     "PROP-001",
					"category":     "event",
					"statement":    "可以增加一次公开质证。",
					"status":       "PROPOSAL",
					"source_refs":  []any{},
					"confidence":   "medium",
					"locked":       false,
					"approval_ref": nil,
					"notes":        "等待用户确认",
				},
			},
		}
	}
	t.Fatal("source_manifest is missing from context pack")
	return nil
}

func nonNovelTraceFromUpstream(
	t *testing.T,
	claim *TaskClaim,
) map[string]any {
	t.Helper()
	for _, upstream := range claim.ContextPack.UpstreamContext {
		if upstream.ArtifactType != "material_bank" &&
			upstream.ArtifactType != "story_seed" {
			continue
		}
		var payload struct {
			SourceTrace map[string]any `json:"source_trace"`
		}
		if err := json.Unmarshal(upstream.Content, &payload); err != nil ||
			payload.SourceTrace == nil {
			t.Fatalf(
				"upstream source trace = %s, error = %v",
				upstream.Content,
				err,
			)
		}
		return payload.SourceTrace
	}
	t.Fatal("content claims are missing from upstream context")
	return nil
}

func commitNonNovelResponse(
	t *testing.T,
	store *Store,
	claim *TaskClaim,
	response json.RawMessage,
) ArtifactCommitResult {
	t.Helper()
	received, err := store.SubmitExecutionResult(
		context.Background(),
		SubmitExecutionResultCommand{
			AttemptID:         claim.Attempt.AttemptID,
			AttemptToken:      claim.AttemptToken,
			InputSnapshotHash: claim.Attempt.InputSnapshotHash,
			ResponsePayload:   response,
		},
	)
	if err != nil || received.ResponseHash == nil {
		t.Fatalf("SubmitExecutionResult(%s) = %+v, error = %v", claim.ContextPack.Step.StepID, received, err)
	}
	committed, err := store.CommitExecutionResult(
		context.Background(),
		CommitExecutionResultCommand{
			AttemptID:            claim.Attempt.AttemptID,
			ExpectedResponseHash: *received.ResponseHash,
		},
	)
	if err != nil {
		t.Fatalf("CommitExecutionResult(%s) error = %v", claim.ContextPack.Step.StepID, err)
	}
	return committed
}

func approveSnapshotAndResume(
	t *testing.T,
	store *Store,
	snapshot RunSnapshot,
) RunSnapshot {
	t.Helper()
	if snapshot.CurrentApproval == nil {
		t.Fatalf("run %s has no current approval", snapshot.Run.RunID)
	}
	approved, err := store.ResolveApproval(
		context.Background(),
		ResolveApprovalCommand{
			ApprovalRequestID:       snapshot.CurrentApproval.ApprovalRequestID,
			Action:                  "approve",
			ExpectedApprovalVersion: snapshot.CurrentApproval.Version,
			SubjectSnapshotHash:     snapshot.CurrentApproval.SubjectSnapshotHash,
		},
	)
	if err != nil {
		t.Fatalf("ResolveApproval(%s) error = %v", currentStepID(snapshot), err)
	}
	resumed, err := store.ResumeRun(
		context.Background(),
		ResumeRunCommand{RunID: approved.Run.RunID},
	)
	if err != nil {
		t.Fatalf("ResumeRun(%s) error = %v", currentStepID(approved), err)
	}
	return resumed
}

func claimTaskForExecutor(
	t *testing.T,
	store *Store,
	executorID string,
) *TaskClaim {
	t.Helper()
	claim, err := store.ClaimExecutionTask(
		context.Background(),
		ClaimExecutionTaskCommand{
			WorkerID:     "non_novel_worker",
			ExecutorIDs:  []string{executorID},
			ProviderID:   "provider_test",
			LeaseSeconds: 60,
		},
	)
	if err != nil {
		t.Fatalf("ClaimExecutionTask(%s) error = %v", executorID, err)
	}
	if claim == nil {
		t.Fatalf("ClaimExecutionTask(%s) returned no task", executorID)
	}
	return claim
}

func currentStepID(snapshot RunSnapshot) string {
	if snapshot.Run.CurrentStepRunID == nil {
		return ""
	}
	for _, step := range snapshot.Steps {
		if step.StepRunID == *snapshot.Run.CurrentStepRunID {
			return step.StepID
		}
	}
	return ""
}

func fixedNonNovelFixtureContent(t *testing.T, filename string) string {
	t.Helper()
	_, file, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	projectRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	if override := strings.TrimSpace(os.Getenv("CONTENT_AGENT_TEST_PROJECT_ROOT")); override != "" {
		projectRoot = override
	}
	content, err := os.ReadFile(filepath.Join(
		projectRoot,
		"acceptance",
		"fixtures",
		"non-novel",
		filename,
	))
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", filename, err)
	}
	return string(content)
}

func buildNonNovelSourceManifestForTest(
	t *testing.T,
	store *Store,
	title string,
	filename string,
	content string,
) sourceManifestPayload {
	t.Helper()
	project, _, initial := startNonNovelRunWithContent(
		t,
		store,
		title,
		filename,
		content,
		3,
	)
	running := approveAndResumeLifecycleRun(t, store, project, initial)
	if running.Run.Status != "running" {
		t.Fatalf("run status after source approval = %s, want running", running.Run.Status)
	}

	artifacts, err := store.ListArtifactsByRun(
		context.Background(),
		running.Run.RunID,
	)
	if err != nil {
		t.Fatalf("ListArtifactsByRun() error = %v", err)
	}
	for _, artifact := range artifacts {
		if artifact.ArtifactType != "source_manifest" {
			continue
		}
		version, err := store.GetArtifactVersion(
			context.Background(),
			artifact.CurrentVersionID,
		)
		if err != nil {
			t.Fatalf("GetArtifactVersion(source_manifest) error = %v", err)
		}
		var manifest sourceManifestPayload
		if err := json.Unmarshal(version.Payload, &manifest); err != nil {
			t.Fatalf("Unmarshal(source_manifest) error = %v", err)
		}
		if version.Status != "confirmed" {
			t.Fatalf("source manifest version = %+v", version)
		}
		return manifest
	}
	t.Fatal("source_manifest artifact not found")
	return sourceManifestPayload{}
}

func startNonNovelRunWithContent(
	t *testing.T,
	store *Store,
	title string,
	filename string,
	content string,
	targetEpisodeCount int,
) (Project, AssetResult, RunSnapshot) {
	t.Helper()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, title)
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	message, err := store.CreateUserMessage(
		ctx,
		project.PrimaryConversationID,
		"开始生成非小说剧本",
	)
	if err != nil {
		t.Fatalf("CreateUserMessage() error = %v", err)
	}
	sourceAsset := createTextAsset(t, store, project.ProjectID, filename, content)
	input := mustJSONNoTest(map[string]any{
		"project_id":  project.ProjectID,
		"source_type": "non_novel",
		"assets": []map[string]any{{
			"asset_id":          sourceAsset.Asset.AssetID,
			"asset_snapshot_id": sourceAsset.Snapshot.AssetSnapshotID,
			"role":              "primary_source",
			"order":             1,
		}},
		"declared_material_types": []string{"story_outline"},
		"user_request_message_id": message.MessageID,
		"user_notes":              []string{},
	})
	config := mustJSONNoTest(map[string]any{
		"config_ref": "creation",
		"payload": map[string]any{
			"target_episode_count":     targetEpisodeCount,
			"episode_duration_minutes": 2,
		},
	})
	command := bindStartRunProposal(t, store, StartRunCommand{
		ProjectID:         project.ProjectID,
		ConversationID:    project.PrimaryConversationID,
		CapabilityID:      "non_novel_to_script",
		CapabilityVersion: "1.3.0",
		RunKind:           "generation",
		Input:             input,
		Config:            config,
		Confirmed:         true,
	})
	snapshot, err := store.StartRun(ctx, command)
	if err != nil {
		t.Fatalf("StartRun(non-novel) error = %v", err)
	}
	return project, sourceAsset, snapshot
}
