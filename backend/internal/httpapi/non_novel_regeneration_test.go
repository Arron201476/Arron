package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func nonNovelStartAgentDecision(
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
		"project_id":              input.ProjectID,
		"source_type":             "non_novel",
		"assets":                  assets,
		"declared_material_types": []string{"story_outline"},
		"user_notes":              []string{},
	})
	config, _ := json.Marshal(map[string]any{
		"config_ref": "creation",
		"payload": map[string]any{
			"target_episode_count":     3,
			"episode_duration_minutes": 2,
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

func TestRuntimeHTTPNonNovelSourceEditRegenerationFlow(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", defaultTestInternalServiceToken)
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatalf("LoadRegistry() error = %v", err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatalf("runtime.Open() error = %v", err)
	}
	t.Cleanup(func() { store.Close() })
	server := NewWithRuntime(
		shell.NewWithAgentService(registry, &scriptedSDKAgent{
			store: store, decide: nonNovelStartAgentDecision,
		}),
		store,
		nil,
	)
	startTestAgentTurnManager(t, server)
	handler := server.Handler()

	sourceBytes, err := os.ReadFile(filepath.Join(
		testRoot(t),
		"acceptance",
		"fixtures",
		"non-novel",
		"complete-outline.txt",
	))
	if err != nil {
		t.Fatalf("ReadFile(complete-outline.txt) error = %v", err)
	}
	project := objectAt(t, performJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/projects",
		map[string]any{"title": "HTTP 非小说重生成"},
		httpNonNovelKey("01"),
		http.StatusCreated,
	), "data")
	projectID := stringAt(t, project, "project_id")
	conversationID := stringAt(t, project, "primary_conversation_id")

	uploadItems := arrayAt(t, objectAt(t, performJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/projects/"+projectID+"/upload-sessions",
		map[string]any{"items": []map[string]any{{
			"client_item_key":     "outline_1",
			"kind":                "text",
			"original_filename":   "complete-outline.txt",
			"declared_mime_type":  "text/plain",
			"declared_size_bytes": len(sourceBytes),
		}}},
		httpNonNovelKey("02"),
		http.StatusCreated,
	), "data"), "items")
	uploadItemID := stringAt(t, uploadItems[0].(map[string]any), "upload_item_id")
	performBytes(
		t,
		handler,
		http.MethodPut,
		"/api/v1/upload-items/"+uploadItemID+"/content",
		sourceBytes,
		http.StatusOK,
	)
	assetResult := objectAt(t, performJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/upload-items/"+uploadItemID+"/complete",
		nil,
		httpNonNovelKey("03"),
		http.StatusCreated,
	), "data")
	assetID := stringAt(t, objectAt(t, assetResult, "asset"), "asset_id")
	assetSnapshotID := stringAt(t, objectAt(t, assetResult, "asset_snapshot"), "asset_snapshot_id")

	messageResponse := performJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/conversations/"+conversationID+"/messages",
		map[string]any{
			"content": "根据这份完整故事大纲生成三集剧本",
			"capability_ref": map[string]any{
				"capability_id": "non_novel_to_script",
				"version":       "1.3.0",
			},
			"attachment_refs": []map[string]any{{
				"asset_id":          assetID,
				"asset_snapshot_id": assetSnapshotID,
			}},
		},
		httpNonNovelKey("04"),
		http.StatusAccepted,
	)
	turnID := stringAt(t, objectAt(t, messageResponse, "data"), "agent_turn_id")
	messageData := testExchangeObject(t, waitForCommittedTestExchange(
		t, store, projectID, httpNonNovelKey("04")["Idempotency-Key"], turnID,
	))
	stringAt(t, objectAt(t, messageData, "user_message"), "message_id")
	proposedAction := objectAt(t, messageData, "proposed_action")

	initial := objectAt(t, performJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/projects/"+projectID+"/runs",
		map[string]any{
			"capability_id":      "non_novel_to_script",
			"capability_version": "1.3.0",
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
		},
		httpNonNovelKey("05"),
		http.StatusCreated,
	), "data")
	run := objectAt(t, initial, "run")
	runID := stringAt(t, run, "run_id")
	sourceArtifact := arrayAt(t, initial, "artifact_summary")[0].(map[string]any)
	sourceArtifactID := stringAt(t, sourceArtifact, "artifact_id")
	sourceVersionID := stringAt(t, sourceArtifact, "current_version_id")

	httpNonNovelApproveCurrent(t, handler, initial, "06")
	httpNonNovelResume(t, handler, runID, "07", "build_material_bank")
	initialMaterial := httpNonNovelClaimAndCommit(
		t,
		handler,
		"build_material_bank",
		httpNonNovelMaterialBankResponse,
		"initial_material",
	)
	initialMaterialArtifact := objectAt(t, initialMaterial, "artifact")
	initialMaterialVersion := objectAt(t, initialMaterial, "artifact_version")

	httpNonNovelApproveCurrent(t, handler, objectAt(t, initialMaterial, "run_snapshot"), "08")
	httpNonNovelResume(t, handler, runID, "09", "build_story_seed")
	initialStory := httpNonNovelClaimAndCommit(
		t,
		handler,
		"build_story_seed",
		httpNonNovelStorySeedResponse,
		"initial_story",
	)
	initialStoryArtifact := objectAt(t, initialStory, "artifact")
	initialStoryVersion := objectAt(t, initialStory, "artifact_version")

	sourceVersion := objectAt(t, performJSON(
		t,
		handler,
		http.MethodGet,
		"/api/v1/artifact-versions/"+sourceVersionID,
		nil,
		http.StatusOK,
	), "data")
	editData := objectAt(t, performJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/artifacts/"+sourceArtifactID+"/versions",
		map[string]any{
			"base_version_id": sourceVersionID,
			"base_version":    int(numberAt(t, sourceVersion, "version")),
			"change_mode":     "full_payload",
			"new_payload":     sourceVersion["payload"],
		},
		httpNonNovelKey("10"),
		http.StatusCreated,
	), "data")
	impactReview := objectAt(t, editData, "impact_review")
	if got := len(arrayAt(t, impactReview, "affected_items")); got != 2 {
		t.Fatalf("affected item count = %d, want 2; review = %+v", got, impactReview)
	}
	if stringAt(t, impactReview, "status") != "regeneration_planned" {
		t.Fatalf("impact review was not resolved automatically: %+v", impactReview)
	}
	resolution := objectAt(t, editData, "propagation")
	plan := objectAt(t, resolution, "regeneration_plan")
	groups := arrayAt(t, plan, "groups")
	if len(groups) != 2 ||
		stringAt(t, groups[0].(map[string]any), "step_id") != "build_material_bank" ||
		stringAt(t, groups[0].(map[string]any), "status") != "ready" ||
		stringAt(t, groups[1].(map[string]any), "step_id") != "build_story_seed" ||
		stringAt(t, groups[1].(map[string]any), "status") != "blocked" {
		t.Fatalf("initial regeneration plan = %+v", plan)
	}
	planID := stringAt(t, plan, "regeneration_plan_id")

	httpNonNovelResume(t, handler, runID, "12", "build_material_bank")
	replacementMaterial := httpNonNovelClaimAndCommit(
		t,
		handler,
		"build_material_bank",
		httpNonNovelMaterialBankResponse,
		"replacement_material",
	)
	httpNonNovelAssertReplacement(
		t,
		replacementMaterial,
		stringAt(t, initialMaterialArtifact, "artifact_id"),
		stringAt(t, initialMaterialVersion, "artifact_version_id"),
	)
	approvedMaterial := httpNonNovelApproveCurrent(
		t,
		handler,
		objectAt(t, replacementMaterial, "run_snapshot"),
		"13",
	)
	httpNonNovelAssertCurrentStep(t, approvedMaterial, "build_story_seed")

	httpNonNovelResume(t, handler, runID, "14", "build_story_seed")
	replacementStory := httpNonNovelClaimAndCommit(
		t,
		handler,
		"build_story_seed",
		httpNonNovelStorySeedResponse,
		"replacement_story",
	)
	httpNonNovelAssertReplacement(
		t,
		replacementStory,
		stringAt(t, initialStoryArtifact, "artifact_id"),
		stringAt(t, initialStoryVersion, "artifact_version_id"),
	)
	approvedStory := httpNonNovelApproveCurrent(
		t,
		handler,
		objectAt(t, replacementStory, "run_snapshot"),
		"15",
	)
	httpNonNovelAssertCurrentStep(t, approvedStory, "build_series_blueprint")

	completedPlan := objectAt(t, performJSON(
		t,
		handler,
		http.MethodGet,
		"/api/v1/regeneration-plans/"+planID,
		nil,
		http.StatusOK,
	), "data")
	completedGroups := arrayAt(t, completedPlan, "groups")
	if stringAt(t, completedPlan, "status") != "completed" ||
		len(completedGroups) != 2 ||
		stringAt(t, completedGroups[0].(map[string]any), "status") != "completed" ||
		stringAt(t, completedGroups[1].(map[string]any), "status") != "completed" {
		t.Fatalf("completed regeneration plan = %+v", completedPlan)
	}

	httpNonNovelResume(t, handler, runID, "16", "build_series_blueprint")
	seriesBlueprint := httpNonNovelClaimAndCommit(
		t,
		handler,
		"build_series_blueprint",
		httpNonNovelSeriesBlueprintResponse,
		"series_blueprint",
	)
	httpNonNovelApproveCurrent(
		t,
		handler,
		objectAt(t, seriesBlueprint, "run_snapshot"),
		"17",
	)
	httpNonNovelResume(t, handler, runID, "18", "build_episode_cards")
	episodeCards := httpNonNovelClaimAndCommit(
		t,
		handler,
		"build_episode_cards",
		httpNonNovelEpisodeCardsResponse,
		"episode_cards",
	)
	httpNonNovelApproveCurrent(
		t,
		handler,
		objectAt(t, episodeCards, "run_snapshot"),
		"19",
	)
	httpNonNovelResume(t, handler, runID, "20", "generate_script_units")

	artifactItems := arrayAt(t, objectAt(t, performJSON(
		t,
		handler,
		http.MethodGet,
		"/api/v1/projects/"+projectID+"/artifacts",
		nil,
		http.StatusOK,
	), "data"), "items")
	contextVersions := make([]string, 0, 3)
	for _, item := range artifactItems {
		artifact := item.(map[string]any)
		if artifact["artifact_type"] != "script_context" {
			continue
		}
		contextVersions = append(
			contextVersions,
			stringAt(t, artifact, "current_version_id"),
		)
	}
	if len(contextVersions) != 3 {
		t.Fatalf("script context count = %d, want 3", len(contextVersions))
	}
	seenEpisodes := map[int]bool{}
	for _, versionID := range contextVersions {
		version := objectAt(t, performJSON(
			t,
			handler,
			http.MethodGet,
			"/api/v1/artifact-versions/"+versionID,
			nil,
			http.StatusOK,
		), "data")
		episodeNo := int(numberAt(t, objectAt(t, version, "payload"), "episode_no"))
		if stringAt(t, version, "status") != "confirmed" ||
			episodeNo < 1 || episodeNo > 3 || seenEpisodes[episodeNo] {
			t.Fatalf("script context episode %d = %+v", episodeNo, version)
		}
		seenEpisodes[episodeNo] = true
		lineage := objectAt(t, performJSON(
			t,
			handler,
			http.MethodGet,
			"/api/v1/artifact-versions/"+versionID+"/lineage?max_depth=1",
			nil,
			http.StatusOK,
		), "data")
		if got := len(arrayAt(t, lineage, "upstream")); got != 5 {
			t.Fatalf("script context %d upstream count = %d, want 5", episodeNo, got)
		}
	}

	scriptClaim := objectAt(t, performInternalJSON(
		t,
		handler,
		http.MethodPost,
		"/internal/v1/executor/task-claims",
		map[string]any{
			"worker_id":     "http_script_worker",
			"executor_ids":  []string{"workflow.shared_script_generation"},
			"provider_id":   "http_provider",
			"model_id":      "http_model",
			"lease_seconds": 60,
		},
		http.StatusOK,
	), "data")
	scriptPack := objectAt(t, scriptClaim, "context_pack")
	scriptUpstream := arrayAt(t, scriptPack, "upstream_context")
	if len(scriptUpstream) != 2 {
		t.Fatalf("script context pack upstream = %+v", scriptUpstream)
	}
	contextItemFound := false
	for _, item := range scriptUpstream {
		upstream := item.(map[string]any)
		if upstream["artifact_type"] == "script_context" {
			contextItemFound = stringAt(t, upstream, "scope_key") == "episode:1"
		}
	}
	if !contextItemFound {
		t.Fatalf("episode 1 script context missing from pack: %+v", scriptPack)
	}

	var scriptCommit map[string]any
	for episodeNo := 1; episodeNo <= 3; episodeNo++ {
		claim := scriptClaim
		if episodeNo > 1 {
			claim = objectAt(t, performInternalJSON(
				t,
				handler,
				http.MethodPost,
				"/internal/v1/executor/task-claims",
				map[string]any{
					"worker_id":     "http_script_worker",
					"executor_ids":  []string{"workflow.shared_script_generation"},
					"provider_id":   "http_provider",
					"model_id":      "http_model",
					"lease_seconds": 60,
				},
				http.StatusOK,
			), "data")
		}
		scriptCommit = httpNonNovelCommitClaim(
			t,
			handler,
			claim,
			httpNonNovelScriptResponse(episodeNo),
			fmt.Sprintf("script_%d", episodeNo),
		)
	}
	if stringAt(t, scriptCommit, "commit_status") != "waiting_approval" {
		t.Fatalf("final script commit = %+v", scriptCommit)
	}

	artifactItems = arrayAt(t, objectAt(t, performJSON(
		t,
		handler,
		http.MethodGet,
		"/api/v1/projects/"+projectID+"/artifacts",
		nil,
		http.StatusOK,
	), "data"), "items")
	var episodeTwoScript map[string]any
	for _, item := range artifactItems {
		artifact := item.(map[string]any)
		if artifact["artifact_type"] == "script_unit" &&
			artifact["scope_key"] == "episode:2" {
			episodeTwoScript = artifact
			break
		}
	}
	if episodeTwoScript == nil {
		t.Fatal("episode 2 script artifact is missing")
	}
	episodeTwoVersion := objectAt(t, performJSON(
		t,
		handler,
		http.MethodGet,
		"/api/v1/artifact-versions/"+stringAt(t, episodeTwoScript, "current_version_id"),
		nil,
		http.StatusOK,
	), "data")
	editedPayload := objectAt(t, episodeTwoVersion, "payload")
	editedPayload["title"] = "第2集（用户修改）"
	editResult := objectAt(t, performJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/artifacts/"+stringAt(t, episodeTwoScript, "artifact_id")+"/versions",
		map[string]any{
			"base_version_id": stringAt(t, episodeTwoVersion, "artifact_version_id"),
			"base_version":    int(numberAt(t, episodeTwoVersion, "version")),
			"change_mode":     "full_payload",
			"new_payload":     editedPayload,
		},
		httpNonNovelKey("21"),
		http.StatusCreated,
	), "data")
	if refreshRequired, ok := editResult["handoff_refresh_required"].(bool); !ok ||
		!refreshRequired ||
		len(arrayAt(t, editResult, "pending_refresh_scopes")) != 1 {
		t.Fatalf("HTTP script edit result = %+v", editResult)
	}
	editedScriptVersionID := stringAt(
		t,
		objectAt(t, editResult, "artifact_version"),
		"artifact_version_id",
	)
	editCompletion := objectAt(t, performJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/runs/"+runID+"/script-edit-completions",
		map[string]any{
			"expected_script_version_ids": []string{editedScriptVersionID},
		},
		httpNonNovelKey("22"),
		http.StatusAccepted,
	), "data")
	if stringAt(t, objectAt(t, objectAt(t, editCompletion, "run_snapshot"), "run"), "status") != "running" ||
		len(arrayAt(t, editCompletion, "refresh_task_ids")) != 1 {
		t.Fatalf("HTTP script edit completion = %+v", editCompletion)
	}
	refreshClaim := objectAt(t, performInternalJSON(
		t,
		handler,
		http.MethodPost,
		"/internal/v1/executor/task-claims",
		map[string]any{
			"worker_id":     "http_handoff_refresh_worker",
			"executor_ids":  []string{"workflow.shared_script_generation"},
			"provider_id":   "http_provider",
			"model_id":      "http_model",
			"lease_seconds": 60,
		},
		http.StatusOK,
	), "data")
	refreshPack := objectAt(t, refreshClaim, "context_pack")
	if stringAt(t, objectAt(t, refreshPack, "intent"), "operation") != "refresh_script_handoff" ||
		stringAt(t, objectAt(t, refreshPack, "output_contract"), "artifact_type") != "script_handoff" ||
		stringAt(t, objectAt(t, refreshPack, "target"), "scope_key") != "episode:2" {
		t.Fatalf("HTTP handoff refresh context = %+v", refreshPack)
	}
	refreshCommit := httpNonNovelCommitClaim(
		t,
		handler,
		refreshClaim,
		map[string]any{"script_handoff": httpNonNovelScriptHandoff(2)},
		"handoff_refresh_2",
	)
	if stringAt(t, refreshCommit, "commit_status") != "waiting_approval" ||
		stringAt(t, objectAt(t, refreshCommit, "artifact"), "artifact_type") != "script_handoff" ||
		int(numberAt(t, objectAt(t, refreshCommit, "artifact_version"), "version")) != 2 {
		t.Fatalf("HTTP handoff refresh commit = %+v", refreshCommit)
	}
}

