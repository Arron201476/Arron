package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"novel2script-agent/backend/internal/agent"
)

func TestUpdateArtifactIncrementsVersionAndEmitsEvent(t *testing.T) {
	runtime := NewRuntime(nil)
	run := agent.Run{
		RunID:      "run_test",
		ProjectID:  "project_test",
		SourceMode: agent.SourceModeNonNovel,
		Status:     agent.RunCompleted,
	}
	artifact := runtime.artifact(run, "material_bank", agent.ArtifactConfirmed, nil, map[string]any{
		"before": "value",
	})
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{artifact}

	updated, events, artifacts, ok := runtime.UpdateArtifact(artifact.ArtifactID, map[string]any{
		"after": "value",
	})
	if !ok {
		t.Fatal("expected artifact update to succeed")
	}
	if updated.Version != artifact.Version+1 {
		t.Fatalf("expected version %d, got %d", artifact.Version+1, updated.Version)
	}
	if updated.Payload["after"] != "value" {
		t.Fatalf("expected updated payload, got %#v", updated.Payload)
	}
	if len(events) != 1 || events[0].Type != agent.EventArtifactUpdated {
		t.Fatalf("expected one artifact_updated event, got %#v", events)
	}
	if len(artifacts) != 2 || artifacts[0].Status != agent.ArtifactSuperseded || artifacts[1].ArtifactID != updated.ArtifactID {
		t.Fatalf("expected old and new artifact versions, got %#v", artifacts)
	}
	if len(runtime.runs[run.RunID].UpdatedArtifacts) != 1 {
		t.Fatalf("expected run updated artifact index to be recorded")
	}
}

