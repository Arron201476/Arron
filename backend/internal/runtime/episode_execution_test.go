package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
)

func TestInitialEpisodeExecutionMode(t *testing.T) {
	config := json.RawMessage(`{"config_ref":"creation","payload":{"episode_execution_mode":"review_each"}}`)
	if mode := initialEpisodeExecutionMode(config); mode != episodeExecutionModeReviewEach {
		t.Fatalf("initial mode = %q", mode)
	}
	if mode := initialEpisodeExecutionMode(json.RawMessage(`{"config_ref":"creation","payload":{}}`)); mode != episodeExecutionModeContinuous {
		t.Fatalf("default mode = %q", mode)
	}
}

func TestApprovalRevisionTargetPrefersScriptBodyWithinEpisodeCheckpoint(t *testing.T) {
	targets := []regenerationTarget{
		{Artifact: Artifact{ArtifactType: "script_handoff", ScopeKey: "episode:2"}},
		{Artifact: Artifact{ArtifactType: "script_unit", ScopeKey: "episode:2"}},
	}
	target, err := approvalRevisionTarget(targets, "把本集结尾的钩子调弱一些")
	if err != nil {
		t.Fatalf("approvalRevisionTarget() error = %v", err)
	}
	if target.Artifact.ArtifactType != "script_unit" || target.Artifact.ScopeKey != "episode:2" {
		t.Fatalf("revision target = %+v", target.Artifact)
	}
}