func httpNonNovelCommitClaim(
	t *testing.T,
	handler http.Handler,
	claim map[string]any,
	response map[string]any,
	traceRef string,
) map[string]any {
	t.Helper()
	attempt := objectAt(t, claim, "attempt")
	attemptID := stringAt(t, attempt, "attempt_id")
	contextPack := objectAt(t, claim, "context_pack")
	received := objectAt(t, performInternalJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/internal/v1/executor/attempts/"+attemptID+"/results",
		map[string]any{
			"input_snapshot_hash": stringAt(t, attempt, "input_snapshot_hash"),
			"response_payload":    response,
			"usage":               httpSDKContextUsage(t, contextPack),
			"trace_ref":           traceRef,
		},
		map[string]string{"X-Attempt-Token": stringAt(t, claim, "attempt_token")},
		http.StatusAccepted,
	), "data")
	return objectAt(t, performInternalJSON(
		t,
		handler,
		http.MethodPost,
		"/internal/v1/executor/attempts/"+attemptID+"/commit",
		map[string]any{"expected_response_hash": stringAt(t, received, "response_hash")},
		http.StatusCreated,
	), "data")
}

func httpNonNovelScriptResponse(episodeNo int) map[string]any {
	return map[string]any{
		"script_unit": map[string]any{
			"episode_no": episodeNo,
			"title":      fmt.Sprintf("第%d集", episodeNo),
			"script_text": fmt.Sprintf(
				"%d-1\n地点：旧物店 | 内 | 日\n△许知夏检查新出现的失物。",
				episodeNo,
			),
			"scenes": []any{map[string]any{
				"scene_id":          fmt.Sprintf("%d-1", episodeNo),
				"heading":           "旧物店 内 日",
				"location":          "旧物店",
				"interior_exterior": "内",
				"time_of_day":       "日",
				"characters":        []string{"许知夏"},
				"blocks": []any{map[string]any{
					"line_id":     fmt.Sprintf("E%03d-L001", episodeNo),
					"block_type":  "action",
					"text":        "许知夏检查新出现的失物。",
					"source_refs": []any{},
					"uncertainty": "none",
				}},
				"source_refs": []any{},
			}},
			"source_refs": []any{},
		},
		"script_handoff": httpNonNovelScriptHandoff(episodeNo),
	}
}