func TestRuntimeShutdownCancelsAndWaitsForJobs(t *testing.T) {
	runtime := NewRuntime(nil)
	started := make(chan struct{})
	runtime.mu.Lock()
	runtime.launchJobLocked("run_shutdown", func(ctx context.Context) {
		close(started)
		<-ctx.Done()
	})
	runtime.mu.Unlock()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteProjectRemovesOnlyOwnedRuntimeStateAndRejectsActiveRun(t *testing.T) {
	runtime := NewRuntime(nil)
	completed := agent.Run{RunID: "run_delete_completed", ProjectID: "project_delete", Status: agent.RunCompleted, StartedAt: time.Now().UTC()}
	other := agent.Run{RunID: "run_keep", ProjectID: "project_keep", Status: agent.RunCompleted, StartedAt: time.Now().UTC()}
	runtime.runs[completed.RunID] = completed
	runtime.runs[other.RunID] = other
	runtime.events[completed.RunID] = []agent.RunEvent{{EventID: "event_delete", RunID: completed.RunID}}
	runtime.artifacts[completed.RunID] = []agent.Artifact{{ArtifactID: "artifact_delete", RunID: completed.RunID, ProjectID: completed.ProjectID}}
	runtime.approvals["approval_delete"] = agent.ApprovalRequest{ApprovalRequestID: "approval_delete", RunID: completed.RunID}

	if err := runtime.DeleteProject(completed.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, ok := runtime.GetRun(completed.RunID); ok || len(runtime.Events(completed.RunID)) != 0 || len(runtime.Artifacts(completed.RunID)) != 0 {
		t.Fatal("deleted project runtime records remain")
	}
	if _, ok := runtime.Approval("approval_delete"); ok {
		t.Fatal("deleted project approval remains")
	}
	if _, ok := runtime.GetRun(other.RunID); !ok {
		t.Fatal("unrelated project runtime state was removed")
	}

	active := agent.Run{RunID: "run_delete_active", ProjectID: "project_active", Status: agent.RunRunning, StartedAt: time.Now().UTC()}
	runtime.runs[active.RunID] = active
	if err := runtime.DeleteProject(active.ProjectID); !errors.Is(err, ErrProjectBusy) {
		t.Fatalf("expected ErrProjectBusy, got %v", err)
	}
	if _, ok := runtime.GetRun(active.RunID); !ok {
		t.Fatal("busy project was partially deleted")
	}
}

func TestStartRunRecordsSelectedRuntime(t *testing.T) {
	runtime := NewRuntimeWithStateAndName(&lengthCorrectionWorker{}, "", "eino")
	response, err := runtime.StartRunAsyncContext(context.Background(), agent.StartRunRequest{
		ProjectID: "project_runtime_name", UserMessage: "素材", SourceMode: agent.SourceModeNonNovel,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Run.Metadata["runtime"] != "eino" {
		t.Fatalf("expected Eino runtime metadata, got %#v", response.Run.Metadata["runtime"])
	}
}

func TestGeneratedScriptLengthUsesConfiguredTolerance(t *testing.T) {
	run := agent.Run{Metadata: map[string]any{
		"generation_config": map[string]any{"target_script_chars": 300},
	}}
	for _, testCase := range []struct {
		name    string
		length  int
		wantErr bool
	}{
		{name: "minimum", length: 180},
		{name: "maximum", length: 450},
		{name: "too short", length: 179, wantErr: true},
		{name: "too long", length: 451, wantErr: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateGeneratedScriptLength(run, map[string]any{"script_text": strings.Repeat("字", testCase.length)})
			if (err != nil) != testCase.wantErr {
				t.Fatalf("validateGeneratedScriptLength() error = %v, wantErr %v", err, testCase.wantErr)
			}
		})
	}
}

func TestCorrectedScriptLengthAllowsSmallFormattingMargin(t *testing.T) {
	run := agent.Run{Metadata: map[string]any{
		"generation_config": map[string]any{"target_script_chars": 300},
	}}
	for _, testCase := range []struct {
		length  int
		wantErr bool
	}{
		{length: 165},
		{length: 495},
		{length: 164, wantErr: true},
		{length: 496, wantErr: true},
	} {
		err := validateCorrectedScriptLength(run, map[string]any{"script_text": strings.Repeat("字", testCase.length)})
		if (err != nil) != testCase.wantErr {
			t.Fatalf("length %d: error=%v wantErr=%v", testCase.length, err, testCase.wantErr)
		}
	}
}

type lengthCorrectionWorker struct {
	calls int
	notes []string
}

func (w *lengthCorrectionWorker) Plan(context.Context, agent.Run, agent.Artifact) ([]PlannedArtifact, *agent.ApprovalRequest, error) {
	return nil, nil, nil
}

func (w *lengthCorrectionWorker) WriteScripts(context.Context, agent.Run, []agent.Artifact, string) ([]PlannedArtifact, error) {
	return nil, nil
}

func (w *lengthCorrectionWorker) PlanStep(context.Context, agent.Run, agent.Artifact, []agent.Artifact, string) (PlannedArtifact, *agent.ApprovalRequest, error) {
	return PlannedArtifact{}, nil, nil
}

func (w *lengthCorrectionWorker) WriteScriptStep(_ context.Context, _ agent.Run, _ []agent.Artifact, _ string, note string) (PlannedArtifact, error) {
	w.calls++
	w.notes = append(w.notes, note)
	length := 500
	if w.calls > 1 {
		length = 300
	}
	return PlannedArtifact{
		ArtifactType: "script_unit",
		Status:       agent.ArtifactPendingApproval,
		Payload: map[string]any{
			"script_text": strings.Repeat("字", length),
			"scenes":      []any{},
		},
	}, nil
}

func TestScriptGenerationCorrectsOutOfRangeLengthBeforeSaving(t *testing.T) {
	worker := &lengthCorrectionWorker{}
	runtime := NewRuntime(worker)
	run := agent.Run{
		RunID: "run_length_correction", ProjectID: "project_length_correction", SourceMode: agent.SourceModeNovel,
		Status: agent.RunRunning, CurrentStepID: "step_generate_script_unit", StartedAt: time.Now().UTC(),
		Metadata: map[string]any{"generation_config": map[string]any{"target_script_chars": 300}},
	}
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{runtime.artifact(run, "script_context", agent.ArtifactConfirmed, nil, map[string]any{})}

	runtime.generateScriptUnitsRangeAsync(context.Background(), run.RunID, "", 0, 0)

	if worker.calls != 2 || len(worker.notes) != 2 || !strings.Contains(worker.notes[1], "CORRECTION") {
		t.Fatalf("expected one constrained rewrite, calls=%d notes=%#v", worker.calls, worker.notes)
	}
	units := activeArtifactsOfType(runtime.Artifacts(run.RunID), "script_unit")
	if len(units) != 1 || len([]rune(units[0].Payload["script_text"].(string))) != 300 {
		t.Fatalf("expected only the corrected script unit to be saved, got %#v", units)
	}
	updatedRun, _ := runtime.GetRun(run.RunID)
	if updatedRun.Status != agent.RunWaitingApproval {
		t.Fatalf("expected corrected script to reach approval, got %s", updatedRun.Status)
	}
}

type stagedLengthCorrectionWorker struct {
	calls int
	notes []string
}

func (w *stagedLengthCorrectionWorker) PlanStep(context.Context, agent.Run, agent.Artifact, []agent.Artifact, string) (PlannedArtifact, *agent.ApprovalRequest, error) {
	return PlannedArtifact{}, nil, nil
}

func (w *stagedLengthCorrectionWorker) WriteScriptStep(_ context.Context, _ agent.Run, _ []agent.Artifact, _ string, note string) (PlannedArtifact, error) {
	w.calls++
	w.notes = append(w.notes, note)
	length := 500
	if w.calls >= 3 {
		length = 300
	}
	return PlannedArtifact{ArtifactType: "script_unit", Status: agent.ArtifactPendingApproval, Payload: map[string]any{
		"script_text": strings.Repeat("字", length), "scenes": []any{},
	}}, nil
}

func TestScriptGenerationUsesProgressiveLengthCorrection(t *testing.T) {
	worker := &stagedLengthCorrectionWorker{}
	runtime := NewRuntime(worker)
	run := agent.Run{
		RunID: "run_progressive_correction", ProjectID: "project_progressive_correction", SourceMode: agent.SourceModeNonNovel,
		Status: agent.RunRunning, CurrentStepID: "step_generate_script_unit", StartedAt: time.Now().UTC(),
		Metadata: map[string]any{"generation_config": map[string]any{"target_script_chars": 300}},
	}
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{runtime.artifact(run, "script_context", agent.ArtifactConfirmed, nil, map[string]any{})}

	runtime.generateScriptUnitsRangeAsync(context.Background(), run.RunID, "", 0, 0)

	if worker.calls != 3 || len(worker.notes) != 3 {
		t.Fatalf("expected initial draft plus two corrections, calls=%d notes=%#v", worker.calls, worker.notes)
	}
	if !strings.Contains(worker.notes[1], "ATTEMPT 1") || !strings.Contains(worker.notes[2], "ATTEMPT 2") || !strings.Contains(worker.notes[2], "current payload.script_text is 500") {
		t.Fatalf("progressive correction did not carry actual length: %#v", worker.notes)
	}
	updatedRun, _ := runtime.GetRun(run.RunID)
	if updatedRun.Status != agent.RunWaitingApproval {
		t.Fatalf("expected second correction to reach approval, got %s", updatedRun.Status)
	}
}

func TestUpdateArtifactCreatesVersionAndDefersDownstreamDecisionForEveryPlanningArtifact(t *testing.T) {
	cases := []struct {
		name       string
		mode       agent.SourceMode
		targetType string
	}{
		{"novel source", agent.SourceModeNovel, "source_input"},
		{"story bible", agent.SourceModeNovel, "story_bible"},
		{"episode split", agent.SourceModeNovel, "episode_split"},
		{"novel episode cards", agent.SourceModeNovel, "episode_cards"},
		{"non novel source", agent.SourceModeNonNovel, "source_input"},
		{"material bank", agent.SourceModeNonNovel, "material_bank"},
		{"story seed", agent.SourceModeNonNovel, "story_seed"},
		{"series blueprint", agent.SourceModeNonNovel, "series_blueprint"},
		{"non novel episode cards", agent.SourceModeNonNovel, "episode_cards"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			runtime := NewRuntime(nil)
			run := agent.Run{RunID: "run_" + strings.ReplaceAll(testCase.name, " ", "_"), ProjectID: "project_matrix", SourceMode: testCase.mode, Status: agent.RunCompleted}
			target := runtime.artifact(run, testCase.targetType, agent.ArtifactConfirmed, nil, map[string]any{"value": "old"})
			artifacts := []agent.Artifact{target}
			for _, downstreamType := range affectedArtifactsAfter(testCase.mode, testCase.targetType) {
				payload := map[string]any{"value": "downstream"}
				if downstreamType == "script_unit" {
					payload["episode_id"] = 1
				}
				artifacts = append(artifacts, runtime.artifact(run, downstreamType, agent.ArtifactConfirmed, nil, payload))
			}
			runtime.runs[run.RunID] = run
			runtime.artifacts[run.RunID] = artifacts

			updated, _, result, ok := runtime.UpdateArtifact(target.ArtifactID, map[string]any{"value": "new"})
			if !ok || updated.Version != 2 || updated.Payload["value"] != "new" {
				t.Fatalf("expected a v2 replacement, got %#v ok=%v", updated, ok)
			}
			if result[0].Status != agent.ArtifactSuperseded {
				t.Fatalf("expected old %s to be superseded, got %s", testCase.targetType, result[0].Status)
			}
			for _, artifact := range result[1 : len(result)-1] {
				if artifact.Status != agent.ArtifactConfirmed {
					t.Fatalf("expected downstream %s to remain visible before user choice, got %s", artifact.ArtifactType, artifact.Status)
				}
			}
			approval, exists := runtime.CurrentApproval(run.RunID)
			if !exists || !containsString(approval.Options, "keep_downstream") || !containsString(approval.Options, "regenerate_downstream") {
				t.Fatalf("expected downstream review approval, got %#v exists=%v", approval, exists)
			}
		})
	}
}

func TestUpdateArtifactMissingID(t *testing.T) {
	runtime := NewRuntime(nil)
	if _, _, _, ok := runtime.UpdateArtifact("missing", map[string]any{"x": "y"}); ok {
		t.Fatal("expected missing artifact update to fail")
	}
}

func TestUpdateArtifactAtVersionRejectsStaleWriteWithoutChangingContent(t *testing.T) {
	runtime := NewRuntime(nil)
	run := agent.Run{RunID: "run_version_conflict", ProjectID: "project_version_conflict", SourceMode: agent.SourceModeNonNovel, Status: agent.RunCompleted}
	original := runtime.artifact(run, "material_bank", agent.ArtifactConfirmed, nil, map[string]any{"value": "original"})
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{original}

	updated, _, _, _, outcome := runtime.UpdateArtifactAtVersion(original.ArtifactID, original.Version, map[string]any{"value": "first save"})
	if outcome != ArtifactUpdateApplied {
		t.Fatalf("expected first save to apply, got %s", outcome)
	}

	_, events, artifacts, current, outcome := runtime.UpdateArtifactAtVersion(original.ArtifactID, original.Version, map[string]any{"value": "stale overwrite"})
	if outcome != ArtifactUpdateConflict {
		t.Fatalf("expected stale save conflict, got %s", outcome)
	}
	if len(events) != 0 {
		t.Fatalf("conflict must not emit events, got %#v", events)
	}
	if current.ArtifactID != updated.ArtifactID || current.Payload["value"] != "first save" {
		t.Fatalf("expected current artifact to remain first save, got %#v", current)
	}
	if len(artifacts) != 2 {
		t.Fatalf("conflict must not create another version, got %d artifacts", len(artifacts))
	}
}

func TestUpdateArtifactPreservesPendingApprovalStatus(t *testing.T) {
	runtime := NewRuntime(nil)
	run := agent.Run{RunID: "run_pending_save", ProjectID: "project_pending_save", SourceMode: agent.SourceModeNovel, Status: agent.RunWaitingApproval}
	artifact := runtime.artifact(run, "story_bible", agent.ArtifactPendingApproval, nil, map[string]any{"value": "old"})
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{artifact}
	updated, _, _, ok := runtime.UpdateArtifact(artifact.ArtifactID, map[string]any{"value": "new"})
	if !ok || updated.Status != agent.ArtifactPendingApproval {
		t.Fatalf("expected saved v2 to remain pending approval, got %#v ok=%v", updated, ok)
	}
}

func TestRegeneratedArtifactIncrementsVersionAndSupersedesPrevious(t *testing.T) {
	runtime := NewRuntime(nil)
	run := agent.Run{
		RunID:      "run_regenerate_version",
		ProjectID:  "project_regenerate_version",
		SourceMode: agent.SourceModeNonNovel,
		Status:     agent.RunRunning,
	}
	previous := runtime.artifact(run, "material_bank", agent.ArtifactConfirmed, nil, map[string]any{"value": "old"})
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{previous}

	runtime.appendPlannedArtifact(run.RunID, run, PlannedArtifact{
		ArtifactType: "material_bank",
		Status:       agent.ArtifactPendingApproval,
		Payload:      map[string]any{"value": "new"},
	}, nil)

	artifacts := runtime.Artifacts(run.RunID)
	if len(artifacts) != 2 {
		t.Fatalf("expected two artifact versions, got %#v", artifacts)
	}
	if artifacts[0].Status != agent.ArtifactSuperseded {
		t.Fatalf("expected old artifact to be superseded, got %s", artifacts[0].Status)
	}
	if artifacts[1].Version != previous.Version+1 {
		t.Fatalf("expected regenerated artifact version %d, got %d", previous.Version+1, artifacts[1].Version)
	}
}

func TestRerunRejectsNonFailedRunAndDifferentStep(t *testing.T) {
	worker := &retryEpisodeWorker{}
	runtime := NewRuntime(worker)
	running := agent.Run{
		RunID:         "run_not_failed",
		ProjectID:     "project_rerun_guard",
		SourceMode:    agent.SourceModeNonNovel,
		Status:        agent.RunRunning,
		CurrentStepID: "step_generate_script_unit",
	}
	runtime.runs[running.RunID] = running
	if _, err := runtime.RerunStep(running.RunID, running.CurrentStepID, agent.RerunStepRequest{}); err == nil {
		t.Fatal("expected a non-failed run to be rejected")
	}

	failed := running
	failed.RunID = "run_failed_step_guard"
	failed.Status = agent.RunFailed
	failed.NextAction = map[string]any{"step_id": "step_generate_script_unit"}
	runtime.runs[failed.RunID] = failed
	if _, err := runtime.RerunStep(failed.RunID, "step_plan_episode_cards", agent.RerunStepRequest{}); err == nil {
		t.Fatal("expected a different step to be rejected")
	}
}

func TestScriptContextIsInternalAndDoesNotRequireApproval(t *testing.T) {
	if artifactRequiresApproval("script_context") {
		t.Fatal("expected script_context to continue without user approval")
	}
	if !artifactRequiresApproval("script_unit") {
		t.Fatal("expected script_unit to remain user-visible for approval")
	}
}

func TestScriptSpanPatchChangesOnlyTargetAndSynchronizesScriptText(t *testing.T) {
	base := map[string]any{
		"episode_id":  3,
		"title":       "keep title",
		"script_text": "第3集\n旧动作\n旧台词",
		"scenes": []any{
			map[string]any{
				"scene_id": "scene_3_1",
				"heading":  "keep heading",
				"blocks": []any{
					map[string]any{"block_type": "action", "text": "旧动作"},
					map[string]any{"block_type": "dialogue", "speaker": "主角", "text": "旧台词"},
				},
			},
		},
	}
	pack := revisionContextPack{RevisionIntent: "patch_script_span"}
	pack.Target.SceneID = "scene_3_1"
	pack.Target.NodeID = "scene_3_1-line-2"
	pack.FocusedContext.SelectedText = "旧台词"
	planned := PlannedArtifact{ArtifactType: "script_unit", Payload: map[string]any{
		"__script_span_patch": map[string]any{
			"scene_id": "scene_3_1", "node_id": "scene_3_1-line-2", "old_text": "旧台词", "new_text": "新台词",
		},
	}}
	pack.TargetArtifact = &struct {
		Payload map[string]any `json:"payload,omitempty"`
	}{Payload: base}

	patched := applyRevisionPatch(planned, pack)
	if patched.Payload["title"] != "keep title" {
		t.Fatalf("expected untargeted title to stay unchanged, got %#v", patched.Payload["title"])
	}
	scenes := patched.Payload["scenes"].([]any)
	blocks := scenes[0].(map[string]any)["blocks"].([]any)
	if blocks[0].(map[string]any)["text"] != "旧动作" || blocks[1].(map[string]any)["text"] != "新台词" {
		t.Fatalf("expected only selected block to change, got %#v", blocks)
	}
	if patched.Payload["script_text"] != "第3集\n旧动作\n新台词" {
		t.Fatalf("expected script_text to stay synchronized, got %#v", patched.Payload["script_text"])
	}
}

func TestScriptSpanPatchReplacesOnlyPartialLineSelection(t *testing.T) {
	base := map[string]any{
		"script_text": "旧台词",
		"scenes": []any{map[string]any{"scene_id": "scene_1", "blocks": []any{
			map[string]any{"line_id": "line_1", "block_type": "dialogue", "speaker": "主角", "text": "旧台词"},
		}}},
	}
	pack := revisionContextPack{RevisionIntent: "patch_script_span"}
	pack.FocusedContext.SelectedText = "台"
	pack.TargetArtifact = &struct {
		Payload map[string]any `json:"payload,omitempty"`
	}{Payload: base}
	planned := PlannedArtifact{ArtifactType: "script_unit", Payload: map[string]any{"__script_span_patch": map[string]any{
		"node_id": "line_1", "old_text": "台", "new_text": "狠", "selection_start": 1, "selection_end": 2,
	}}}
	patched := applyRevisionPatch(planned, pack)
	block := patched.Payload["scenes"].([]any)[0].(map[string]any)["blocks"].([]any)[0].(map[string]any)
	if block["text"] != "旧狠词" || patched.Payload["script_text"] != "旧狠词" {
		t.Fatalf("partial line patch changed the wrong range: %#v", patched.Payload)
	}
}

func TestScriptSpanPatchAcceptsRenderedSpeakerPrefix(t *testing.T) {
	base := map[string]any{
		"script_text": "Chen: (playing dumb) Who are you?",
		"scenes": []any{map[string]any{"scene_id": "scene_1", "blocks": []any{
			map[string]any{"line_id": "line_1", "block_type": "dialogue", "speaker": "Chen", "text": "(playing dumb) Who are you?"},
		}}},
	}
	pack := revisionContextPack{RevisionIntent: "patch_script_span"}
	pack.Target.NodeID = "line_1"
	pack.FocusedContext.SelectedText = "Chen: (playing dumb) Who are you?"
	pack.TargetArtifact = &struct {
		Payload map[string]any `json:"payload,omitempty"`
	}{Payload: base}
	planned := PlannedArtifact{ArtifactType: "script_unit", Payload: map[string]any{"__script_span_patch": map[string]any{
		"node_id": "line_1", "old_text": "Chen: (playing dumb) Who are you?", "new_text": "Chen: (cold provocation) You sure you know me?",
		"selection_start": 0, "selection_end": 34,
	}}}
	patched := applyRevisionPatch(planned, pack)
	block := patched.Payload["scenes"].([]any)[0].(map[string]any)["blocks"].([]any)[0].(map[string]any)
	if block["speaker"] != "Chen" || block["text"] != "(cold provocation) You sure you know me?" {
		t.Fatalf("speaker-prefixed selection was not normalized: %#v", block)
	}
	if patched.Payload["script_text"] != "Chen: (cold provocation) You sure you know me?" {
		t.Fatalf("script text was not synchronized: %#v", patched.Payload["script_text"])
	}
}

func TestScriptSpanPatchUsesBrowserUTF16Offsets(t *testing.T) {
	base := map[string]any{
		"script_text": "甲😀乙丙",
		"scenes": []any{map[string]any{"scene_id": "scene_1", "blocks": []any{
			map[string]any{"line_id": "line_1", "block_type": "action", "text": "甲😀乙丙"},
		}}},
	}
	pack := revisionContextPack{RevisionIntent: "patch_script_span"}
	pack.FocusedContext.SelectedText = "乙"
	pack.TargetArtifact = &struct {
		Payload map[string]any `json:"payload,omitempty"`
	}{Payload: base}
	planned := PlannedArtifact{ArtifactType: "script_unit", Payload: map[string]any{"__script_span_patch": map[string]any{
		"node_id": "line_1", "old_text": "乙", "new_text": "新", "selection_start": 3, "selection_end": 4,
	}}}

	patched := applyRevisionPatch(planned, pack)
	block := patched.Payload["scenes"].([]any)[0].(map[string]any)["blocks"].([]any)[0].(map[string]any)
	if block["text"] != "甲😀新丙" || patched.Payload["script_text"] != "甲😀新丙" {
		t.Fatalf("UTF-16 offsets changed the wrong text: %#v", patched.Payload)
	}
}

func TestScriptSpanPatchPreservesLineStructureAcrossScenes(t *testing.T) {
	base := map[string]any{
		"script_text": "甲乙丙\n丁戊己\n庚辛壬",
		"scenes": []any{
			map[string]any{"scene_id": "scene_1", "blocks": []any{
				map[string]any{"line_id": "line_1", "block_type": "action", "text": "甲乙丙"},
				map[string]any{"line_id": "line_2", "block_type": "dialogue", "speaker": "主角", "text": "丁戊己"},
			}},
			map[string]any{"scene_id": "scene_2", "blocks": []any{
				map[string]any{"line_id": "line_3", "block_type": "action", "text": "庚辛壬"},
			}},
		},
	}
	pack := revisionContextPack{RevisionIntent: "patch_script_span"}
	pack.FocusedContext.SelectedText = "乙丙\n丁戊己\n庚辛"
	pack.FocusedContext.LineIDs = []string{"line_1", "line_2", "line_3"}
	pack.TargetArtifact = &struct {
		Payload map[string]any `json:"payload,omitempty"`
	}{Payload: base}
	planned := PlannedArtifact{ArtifactType: "script_unit", Payload: map[string]any{"__script_span_patch": map[string]any{
		"line_ids": []string{"line_1", "line_2", "line_3"}, "selection_start": 1, "selection_end": 2,
		"old_text": "乙丙\n丁戊己\n庚辛", "replacement_lines": []any{
			map[string]any{"line_id": "line_1", "new_text": "新一"},
			map[string]any{"line_id": "line_2", "new_text": "新二"},
			map[string]any{"line_id": "line_3", "new_text": "新三"},
		},
	}}}
	patched := applyRevisionPatch(planned, pack)
	scenes := patched.Payload["scenes"].([]any)
	firstBlocks := scenes[0].(map[string]any)["blocks"].([]any)
	lastBlock := scenes[1].(map[string]any)["blocks"].([]any)[0].(map[string]any)
	if firstBlocks[0].(map[string]any)["text"] != "甲新一" || firstBlocks[1].(map[string]any)["text"] != "新二" || lastBlock["text"] != "新三壬" {
		t.Fatalf("multi-line patch did not preserve line structure: %#v", patched.Payload)
	}
	if patched.Payload["script_text"] != "甲新一\n新二\n新三壬" {
		t.Fatalf("multi-line patch did not synchronize script_text: %#v", patched.Payload["script_text"])
	}
}

func TestScriptScenePatchRebuildsScriptTextWhenLineCountChanges(t *testing.T) {
	base := map[string]any{
		"title":       "第1集",
		"script_text": "第1集\n旧场景\n旧动作\n保留场景\n保留动作",
		"scenes": []any{
			map[string]any{"scene_id": "scene_1", "heading": "旧场景", "blocks": []any{map[string]any{"line_id": "old_1", "block_type": "action", "text": "旧动作"}}},
			map[string]any{"scene_id": "scene_2", "heading": "保留场景", "blocks": []any{map[string]any{"line_id": "keep_1", "block_type": "action", "text": "保留动作"}}},
		},
	}
	pack := revisionContextPack{RevisionIntent: "regenerate_script_scene"}
	pack.Target.SceneID = "scene_1"
	pack.TargetArtifact = &struct {
		Payload map[string]any `json:"payload,omitempty"`
	}{Payload: base}
	planned := PlannedArtifact{ArtifactType: "script_unit", Payload: map[string]any{"__script_scene_patch": map[string]any{
		"scene_id": "scene_1", "heading": "新场景", "blocks": []any{
			map[string]any{"line_id": "new_1", "block_type": "action", "text": "新动作"},
			map[string]any{"line_id": "new_2", "block_type": "dialogue", "speaker": "主角", "text": "新台词"},
		},
	}}}

	patched := applyRevisionPatch(planned, pack)
	want := "第1集\n新场景\n新动作\n主角: 新台词\n保留场景\n保留动作"
	if patched.Payload["script_text"] != want {
		t.Fatalf("scene patch did not synchronize script_text: got=%q want=%q", patched.Payload["script_text"], want)
	}
	scenes := patched.Payload["scenes"].([]any)
	if len(scenes) != 2 || len(scenes[0].(map[string]any)["blocks"].([]any)) != 2 || scenes[1].(map[string]any)["heading"] != "保留场景" {
		t.Fatalf("scene patch changed sibling structure: %#v", scenes)
	}
}

func TestRevisionExpiresOldApproval(t *testing.T) {
	runtime := NewRuntime(&targetedPatchWorker{})
	run := agent.Run{
		RunID: "run_expire_approval", ProjectID: "project_expire_approval", SourceMode: agent.SourceModeNovel,
		Status: agent.RunWaitingApproval, CurrentStepID: "step_approval_story_bible", ApprovalRequestID: "approval_old",
	}
	target := runtime.artifact(run, "story_bible", agent.ArtifactPendingApproval, nil, map[string]any{"characters": []any{map[string]any{"goal": "old"}}})
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{target}
	runtime.approvals["approval_old"] = agent.ApprovalRequest{
		ApprovalRequestID: "approval_old", RunID: run.RunID, StepID: run.CurrentStepID, Status: "pending",
		ProposedAction: map[string]any{"approved_artifact": "story_bible"},
	}
	instruction := `USER_REVISION_REQUEST:
change goal

REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"patch_artifact_field","target":{"artifact_id":"` + target.ArtifactID + `","artifact_type":"story_bible","field_path":"characters[0].goal","scope":"field"},"target_artifact":{"payload":{"characters":[{"goal":"old"}]}}}`
	if _, err := runtime.ReviseCheckpoint(run.RunID, instruction); err != nil {
		t.Fatalf("revise checkpoint: %v", err)
	}
	waitForRunStatus(t, runtime, run.RunID, agent.RunWaitingApproval)
	approval := runtime.approvals["approval_old"]
	if approval.Status != "revised" || approval.ResolvedAt == nil {
		t.Fatalf("expected old approval to be revised and resolved, got %#v", approval)
	}
	resolvedEvent := false
	for _, event := range runtime.events[run.RunID] {
		if event.Type == agent.EventApprovalResolved {
			resolvedEvent = true
			break
		}
	}
	if !resolvedEvent {
		t.Fatalf("expected approval_resolved event, got %#v", runtime.events[run.RunID])
	}
}

func TestCompletedRunAllowsTargetedRevision(t *testing.T) {
	runtime := NewRuntime(&targetedPatchWorker{})
	run := agent.Run{
		RunID: "run_completed_revision", ProjectID: "project_completed_revision", SourceMode: agent.SourceModeNovel,
		Status: agent.RunCompleted, CurrentStepID: "step_completed",
	}
	target := runtime.artifact(run, "story_bible", agent.ArtifactConfirmed, nil, map[string]any{"characters": []any{map[string]any{"goal": "old"}}})
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{target}
	instruction := `USER_REVISION_REQUEST:
change goal
REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"patch_artifact_field","target":{"artifact_id":"` + target.ArtifactID + `","artifact_type":"story_bible","field_path":"characters[0].goal","scope":"field"},"target_artifact":{"payload":{"characters":[{"goal":"old"}]}}}`
	resp, err := runtime.ReviseCheckpoint(run.RunID, instruction)
	if err != nil {
		t.Fatalf("expected completed run revision to start: %v", err)
	}
	if resp.Run.Status != agent.RunRunning || resp.Run.CurrentStepID != "step_build_story_bible" {
		t.Fatalf("unexpected completed revision state: %#v", resp.Run)
	}
}

func TestAggregateScriptsUsesLatestPatchedScriptText(t *testing.T) {
	runtime := NewRuntime(nil)
	run := agent.Run{RunID: "run_aggregate_patch", ProjectID: "project_aggregate_patch", SourceMode: agent.SourceModeNovel}
	oldUnit := runtime.artifact(run, "script_unit", agent.ArtifactSuperseded, nil, map[string]any{"episode_id": 1, "script_text": "旧句"})
	newUnit := runtime.artifact(run, "script_unit", agent.ArtifactConfirmed, nil, map[string]any{"episode_id": 1, "script_text": "新句"})
	payload := aggregateScriptsPayload(run.SourceMode, []agent.Artifact{oldUnit, newUnit})
	units := payload["script_units"].([]any)
	if len(units) != 1 || units[0].(map[string]any)["script_text"] != "新句" {
		t.Fatalf("expected aggregate to use latest patched unit, got %#v", units)
	}
}

func TestScriptUnitManualSaveSynchronizesDerivedScripts(t *testing.T) {
	runtime := NewRuntime(nil)
	run := agent.Run{RunID: "run_script_sync_save", ProjectID: "project_script_sync", SourceMode: agent.SourceModeNonNovel, Status: agent.RunCompleted}
	unit := runtime.artifact(run, "script_unit", agent.ArtifactConfirmed, nil, map[string]any{
		"episode_id": 1, "script_text": "旧正文", "scenes": []any{map[string]any{"scene_id": "scene_1", "blocks": []any{map[string]any{"text": "旧正文"}}}},
	})
	aggregate := runtime.artifact(run, "scripts", agent.ArtifactConfirmed, []string{unit.ArtifactID}, aggregateScriptsPayload(run.SourceMode, []agent.Artifact{unit}))
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{unit, aggregate}

	updated, _, artifacts, _, outcome := runtime.UpdateArtifactAtVersion(unit.ArtifactID, unit.Version, map[string]any{
		"episode_id": 1, "script_text": "新正文", "scenes": []any{map[string]any{"scene_id": "scene_1", "blocks": []any{map[string]any{"text": "新正文"}}}},
	})
	if outcome != ArtifactUpdateApplied || updated.Payload["script_text"] != "新正文" {
		t.Fatalf("expected script unit save to apply, got %#v outcome=%s", updated, outcome)
	}
	activeScripts := activeArtifactsOfType(artifacts, "scripts")
	if len(activeScripts) != 1 || activeScripts[0].Version != aggregate.Version+1 {
		t.Fatalf("expected one refreshed scripts aggregate, got %#v", activeScripts)
	}
	units := activeScripts[0].Payload["script_units"].([]any)
	if units[0].(map[string]any)["script_text"] != "新正文" {
		t.Fatalf("derived scripts did not use the saved unit: %#v", units)
	}
	if _, exists := runtime.CurrentApproval(run.RunID); exists {
		t.Fatal("derived scripts refresh must not create a downstream approval")
	}
}

func TestAgentScriptUnitRevisionDefersAggregateRefreshUntilApproval(t *testing.T) {
	runtime := NewRuntime(nil)
	run := agent.Run{RunID: "run_script_sync_agent", ProjectID: "project_script_sync", SourceMode: agent.SourceModeNovel, Status: agent.RunRunning, Metadata: map[string]interface{}{}}
	oldUnit := runtime.artifact(run, "script_unit", agent.ArtifactConfirmed, nil, map[string]any{"episode_id": 1, "script_text": "旧正文"})
	aggregate := runtime.artifact(run, "scripts", agent.ArtifactConfirmed, []string{oldUnit.ArtifactID}, aggregateScriptsPayload(run.SourceMode, []agent.Artifact{oldUnit}))
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{oldUnit, aggregate}

	runtime.appendPlannedArtifact(run.RunID, run, PlannedArtifact{ArtifactType: "script_unit", Payload: map[string]any{"episode_id": 1, "script_text": "Agent 新正文"}}, nil)
	activeScripts := activeArtifactsOfType(runtime.Artifacts(run.RunID), "scripts")
	if len(activeScripts) != 1 || activeScripts[0].ArtifactID != aggregate.ArtifactID {
		t.Fatalf("expected the existing aggregate to remain active before approval, got %#v", activeScripts)
	}
	units := activeScripts[0].Payload["script_units"].([]any)
	if units[0].(map[string]any)["script_text"] != "旧正文" {
		t.Fatalf("aggregate changed before the revised script unit was approved: %#v", units)
	}
	for _, event := range runtime.Events(run.RunID) {
		if event.Type == agent.EventArtifactUpdated && event.FocusArtifact["artifact_type"] == "scripts" {
			t.Fatalf("unexpected scripts refresh before approval: %#v", event)
		}
	}
}

func activeArtifactsOfType(artifacts []agent.Artifact, artifactType string) []agent.Artifact {
	out := []agent.Artifact{}
	for _, artifact := range artifacts {
		if artifact.ArtifactType == artifactType && artifact.Status != agent.ArtifactSuperseded && artifact.Status != agent.ArtifactInvalidated {
			out = append(out, artifact)
		}
	}
	return out
}

func TestModelArtifactConfigCopyCannotOverrideRunAuthority(t *testing.T) {
	runtime := NewRuntime(nil)
	run := agent.Run{
		RunID: "run_config_authority", ProjectID: "project_config_authority", SourceMode: agent.SourceModeNovel, Status: agent.RunRunning,
		Metadata: map[string]interface{}{"generation_config": map[string]any{"episode_duration_minutes": 1.5, "target_episode_count": 3}},
	}
	source := runtime.artifact(run, "source_input", agent.ArtifactConfirmed, nil, map[string]any{"generation_config_ref": configReference(run.RunID, 1)})
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{source}
	runtime.appendPlannedArtifact(run.RunID, run, PlannedArtifact{ArtifactType: "episode_split", Payload: map[string]any{
		"generation_config": map[string]any{"episode_duration_minutes": 1.0, "target_episode_count": 3}, "episodes": []any{},
	}}, nil)
	snapshot := runtime.artifactSnapshot(run.RunID)
	for _, artifact := range snapshot {
		config := artifact.Payload["generation_config"].(map[string]any)
		if config["episode_duration_minutes"] != 1.5 {
			t.Fatalf("expected run-authoritative duration 1.5 in %s, got %#v", artifact.ArtifactType, config)
		}
	}
	stored := runtime.Artifacts(run.RunID)
	if _, copied := stored[len(stored)-1].Payload["generation_config"]; copied {
		t.Fatalf("model config copy must not be persisted: %#v", stored[len(stored)-1].Payload)
	}
}

func TestEpisodeCardRevisionInvalidatesOnlyTargetScriptUnit(t *testing.T) {
	run := agent.Run{RunID: "run_scoped_invalidation", ProjectID: "project_scoped_invalidation", SourceMode: agent.SourceModeNovel}
	runtime := NewRuntime(nil)
	artifacts := []agent.Artifact{
		runtime.artifact(run, "script_unit", agent.ArtifactConfirmed, nil, map[string]any{"episode_id": 1}),
		runtime.artifact(run, "script_unit", agent.ArtifactConfirmed, nil, map[string]any{"episode_id": 2}),
		runtime.artifact(run, "scripts", agent.ArtifactConfirmed, nil, map[string]any{}),
	}
	result := invalidateDownstreamArtifactsScoped(artifacts, run.SourceMode, "episode_cards", map[string]any{"episode_id": 2})
	if result[0].Status != agent.ArtifactConfirmed || result[1].Status != agent.ArtifactInvalidated || result[2].Status != agent.ArtifactInvalidated {
		t.Fatalf("unexpected scoped invalidation: %#v", result)
	}
}

func TestEpisodeCardDownstreamReviewIncludesOnlyTargetScriptAndAggregate(t *testing.T) {
	run := agent.Run{RunID: "run_scoped_review", ProjectID: "project_scoped_review", SourceMode: agent.SourceModeNovel}
	runtime := NewRuntime(nil)
	contextArtifact := runtime.artifact(run, "script_context", agent.ArtifactConfirmed, nil, map[string]any{})
	episode1 := runtime.artifact(run, "script_unit", agent.ArtifactConfirmed, nil, map[string]any{"episode_id": 1})
	episode2 := runtime.artifact(run, "script_unit", agent.ArtifactConfirmed, nil, map[string]any{"episode_id": 2})
	scripts := runtime.artifact(run, "scripts", agent.ArtifactConfirmed, nil, map[string]any{})
	ids, types, episodeIDs := existingAffectedDownstream([]agent.Artifact{contextArtifact, episode1, episode2, scripts}, run.SourceMode, "episode_cards", map[string]any{"episode_id": 2})
	if containsString(ids, episode1.ArtifactID) || !containsString(ids, episode2.ArtifactID) || !containsString(ids, contextArtifact.ArtifactID) || !containsString(ids, scripts.ArtifactID) {
		t.Fatalf("unexpected scoped review IDs: %#v", ids)
	}
	if !containsString(types, "script_context") || !containsString(types, "script_unit") || !containsString(types, "scripts") {
		t.Fatalf("unexpected scoped review types: %#v", types)
	}
	if len(episodeIDs) != 1 || episodeNumber(episodeIDs[0]) != 2 {
		t.Fatalf("expected only episode 2 in regeneration range, got %#v", episodeIDs)
	}
}

func TestRevisionWithoutExistingDownstreamKeepsStandardApproval(t *testing.T) {
	runtime := NewRuntime(&sequentialApprovalWorker{})
	run := agent.Run{
		RunID: "run_no_downstream_review", ProjectID: "project_no_downstream_review", SourceMode: agent.SourceModeNovel, Status: agent.RunRunning,
		Metadata: map[string]interface{}{"active_revision": map[string]any{"revision_intent": "patch_artifact_field", "artifact_type": "story_bible", "field_path": "story_overview", "scope": "field"}},
	}
	old := runtime.artifact(run, "story_bible", agent.ArtifactConfirmed, nil, map[string]any{"story_overview": "old"})
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{old}
	runtime.appendPlannedArtifact(run.RunID, run, PlannedArtifact{ArtifactType: "story_bible", Payload: map[string]any{"story_overview": "new"}}, nil)
	runtime.requestArtifactApproval(run.RunID, "story_bible", nil)
	approval, ok := runtime.CurrentApproval(run.RunID)
	if !ok || containsString(approval.Options, "keep_downstream") || containsString(approval.Options, "regenerate_downstream") {
		t.Fatalf("expected ordinary artifact approval when no downstream exists, got %#v ok=%v", approval, ok)
	}
}

func TestFieldPatchPreservesUntargetedPayloadAcrossPlanningArtifacts(t *testing.T) {
	cases := []struct {
		artifactType string
		fieldPath    string
		root         string
	}{
		{"source_input", "notes", "notes"},
		{"story_bible", "story_overview", "story_overview"},
		{"episode_split", "split_strategy", "split_strategy"},
		{"material_bank", "most_promising_direction", "most_promising_direction"},
		{"story_seed", "core_premise", "core_premise"},
		{"series_blueprint", "series_promise", "series_promise"},
		{"episode_cards", "next_action", "next_action"},
	}
	for _, testCase := range cases {
		pack := revisionContextPack{RevisionIntent: "patch_artifact_field"}
		pack.Target.ArtifactType = testCase.artifactType
		pack.Target.FieldPath = testCase.fieldPath
		pack.TargetArtifact = &struct {
			Payload map[string]any `json:"payload,omitempty"`
		}{Payload: map[string]any{testCase.root: "old", "untargeted": "keep"}}
		planned := PlannedArtifact{ArtifactType: testCase.artifactType, Payload: map[string]any{testCase.root: "new"}}
		patched := applyRevisionPatch(planned, pack)
		if patched.Payload[testCase.root] != "new" || patched.Payload["untargeted"] != "keep" {
			t.Fatalf("%s field patch changed the wrong payload: %#v", testCase.artifactType, patched.Payload)
		}
	}
}

func TestSupportedLocalRevisionMatrixChangesOnlyTargetScope(t *testing.T) {
	cases := []struct {
		name         string
		artifactType string
		intent       string
		fieldPath    string
		base         map[string]any
		candidate    map[string]any
		assert       func(t *testing.T, payload map[string]any)
	}{
		{"source field", "source_input", "patch_artifact_field", "notes", map[string]any{"notes": "old", "text": "keep"}, map[string]any{"notes": "new"}, func(t *testing.T, p map[string]any) { assertPatchedAndKept(t, p["notes"], "new", p["text"], "keep") }},
		{"story bible field", "story_bible", "patch_artifact_field", "characters[0].goal", map[string]any{"characters": []any{map[string]any{"name": "A", "goal": "old"}, map[string]any{"name": "B", "goal": "keep"}}, "story_overview": "keep"}, map[string]any{"characters": []any{map[string]any{"goal": "new"}}}, func(t *testing.T, p map[string]any) {
			characters := p["characters"].([]any)
			assertPatchedAndKept(t, characters[0].(map[string]any)["goal"], "new", characters[0].(map[string]any)["name"], "A")
			assertPatchedAndKept(t, characters[1].(map[string]any)["goal"], "keep", p["story_overview"], "keep")
		}},
		{"story bible entity", "story_bible", "patch_artifact_entity", "characters[0]", map[string]any{"characters": []any{map[string]any{"name": "A", "goal": "old"}, map[string]any{"name": "B", "goal": "keep"}}, "story_overview": "keep"}, map[string]any{"characters": []any{map[string]any{"name": "A2", "goal": "new"}}}, func(t *testing.T, p map[string]any) {
			characters := p["characters"].([]any)
			assertPatchedAndKept(t, characters[0].(map[string]any)["name"], "A2", characters[1].(map[string]any)["name"], "B")
			assertPatchedAndKept(t, p["story_overview"], "keep", characters[1].(map[string]any)["goal"], "keep")
		}},
		{"story bible section", "story_bible", "patch_artifact_section", "relationships", map[string]any{"relationships": []any{"old"}, "characters": []any{"keep"}}, map[string]any{"relationships": []any{"new"}}, func(t *testing.T, p map[string]any) {
			assertPatchedAndKept(t, p["relationships"].([]any)[0], "new", p["characters"].([]any)[0], "keep")
		}},
		{"episode split field", "episode_split", "patch_artifact_field", "generation_config.episode_duration_minutes", map[string]any{"generation_config": map[string]any{"episode_duration_minutes": 1.5, "target_episode_count": 2}, "episodes": []any{"keep"}}, map[string]any{"generation_config": map[string]any{"episode_duration_minutes": 1.0}}, func(t *testing.T, p map[string]any) {
			config := p["generation_config"].(map[string]any)
			assertPatchedAndKept(t, config["episode_duration_minutes"], 1.0, config["target_episode_count"], float64(2))
			assertPatchedAndKept(t, p["episodes"].([]any)[0], "keep", "keep", "keep")
		}},
		{"episode split entity", "episode_split", "patch_artifact_entity", "episodes[0]", episodePayload("old", "keep"), map[string]any{"episodes": []any{map[string]any{"episode_id": 1, "hook": "new"}}}, assertFirstEpisodePatched},
		{"episode split section", "episode_split", "patch_artifact_section", "coverage_check", map[string]any{"coverage_check": map[string]any{"status": "old"}, "episodes": []any{"keep"}}, map[string]any{"coverage_check": map[string]any{"status": "new"}}, func(t *testing.T, p map[string]any) {
			assertPatchedAndKept(t, p["coverage_check"].(map[string]any)["status"], "new", p["episodes"].([]any)[0], "keep")
		}},
		{"material bank entity", "material_bank", "patch_artifact_entity", "conflict_materials[0]", map[string]any{"conflict_materials": []any{map[string]any{"id": "a", "value": "old"}, map[string]any{"id": "b", "value": "keep"}}, "most_promising_direction": "keep"}, map[string]any{"conflict_materials": []any{map[string]any{"id": "a", "value": "new"}}}, func(t *testing.T, p map[string]any) {
			values := p["conflict_materials"].([]any)
			assertPatchedAndKept(t, values[0].(map[string]any)["value"], "new", values[1].(map[string]any)["value"], "keep")
			assertPatchedAndKept(t, p["most_promising_direction"], "keep", "keep", "keep")
		}},
		{"material bank field", "material_bank", "patch_artifact_field", "most_promising_direction", map[string]any{"most_promising_direction": "old", "conflict_materials": []any{"keep"}}, map[string]any{"most_promising_direction": "new"}, func(t *testing.T, p map[string]any) {
			assertPatchedAndKept(t, p["most_promising_direction"], "new", p["conflict_materials"].([]any)[0], "keep")
		}},
		{"material bank section", "material_bank", "patch_artifact_section", "gaps_and_questions", map[string]any{"gaps_and_questions": []any{"old"}, "conflict_materials": []any{"keep"}}, map[string]any{"gaps_and_questions": []any{"new"}}, func(t *testing.T, p map[string]any) {
			assertPatchedAndKept(t, p["gaps_and_questions"].([]any)[0], "new", p["conflict_materials"].([]any)[0], "keep")
		}},
		{"story seed field", "story_seed", "patch_artifact_field", "core_premise", map[string]any{"core_premise": "old", "logline": "keep"}, map[string]any{"core_premise": "new"}, func(t *testing.T, p map[string]any) {
			assertPatchedAndKept(t, p["core_premise"], "new", p["logline"], "keep")
		}},
		{"story seed entity", "story_seed", "patch_artifact_entity", "protagonist", map[string]any{"protagonist": map[string]any{"name": "old"}, "logline": "keep"}, map[string]any{"protagonist": map[string]any{"name": "new"}}, func(t *testing.T, p map[string]any) {
			assertPatchedAndKept(t, p["protagonist"].(map[string]any)["name"], "new", p["logline"], "keep")
		}},
		{"story seed section", "story_seed", "patch_artifact_section", "main_plotline", map[string]any{"main_plotline": []any{"old"}, "logline": "keep"}, map[string]any{"main_plotline": []any{"new"}}, func(t *testing.T, p map[string]any) {
			assertPatchedAndKept(t, p["main_plotline"].([]any)[0], "new", p["logline"], "keep")
		}},
		{"series blueprint field", "series_blueprint", "patch_artifact_field", "series_promise", map[string]any{"series_promise": "old", "phase_plan": []any{"keep"}}, map[string]any{"series_promise": "new"}, func(t *testing.T, p map[string]any) {
			assertPatchedAndKept(t, p["series_promise"], "new", p["phase_plan"].([]any)[0], "keep")
		}},
		{"series blueprint entity", "series_blueprint", "patch_artifact_entity", "phase_plan[0]", map[string]any{"phase_plan": []any{map[string]any{"phase": 1, "pace": "old"}, map[string]any{"phase": 2, "pace": "keep"}}, "series_promise": "keep"}, map[string]any{"phase_plan": []any{map[string]any{"phase": 1, "pace": "new"}}}, func(t *testing.T, p map[string]any) {
			phases := p["phase_plan"].([]any)
			assertPatchedAndKept(t, phases[0].(map[string]any)["pace"], "new", phases[1].(map[string]any)["pace"], "keep")
			assertPatchedAndKept(t, p["series_promise"], "keep", "keep", "keep")
		}},
		{"series blueprint section", "series_blueprint", "patch_artifact_section", "payoff_distribution", map[string]any{"payoff_distribution": []any{"old"}, "series_promise": "keep"}, map[string]any{"payoff_distribution": []any{"new"}}, func(t *testing.T, p map[string]any) {
			assertPatchedAndKept(t, p["payoff_distribution"].([]any)[0], "new", p["series_promise"], "keep")
		}},
		{"episode cards field", "episode_cards", "patch_artifact_field", "episodes[0].hook", episodePayload("old", "keep"), map[string]any{"episodes": []any{map[string]any{"hook": "new"}}}, assertFirstEpisodePatched},
		{"episode cards entity", "episode_cards", "patch_artifact_entity", "episodes[0]", episodePayload("old", "keep"), map[string]any{"episodes": []any{map[string]any{"episode_id": 1, "hook": "new"}}}, assertFirstEpisodePatched},
		{"episode cards section", "episode_cards", "patch_artifact_section", "continuity_delta", map[string]any{"continuity_delta": map[string]any{"status": "old"}, "episodes": []any{"keep"}}, map[string]any{"continuity_delta": map[string]any{"status": "new"}}, func(t *testing.T, p map[string]any) {
			assertPatchedAndKept(t, p["continuity_delta"].(map[string]any)["status"], "new", p["episodes"].([]any)[0], "keep")
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			pack := revisionContextPack{RevisionIntent: testCase.intent}
			pack.Target.ArtifactType = testCase.artifactType
			pack.Target.FieldPath = testCase.fieldPath
			pack.TargetArtifact = &struct {
				Payload map[string]any `json:"payload,omitempty"`
			}{Payload: testCase.base}
			patched := applyRevisionPatch(PlannedArtifact{ArtifactType: testCase.artifactType, Payload: testCase.candidate}, pack)
			testCase.assert(t, patched.Payload)
		})
	}
}

func TestEntityAppendAndDeletePreserveSiblingItems(t *testing.T) {
	base := map[string]any{
		"conflict_materials": []any{map[string]any{"id": "M1", "summary": "保留"}},
		"untargeted":         "keep",
	}
	appendPack := revisionContextPack{RevisionIntent: "patch_artifact_entity"}
	appendPack.Target.FieldPath = "conflict_materials"
	appendPack.TargetArtifact = &struct {
		Payload map[string]any `json:"payload,omitempty"`
	}{Payload: base}
	appended := applyRevisionPatch(PlannedArtifact{ArtifactType: "material_bank", Payload: map[string]any{
		"__artifact_entity_patch": map[string]any{"operation": "append_entity", "field_path": "conflict_materials", "patch_value": map[string]any{"id": "M2", "summary": "新增"}},
	}}, appendPack)
	items := appended.Payload["conflict_materials"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["id"] != "M1" || items[1].(map[string]any)["id"] != "M2" || appended.Payload["untargeted"] != "keep" {
		t.Fatalf("append entity corrupted payload: %#v", appended.Payload)
	}

	deletePack := revisionContextPack{RevisionIntent: "patch_artifact_entity"}
	deletePack.Target.FieldPath = "conflict_materials[0]"
	deletePack.TargetArtifact = &struct {
		Payload map[string]any `json:"payload,omitempty"`
	}{Payload: appended.Payload}
	deleted := applyRevisionPatch(PlannedArtifact{ArtifactType: "material_bank", Payload: map[string]any{
		"__artifact_entity_patch": map[string]any{"operation": "delete_entity", "field_path": "conflict_materials[0]", "patch_value": nil},
	}}, deletePack)
	items = deleted.Payload["conflict_materials"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != "M2" || deleted.Payload["untargeted"] != "keep" {
		t.Fatalf("delete entity corrupted payload: %#v", deleted.Payload)
	}
}

func TestCollectionOperationsPreserveSiblingsAndRenumberEpisodes(t *testing.T) {
	base := map[string]any{
		"target_episode_count": 3,
		"actual_episode_count": 3,
		"episodes": []any{
			map[string]any{"episode_id": 1, "title": "keep-before"},
			map[string]any{"episode_id": 2, "title": "split-me"},
			map[string]any{"episode_id": 3, "title": "keep-after"},
		},
		"coverage_check": map[string]any{"status": "keep"},
	}
	pack := revisionContextPack{RevisionIntent: "patch_artifact_collection"}
	pack.Target.FieldPath = "episodes"
	pack.TargetArtifact = &struct {
		Payload map[string]any `json:"payload,omitempty"`
	}{Payload: base}
	planned := applyRevisionPatch(PlannedArtifact{ArtifactType: "episode_split", Payload: map[string]any{
		"__artifact_collection_patch": map[string]any{
			"operation": "replace_range", "field_path": "episodes", "start_index": 1, "delete_count": 1,
			"items": []any{map[string]any{"title": "part-a"}, map[string]any{"title": "part-b"}},
		},
	}}, pack)
	episodes := planned.Payload["episodes"].([]any)
	if len(episodes) != 4 || episodes[0].(map[string]any)["title"] != "keep-before" || episodes[3].(map[string]any)["title"] != "keep-after" {
		t.Fatalf("replace_range changed sibling episodes: %#v", episodes)
	}
	for index, episode := range episodes {
		if intValueOrZero(episode.(map[string]any)["episode_id"]) != index+1 {
			t.Fatalf("episode %d was not deterministically renumbered: %#v", index, episode)
		}
	}
	if planned.Payload["actual_episode_count"] != 4 || planned.Payload["target_episode_count"] != 4 || planned.Payload["coverage_check"].(map[string]any)["status"] != "keep" {
		t.Fatalf("collection metadata or unrelated section was corrupted: %#v", planned.Payload)
	}

	operationCases := []struct {
		name      string
		operation map[string]any
		want      []string
	}{
		{"insert", map[string]any{"operation": "insert_entities", "field_path": "items", "start_index": 1, "items": []any{map[string]any{"id": "x"}}}, []string{"a", "x", "b", "c"}},
		{"delete", map[string]any{"operation": "delete_entities", "field_path": "items", "start_index": 1, "delete_count": 1, "items": []any{}}, []string{"a", "c"}},
		{"move", map[string]any{"operation": "move_entity", "field_path": "items", "start_index": 0, "move_to": 2, "items": []any{}}, []string{"b", "c", "a"}},
	}
	for _, testCase := range operationCases {
		t.Run(testCase.name, func(t *testing.T) {
			payload := map[string]any{"items": []any{map[string]any{"id": "a"}, map[string]any{"id": "b"}, map[string]any{"id": "c"}}, "keep": true}
			casePack := revisionContextPack{RevisionIntent: "patch_artifact_collection"}
			casePack.Target.FieldPath = "items"
			casePack.TargetArtifact = &struct {
				Payload map[string]any `json:"payload,omitempty"`
			}{Payload: payload}
			result := applyRevisionPatch(PlannedArtifact{ArtifactType: "material_bank", Payload: map[string]any{"__artifact_collection_patch": testCase.operation}}, casePack)
			items := result.Payload["items"].([]any)
			got := make([]string, 0, len(items))
			for _, item := range items {
				got = append(got, fmt.Sprint(item.(map[string]any)["id"]))
			}
			if !reflect.DeepEqual(got, testCase.want) || result.Payload["keep"] != true {
				t.Fatalf("%s produced %#v, want %#v", testCase.name, got, testCase.want)
			}
		})
	}
}

func TestCollectionEpisodeCountUpdatesAuthoritativeGenerationConfig(t *testing.T) {
	runtime := NewRuntime(nil)
	run := agent.Run{RunID: "run_collection_config", Metadata: map[string]any{
		"generation_config":         map[string]any{"target_episode_count": 3, "episode_duration_minutes": 2},
		"generation_config_version": 1,
	}}
	runtime.runs[run.RunID] = run
	pack := revisionContextPack{RevisionIntent: "patch_artifact_collection"}
	pack.Target.FieldPath = "episodes"
	runtime.syncGenerationConfigForCollectionRevision(run.RunID, "episode_split", map[string]any{"episodes": []any{1, 2, 3, 4}}, pack)
	updated := runtime.runs[run.RunID]
	config := authoritativeGenerationConfigLocked(updated)
	if intValueOrZero(config["target_episode_count"]) != 4 || intValueOrZero(config["episode_duration_minutes"]) != 2 || generationConfigVersion(updated) != 2 {
		t.Fatalf("authoritative generation config was not synchronized: run=%#v config=%#v", updated, config)
	}
}

func TestInvalidSparsePatchFailsClosedToCurrentPayload(t *testing.T) {
	pack := revisionContextPack{RevisionIntent: "patch_artifact_field"}
	pack.Target.FieldPath = "core_premise"
	pack.TargetArtifact = &struct {
		Payload map[string]any `json:"payload,omitempty"`
	}{Payload: map[string]any{"core_premise": "old", "untargeted": "keep"}}
	planned := applyRevisionPatch(PlannedArtifact{ArtifactType: "story_seed", Payload: map[string]any{"wrong_field": "bad"}}, pack)
	if planned.Payload["core_premise"] != "old" || planned.Payload["untargeted"] != "keep" || planned.Payload["wrong_field"] != nil {
		t.Fatalf("invalid sparse patch replaced current payload: %#v", planned.Payload)
	}
}

func TestNamedSelectorPatchResolvesCharacterWithoutChangingSiblings(t *testing.T) {
	base := map[string]any{"characters": []any{
		map[string]any{"name": "protagonist", "goal": "old"},
		map[string]any{"name": "friend", "goal": "keep"},
	}, "story_overview": "keep"}
	pack := revisionContextPack{RevisionIntent: "patch_artifact_field"}
	pack.Target.FieldPath = "characters[name=protagonist].goal"
	pack.TargetArtifact = &struct {
		Payload map[string]any `json:"payload,omitempty"`
	}{Payload: base}
	planned := applyRevisionPatch(PlannedArtifact{ArtifactType: "story_bible", Payload: map[string]any{
		"__artifact_value_patch": map[string]any{"field_path": "characters[name=protagonist].goal", "patch_value": "new"},
	}}, pack)
	characters := planned.Payload["characters"].([]any)
	if characters[0].(map[string]any)["goal"] != "new" || characters[1].(map[string]any)["goal"] != "keep" || planned.Payload["story_overview"] != "keep" {
		t.Fatalf("named selector patch changed the wrong character: %#v", planned.Payload)
	}
}

func TestNamedSelectorDeleteRemovesOnlyMatchedEntity(t *testing.T) {
	base := map[string]any{"characters": []any{
		map[string]any{"name": "remove", "goal": "old"},
		map[string]any{"name": "keep", "goal": "keep"},
	}}
	pack := revisionContextPack{RevisionIntent: "patch_artifact_entity"}
	pack.Target.FieldPath = "characters[name=remove]"
	pack.TargetArtifact = &struct {
		Payload map[string]any `json:"payload,omitempty"`
	}{Payload: base}
	planned := applyRevisionPatch(PlannedArtifact{ArtifactType: "story_bible", Payload: map[string]any{
		"__artifact_entity_patch": map[string]any{"operation": "delete_entity", "field_path": "characters[name=remove]", "patch_value": nil},
	}}, pack)
	characters := planned.Payload["characters"].([]any)
	if len(characters) != 1 || characters[0].(map[string]any)["name"] != "keep" {
		t.Fatalf("named selector delete changed the wrong entity: %#v", planned.Payload)
	}
}

func TestRevisionTargetResolvesRequestedScriptEpisode(t *testing.T) {
	runtime := NewRuntime(nil)
	run := agent.Run{RunID: "run_episode_target", ProjectID: "project_episode_target", SourceMode: agent.SourceModeNovel}
	episode1 := runtime.artifact(run, "script_unit", agent.ArtifactConfirmed, nil, map[string]any{"episode_id": 1, "script_text": "第一集"})
	episode2 := runtime.artifact(run, "script_unit", agent.ArtifactConfirmed, nil, map[string]any{"episode_id": 2, "script_text": "第二集"})
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{episode1, episode2}
	pack := revisionContextPack{}
	pack.Target.EpisodeID = "1"

	target, ok := runtime.targetArtifactForRevision(run.RunID, "script_unit", pack)
	if !ok || target.ArtifactID != episode1.ArtifactID {
		t.Fatalf("resolved wrong script episode: %#v ok=%v", target, ok)
	}
}

func episodePayload(first string, second string) map[string]any {
	return map[string]any{"episodes": []any{map[string]any{"episode_id": 1, "hook": first}, map[string]any{"episode_id": 2, "hook": second}}, "untargeted": "keep"}
}

func assertFirstEpisodePatched(t *testing.T, payload map[string]any) {
	t.Helper()
	episodes := payload["episodes"].([]any)
	assertPatchedAndKept(t, episodes[0].(map[string]any)["hook"], "new", episodes[1].(map[string]any)["hook"], "keep")
	assertPatchedAndKept(t, payload["untargeted"], "keep", "keep", "keep")
}

func assertPatchedAndKept(t *testing.T, got any, want any, kept any, wantKept any) {
	t.Helper()
	if got != want || kept != wantKept {
		t.Fatalf("target or sibling changed incorrectly: got=%#v want=%#v kept=%#v wantKept=%#v", got, want, kept, wantKept)
	}
}

func TestFullRegenerationVersionAndDownstreamDecisionMatrix(t *testing.T) {
	cases := []struct {
		mode       agent.SourceMode
		targetType string
	}{
		{agent.SourceModeNovel, "story_bible"},
		{agent.SourceModeNovel, "episode_split"},
		{agent.SourceModeNovel, "episode_cards"},
		{agent.SourceModeNonNovel, "material_bank"},
		{agent.SourceModeNonNovel, "story_seed"},
		{agent.SourceModeNonNovel, "series_blueprint"},
		{agent.SourceModeNonNovel, "episode_cards"},
	}
	for _, testCase := range cases {
		t.Run(string(testCase.mode)+"_"+testCase.targetType, func(t *testing.T) {
			runtime := NewRuntime(nil)
			run := agent.Run{RunID: "run_regen_" + testCase.targetType, ProjectID: "project_regen", SourceMode: testCase.mode, Status: agent.RunRunning, Metadata: map[string]interface{}{
				"active_revision": map[string]any{"revision_intent": "regenerate_artifact", "artifact_type": testCase.targetType, "scope": "artifact"},
			}}
			old := runtime.artifact(run, testCase.targetType, agent.ArtifactConfirmed, nil, map[string]any{"value": "old"})
			artifacts := []agent.Artifact{old}
			for _, downstreamType := range affectedArtifactsAfter(testCase.mode, testCase.targetType) {
				payload := map[string]any{"value": "keep"}
				if downstreamType == "script_unit" {
					payload["episode_id"] = 1
				}
				artifacts = append(artifacts, runtime.artifact(run, downstreamType, agent.ArtifactConfirmed, nil, payload))
			}
			runtime.runs[run.RunID] = run
			runtime.artifacts[run.RunID] = artifacts
			runtime.appendPlannedArtifact(run.RunID, run, PlannedArtifact{ArtifactType: testCase.targetType, Payload: map[string]any{"value": "new"}}, nil)
			result := runtime.Artifacts(run.RunID)
			if result[0].Status != agent.ArtifactSuperseded || result[len(result)-1].Version != 2 || result[len(result)-1].Payload["value"] != "new" {
				t.Fatalf("incorrect regenerated versions: %#v", result)
			}
			for _, artifact := range result[1 : len(result)-1] {
				if artifact.Status != agent.ArtifactConfirmed {
					t.Fatalf("expected %s downstream preserved before user choice, got %s", artifact.ArtifactType, artifact.Status)
				}
			}
			updatedRun, _ := runtime.GetRun(run.RunID)
			if pendingDownstreamReviewFromRun(updatedRun) == nil {
				t.Fatal("expected existing downstream to create a pending review")
			}
		})
	}
}

func TestAffectedArtifactSequenceCoversEveryNodeInBothFlows(t *testing.T) {
	cases := []struct {
		name       string
		mode       agent.SourceMode
		targetType string
		want       []string
	}{
		{"novel_source", agent.SourceModeNovel, "source_input", []string{"story_bible", "episode_split", "episode_cards", "script_context", "script_unit", "scripts"}},
		{"novel_story_bible", agent.SourceModeNovel, "story_bible", []string{"episode_split", "episode_cards", "script_context", "script_unit", "scripts"}},
		{"novel_episode_split", agent.SourceModeNovel, "episode_split", []string{"episode_cards", "script_context", "script_unit", "scripts"}},
		{"novel_episode_cards", agent.SourceModeNovel, "episode_cards", []string{"script_context", "script_unit", "scripts"}},
		{"novel_script_context", agent.SourceModeNovel, "script_context", []string{"script_unit", "scripts"}},
		{"novel_script_unit", agent.SourceModeNovel, "script_unit", []string{"scripts"}},
		{"novel_scripts", agent.SourceModeNovel, "scripts", nil},
		{"nonnovel_source", agent.SourceModeNonNovel, "source_input", []string{"material_bank", "story_seed", "series_blueprint", "episode_cards", "script_context", "script_unit", "scripts"}},
		{"nonnovel_material_bank", agent.SourceModeNonNovel, "material_bank", []string{"story_seed", "series_blueprint", "episode_cards", "script_context", "script_unit", "scripts"}},
		{"nonnovel_story_seed", agent.SourceModeNonNovel, "story_seed", []string{"series_blueprint", "episode_cards", "script_context", "script_unit", "scripts"}},
		{"nonnovel_series_blueprint", agent.SourceModeNonNovel, "series_blueprint", []string{"episode_cards", "script_context", "script_unit", "scripts"}},
		{"nonnovel_episode_cards", agent.SourceModeNonNovel, "episode_cards", []string{"script_context", "script_unit", "scripts"}},
		{"nonnovel_script_context", agent.SourceModeNonNovel, "script_context", []string{"script_unit", "scripts"}},
		{"nonnovel_script_unit", agent.SourceModeNonNovel, "script_unit", []string{"scripts"}},
		{"nonnovel_scripts", agent.SourceModeNonNovel, "scripts", nil},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := affectedArtifactsAfter(testCase.mode, testCase.targetType); !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("affected artifacts mismatch: got=%v want=%v", got, testCase.want)
			}
		})
	}
}

func TestRegenerateScriptEpisodeReplacesOnlyTargetAndDefersAggregateRefresh(t *testing.T) {
	runtime := NewRuntime(nil)
	run := agent.Run{
		RunID: "run_regenerate_script_episode", ProjectID: "project_script_episode", SourceMode: agent.SourceModeNovel,
		Status: agent.RunRunning, Metadata: map[string]interface{}{"active_revision": map[string]any{
			"revision_intent": "regenerate_script_episode", "artifact_type": "script_unit", "episode_id": "2", "scope": "episode",
		}},
	}
	unit1 := runtime.artifact(run, "script_unit", agent.ArtifactConfirmed, nil, map[string]any{"episode_id": 1, "script_text": "episode one"})
	unit2 := runtime.artifact(run, "script_unit", agent.ArtifactConfirmed, nil, map[string]any{"episode_id": 2, "script_text": "old episode two"})
	aggregate := runtime.artifact(run, "scripts", agent.ArtifactConfirmed, []string{unit1.ArtifactID, unit2.ArtifactID}, aggregateScriptsPayload(run.SourceMode, []agent.Artifact{unit1, unit2}))
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{unit1, unit2, aggregate}

	runtime.appendPlannedArtifact(run.RunID, run, PlannedArtifact{ArtifactType: "script_unit", Payload: map[string]any{
		"episode_id": 2, "script_text": "new episode two",
	}}, nil)

	artifacts := runtime.Artifacts(run.RunID)
	activeUnits := activeArtifactsOfType(artifacts, "script_unit")
	if len(activeUnits) != 2 {
		t.Fatalf("expected two active script episodes, got %#v", activeUnits)
	}
	for _, unit := range activeUnits {
		switch episodeNumber(unit.Payload["episode_id"]) {
		case 1:
			if unit.ArtifactID != unit1.ArtifactID || unit.Version != 1 || unit.Payload["script_text"] != "episode one" {
				t.Fatalf("sibling episode changed: %#v", unit)
			}
		case 2:
			if unit.Version != 2 || unit.Payload["script_text"] != "new episode two" || unit.Status != agent.ArtifactPendingApproval {
				t.Fatalf("target episode was not replaced correctly: %#v", unit)
			}
		default:
			t.Fatalf("unexpected script episode: %#v", unit)
		}
	}
	activeAggregates := activeArtifactsOfType(artifacts, "scripts")
	if len(activeAggregates) != 1 || activeAggregates[0].ArtifactID != aggregate.ArtifactID || activeAggregates[0].Version != 1 {
		t.Fatalf("expected the existing aggregate to remain active before approval, got %#v", activeAggregates)
	}
	units := activeAggregates[0].Payload["script_units"].([]any)
	if len(units) != 2 || units[0].(map[string]any)["script_text"] != "episode one" || units[1].(map[string]any)["script_text"] != "old episode two" {
		t.Fatalf("aggregate changed before the target episode was approved: %#v", units)
	}
	updatedRun, _ := runtime.GetRun(run.RunID)
	if pendingDownstreamReviewFromRun(updatedRun) != nil {
		t.Fatalf("derived scripts aggregate must refresh directly without downstream review: %#v", updatedRun.Metadata)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestKeepDownstreamPreservesExistingArtifactsAndCompletesRevision(t *testing.T) {
	runtime := NewRuntime(&sequentialApprovalWorker{})
	run := agent.Run{
		RunID: "run_keep_downstream", ProjectID: "project_keep_downstream", SourceMode: agent.SourceModeNovel,
		Status: agent.RunWaitingApproval, CurrentStepID: "step_approval_story_bible", ApprovalRequestID: "approval_keep",
		Metadata: map[string]interface{}{"pending_downstream_review": map[string]any{
			"source_artifact_type": "story_bible", "artifact_ids": []string{"split_current", "cards_current"},
			"artifact_types": []string{"episode_split", "episode_cards"},
		}},
	}
	story := runtime.artifact(run, "story_bible", agent.ArtifactPendingApproval, nil, map[string]any{"value": "new"})
	split := runtime.artifact(run, "episode_split", agent.ArtifactConfirmed, nil, map[string]any{"value": "keep"})
	split.ArtifactID = "split_current"
	cards := runtime.artifact(run, "episode_cards", agent.ArtifactConfirmed, nil, map[string]any{"value": "keep"})
	cards.ArtifactID = "cards_current"
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{story, split, cards}
	runtime.approvals[run.ApprovalRequestID] = agent.ApprovalRequest{
		ApprovalRequestID: run.ApprovalRequestID, RunID: run.RunID, StepID: run.CurrentStepID, Status: "pending",
		ProposedAction: map[string]any{"approved_artifact": "story_bible", "downstream_review": true},
	}

	response, err := runtime.ContinueRunAsyncContext(context.Background(), run.RunID, agent.ContinueRunRequest{Decision: "keep_downstream"})
	if err != nil {
		t.Fatalf("keep downstream: %v", err)
	}
	if response.Run.Status != agent.RunCompleted || len(response.Run.Invalidated) != 0 {
		t.Fatalf("expected completed revision without invalidation, got %#v", response.Run)
	}
	if response.Artifacts[1].Status != agent.ArtifactConfirmed || response.Artifacts[2].Status != agent.ArtifactConfirmed {
		t.Fatalf("expected downstream artifacts preserved, got %#v", response.Artifacts)
	}
}

func TestRegenerateDownstreamMarksOnlyActualArtifactsStaleAndRecordsIDs(t *testing.T) {
	worker := &blockingDownstreamWorker{started: make(chan struct{}), release: make(chan struct{})}
	runtime := NewRuntime(worker)
	run := agent.Run{
		RunID: "run_regenerate_downstream", ProjectID: "project_regenerate_downstream", SourceMode: agent.SourceModeNovel,
		Status: agent.RunWaitingApproval, CurrentStepID: "step_approval_story_bible", ApprovalRequestID: "approval_regenerate",
		Metadata: map[string]interface{}{"pending_downstream_review": map[string]any{
			"source_artifact_type": "story_bible", "artifact_ids": []string{"split_current", "cards_current"},
			"artifact_types": []string{"episode_split", "episode_cards"}, "revision": map[string]any{"scope": "field"},
		}},
	}
	story := runtime.artifact(run, "story_bible", agent.ArtifactPendingApproval, nil, map[string]any{"value": "new"})
	split := runtime.artifact(run, "episode_split", agent.ArtifactConfirmed, nil, map[string]any{"value": "old"})
	split.ArtifactID = "split_current"
	cards := runtime.artifact(run, "episode_cards", agent.ArtifactConfirmed, nil, map[string]any{"value": "old"})
	cards.ArtifactID = "cards_current"
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{story, split, cards}
	runtime.approvals[run.ApprovalRequestID] = agent.ApprovalRequest{
		ApprovalRequestID: run.ApprovalRequestID, RunID: run.RunID, StepID: run.CurrentStepID, Status: "pending",
		ProposedAction: map[string]any{"approved_artifact": "story_bible", "downstream_review": true},
	}

	response, err := runtime.ContinueRunAsyncContext(context.Background(), run.RunID, agent.ContinueRunRequest{Decision: "regenerate_downstream"})
	if err != nil {
		t.Fatalf("regenerate downstream: %v", err)
	}
	if response.Artifacts[1].Status != agent.ArtifactStale || response.Artifacts[2].Status != agent.ArtifactStale {
		t.Fatalf("expected affected downstream to remain visible as stale, got %#v", response.Artifacts)
	}
	if !containsString(response.Run.Invalidated, "split_current") || !containsString(response.Run.Invalidated, "cards_current") {
		t.Fatalf("expected run invalidated IDs to match actual stale artifacts, got %#v", response.Run.Invalidated)
	}
	select {
	case <-worker.started:
	case <-time.After(time.Second):
		t.Fatal("expected downstream regeneration to start")
	}
	close(worker.release)
}

func TestRuntimeStateRestoresApprovalAfterRestart(t *testing.T) {
	statePath := t.TempDir() + "/runtime_state.json"
	runtime := NewRuntimeWithState(nil, statePath)
	run := agent.Run{
		RunID:             "run_persist",
		ProjectID:         "project_persist",
		SourceMode:        agent.SourceModeNovel,
		Status:            agent.RunWaitingApproval,
		CurrentStepID:     "step_approval_episode_cards",
		ApprovalRequestID: "approval_persist",
		StartedAt:         time.Now().UTC(),
	}
	artifact := runtime.artifact(run, "episode_cards", agent.ArtifactPendingApproval, nil, map[string]any{
		"episodes": []any{map[string]any{"episode_id": 1}},
	})
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{artifact}
	runtime.events[run.RunID] = []agent.RunEvent{runtime.event(run.RunID, run.CurrentStepID, agent.EventApprovalRequested, "approval requested", nil, nil, nil)}
	runtime.approvals[run.ApprovalRequestID] = agent.ApprovalRequest{
		ApprovalRequestID: run.ApprovalRequestID,
		RunID:             run.RunID,
		StepID:            run.CurrentStepID,
		ProposedAction:    map[string]any{"approved_artifact": "episode_cards"},
		Status:            "pending",
		CreatedAt:         time.Now().UTC(),
	}
	runtime.saveLocked()

	restored := NewRuntimeWithState(nil, statePath)
	if _, ok := restored.GetRun(run.RunID); !ok {
		t.Fatal("expected run to be restored")
	}
	if _, ok := restored.Approval(run.ApprovalRequestID); !ok {
		t.Fatal("expected approval to be restored")
	}
	if artifacts := restored.Artifacts(run.RunID); len(artifacts) != 1 || artifacts[0].ArtifactID != artifact.ArtifactID {
		t.Fatalf("expected artifact to be restored, got %#v", artifacts)
	}
	if latest, ok := restored.LatestRunForProject(run.ProjectID); !ok || latest.RunID != run.RunID {
		t.Fatalf("expected latest project run to be restored, got %#v ok=%v", latest, ok)
	}
}

func TestRuntimeStateExpiresOrphanedPendingApprovalAfterRestart(t *testing.T) {
	statePath := t.TempDir() + "/runtime_state.json"
	runtime := NewRuntimeWithState(nil, statePath)
	run := agent.Run{RunID: "run_completed_old_approval", ProjectID: "project_old_approval", Status: agent.RunCompleted}
	runtime.runs[run.RunID] = run
	runtime.approvals["approval_orphaned"] = agent.ApprovalRequest{
		ApprovalRequestID: "approval_orphaned", RunID: run.RunID, Status: "pending",
	}
	runtime.saveLocked()

	restored := NewRuntimeWithState(nil, statePath)
	approval, ok := restored.Approval("approval_orphaned")
	if !ok || approval.Status != "expired" || approval.ResolvedAt == nil {
		t.Fatalf("expected orphaned approval to expire on restore, got %#v ok=%v", approval, ok)
	}
}

func TestRuntimeStateMigratesLegacyImmediateDownstreamInvalidation(t *testing.T) {
	statePath := t.TempDir() + "/runtime_state.json"
	runtime := NewRuntimeWithState(nil, statePath)
	run := agent.Run{
		RunID: "run_legacy_invalidation", ProjectID: "project_legacy_invalidation", SourceMode: agent.SourceModeNovel,
		Status: agent.RunWaitingApproval, CurrentStepID: "step_approval_story_bible", ApprovalRequestID: "approval_legacy",
	}
	story := runtime.artifact(run, "story_bible", agent.ArtifactPendingApproval, nil, map[string]any{"value": "new"})
	split := runtime.artifact(run, "episode_split", agent.ArtifactInvalidated, nil, map[string]any{"value": "old"})
	cards := runtime.artifact(run, "episode_cards", agent.ArtifactInvalidated, nil, map[string]any{"value": "old"})
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{story, split, cards}
	runtime.approvals[run.ApprovalRequestID] = agent.ApprovalRequest{
		ApprovalRequestID: run.ApprovalRequestID, RunID: run.RunID, StepID: run.CurrentStepID, Status: "pending",
		ProposedAction: map[string]any{"approved_artifact": "story_bible"},
	}
	runtime.saveLocked()

	restored := NewRuntimeWithState(nil, statePath)
	artifacts := restored.Artifacts(run.RunID)
	if artifacts[1].Status != agent.ArtifactConfirmed || artifacts[2].Status != agent.ArtifactConfirmed {
		t.Fatalf("expected legacy invalidated downstream to be restored, got %#v", artifacts)
	}
	approval, ok := restored.CurrentApproval(run.RunID)
	if !ok || !containsString(approval.Options, "keep_downstream") || !containsString(approval.Options, "regenerate_downstream") {
		t.Fatalf("expected migrated downstream review approval, got %#v ok=%v", approval, ok)
	}
	restoredRun, _ := restored.GetRun(run.RunID)
	if pendingDownstreamReviewFromRun(restoredRun) == nil {
		t.Fatal("expected migrated pending downstream review metadata")
	}
}

type retryEpisodeWorker struct {
	notes []string
}

func (w *retryEpisodeWorker) Plan(ctx context.Context, run agent.Run, source agent.Artifact) ([]PlannedArtifact, *agent.ApprovalRequest, error) {
	return nil, nil, nil
}

func (w *retryEpisodeWorker) WriteScripts(ctx context.Context, run agent.Run, artifacts []agent.Artifact, note string) ([]PlannedArtifact, error) {
	return nil, nil
}

func (w *retryEpisodeWorker) PlanStep(ctx context.Context, run agent.Run, source agent.Artifact, existing []agent.Artifact, artifactType string) (PlannedArtifact, *agent.ApprovalRequest, error) {
	return PlannedArtifact{}, nil, nil
}

func (w *retryEpisodeWorker) WriteScriptStep(ctx context.Context, run agent.Run, artifacts []agent.Artifact, artifactType string, note string) (PlannedArtifact, error) {
	w.notes = append(w.notes, note)
	episodeID := 0
	for _, candidate := range []int{1, 2, 3, 4} {
		if strings.Contains(note, "episode_id="+string(rune('0'+candidate))) {
			episodeID = candidate
			break
		}
	}
	return PlannedArtifact{
		ArtifactType: "script_unit",
		Status:       agent.ArtifactConfirmed,
		Payload: map[string]any{
			"episode_id": episodeID,
			"title":      "episode",
			"scenes":     []any{},
		},
	}, nil
}

func TestRerunScriptUnitResumesFromFailedEpisodeTask(t *testing.T) {
	worker := &retryEpisodeWorker{}
	runtime := NewRuntime(worker)
	run := agent.Run{
		RunID:         "run_retry_episode",
		ProjectID:     "project_retry_episode",
		SourceMode:    agent.SourceModeNonNovel,
		Status:        agent.RunFailed,
		CurrentStepID: "step_generate_script_unit",
		StartedAt:     time.Now().UTC(),
		Metadata: map[string]interface{}{
			"active_task": map[string]any{
				"task_id":       "task_generate_script_episode_3",
				"step_id":       "step_generate_script_unit",
				"artifact_type": "script_unit",
				"task_cursor":   2,
				"task_index":    3,
				"task_total":    4,
				"episode_id":    3,
			},
		},
	}
	source := runtime.artifact(run, "source_input", agent.ArtifactConfirmed, nil, map[string]any{"text": "source"})
	cards := runtime.artifact(run, "episode_cards", agent.ArtifactConfirmed, []string{source.ArtifactID}, map[string]any{
		"episodes": []any{
			map[string]any{"episode_id": 1},
			map[string]any{"episode_id": 2},
			map[string]any{"episode_id": 3},
			map[string]any{"episode_id": 4},
		},
	})
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{source, cards}

	resp, err := runtime.RerunStep(run.RunID, run.CurrentStepID, agent.RerunStepRequest{Reason: "retry"})
	if err != nil {
		t.Fatalf("expected rerun to start, got error: %v", err)
	}
	if resp.Run.Status != agent.RunRunning {
		t.Fatalf("expected run to be running after rerun, got %s", resp.Run.Status)
	}

	var snapshot agent.Run
	for attempt := 0; attempt < 20; attempt++ {
		snapshot, _ = runtime.GetRun(run.RunID)
		if snapshot.Status == agent.RunWaitingApproval {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if snapshot.Status != agent.RunWaitingApproval {
		t.Fatalf("expected rerun to reach approval, got %s", snapshot.Status)
	}
	if len(worker.notes) != 2 {
		t.Fatalf("expected only failed and following episodes to run, got %d notes: %#v", len(worker.notes), worker.notes)
	}
	if !strings.Contains(worker.notes[0], "episode_id=3") || !strings.Contains(worker.notes[1], "episode_id=4") {
		t.Fatalf("expected rerun from episode 3 onward, got %#v", worker.notes)
	}
}

func TestRerunFailedScriptPatchRetriesPatchTaskWithOriginalInstruction(t *testing.T) {
	worker := &targetedPatchWorker{patchResult: map[string]any{"__script_span_patch": map[string]any{
		"node_id": "line_1", "old_text": "old dialogue", "new_text": "stronger dialogue",
	}}}
	runtime := NewRuntime(worker)
	run := agent.Run{
		RunID: "run_retry_patch", ProjectID: "project_retry_patch", SourceMode: agent.SourceModeNovel,
		Status: agent.RunFailed, CurrentStepID: "step_generate_script_unit", StartedAt: time.Now().UTC(),
		Metadata: map[string]interface{}{
			"active_task": map[string]any{
				"task_id": "task_generate_script_unit_patch_request", "step_id": "step_generate_script_unit",
				"artifact_type": "script_unit", "task_cursor": 0, "task_index": 1, "task_total": 1,
			},
			"active_revision": map[string]any{
				"revision_intent": "patch_script_span", "artifact_type": "script_unit", "episode_id": "2", "scope": "selection",
			},
		},
	}
	target := runtime.artifact(run, "script_unit", agent.ArtifactConfirmed, nil, map[string]any{
		"episode_id": 2, "script_text": "old dialogue", "scenes": []any{map[string]any{
			"scene_id": "scene_1", "blocks": []any{map[string]any{"line_id": "line_1", "block_type": "dialogue", "text": "old dialogue"}},
		}},
	})
	instruction := `USER_REVISION_REQUEST:
strengthen the selected dialogue

REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"patch_script_span","target":{"artifact_id":"` + target.ArtifactID + `","artifact_type":"script_unit","episode_id":"2","scene_id":"scene_1","node_id":"line_1","scope":"selection"},"focused_context":{"episode_id":"2","scene_id":"scene_1","node_id":"line_1","selected_text":"old dialogue"},"target_artifact":{"payload":{"episode_id":2,"script_text":"old dialogue","scenes":[{"scene_id":"scene_1","blocks":[{"line_id":"line_1","block_type":"dialogue","text":"old dialogue"}]}]}}}`
	run.Metadata["active_revision_instruction"] = instruction
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{target}

	if _, err := runtime.RerunStep(run.RunID, run.CurrentStepID, agent.RerunStepRequest{Reason: "retry"}); err != nil {
		t.Fatalf("rerun failed patch: %v", err)
	}
	var snapshot agent.Run
	for attempt := 0; attempt < 20; attempt++ {
		snapshot, _ = runtime.GetRun(run.RunID)
		if snapshot.Status == agent.RunWaitingApproval || snapshot.Status == agent.RunFailed {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if snapshot.Status != agent.RunWaitingApproval {
		t.Fatalf("expected patch retry to reach approval, got %s", snapshot.Status)
	}
	if worker.patchCalls != 1 || worker.planCalls != 0 {
		t.Fatalf("expected only patch worker retry, patch=%d plan=%d", worker.patchCalls, worker.planCalls)
	}
	artifacts := runtime.Artifacts(run.RunID)
	latest := artifacts[len(artifacts)-1]
	if latest.Payload["script_text"] != "stronger dialogue" {
		t.Fatalf("patch retry changed the wrong scope: %#v", latest.Payload)
	}
	if _, exists := snapshot.Metadata["active_revision_instruction"]; exists {
		t.Fatalf("completed patch retry retained stale revision instruction")
	}
}

func TestRuntimeRestartMarksRunningTaskRecoverableAndKeepsCursor(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "runtime-state.json")
	worker := &retryEpisodeWorker{}
	runtime := NewRuntimeWithState(worker, statePath)
	run := agent.Run{
		RunID: "run_restart_recovery", ProjectID: "project_restart_recovery", SourceMode: agent.SourceModeNonNovel,
		Status: agent.RunRunning, CurrentStepID: "step_generate_script_unit", StartedAt: time.Now().UTC(),
		Metadata: map[string]interface{}{"active_task": map[string]any{
			"task_id": "task_generate_script_episode_3", "step_id": "step_generate_script_unit", "artifact_type": "script_unit",
			"task_cursor": 2, "task_index": 3, "task_total": 4, "episode_id": 3,
		}},
	}
	source := runtime.artifact(run, "source_input", agent.ArtifactConfirmed, nil, map[string]any{"text": "source"})
	cards := runtime.artifact(run, "episode_cards", agent.ArtifactConfirmed, []string{source.ArtifactID}, map[string]any{"episodes": []any{
		map[string]any{"episode_id": 1}, map[string]any{"episode_id": 2}, map[string]any{"episode_id": 3}, map[string]any{"episode_id": 4},
	}})
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{source, cards}
	runtime.saveLocked()

	restored := NewRuntimeWithState(worker, statePath)
	restoredRun, ok := restored.GetRun(run.RunID)
	if !ok || restoredRun.Status != agent.RunFailed {
		t.Fatalf("expected interrupted run to become failed, got %#v", restoredRun)
	}
	if failedStepIDFromRun(restoredRun) != "step_generate_script_unit" || taskCursorFromRun(restoredRun) != 2 {
		t.Fatalf("expected failed step and cursor to survive restart, got %#v", restoredRun)
	}
	events := restored.Events(run.RunID)
	if len(events) == 0 || events[len(events)-1].Type != agent.EventStepFailed || events[len(events)-1].Payload["code"] != "SERVICE_RESTART_INTERRUPTED" {
		t.Fatalf("expected a recoverable restart event, got %#v", events)
	}

	if _, err := restored.RerunStep(run.RunID, restoredRun.CurrentStepID, agent.RerunStepRequest{Reason: "resume after restart"}); err != nil {
		t.Fatalf("expected recovered run to be retryable: %v", err)
	}
	for attempt := 0; attempt < 120; attempt++ {
		snapshot, _ := restored.GetRun(run.RunID)
		if snapshot.Status == agent.RunWaitingApproval {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(worker.notes) != 2 || !strings.Contains(worker.notes[0], "episode_id=3") || !strings.Contains(worker.notes[1], "episode_id=4") {
		t.Fatalf("expected retry to resume at episode 3, got %#v", worker.notes)
	}
}

func TestEpisodeCursorUsesRevisionContextTarget(t *testing.T) {
	instruction := `USER_REVISION_REQUEST:
重写这一集

REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"regenerate_script_episode","target":{"artifact_type":"script_unit","episode_id":"第3集","scope":"episode"}}`
	cursor := episodeCursorFromInstruction(instruction, []episodeTask{
		{EpisodeID: 1, Cursor: 0},
		{EpisodeID: 2, Cursor: 1},
		{EpisodeID: 3, Cursor: 2},
	})
	if cursor != 2 {
		t.Fatalf("expected cursor 2 from revision context, got %d", cursor)
	}
}

func TestMetadataOnlyRevisionDoesNotInvalidateDownstream(t *testing.T) {
	if revisionChangesDownstream(map[string]any{"field_path": "source_evidence[0]"}) {
		t.Fatal("source evidence metadata should not invalidate generated downstream content")
	}
	if !revisionChangesDownstream(map[string]any{"field_path": "characters[0].goal"}) {
		t.Fatal("character goal changes must remain downstream-affecting")
	}
}

func TestEpisodeSplitSourceCountIsDeterministic(t *testing.T) {
	source := agent.Artifact{ArtifactType: "source_input", Status: agent.ArtifactConfirmed, Payload: map[string]any{"text": "一二三\n四五"}}
	payload := map[string]any{"source_volume_assessment": map[string]any{"source_chars": 1200}}
	applyDeterministicSourceMetrics("episode_split", payload, []agent.Artifact{source})
	assessment := payload["source_volume_assessment"].(map[string]any)
	if assessment["source_chars"] != 6 {
		t.Fatalf("expected deterministic source count 6, got %#v", assessment["source_chars"])
	}
}

func TestRequestedFieldLengthRejectsMaterialOverrun(t *testing.T) {
	var pack revisionContextPack
	pack.Target.FieldPath = "story_overview.one_sentence_logline"
	payload := map[string]any{"story_overview": map[string]any{"one_sentence_logline": strings.Repeat("长", 186)}}
	if err := validateRequestedTextLength(payload, pack, "改成150字左右"); err == nil {
		t.Fatal("expected 186 characters to violate a 150-character request")
	}
	payload["story_overview"].(map[string]any)["one_sentence_logline"] = strings.Repeat("合", 158)
	if err := validateRequestedTextLength(payload, pack, "改成150字左右"); err != nil {
		t.Fatalf("expected bounded tolerance to accept 158 characters: %v", err)
	}
}

func TestReviseCheckpointUsesRevisionContextArtifactTarget(t *testing.T) {
	worker := &sequentialApprovalWorker{}
	runtime := NewRuntime(worker)
	run := agent.Run{
		RunID:         "run_revision_target",
		ProjectID:     "project_revision_target",
		SourceMode:    agent.SourceModeNovel,
		Status:        agent.RunCompleted,
		CurrentStepID: "step_completed",
		StartedAt:     time.Now().UTC(),
	}
	source := runtime.artifact(run, "source_input", agent.ArtifactConfirmed, nil, map[string]any{"text": "source"})
	storyBible := runtime.artifact(run, "story_bible", agent.ArtifactPendingApproval, []string{source.ArtifactID}, map[string]any{
		"characters": []any{map[string]any{"name": "主角", "goal": "旧目标"}},
	})
	cards := runtime.artifact(run, "episode_cards", agent.ArtifactPendingApproval, []string{storyBible.ArtifactID}, map[string]any{
		"episodes": []any{map[string]any{"episode_id": 1}},
	})
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{source, storyBible, cards}
	instruction := `USER_REVISION_REQUEST:
修改故事圣经里的主角目标

REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"patch_artifact_field","target":{"artifact_id":"` + storyBible.ArtifactID + `","artifact_type":"story_bible","field_path":"characters[0].goal","scope":"field"}}`

	resp, err := runtime.ReviseCheckpoint(run.RunID, instruction)
	if err != nil {
		t.Fatalf("expected revision to start, got %v", err)
	}
	if resp.Run.CurrentStepID != "step_build_story_bible" {
		t.Fatalf("expected story_bible step, got %s", resp.Run.CurrentStepID)
	}
}

func TestReviseCheckpointReplacesFailedLocalRevision(t *testing.T) {
	worker := &targetedPatchWorker{}
	runtime := NewRuntime(worker)
	run := agent.Run{
		RunID: "run_replace_failed_revision", ProjectID: "project_replace_failed_revision",
		SourceMode: agent.SourceModeNovel, Status: agent.RunFailed, CurrentStepID: "step_build_story_bible",
		StartedAt: time.Now().UTC(), Metadata: map[string]any{
			"active_revision": map[string]any{"revision_intent": "patch_artifact_field", "artifact_type": "story_bible"},
			"active_task":     map[string]any{"task_id": "old_failed_patch", "status": "running"},
		},
	}
	storyBible := runtime.artifact(run, "story_bible", agent.ArtifactConfirmed, nil, map[string]any{
		"theme": "keep", "characters": []any{map[string]any{"name": "主角", "goal": "旧目标"}},
	})
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{storyBible}
	instruction := `USER_REVISION_REQUEST:
修改主角目标

REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"patch_artifact_field","target":{"artifact_id":"` + storyBible.ArtifactID + `","artifact_type":"story_bible","field_path":"characters[0].goal","scope":"field"}}`

	resp, err := runtime.ReviseCheckpoint(run.RunID, instruction)
	if err != nil {
		t.Fatalf("expected a new local revision to replace the failed one: %v", err)
	}
	if resp.Run.Status != agent.RunRunning || resp.Run.CurrentStepID != "step_build_story_bible" {
		t.Fatalf("unexpected replacement run state: %#v", resp.Run)
	}
	if _, exists := resp.Run.Metadata["active_task"]; exists {
		t.Fatalf("old failed task leaked into replacement revision: %#v", resp.Run.Metadata["active_task"])
	}
}

func TestReviseCheckpointPatchFieldUsesPatchWorker(t *testing.T) {
	worker := &targetedPatchWorker{}
	runtime := NewRuntime(worker)
	run := agent.Run{
		RunID:             "run_revision_patch_worker",
		ProjectID:         "project_revision_patch_worker",
		SourceMode:        agent.SourceModeNovel,
		Status:            agent.RunWaitingApproval,
		CurrentStepID:     "step_approval_story_bible",
		ApprovalRequestID: "approval_story_bible",
		StartedAt:         time.Now().UTC(),
	}
	source := runtime.artifact(run, "source_input", agent.ArtifactConfirmed, nil, map[string]any{"text": "source"})
	storyBible := runtime.artifact(run, "story_bible", agent.ArtifactPendingApproval, []string{source.ArtifactID}, map[string]any{
		"theme": "keep theme",
		"characters": []any{
			map[string]any{"name": "keep name", "goal": "old goal"},
		},
	})
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{source, storyBible}
	runtime.approvals[run.ApprovalRequestID] = agent.ApprovalRequest{
		ApprovalRequestID: run.ApprovalRequestID,
		RunID:             run.RunID,
		StepID:            "step_approval_story_bible",
		ProposedAction:    map[string]any{"approved_artifact": "story_bible"},
		Status:            "pending",
	}

	instruction := `USER_REVISION_REQUEST:
only change goal

REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"patch_artifact_field","target":{"artifact_id":"` + storyBible.ArtifactID + `","artifact_type":"story_bible","field_path":"characters[0].goal","scope":"field"},"target_artifact":{"payload":{"theme":"keep theme","characters":[{"name":"keep name","goal":"old goal"}]}}}`

	if _, err := runtime.ReviseCheckpoint(run.RunID, instruction); err != nil {
		t.Fatalf("expected revision to start, got %v", err)
	}
	waitForRunStatus(t, runtime, run.RunID, agent.RunWaitingApproval)
	if worker.patchCalls != 1 {
		t.Fatalf("expected patch worker to be used once, got %d", worker.patchCalls)
	}
	if worker.planCalls != 0 {
		t.Fatalf("expected full PlanStep not to be used, got %d", worker.planCalls)
	}
	artifacts := runtime.Artifacts(run.RunID)
	latest := artifacts[len(artifacts)-1]
	if latest.ArtifactType != "story_bible" {
		t.Fatalf("expected latest story_bible, got %s", latest.ArtifactType)
	}
	characters, ok := latest.Payload["characters"].([]any)
	if !ok || len(characters) != 1 {
		t.Fatalf("expected one character, got %#v", latest.Payload["characters"])
	}
	character, ok := characters[0].(map[string]any)
	if !ok {
		t.Fatalf("expected character map, got %#v", characters[0])
	}
	if character["goal"] != "new goal" {
		t.Fatalf("expected patched goal, got %#v", character["goal"])
	}
	if character["name"] != "keep name" || latest.Payload["theme"] != "keep theme" {
		t.Fatalf("expected unrelated fields preserved, got %#v", latest.Payload)
	}
}

func TestReviseCheckpointFailsWhenPatchDoesNotChangeTarget(t *testing.T) {
	worker := &targetedPatchWorker{patchResult: map[string]any{"unrelated": "value"}}
	runtime := NewRuntime(worker)
	run := agent.Run{
		RunID: "run_revision_noop", ProjectID: "project_revision_noop", SourceMode: agent.SourceModeNovel,
		Status: agent.RunCompleted, CurrentStepID: "step_build_story_bible", StartedAt: time.Now().UTC(),
	}
	storyBible := runtime.artifact(run, "story_bible", agent.ArtifactConfirmed, nil, map[string]any{
		"theme": "keep theme", "characters": []any{map[string]any{"name": "main", "goal": "old goal"}},
	})
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{storyBible}
	instruction := `USER_REVISION_REQUEST:
change goal

REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"patch_artifact_field","target":{"artifact_id":"` + storyBible.ArtifactID + `","artifact_type":"story_bible","field_path":"characters[0].goal","scope":"field"},"target_artifact":{"payload":{"theme":"keep theme","characters":[{"name":"main","goal":"old goal"}]}}}`
	if _, err := runtime.ReviseCheckpoint(run.RunID, instruction); err != nil {
		t.Fatalf("expected async revision to start, got %v", err)
	}
	waitForRunStatus(t, runtime, run.RunID, agent.RunFailed)
	artifacts := runtime.Artifacts(run.RunID)
	if len(artifacts) != 1 || artifacts[0].Version != storyBible.Version || artifacts[0].Status != agent.ArtifactConfirmed {
		t.Fatalf("no-op patch must not create or supersede artifacts: %#v", artifacts)
	}
	events := runtime.Events(run.RunID)
	if len(events) == 0 || events[len(events)-1].Type != agent.EventStepFailed {
		t.Fatalf("expected explicit patch failure event, got %#v", events)
	}
}

func TestFailRunClassifiesModelTimeoutForUser(t *testing.T) {
	runtime := NewRuntime(nil)
	run := agent.Run{
		RunID: "run_model_timeout", ProjectID: "project_model_timeout", SourceMode: agent.SourceModeNovel,
		Status: agent.RunRunning, CurrentStepID: "step_build_story_bible", StartedAt: time.Now().UTC(),
		Metadata: map[string]any{"active_task": map[string]any{
			"task_id": "task_build_story_bible_model_request", "step_id": "step_build_story_bible", "task_cursor": 0, "task_total": 1,
		}},
	}
	runtime.runs[run.RunID] = run
	runtime.failRun(run.RunID, run.CurrentStepID, "Planning failed", fmt.Errorf("invoke model: status=504"))
	events := runtime.Events(run.RunID)
	failed := events[len(events)-1]
	if failed.Payload["error_code"] != "MODEL_TIMEOUT" {
		t.Fatalf("expected model timeout code, got %#v", failed.Payload)
	}
	if !strings.Contains(fmt.Sprint(failed.Payload["user_message"]), "模型服务响应超时") {
		t.Fatalf("expected user-facing timeout message, got %#v", failed.Payload)
	}
	current, _ := runtime.GetRun(run.RunID)
	if current.Status != agent.RunFailed || taskString(current.NextAction, "task_id") != "task_build_story_bible_model_request" {
		t.Fatalf("timeout must preserve failed task retry cursor: %#v", current)
	}
}

func TestFailureEventPayloadDoesNotMislabelBusinessFailure(t *testing.T) {
	payload := failureEventPayload(fmt.Errorf("revision patch produced no changes"))
	if _, exists := payload["error_code"]; exists {
		t.Fatalf("business failure was mislabeled as model timeout: %#v", payload)
	}
	if strings.Contains(fmt.Sprint(payload["error"]), "revision patch") {
		t.Fatalf("failure payload exposed internal error detail: %#v", payload)
	}
}

func TestFailureEventPayloadHidesProviderURLAndNodePath(t *testing.T) {
	payload := failureEventPayload(fmt.Errorf(`Post "https://model.internal/v1/chat": context deadline exceeded; node path: [invoke_model]`))
	serialized := fmt.Sprint(payload)
	if strings.Contains(serialized, "model.internal") || strings.Contains(serialized, "invoke_model") {
		t.Fatalf("failure payload exposed provider internals: %#v", payload)
	}
	if payload["error_code"] != "MODEL_TIMEOUT" {
		t.Fatalf("sanitized timeout lost its public error code: %#v", payload)
	}
}

func TestReviseCheckpointPreservesPendingApprovalForDifferentArtifact(t *testing.T) {
	runtime := NewRuntime(&targetedPatchWorker{patchResult: map[string]any{"text": "new"}})
	run := agent.Run{
		RunID: "run_cross_approval", ProjectID: "project_cross_approval", SourceMode: agent.SourceModeNovel,
		Status: agent.RunWaitingApproval, CurrentStepID: "step_approval_story_bible", ApprovalRequestID: "approval_story",
		StartedAt: time.Now().UTC(),
	}
	runtime.runs[run.RunID] = run
	runtime.approvals[run.ApprovalRequestID] = agent.ApprovalRequest{
		ApprovalRequestID: run.ApprovalRequestID, RunID: run.RunID, Status: "pending",
		ProposedAction: map[string]any{"approved_artifact": "story_bible"},
	}
	runtime.artifacts[run.RunID] = []agent.Artifact{
		runtime.artifact(run, "story_bible", agent.ArtifactPendingApproval, nil, map[string]any{"theme": "new"}),
		runtime.artifact(run, "script_unit", agent.ArtifactConfirmed, nil, map[string]any{"episode_id": 1, "script_text": "old"}),
	}

	instruction := `USER_REVISION_REQUEST:
change script

REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"patch_script_span","target":{"artifact_type":"script_unit","scope":"selection"}}`
	if _, err := runtime.ReviseCheckpoint(run.RunID, instruction); err == nil {
		t.Fatal("expected cross-artifact revision to be rejected")
	}
	current, _ := runtime.GetRun(run.RunID)
	if current.Status != agent.RunWaitingApproval || current.ApprovalRequestID != "approval_story" {
		t.Fatalf("pending approval was changed: %+v", current)
	}
	approval, ok := runtime.CurrentApproval(run.RunID)
	if !ok || approval.Status != "pending" {
		t.Fatalf("pending approval was lost: %#v", approval)
	}
}

func TestFailedRevisionRestoresOriginalApproval(t *testing.T) {
	worker := &targetedPatchWorker{patchResult: map[string]any{"unrelated": "value"}}
	runtime := NewRuntime(worker)
	run := agent.Run{
		RunID: "run_restore_approval", ProjectID: "project_restore_approval", SourceMode: agent.SourceModeNovel,
		Status: agent.RunWaitingApproval, CurrentStepID: "step_approval_story_bible", ApprovalRequestID: "approval_story",
		StartedAt: time.Now().UTC(),
	}
	storyBible := runtime.artifact(run, "story_bible", agent.ArtifactPendingApproval, nil, map[string]any{
		"characters": []any{map[string]any{"name": "main", "goal": "old"}},
	})
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{storyBible}
	runtime.approvals[run.ApprovalRequestID] = agent.ApprovalRequest{
		ApprovalRequestID: run.ApprovalRequestID, RunID: run.RunID, StepID: "step_approval_story_bible", Status: "pending",
		ProposedAction: map[string]any{"approved_artifact": "story_bible"},
	}
	instruction := `USER_REVISION_REQUEST:
change goal

REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"patch_artifact_field","target":{"artifact_id":"` + storyBible.ArtifactID + `","artifact_type":"story_bible","field_path":"characters[0].goal","scope":"field"},"target_artifact":{"payload":{"characters":[{"name":"main","goal":"old"}]}}}`

	if _, err := runtime.ReviseCheckpoint(run.RunID, instruction); err != nil {
		t.Fatalf("expected revision to start: %v", err)
	}
	waitForRunStatus(t, runtime, run.RunID, agent.RunWaitingApproval)
	current, _ := runtime.GetRun(run.RunID)
	if current.ApprovalRequestID != "approval_story" {
		t.Fatalf("original approval was not restored: %+v", current)
	}
	approval, ok := runtime.CurrentApproval(run.RunID)
	if !ok || approval.Status != "pending" {
		t.Fatalf("restored approval is not pending: %#v", approval)
	}
	if activeTaskFromRun(current) != nil || lastFailedTaskFromRun(current) == nil {
		t.Fatalf("restored revision did not close active task and retain retry state: %#v", current.Metadata)
	}
	if _, ok := current.Metadata["last_failed_revision_instruction"]; !ok {
		t.Fatalf("restored revision lost its original instruction: %#v", current.Metadata)
	}

	worker.patchResult = map[string]any{"characters": []any{map[string]any{"goal": "new"}}}
	if _, err := runtime.RerunStep(run.RunID, "step_build_story_bible", agent.RerunStepRequest{Reason: "重新试一下"}); err != nil {
		t.Fatalf("restored failed revision could not be retried: %v", err)
	}
	waitForRunStatus(t, runtime, run.RunID, agent.RunWaitingApproval)
	var latest agent.Artifact
	for _, artifact := range runtime.Artifacts(run.RunID) {
		if artifact.ArtifactType == "story_bible" && artifact.Status != agent.ArtifactSuperseded {
			latest = artifact
		}
	}
	characters, _ := latest.Payload["characters"].([]any)
	if len(characters) == 0 || characters[0].(map[string]any)["goal"] != "new" {
		t.Fatalf("retried revision did not create the requested version: %#v", latest)
	}
}

func TestReconcileOrphanedRevisionApproval(t *testing.T) {
	runtime := NewRuntime(nil)
	run := agent.Run{
		RunID: "run_orphaned", ProjectID: "project_orphaned", SourceMode: agent.SourceModeNovel,
		Status: agent.RunFailed, CurrentStepID: "step_generate_script_unit", StartedAt: time.Now().UTC(),
		Metadata: map[string]any{"pending_downstream_review": map[string]any{
			"source_artifact_type": "story_bible",
			"artifact_ids":         []any{"episode_split_1"},
			"artifact_types":       []any{"episode_split"},
		}},
	}
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{
		runtime.artifact(run, "story_bible", agent.ArtifactPendingApproval, nil, map[string]any{"theme": "new"}),
	}

	runtime.reconcileOrphanedRevisionApprovals()
	current, _ := runtime.GetRun(run.RunID)
	if current.Status != agent.RunWaitingApproval || current.ApprovalRequestID == "" {
		t.Fatalf("orphaned approval was not recovered: %+v", current)
	}
	approval, ok := runtime.CurrentApproval(run.RunID)
	if !ok || approval.ProposedAction["approved_artifact"] != "story_bible" {
		t.Fatalf("wrong recovered approval: %#v", approval)
	}
}

func TestRevisionPatchFieldPreservesUntargetedPayload(t *testing.T) {
	worker := &fieldPatchWorker{}
	runtime := NewRuntime(worker)
	run := agent.Run{
		RunID:      "run_patch_field",
		ProjectID:  "project_patch_field",
		SourceMode: agent.SourceModeNovel,
		Status:     agent.RunRunning,
		StartedAt:  time.Now().UTC(),
	}
	source := runtime.artifact(run, "source_input", agent.ArtifactConfirmed, nil, map[string]any{"text": "source"})
	storyBible := runtime.artifact(run, "story_bible", agent.ArtifactPendingApproval, []string{source.ArtifactID}, map[string]any{
		"theme": "keep theme",
		"characters": []any{
			map[string]any{"name": "keep name", "goal": "old goal"},
		},
	})
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{source, storyBible}

	instruction := `USER_REVISION_REQUEST:
only change goal

REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"patch_artifact_field","target":{"artifact_id":"` + storyBible.ArtifactID + `","artifact_type":"story_bible","field_path":"characters[0].goal","scope":"field"},"target_artifact":{"payload":{"theme":"keep theme","characters":[{"name":"keep name","goal":"old goal"}]}}}`

	runtime.generateArtifactStep(context.Background(), run.RunID, run, source, "story_bible", instruction, worker)
	artifacts := runtime.Artifacts(run.RunID)
	latest := artifacts[len(artifacts)-1]
	characters, ok := latest.Payload["characters"].([]any)
	if !ok || len(characters) != 1 {
		t.Fatalf("expected one character, got %#v", latest.Payload["characters"])
	}
	character, ok := characters[0].(map[string]any)
	if !ok {
		t.Fatalf("expected character map, got %#v", characters[0])
	}
	if character["goal"] != "new goal" {
		t.Fatalf("expected patched goal, got %#v", character["goal"])
	}
	if character["name"] != "keep name" {
		t.Fatalf("expected unrelated name to be preserved, got %#v", character["name"])
	}
	if latest.Payload["theme"] != "keep theme" {
		t.Fatalf("expected unrelated top-level field to be preserved, got %#v", latest.Payload["theme"])
	}
}

func TestRevisionPatchEntityPreservesSiblingEntities(t *testing.T) {
	worker := &entityPatchWorker{}
	runtime := NewRuntime(worker)
	run := agent.Run{
		RunID:      "run_patch_entity",
		ProjectID:  "project_patch_entity",
		SourceMode: agent.SourceModeNovel,
		Status:     agent.RunRunning,
		StartedAt:  time.Now().UTC(),
	}
	source := runtime.artifact(run, "source_input", agent.ArtifactConfirmed, nil, map[string]any{"text": "source"})
	storyBible := runtime.artifact(run, "story_bible", agent.ArtifactPendingApproval, []string{source.ArtifactID}, map[string]any{
		"theme": "keep theme",
		"characters": []any{
			map[string]any{"name": "main", "goal": "old goal", "voice": "old voice"},
			map[string]any{"name": "side", "goal": "side goal", "voice": "side voice"},
		},
	})
	runtime.runs[run.RunID] = run
	runtime.artifacts[run.RunID] = []agent.Artifact{source, storyBible}

	instruction := `USER_REVISION_REQUEST:
rewrite protagonist profile

REVISION_CONTEXT_PACK_JSON:
{"revision_intent":"patch_artifact_entity","target":{"artifact_id":"` + storyBible.ArtifactID + `","artifact_type":"story_bible","field_path":"characters[0].goal","scope":"entity"},"target_artifact":{"payload":{"theme":"keep theme","characters":[{"name":"main","goal":"old goal","voice":"old voice"},{"name":"side","goal":"side goal","voice":"side voice"}]}}}`

	runtime.generateArtifactStep(context.Background(), run.RunID, run, source, "story_bible", instruction, worker)
	artifacts := runtime.Artifacts(run.RunID)
	latest := artifacts[len(artifacts)-1]
	characters, ok := latest.Payload["characters"].([]any)
	if !ok || len(characters) != 2 {
		t.Fatalf("expected two characters, got %#v", latest.Payload["characters"])
	}
	mainCharacter, ok := characters[0].(map[string]any)
	if !ok {
		t.Fatalf("expected main character map, got %#v", characters[0])
	}
	sideCharacter, ok := characters[1].(map[string]any)
	if !ok {
		t.Fatalf("expected side character map, got %#v", characters[1])
	}
	if mainCharacter["goal"] != "new goal" || mainCharacter["voice"] != "new voice" {
		t.Fatalf("expected main character entity replacement, got %#v", mainCharacter)
	}
	if sideCharacter["goal"] != "side goal" || sideCharacter["voice"] != "side voice" {
		t.Fatalf("expected sibling character to be preserved, got %#v", sideCharacter)
	}
	if latest.Payload["theme"] != "keep theme" {
		t.Fatalf("expected unrelated top-level field to be preserved, got %#v", latest.Payload["theme"])
	}
}

type fieldPatchWorker struct{}

type targetedPatchWorker struct {
	patchCalls  int
	planCalls   int
	patchResult map[string]any
}

func (w *targetedPatchWorker) Plan(ctx context.Context, run agent.Run, source agent.Artifact) ([]PlannedArtifact, *agent.ApprovalRequest, error) {
	return nil, nil, nil
}

func (w *targetedPatchWorker) WriteScripts(ctx context.Context, run agent.Run, artifacts []agent.Artifact, note string) ([]PlannedArtifact, error) {
	return nil, nil
}

func (w *targetedPatchWorker) PlanStep(ctx context.Context, run agent.Run, source agent.Artifact, existing []agent.Artifact, artifactType string) (PlannedArtifact, *agent.ApprovalRequest, error) {
	w.planCalls++
	return PlannedArtifact{
		ArtifactType: artifactType,
		Status:       agent.ArtifactPendingApproval,
		Payload: map[string]any{
			"theme": "wrong theme",
			"characters": []any{
				map[string]any{"name": "wrong name", "goal": "wrong full rewrite"},
			},
		},
	}, nil, nil
}

func (w *targetedPatchWorker) WriteScriptStep(ctx context.Context, run agent.Run, artifacts []agent.Artifact, artifactType string, note string) (PlannedArtifact, error) {
	return PlannedArtifact{}, nil
}

func (w *targetedPatchWorker) PatchArtifactStep(ctx context.Context, run agent.Run, target agent.Artifact, existing []agent.Artifact, artifactType string, instruction string) (PlannedArtifact, error) {
	w.patchCalls++
	payload := w.patchResult
	if payload == nil {
		payload = map[string]any{
			"characters": []any{
				map[string]any{"goal": "new goal"},
			},
		}
	}
	return PlannedArtifact{
		ArtifactType: artifactType,
		Status:       agent.ArtifactPendingApproval,
		Payload:      payload,
	}, nil
}

func (w *fieldPatchWorker) Plan(ctx context.Context, run agent.Run, source agent.Artifact) ([]PlannedArtifact, *agent.ApprovalRequest, error) {
	return nil, nil, nil
}

func (w *fieldPatchWorker) WriteScripts(ctx context.Context, run agent.Run, artifacts []agent.Artifact, note string) ([]PlannedArtifact, error) {
	return nil, nil
}

func (w *fieldPatchWorker) PlanStep(ctx context.Context, run agent.Run, source agent.Artifact, existing []agent.Artifact, artifactType string) (PlannedArtifact, *agent.ApprovalRequest, error) {
	return PlannedArtifact{
		ArtifactType: artifactType,
		Status:       agent.ArtifactPendingApproval,
		Payload: map[string]any{
			"theme": "wrong theme",
			"characters": []any{
				map[string]any{"name": "wrong name", "goal": "new goal"},
			},
		},
	}, nil, nil
}

func (w *fieldPatchWorker) WriteScriptStep(ctx context.Context, run agent.Run, artifacts []agent.Artifact, artifactType string, note string) (PlannedArtifact, error) {
	return PlannedArtifact{}, nil
}

type entityPatchWorker struct{}

func (w *entityPatchWorker) Plan(ctx context.Context, run agent.Run, source agent.Artifact) ([]PlannedArtifact, *agent.ApprovalRequest, error) {
	return nil, nil, nil
}

func (w *entityPatchWorker) WriteScripts(ctx context.Context, run agent.Run, artifacts []agent.Artifact, note string) ([]PlannedArtifact, error) {
	return nil, nil
}

func (w *entityPatchWorker) PlanStep(ctx context.Context, run agent.Run, source agent.Artifact, existing []agent.Artifact, artifactType string) (PlannedArtifact, *agent.ApprovalRequest, error) {
	return PlannedArtifact{
		ArtifactType: artifactType,
		Status:       agent.ArtifactPendingApproval,
		Payload: map[string]any{
			"theme": "wrong theme",
			"characters": []any{
				map[string]any{"name": "main", "goal": "new goal", "voice": "new voice"},
			},
		},
	}, nil, nil
}

func (w *entityPatchWorker) WriteScriptStep(ctx context.Context, run agent.Run, artifacts []agent.Artifact, artifactType string, note string) (PlannedArtifact, error) {
	return PlannedArtifact{}, nil
}

type sequentialApprovalWorker struct {
	planned []string
}

type blockingDownstreamWorker struct {
	started chan struct{}
	release chan struct{}
}

func (w *blockingDownstreamWorker) Plan(ctx context.Context, run agent.Run, source agent.Artifact) ([]PlannedArtifact, *agent.ApprovalRequest, error) {
	return nil, nil, nil
}

func (w *blockingDownstreamWorker) WriteScripts(ctx context.Context, run agent.Run, artifacts []agent.Artifact, note string) ([]PlannedArtifact, error) {
	return nil, nil
}

func (w *blockingDownstreamWorker) PlanStep(ctx context.Context, run agent.Run, source agent.Artifact, existing []agent.Artifact, artifactType string) (PlannedArtifact, *agent.ApprovalRequest, error) {
	select {
	case <-w.started:
	default:
		close(w.started)
	}
	<-w.release
	return PlannedArtifact{ArtifactType: artifactType, Status: agent.ArtifactPendingApproval, Payload: map[string]any{"value": "regenerated"}}, nil, nil
}

func (w *blockingDownstreamWorker) WriteScriptStep(ctx context.Context, run agent.Run, artifacts []agent.Artifact, artifactType string, note string) (PlannedArtifact, error) {
	return PlannedArtifact{ArtifactType: artifactType, Status: agent.ArtifactConfirmed, Payload: map[string]any{}}, nil
}

func (w *sequentialApprovalWorker) Plan(ctx context.Context, run agent.Run, source agent.Artifact) ([]PlannedArtifact, *agent.ApprovalRequest, error) {
	return nil, nil, nil
}

func (w *sequentialApprovalWorker) WriteScripts(ctx context.Context, run agent.Run, artifacts []agent.Artifact, note string) ([]PlannedArtifact, error) {
	return nil, nil
}

func (w *sequentialApprovalWorker) PlanStep(ctx context.Context, run agent.Run, source agent.Artifact, existing []agent.Artifact, artifactType string) (PlannedArtifact, *agent.ApprovalRequest, error) {
	w.planned = append(w.planned, artifactType)
	return PlannedArtifact{
			ArtifactType: artifactType,
			Status:       agent.ArtifactPendingApproval,
			Payload: map[string]any{
				"type": artifactType,
			},
		}, &agent.ApprovalRequest{
			Title:  "confirm " + artifactType,
			Reason: "confirm " + artifactType,
			ProposedAction: map[string]any{
				"artifact_type": artifactType,
			},
			Options: []string{"approve", "pause"},
		}, nil
}

func (w *sequentialApprovalWorker) WriteScriptStep(ctx context.Context, run agent.Run, artifacts []agent.Artifact, artifactType string, note string) (PlannedArtifact, error) {
	w.planned = append(w.planned, artifactType)
	return PlannedArtifact{
		ArtifactType: artifactType,
		Status:       agent.ArtifactPendingApproval,
		Payload: map[string]any{
			"type": artifactType,
		},
	}, nil
}

func TestStartRunAsyncStopsAfterEachArtifactApproval(t *testing.T) {
	worker := &sequentialApprovalWorker{}
	runtime := NewRuntime(worker)

	resp, err := runtime.StartRunAsyncContext(context.Background(), agent.StartRunRequest{
		ProjectID:   "project_step_by_step",
		SourceMode:  agent.SourceModeNonNovel,
		UserMessage: "宫斗复仇短剧",
	})
	if err != nil {
		t.Fatalf("expected async run to start, got error: %v", err)
	}

	run := waitForRunStatus(t, runtime, resp.Run.RunID, agent.RunWaitingApproval)
	if run.CurrentStepID != "step_approval_material_bank" {
		t.Fatalf("expected first approval at material bank step, got %s", run.CurrentStepID)
	}
	assertArtifactTypes(t, runtime.Artifacts(resp.Run.RunID), []string{"source_input", "material_bank"})

	_, err = runtime.ContinueRunAsyncContext(context.Background(), resp.Run.RunID, agent.ContinueRunRequest{Decision: "approve"})
	if err != nil {
		t.Fatalf("expected approval to continue run, got error: %v", err)
	}

	run = waitForRunStatus(t, runtime, resp.Run.RunID, agent.RunWaitingApproval)
	if run.CurrentStepID != "step_approval_story_seed" {
		t.Fatalf("expected second approval at story seed step, got %s", run.CurrentStepID)
	}
	assertArtifactTypes(t, runtime.Artifacts(resp.Run.RunID), []string{"source_input", "material_bank", "story_seed"})

	if len(worker.planned) != 2 || worker.planned[0] != "material_bank" || worker.planned[1] != "story_seed" {
		t.Fatalf("expected only first two artifacts to be planned after one approval, got %#v", worker.planned)
	}
}

func waitForRunStatus(t *testing.T, runtime *Runtime, runID string, status agent.RunStatus) agent.Run {
	t.Helper()
	for attempt := 0; attempt < 120; attempt++ {
		run, ok := runtime.GetRun(runID)
		if !ok {
			t.Fatalf("run not found: %s", runID)
		}
		if run.Status == status {
			return run
		}
		time.Sleep(20 * time.Millisecond)
	}
	run, _ := runtime.GetRun(runID)
	t.Fatalf("expected run status %s, got %s", status, run.Status)
	return agent.Run{}
}

type pauseResumeWorker struct {
	entered chan int
	calls   int
}

func (w *pauseResumeWorker) PlanStep(ctx context.Context, _ agent.Run, _ agent.Artifact, _ []agent.Artifact, artifactType string) (PlannedArtifact, *agent.ApprovalRequest, error) {
	w.calls++
	w.entered <- w.calls
	if w.calls == 1 {
		<-ctx.Done()
		return PlannedArtifact{}, nil, ctx.Err()
	}
	return PlannedArtifact{ArtifactType: artifactType, Status: agent.ArtifactPendingApproval, Payload: map[string]any{"explicit_user_material": []any{}}}, nil, nil
}

func (w *pauseResumeWorker) WriteScriptStep(context.Context, agent.Run, []agent.Artifact, string, string) (PlannedArtifact, error) {
	return PlannedArtifact{}, nil
}

func TestPauseRunningRunCancelsModelAndResumeKeepsCompletedState(t *testing.T) {
	worker := &pauseResumeWorker{entered: make(chan int, 2)}
	runtime := NewRuntime(worker)
	started, err := runtime.StartRunAsyncContext(context.Background(), agent.StartRunRequest{ProjectID: "project_pause", UserMessage: "素材", SourceMode: agent.SourceModeNonNovel})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-worker.entered:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	paused, _, err := runtime.PauseRunningRun(started.Run.RunID, "用户暂停")
	if err != nil {
		t.Fatal(err)
	}
	if paused.Status != agent.RunPaused {
		t.Fatalf("expected paused, got %s", paused.Status)
	}
	time.Sleep(30 * time.Millisecond)
	if current, _ := runtime.GetRun(started.Run.RunID); current.Status != agent.RunPaused {
		t.Fatalf("cancelled worker overwrote paused status: %s", current.Status)
	}
	resumed, _, err := runtime.ResumePausedRun(started.Run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != agent.RunRunning {
		t.Fatalf("expected running, got %s", resumed.Status)
	}
	select {
	case call := <-worker.entered:
		if call != 2 {
			t.Fatalf("expected second worker call, got %d", call)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not resume")
	}
	waitForRunStatus(t, runtime, started.Run.RunID, agent.RunWaitingApproval)
	if len(runtime.Artifacts(started.Run.RunID)) != 2 {
		t.Fatalf("expected source and generated artifact after resume")
	}
}

func TestResumeUserPausedApprovalReturnsToWaitingWithoutApproving(t *testing.T) {
	worker := &pauseResumeWorker{entered: make(chan int, 2), calls: 1}
	runtime := NewRuntime(worker)
	started, err := runtime.StartRunAsyncContext(context.Background(), agent.StartRunRequest{ProjectID: "project_pause_approval", UserMessage: "素材", SourceMode: agent.SourceModeNonNovel})
	if err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, runtime, started.Run.RunID, agent.RunWaitingApproval)
	before, _ := runtime.GetRun(started.Run.RunID)
	if before.ApprovalRequestID == "" {
		t.Fatal("expected a pending approval before pause")
	}
	pausedResponse, err := runtime.ContinueRun(started.Run.RunID, agent.ContinueRunRequest{Decision: "pause"})
	if err != nil {
		t.Fatal(err)
	}
	if pausedResponse.Run.Status != agent.RunPaused || pausedResponse.Run.Metadata["paused_by_user"] != true {
		t.Fatalf("approval pause must record user ownership: %#v", pausedResponse.Run)
	}
	resumed, _, err := runtime.ResumePausedRun(started.Run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != agent.RunWaitingApproval || resumed.ApprovalRequestID != before.ApprovalRequestID {
		t.Fatalf("resume must restore the same approval without approving it: %#v", resumed)
	}
	approval, ok := runtime.CurrentApproval(started.Run.RunID)
	if !ok || approval.Status != "pending" {
		t.Fatalf("approval must remain pending after resume: %#v", approval)
	}
}

func TestEpisodeTasksIgnoreSupersededAndInvalidatedCards(t *testing.T) {
	artifacts := []agent.Artifact{
		{ArtifactType: "episode_cards", Version: 1, Status: agent.ArtifactConfirmed, Payload: map[string]any{"episodes": []any{map[string]any{"episode_id": 1}}}},
		{ArtifactType: "episode_cards", Version: 2, Status: agent.ArtifactInvalidated, Payload: map[string]any{"episodes": []any{map[string]any{"episode_id": 9}}}},
	}
	tasks := episodeTasksFromArtifacts(artifacts)
	if len(tasks) != 1 || episodeNumber(tasks[0].EpisodeID) != 1 {
		t.Fatalf("expected current confirmed cards only, got %#v", tasks)
	}
}

func assertArtifactTypes(t *testing.T, artifacts []agent.Artifact, expected []string) {
	t.Helper()
	if len(artifacts) != len(expected) {
		t.Fatalf("expected %d artifacts, got %d: %#v", len(expected), len(artifacts), artifacts)
	}
	for index, artifact := range artifacts {
		if artifact.ArtifactType != expected[index] {
			t.Fatalf("expected artifact %d to be %s, got %s", index, expected[index], artifact.ArtifactType)
		}
	}
}