func TestSetEpisodeExecutionModeCreatesAuthoritativeSnapshot(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	_, initial := prepareStoryBibleForBatch(t, store, "逐集确认配置", fixedNovelFixtureContent(t))
	updated, err := store.SetEpisodeExecutionMode(ctx, SetEpisodeExecutionModeCommand{
		RunID: initial.Run.RunID,
		Mode:  episodeExecutionModeReviewEach,
	})
	if err != nil {
		t.Fatalf("SetEpisodeExecutionMode() error = %v", err)
	}
	if updated.Run.EpisodeExecutionMode != episodeExecutionModeReviewEach {
		t.Fatalf("updated mode = %q", updated.Run.EpisodeExecutionMode)
	}

	loaded, err := store.GetRun(ctx, initial.Run.RunID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if loaded.EpisodeExecutionMode != episodeExecutionModeReviewEach {
		t.Fatalf("loaded mode = %q", loaded.EpisodeExecutionMode)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM run_config_snapshots
		WHERE run_id = ? AND config_ref = 'execution_control' AND status = 'sealed'`,
		initial.Run.RunID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("execution control snapshots = %d, error = %v", count, err)
	}
}

func prepareEpisodeCheckpoint(t *testing.T, store *Store) (RunSnapshot, Approval, string, string) {
	t.Helper()
	ctx := context.Background()
	project, initial := prepareStoryBibleForBatch(t, store, "逐集确认状态机", fixedNovelFixtureContent(t))
	now := store.now()
	stepRunID := store.newID("step")
	scriptVersionID := store.newID("av")
	handoffVersionID := store.newID("av")
	if _, err := store.db.Exec(`UPDATE approvals SET status = 'approved', resolved_at = ? WHERE run_id = ? AND status = 'pending'`, formatTime(now), initial.Run.RunID); err != nil {
		t.Fatalf("close fixture approval: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE step_runs SET status = 'completed', ended_at = ? WHERE run_id = ? AND status = 'waiting_approval'`, formatTime(now), initial.Run.RunID); err != nil {
		t.Fatalf("close fixture step: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO step_runs(
		step_run_id, run_id, step_id, status, attempt_count, approval_policy,
		input_version_snapshot_json, task_cursor_json, started_at
	) VALUES(?, ?, 'generate_script_units', 'running', 1, 'batch_checkpoint', '[]', '{}', ?)`,
		stepRunID, initial.Run.RunID, formatTime(now)); err != nil {
		t.Fatalf("insert script step: %v", err)
	}
	for order, status := range []string{"succeeded", "pending"} {
		outputVersion := any(nil)
		if order == 0 {
			outputVersion = scriptVersionID
		}
		if _, err := store.db.Exec(`INSERT INTO task_items(
			task_item_id, step_run_id, run_id, item_key, item_order, status,
			attempt_count, input_snapshot_json, cursor_json, output_artifact_version_id,
			created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, '{}', '{}', ?, ?, ?)`,
			store.newID("tsk"), stepRunID, initial.Run.RunID, "episode:"+string(rune('1'+order)), order+1,
			status, order, outputVersion, formatTime(now), formatTime(now)); err != nil {
			t.Fatalf("insert task %d: %v", order+1, err)
		}
	}
	insertArtifact := func(artifactType, versionID string, payload json.RawMessage) {
		artifactID := store.newID("art")
		if _, err := store.db.Exec(`INSERT INTO artifacts(
			artifact_id, project_id, run_id, step_run_id, capability_id, artifact_type,
			scope_key, current_version_id, created_at, updated_at
		) VALUES(?, ?, ?, ?, 'novel_to_script', ?, 'episode:1', ?, ?, ?)`,
			artifactID, project.ProjectID, initial.Run.RunID, stepRunID, artifactType,
			versionID, formatTime(now), formatTime(now)); err != nil {
			t.Fatalf("insert %s artifact: %v", artifactType, err)
		}
		if _, err := store.db.Exec(`INSERT INTO artifact_versions(
			artifact_version_id, artifact_id, version, status, payload_json, schema_id,
			schema_version, created_by_kind, actor_ref, creation_reason, created_at
		) VALUES(?, ?, 1, 'pending_approval', ?, ?, '1.0.0', 'model', 'fixture', 'initial', ?)`,
			versionID, artifactID, string(payload), artifactType, formatTime(now)); err != nil {
			t.Fatalf("insert %s version: %v", artifactType, err)
		}
	}
	insertArtifact("script_unit", scriptVersionID, json.RawMessage(`{"episode_no":1,"title":"第一集","script_text":"第一集正文"}`))
	insertArtifact("script_handoff", handoffVersionID, json.RawMessage(`{"episode_no":1,"script_artifact_version_id":"`+scriptVersionID+`"}`))
	if _, err := store.db.Exec(`UPDATE runs SET current_step_run_id = ?, status = 'running', updated_at = ? WHERE run_id = ?`, stepRunID, formatTime(now), initial.Run.RunID); err != nil {
		t.Fatalf("activate script step: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE projects SET status = 'running', active_write_run_id = ? WHERE project_id = ?`, initial.Run.RunID, project.ProjectID); err != nil {
		t.Fatalf("activate project run: %v", err)
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin approval tx: %v", err)
	}
	step, err := store.compiledStepForRunTx(ctx, tx, initial.Run.RunID, stepRunID)
	if err != nil {
		t.Fatalf("compiled script step: %v", err)
	}
	approval, err := store.createEpisodeCheckpointApprovalTx(ctx, tx, project.ProjectID, initial.Run.RunID, stepRunID, "episode:1", "script_bundle", step, now)
	if err == nil {
		err = store.markStepWaitingApprovalTx(ctx, tx, project.ProjectID, initial.Run.RunID, stepRunID, approval, now)
	}
	if err != nil {
		tx.Rollback()
		t.Fatalf("create checkpoint approval: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit checkpoint approval: %v", err)
	}
	return initial, approval, scriptVersionID, handoffVersionID
}

func TestEpisodeCheckpointConfirmsOneEpisodeAndPausesBeforeNext(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, approval, scriptVersionID, handoffVersionID := prepareEpisodeCheckpoint(t, store)
	stepRunID := approval.StepRunID
	resolved, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
		ApprovalRequestID:       approval.ApprovalRequestID,
		Action:                  "approve",
		ExpectedApprovalVersion: approval.Version,
		SubjectSnapshotHash:     approval.SubjectSnapshotHash,
	})
	if err != nil {
		t.Fatalf("ResolveApproval(checkpoint) error = %v", err)
	}
	if resolved.Run.Status != "paused" || resolved.Run.CurrentStepRunID == nil || *resolved.Run.CurrentStepRunID != stepRunID {
		t.Fatalf("resolved run = %+v", resolved.Run)
	}
	var confirmed int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact_versions WHERE artifact_version_id IN (?, ?) AND status = 'confirmed'`, scriptVersionID, handoffVersionID).Scan(&confirmed); err != nil || confirmed != 2 {
		t.Fatalf("confirmed episode versions = %d, error = %v", confirmed, err)
	}
	var nextStatus string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM task_items WHERE step_run_id = ? AND item_key = 'episode:2'`, stepRunID).Scan(&nextStatus); err != nil || nextStatus != "pending" {
		t.Fatalf("next task status = %q, error = %v", nextStatus, err)
	}
}