func httpNonNovelScriptHandoff(episodeNo int) map[string]any {
	return map[string]any{
		"episode_no":                 episodeNo,
		"script_artifact_version_id": "",
		"source_refs":                []any{},
		"continuity_delta": map[string]any{
			"new_facts":               []string{},
			"character_state_changes": []string{},
			"relationship_changes":    []string{},
			"hooks_opened":            []string{},
			"hooks_resolved":          []string{},
		},
		"runtime_check":                     map[string]any{},
		"critical_presentation_constraints": []string{},
		"review_focus":                      []string{},
		"next_episode_must_address":         nil,
		"self_check": map[string]any{
			"format_risks":          []string{},
			"continuity_risks":      []string{},
			"source_fidelity_risks": []string{},
		},
	}
}

func httpNonNovelClaimAndCommit(
	t *testing.T,
	handler http.Handler,
	wantStep string,
	responseBuilder func(*testing.T, map[string]any) map[string]any,
	traceRef string,
) map[string]any {
	t.Helper()
	executorID := "worker.structured_content"
	if wantStep == "build_episode_cards" {
		executorID = "workflow.episode_cards"
	}
	claim := objectAt(t, performInternalJSON(
		t,
		handler,
		http.MethodPost,
		"/internal/v1/executor/task-claims",
		map[string]any{
			"worker_id":     "http_non_novel_worker",
			"executor_ids":  []string{executorID},
			"provider_id":   "http_provider",
			"model_id":      "http_model",
			"lease_seconds": 60,
		},
		http.StatusOK,
	), "data")
	contextPack := objectAt(t, claim, "context_pack")
	if got := stringAt(t, objectAt(t, contextPack, "step"), "step_id"); got != wantStep {
		t.Fatalf("claimed step = %q, want %q", got, wantStep)
	}
	attempt := objectAt(t, claim, "attempt")
	attemptID := stringAt(t, attempt, "attempt_id")
	received := objectAt(t, performInternalJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/internal/v1/executor/attempts/"+attemptID+"/results",
		map[string]any{
			"input_snapshot_hash": stringAt(t, attempt, "input_snapshot_hash"),
			"response_payload":    responseBuilder(t, contextPack),
			"usage":               httpSDKContextUsage(t, contextPack),
			"trace_ref":           traceRef,
		},
		map[string]string{"X-Attempt-Token": stringAt(t, claim, "attempt_token")},
		http.StatusAccepted,
	), "data")
	return objectAt(t, performInternalJSON(
		t,
		handler,
		http.MethodPost,
		"/internal/v1/executor/attempts/"+attemptID+"/commit",
		map[string]any{"expected_response_hash": stringAt(t, received, "response_hash")},
		http.StatusCreated,
	), "data")
}

func httpSDKContextUsage(t *testing.T, contextPack map[string]any) map[string]any {
	t.Helper()
	versionIDs := []string{}
	for _, raw := range arrayAt(t, contextPack, "upstream_context") {
		item, ok := raw.(map[string]any)
		if !ok || stringAt(t, item, "selection_policy") != "sdk_context_candidate" {
			continue
		}
		versionIDs = append(versionIDs, stringAt(t, item, "artifact_version_id"))
	}
	return map[string]any{
		"input_tokens":                 100,
		"output_tokens":                100,
		"context_artifact_version_ids": versionIDs,
	}
}

func httpNonNovelApproveCurrent(
	t *testing.T,
	handler http.Handler,
	snapshot map[string]any,
	keySuffix string,
) map[string]any {
	t.Helper()
	approval := objectAt(t, snapshot, "current_approval")
	return objectAt(t, performJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/approvals/"+stringAt(t, approval, "approval_request_id")+"/resolutions",
		map[string]any{
			"action":                    "approve",
			"expected_approval_version": int(numberAt(t, approval, "version")),
			"subject_snapshot_hash":     stringAt(t, approval, "subject_snapshot_hash"),
		},
		httpNonNovelKey(keySuffix),
		http.StatusOK,
	), "data")
}

func httpNonNovelResume(
	t *testing.T,
	handler http.Handler,
	runID string,
	keySuffix string,
	wantStep string,
) map[string]any {
	t.Helper()
	snapshot := objectAt(t, performJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/runs/"+runID+"/resume",
		nil,
		httpNonNovelKey(keySuffix),
		http.StatusOK,
	), "data")
	if got := stringAt(t, objectAt(t, snapshot, "run"), "status"); got != "running" {
		t.Fatalf("resumed run status = %q, want running", got)
	}
	httpNonNovelAssertCurrentStep(t, snapshot, wantStep)
	return snapshot
}