func TestScriptBundleProgressionIncludesEpisodesConfirmedAcrossCheckpoints(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, initial := prepareStoryBibleForBatch(t, store, "逐集切自动聚合", fixedNovelFixtureContent(t))
	now := store.now()
	stepRunID := store.newID("step")
	var configJSON string
	if err := store.db.QueryRow(`
		SELECT payload_json FROM run_config_snapshots
		WHERE run_id = ? AND config_ref = 'creation'
		ORDER BY version DESC LIMIT 1`, initial.Run.RunID).Scan(&configJSON); err != nil {
		t.Fatalf("load creation config: %v", err)
	}
	var configEnvelope map[string]any
	if err := json.Unmarshal([]byte(configJSON), &configEnvelope); err != nil {
		t.Fatalf("decode creation config: %v", err)
	}
	configPayload, ok := configEnvelope["payload"].(map[string]any)
	if !ok {
		t.Fatalf("creation config payload = %#v", configEnvelope["payload"])
	}
	configPayload["target_episode_count"] = 2
	configJSONBytes, err := json.Marshal(configEnvelope)
	if err != nil {
		t.Fatalf("encode creation config: %v", err)
	}
	if _, err := store.db.Exec(`
		UPDATE run_config_snapshots SET payload_json = ?
		WHERE run_id = ? AND config_ref = 'creation'`,
		string(configJSONBytes), initial.Run.RunID); err != nil {
		t.Fatalf("update creation config: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE approvals SET status = 'approved', resolved_at = ? WHERE run_id = ? AND status = 'pending'`, formatTime(now), initial.Run.RunID); err != nil {
		t.Fatalf("close fixture approval: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE step_runs SET status = 'completed', ended_at = ? WHERE run_id = ? AND status = 'waiting_approval'`, formatTime(now), initial.Run.RunID); err != nil {
		t.Fatalf("close fixture step: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO step_runs(
		step_run_id, run_id, step_id, status, attempt_count, approval_policy,
		input_version_snapshot_json, task_cursor_json, started_at
	) VALUES(?, ?, 'generate_script_units', 'waiting_approval', 1, 'batch_checkpoint', '[]', '{}', ?)`,
		stepRunID, initial.Run.RunID, formatTime(now)); err != nil {
		t.Fatalf("insert script step: %v", err)
	}

	versionIDs := map[string]string{}
	for episodeNo := 1; episodeNo <= 2; episodeNo++ {
		scopeKey := fmt.Sprintf("episode:%d", episodeNo)
		scriptVersionID := store.newID("av")
		handoffVersionID := store.newID("av")
		contextVersionID := store.newID("av")
		versionIDs[fmt.Sprintf("script:%d", episodeNo)] = scriptVersionID
		versionIDs[fmt.Sprintf("handoff:%d", episodeNo)] = handoffVersionID
		versionIDs[fmt.Sprintf("context:%d", episodeNo)] = contextVersionID
		if _, err := store.db.Exec(`INSERT INTO task_items(
			task_item_id, step_run_id, run_id, item_key, item_order, status,
			attempt_count, input_snapshot_json, cursor_json, output_artifact_version_id,
			created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, 'succeeded', 1, '{}', '{}', ?, ?, ?)`,
			store.newID("tsk"), stepRunID, initial.Run.RunID, scopeKey, episodeNo,
			scriptVersionID, formatTime(now), formatTime(now)); err != nil {
			t.Fatalf("insert task %d: %v", episodeNo, err)
		}
		for _, output := range []struct {
			artifactType string
			versionID    string
			payload      string
			status       string
		}{
			{"script_unit", scriptVersionID, fmt.Sprintf(`{"episode_no":%d,"title":"第%d集","script_text":"第%d集正文"}`, episodeNo, episodeNo, episodeNo), map[bool]string{true: "confirmed", false: "pending_approval"}[episodeNo == 1]},
			{"script_handoff", handoffVersionID, fmt.Sprintf(`{"episode_no":%d,"script_artifact_version_id":"%s"}`, episodeNo, scriptVersionID), map[bool]string{true: "confirmed", false: "pending_approval"}[episodeNo == 1]},
			{"script_context", contextVersionID, fmt.Sprintf(`{"episode_no":%d}`, episodeNo), "confirmed"},
		} {
			artifactID := store.newID("art")
			if _, err := store.db.Exec(`INSERT INTO artifacts(
				artifact_id, project_id, run_id, step_run_id, capability_id, artifact_type,
				scope_key, current_version_id, created_at, updated_at
			) VALUES(?, ?, ?, ?, 'novel_to_script', ?, ?, ?, ?, ?)`,
				artifactID, project.ProjectID, initial.Run.RunID, stepRunID, output.artifactType,
				scopeKey, output.versionID, formatTime(now), formatTime(now)); err != nil {
				t.Fatalf("insert %s artifact: %v", output.artifactType, err)
			}
			if _, err := store.db.Exec(`INSERT INTO artifact_versions(
				artifact_version_id, artifact_id, version, status, payload_json, schema_id,
				schema_version, created_by_kind, actor_ref, creation_reason, created_at
			) VALUES(?, ?, 1, ?, ?, ?, '1.0.0', 'model', 'fixture', 'initial', ?)`,
				output.versionID, artifactID, output.status, output.payload, output.artifactType, formatTime(now)); err != nil {
				t.Fatalf("insert %s version: %v", output.artifactType, err)
			}
		}
	}
	if _, err := store.db.Exec(`UPDATE runs SET current_step_run_id = ?, status = 'waiting_approval', updated_at = ? WHERE run_id = ?`, stepRunID, formatTime(now), initial.Run.RunID); err != nil {
		t.Fatalf("activate script step: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE projects SET status = 'waiting_approval', active_write_run_id = ? WHERE project_id = ?`, initial.Run.RunID, project.ProjectID); err != nil {
		t.Fatalf("activate project run: %v", err)
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin approval tx: %v", err)
	}
	step, err := store.compiledStepForRunTx(ctx, tx, initial.Run.RunID, stepRunID)
	if err != nil {
		t.Fatalf("compiled script step: %v", err)
	}
	approval, err := store.createArtifactVersionSetApprovalTx(ctx, tx, project.ProjectID, initial.Run.RunID, stepRunID, "script_bundle", step, now)
	if err != nil {
		tx.Rollback()
		t.Fatalf("create final approval: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit final approval: %v", err)
	}

	resolved, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
		ApprovalRequestID:       approval.ApprovalRequestID,
		Action:                  "approve",
		ExpectedApprovalVersion: approval.Version,
		SubjectSnapshotHash:     approval.SubjectSnapshotHash,
	})
	if err != nil {
		t.Fatalf("ResolveApproval(final) error = %v", err)
	}
	if resolved.Run.Status != "running" || resolved.Run.CurrentStepRunID == nil {
		t.Fatalf("resolved run = %+v", resolved.Run)
	}
	var stepID, inputJSON string
	if err := store.db.QueryRowContext(ctx, `SELECT step_id, input_version_snapshot_json FROM step_runs WHERE step_run_id = ?`, *resolved.Run.CurrentStepRunID).Scan(&stepID, &inputJSON); err != nil {
		t.Fatalf("load quality review step: %v", err)
	}
	if stepID != "review_script_set" {
		t.Fatalf("next step = %q", stepID)
	}
	var inputs []struct {
		ArtifactVersionID string `json:"artifact_version_id"`
		ScopeKey          string `json:"scope_key"`
		Status            string `json:"status"`
	}
	if err := json.Unmarshal([]byte(inputJSON), &inputs); err != nil {
		t.Fatalf("decode aggregate inputs: %v", err)
	}
	if len(inputs) != 6 {
		t.Fatalf("quality review inputs = %+v", inputs)
	}
	for _, input := range inputs {
		if input.Status != "confirmed" {
			t.Fatalf("quality review input is not confirmed: %+v", input)
		}
	}
	for episodeNo := 1; episodeNo <= 2; episodeNo++ {
		for _, kind := range []string{"script", "handoff", "context"} {
			expectedID := versionIDs[fmt.Sprintf("%s:%d", kind, episodeNo)]
			found := false
			for _, input := range inputs {
				if input.ArtifactVersionID == expectedID && input.ScopeKey == fmt.Sprintf("episode:%d", episodeNo) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("missing %s episode %d input: %+v", kind, episodeNo, inputs)
			}
		}
	}
}