func httpNonNovelAssertCurrentStep(t *testing.T, snapshot map[string]any, want string) {
	t.Helper()
	run := objectAt(t, snapshot, "run")
	currentStepRunID := stringAt(t, run, "current_step_run_id")
	for _, item := range arrayAt(t, snapshot, "steps") {
		step := item.(map[string]any)
		if stringAt(t, step, "step_run_id") == currentStepRunID {
			if got := stringAt(t, step, "step_id"); got != want {
				t.Fatalf("current step = %q, want %q", got, want)
			}
			return
		}
	}
	t.Fatalf("current step run %q not found", currentStepRunID)
}

func httpNonNovelAssertReplacement(
	t *testing.T,
	commit map[string]any,
	wantArtifactID string,
	wantBaseVersionID string,
) {
	t.Helper()
	artifact := objectAt(t, commit, "artifact")
	version := objectAt(t, commit, "artifact_version")
	if stringAt(t, artifact, "artifact_id") != wantArtifactID ||
		int(numberAt(t, version, "version")) != 2 ||
		stringAt(t, version, "creation_reason") != "regeneration" ||
		stringAt(t, version, "base_version_id") != wantBaseVersionID {
		t.Fatalf("replacement artifact = %+v, version = %+v", artifact, version)
	}
}

func httpNonNovelMaterialBankResponse(t *testing.T, contextPack map[string]any) map[string]any {
	t.Helper()
	return map[string]any{"material_bank": map[string]any{
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
			"material_sufficiency": "sufficient",
			"can_support_target":   true,
			"risks":                []string{},
			"questions":            []string{},
		},
		"source_trace": httpNonNovelTraceFromManifest(t, contextPack),
	}}
}

func httpNonNovelStorySeedResponse(t *testing.T, contextPack map[string]any) map[string]any {
	t.Helper()
	return map[string]any{"story_seed": map[string]any{
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
		"source_trace":      httpNonNovelTraceFromUpstream(t, contextPack),
	}}
}

func httpNonNovelSeriesBlueprintResponse(
	t *testing.T,
	contextPack map[string]any,
) map[string]any {
	t.Helper()
	return map[string]any{"series_blueprint": map[string]any{
		"resolved_episode_count":    3,
		"recommended_episode_count": 3,
		"episode_count_reason":      "按已确认体量推进主线。",
		"series_promise":            "逐件失物揭开旧案。",
		"phase_plan": []any{map[string]any{
			"phase_id":       "P1",
			"episode_range":  "1-3",
			"phase_function": "完成调查与公开真相",
			"main_conflict":  "调查者与地产商争夺证据",
			"payoff_focus":   "旧案真相",
			"hook_strategy":  "每集新增一件关键失物",
		}},
		"payoff_distribution":      []any{},
		"hook_distribution":        []any{},
		"first_major_climax_plan":  map[string]any{"episode": 3},
		"pacing_density_plan":      []any{},
		"character_progression":    []any{},
		"relationship_progression": []any{},
		"continuity_rules":         []string{"周既明不是反派"},
		"generated_additions":      []any{},
		"fit_risks":                []string{},
		"source_trace":             httpNonNovelTraceFromUpstream(t, contextPack),
	}}
}

func httpNonNovelEpisodeCardsResponse(
	t *testing.T,
	contextPack map[string]any,
) map[string]any {
	t.Helper()
	cursor := objectAt(t, objectAt(t, contextPack, "task_cursor"), "batch")
	start := int(numberAt(t, cursor, "episode_start"))
	end := int(numberAt(t, cursor, "episode_end"))
	episodes := make([]map[string]any, 0, end-start+1)
	for episodeNo := start; episodeNo <= end; episodeNo++ {
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
	return map[string]any{"episode_cards": map[string]any{
		"episodes": episodes,
		"continuity_delta": map[string]any{
			"new_facts":               []string{},
			"character_state_changes": []string{},
			"relationship_changes":    []string{},
			"hooks_opened":            []string{},
			"hooks_resolved":          []string{},
		},
	}}
}

func httpNonNovelTraceFromManifest(t *testing.T, contextPack map[string]any) map[string]any {
	t.Helper()
	manifest := httpNonNovelUpstreamContent(t, contextPack, "source_manifest")
	unit := arrayAt(t, manifest, "units")[0].(map[string]any)
	reference := map[string]any{
		"source_type":       "asset_text_range",
		"asset_id":          stringAt(t, unit, "asset_id"),
		"asset_snapshot_id": stringAt(t, unit, "asset_snapshot_id"),
		"source_unit_id":    stringAt(t, unit, "source_unit_id"),
		"range_label":       stringAt(t, unit, "source_unit_id"),
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

func httpNonNovelTraceFromUpstream(t *testing.T, contextPack map[string]any) map[string]any {
	t.Helper()
	for _, artifactType := range []string{"material_bank", "story_seed"} {
		content, ok := httpNonNovelFindUpstreamContent(contextPack, artifactType)
		if !ok {
			continue
		}
		return objectAt(t, content, "source_trace")
	}
	t.Fatal("content claims are missing from upstream context")
	return nil
}

func httpNonNovelUpstreamContent(
	t *testing.T,
	contextPack map[string]any,
	artifactType string,
) map[string]any {
	t.Helper()
	content, ok := httpNonNovelFindUpstreamContent(contextPack, artifactType)
	if !ok {
		t.Fatalf("%s is missing from upstream context", artifactType)
	}
	return content
}

func httpNonNovelFindUpstreamContent(
	contextPack map[string]any,
	artifactType string,
) (map[string]any, bool) {
	upstream, ok := contextPack["upstream_context"].([]any)
	if !ok {
		return nil, false
	}
	for _, item := range upstream {
		entry, ok := item.(map[string]any)
		if !ok || entry["artifact_type"] != artifactType {
			continue
		}
		if content, ok := entry["content"].(map[string]any); ok {
			return content, true
		}
		if encoded, ok := entry["content"].(string); ok {
			var content map[string]any
			if json.Unmarshal([]byte(encoded), &content) == nil {
				return content, true
			}
		}
	}
	return nil, false
}

func httpNonNovelKey(suffix string) map[string]string {
	return map[string]string{
		"Idempotency-Key": "77777777-7777-4777-8777-7777777777" + suffix,
	}
}
