package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"time"
	"unicode/utf8"

	"content-agent/backend/internal/capability"
)

const minimumSourceCharactersPerEpisodeMinute = 80

type runtimeStepOutcome struct {
	NextStepRunID   *string
	WaitingApproval bool
	Completed       bool
}

func (s *Store) executeRuntimeStepForResumeTx(
	ctx context.Context,
	tx *sql.Tx,
	run Run,
	definition *capability.CompiledDefinition,
	stepRunID string,
	step capability.CompiledStep,
	stepInputJSON string,
	now time.Time,
) (runtimeStepOutcome, error) {
	switch step.ExecutorRef {
	case "runtime.build_source_manifest":
		return s.executeSourceManifestBuildTx(
			ctx,
			tx,
			run,
			definition,
			stepRunID,
			step,
			stepInputJSON,
			now,
		)
	case "runtime.review_volume_fit":
		return s.executeVolumeFitReviewTx(
			ctx,
			tx,
			run,
			stepRunID,
			step,
			stepInputJSON,
			now,
		)
	case "runtime.build_script_contexts":
		return s.executeScriptContextsBuildTx(
			ctx,
			tx,
			run,
			definition,
			stepRunID,
			step,
			stepInputJSON,
			now,
		)
	case "runtime.aggregate_scripts":
		return s.executeScriptsAggregationTx(
			ctx,
			tx,
			run,
			stepRunID,
			step,
			stepInputJSON,
			now,
		)
	case "runtime.aggregate_reference_scripts":
		return s.executeReferenceScriptsAggregationTx(
			ctx, tx, run, stepRunID, step, stepInputJSON, now,
		)
	default:
		return runtimeStepOutcome{}, domainError(
			"RUN_STATE_CONFLICT",
			"当前 Runtime 步骤尚未实现执行器。",
		)
	}
}

func (s *Store) executeReferenceScriptsAggregationTx(
	ctx context.Context,
	tx *sql.Tx,
	run Run,
	stepRunID string,
	step capability.CompiledStep,
	stepInputJSON string,
	now time.Time,
) (runtimeStepOutcome, error) {
	if len(step.OutputRefs) != 1 ||
		step.OutputRefs[0].ArtifactType != "reference_scripts" ||
		step.OutputRefs[0].Cardinality != "one" ||
		step.OutputRefs[0].InitialStatus != "confirmed" || step.Approval.Required {
		return runtimeStepOutcome{}, domainError("CAPABILITY_VERSION_UNAVAILABLE", "参考剧本聚合步骤合同无效。")
	}
	var inputs []struct {
		ArtifactID        string `json:"artifact_id"`
		ArtifactVersionID string `json:"artifact_version_id"`
		Status            string `json:"status"`
		ScopeKey          string `json:"scope_key"`
	}
	if err := json.Unmarshal([]byte(stepInputJSON), &inputs); err != nil || len(inputs) == 0 {
		return runtimeStepOutcome{}, domainError("CONTEXT_REQUIRED_UPSTREAM_MISSING", "参考剧本聚合缺少已确认的逐集版本。")
	}
	type orderedInput struct {
		episodeOrder int
		artifactID   string
		versionID    string
		scopeKey     string
	}
	ordered := make([]orderedInput, 0, len(inputs))
	sourceSteps := make(map[string][]string)
	for _, input := range inputs {
		episodeOrder, err := episodeNumberFromScope(input.ScopeKey)
		if err != nil || input.ArtifactID == "" || input.ArtifactVersionID == "" || input.Status != "confirmed" {
			return runtimeStepOutcome{}, domainError("DEPENDENCY_INCOMPLETE", "参考剧本聚合输入缺少有效集序。")
		}
		var currentVersionID, artifactType, versionStatus, sourceStepID string
		if err := tx.QueryRowContext(ctx, `
			SELECT a.current_version_id, a.artifact_type, av.status, a.step_run_id
			FROM artifacts a JOIN artifact_versions av ON av.artifact_id = a.artifact_id
			WHERE a.artifact_id = ? AND av.artifact_version_id = ?
				AND a.project_id = ? AND a.run_id = ?`,
			input.ArtifactID, input.ArtifactVersionID, run.ProjectID, run.RunID,
		).Scan(&currentVersionID, &artifactType, &versionStatus, &sourceStepID); err != nil {
			return runtimeStepOutcome{}, err
		}
		if currentVersionID != input.ArtifactVersionID || artifactType != "video_script_unit" || versionStatus != "confirmed" {
			return runtimeStepOutcome{}, domainError("DEPENDENCY_INCOMPLETE", "参考剧本逐集版本已经变化。")
		}
		ordered = append(ordered, orderedInput{episodeOrder, input.ArtifactID, input.ArtifactVersionID, input.ScopeKey})
		sourceSteps[sourceStepID] = append(sourceSteps[sourceStepID], input.ArtifactVersionID)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].episodeOrder < ordered[j].episodeOrder })
	var runInputJSON string
	if err := tx.QueryRowContext(ctx, `
		SELECT payload_json FROM run_input_snapshot_versions
		WHERE run_input_snapshot_version_id = ? AND run_id = ? AND status = 'sealed'`,
		run.CurrentInputSnapshotVersionID, run.RunID,
	).Scan(&runInputJSON); err != nil {
		return runtimeStepOutcome{}, err
	}
	var source sourceInputRequest
	if err := json.Unmarshal([]byte(runInputJSON), &source); err != nil || source.AssetSetVersionID == "" {
		return runtimeStepOutcome{}, domainError("DEPENDENCY_INCOMPLETE", "参考剧本聚合缺少封存来源集合。")
	}
	set, err := getAssetSetSnapshotTx(ctx, tx, source.AssetSetID, source.AssetSetVersionID)
	if err != nil {
		return runtimeStepOutcome{}, err
	}
	unitRefs := make([]map[string]any, 0, len(ordered))
	episodeOrder := make([]int, 0, len(ordered))
	for _, input := range ordered {
		unitRefs = append(unitRefs, map[string]any{
			"episode_no": input.episodeOrder, "artifact_version_id": input.versionID,
		})
		episodeOrder = append(episodeOrder, input.episodeOrder)
	}
	continueIncomplete, err := sourceIncompleteMaterialConfirmedTx(ctx, tx, run.RunID)
	if err != nil {
		return runtimeStepOutcome{}, err
	}
	failedEpisodeSet := make(map[int]bool)
	for sourceStepID, versions := range sourceSteps {
		failedKeys, _, err := partialBatchContinuationForStepTx(ctx, tx, run.RunID, sourceStepID, versions)
		if err != nil {
			return runtimeStepOutcome{}, err
		}
		for _, key := range failedKeys {
			episodeNo, err := episodeNumberFromScope(key)
			if err != nil {
				return runtimeStepOutcome{}, err
			}
			failedEpisodeSet[episodeNo] = true
		}
	}
	for _, input := range ordered {
		delete(failedEpisodeSet, input.episodeOrder)
	}
	failedEpisodeNos := make([]int, 0, len(failedEpisodeSet))
	for episodeNo := range failedEpisodeSet {
		failedEpisodeNos = append(failedEpisodeNos, episodeNo)
	}
	sort.Ints(failedEpisodeNos)
	continueIncomplete = continueIncomplete || len(failedEpisodeNos) > 0
	completeness := "complete"
	if continueIncomplete {
		completeness = "incomplete"
	}
	payload, err := json.Marshal(map[string]any{
		"sealed_input_snapshot_id":          source.AssetSetVersionID,
		"episode_count":                     len(ordered),
		"unit_refs":                         unitRefs,
		"episode_order":                     episodeOrder,
		"missing_episode_nos":               set.Version.Completeness.MissingEpisodeNumbers,
		"failed_episode_nos":                failedEpisodeNos,
		"duplicate_episode_nos":             set.Version.Completeness.DuplicateEpisodeNumbers,
		"completeness":                      completeness,
		"continue_with_incomplete_material": continueIncomplete,
	})
	if err != nil {
		return runtimeStepOutcome{}, err
	}
	artifactID, versionID := s.newID("art"), s.newID("av")
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifacts(artifact_id, project_id, run_id, step_run_id, capability_id,
			artifact_type, scope_key, current_version_id, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, 'reference_scripts', 'singleton', ?, ?, ?)`,
		artifactID, run.ProjectID, run.RunID, stepRunID, run.CapabilityID,
		versionID, formatTime(now), formatTime(now)); err != nil {
		return runtimeStepOutcome{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifact_versions(artifact_version_id, artifact_id, version, status,
			payload_json, schema_id, schema_version, created_by_kind, actor_ref,
			creation_reason, created_at, confirmed_at)
		VALUES(?, ?, 1, 'confirmed', ?, 'reference_scripts', '1.0.0', 'runtime',
			'runtime.aggregate_reference_scripts', 'aggregation', ?, ?)`,
		versionID, artifactID, string(payload), formatTime(now), formatTime(now)); err != nil {
		return runtimeStepOutcome{}, err
	}
	for _, input := range ordered {
		if err := s.insertArtifactDependencyTx(ctx, tx, ArtifactDependency{
			ProjectID: run.ProjectID, RunID: run.RunID,
			DownstreamArtifactVersionID: versionID, UpstreamKind: "artifact_version",
			UpstreamRefID: input.versionID, Relation: "aggregates",
			UpstreamScope:   dependencyScope("episode", input.scopeKey),
			DownstreamScope: dependencyScope("artifact", "singleton"), ImpactPolicyID: "aggregate_member",
		}, now); err != nil {
			return runtimeStepOutcome{}, err
		}
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE step_runs SET status = 'completed', attempt_count = 1,
			started_at = COALESCE(started_at, ?), ended_at = ?
		WHERE step_run_id = ? AND status = 'pending'`,
		"参考剧本聚合步骤已经变化。", formatTime(now), formatTime(now), stepRunID); err != nil {
		return runtimeStepOutcome{}, err
	}
	var transitionFacts []string
	if continueIncomplete {
		transitionFacts = append(transitionFacts, "incomplete_material_confirmed")
	}
	nextStepRunID, completed, err := s.advanceRunAfterConfirmedArtifactTx(ctx, tx, run.RunID, stepRunID, versionID, now, transitionFacts...)
	if err != nil {
		return runtimeStepOutcome{}, err
	}
	runRef, stepRef := run.RunID, stepRunID
	for _, event := range []struct{ eventType, subjectType, subjectID string }{
		{"artifact.created", "artifact", artifactID},
		{"artifact.version_created", "artifact_version", versionID},
		{"step.completed", "step_run", stepRunID},
	} {
		if _, err := s.appendEvent(ctx, tx, run.ProjectID, &runRef, &stepRef,
			event.eventType, event.subjectType, event.subjectID, nil); err != nil {
			return runtimeStepOutcome{}, err
		}
	}
	return runtimeStepOutcome{NextStepRunID: nextStepRunID, Completed: completed}, nil
}

func (s *Store) executeScriptsAggregationTx(
	ctx context.Context,
	tx *sql.Tx,
	run Run,
	stepRunID string,
	step capability.CompiledStep,
	stepInputJSON string,
	now time.Time,
) (runtimeStepOutcome, error) {
	if len(step.OutputRefs) != 1 ||
		step.OutputRefs[0].ArtifactType != "scripts" ||
		step.OutputRefs[0].Cardinality != "one" ||
		step.OutputRefs[0].InitialStatus != "confirmed" ||
		step.Approval.Required {
		return runtimeStepOutcome{}, domainError(
			"CAPABILITY_VERSION_UNAVAILABLE",
			"剧本聚合步骤合同无效。",
		)
	}
	var inputs []struct {
		ArtifactID        string `json:"artifact_id"`
		ArtifactVersionID string `json:"artifact_version_id"`
		Version           int    `json:"version"`
		Status            string `json:"status"`
		ScopeKey          string `json:"scope_key"`
	}
	if err := json.Unmarshal([]byte(stepInputJSON), &inputs); err != nil ||
		len(inputs) == 0 {
		return runtimeStepOutcome{}, domainError(
			"CONTEXT_REQUIRED_UPSTREAM_MISSING",
			"剧本聚合缺少已确认的单集版本。",
		)
	}
	configPayload, err := stepConfigPayloadTx(ctx, tx, run, step)
	if err != nil {
		return runtimeStepOutcome{}, err
	}
	targetCount, err := targetEpisodeCount(configPayload)
	if err != nil {
		return runtimeStepOutcome{}, err
	}
	if len(inputs) != targetCount {
		return runtimeStepOutcome{}, domainError(
			"DEPENDENCY_INCOMPLETE",
			"剧本聚合的单集版本数量与目标集数不一致。",
		)
	}
	unitRefs := make([]map[string]any, 0, len(inputs))
	for index, input := range inputs {
		episodeNo, err := episodeNumberFromScope(input.ScopeKey)
		if err != nil || episodeNo != index+1 ||
			input.ArtifactID == "" ||
			input.ArtifactVersionID == "" ||
			input.Status != "confirmed" {
			return runtimeStepOutcome{}, domainError(
				"DEPENDENCY_INCOMPLETE",
				"剧本聚合输入不是完整、连续的已确认单集版本。",
			)
		}
		var currentVersionID, artifactType, versionStatus string
		if err := tx.QueryRowContext(ctx, `
			SELECT a.current_version_id, a.artifact_type, av.status
			FROM artifacts a
			JOIN artifact_versions av
				ON av.artifact_id = a.artifact_id
			WHERE a.artifact_id = ? AND av.artifact_version_id = ?
				AND a.project_id = ? AND a.run_id = ?`,
			input.ArtifactID,
			input.ArtifactVersionID,
			run.ProjectID,
			run.RunID,
		).Scan(&currentVersionID, &artifactType, &versionStatus); err != nil {
			return runtimeStepOutcome{}, err
		}
		if currentVersionID != input.ArtifactVersionID ||
			artifactType != "script_unit" ||
			versionStatus != "confirmed" {
			return runtimeStepOutcome{}, domainError(
				"DEPENDENCY_INCOMPLETE",
				"剧本聚合输入版本已经变化。",
			)
		}
		unitRefs = append(unitRefs, map[string]any{
			"episode_no":          episodeNo,
			"artifact_version_id": input.ArtifactVersionID,
		})
	}
	payload, err := json.Marshal(map[string]any{
		"episode_count": targetCount,
		"unit_refs":     unitRefs,
		"completeness":  "complete",
		"global_continuity_state": map[string]any{
			"source": "confirmed_script_units",
		},
		"quality_flags": []string{},
	})
	if err != nil {
		return runtimeStepOutcome{}, err
	}

	artifactID := ""
	baseVersionID := ""
	versionNumber := 1
	creationReason := "aggregation"
	createdArtifact := false
	err = tx.QueryRowContext(ctx, `
		SELECT artifact_id, current_version_id FROM artifacts
		WHERE run_id = ? AND artifact_type = 'scripts' AND scope_key = 'singleton'`,
		run.RunID).Scan(&artifactID, &baseVersionID)
	if errors.Is(err, sql.ErrNoRows) {
		artifactID = s.newID("art")
		createdArtifact = true
	} else if err != nil {
		return runtimeStepOutcome{}, err
	} else {
		versionNumber, err = nextArtifactVersionTx(ctx, tx, artifactID)
		if err != nil {
			return runtimeStepOutcome{}, err
		}
		creationReason = "regeneration"
	}
	versionID := s.newID("av")
	if createdArtifact {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO artifacts(
				artifact_id, project_id, run_id, step_run_id, capability_id, artifact_type,
				scope_key, current_version_id, created_at, updated_at
			) VALUES(?, ?, ?, ?, ?, 'scripts', 'singleton', ?, ?, ?)`,
			artifactID, run.ProjectID, run.RunID, stepRunID, run.CapabilityID, versionID,
			formatTime(now), formatTime(now)); err != nil {
			return runtimeStepOutcome{}, err
		}
	} else {
		if err := updateExactlyOne(ctx, tx, `
			UPDATE artifact_versions SET status = 'superseded'
			WHERE artifact_version_id = ? AND status IN ('confirmed', 'stale')`,
			"完整剧本版本已经变化。", baseVersionID); err != nil {
			return runtimeStepOutcome{}, err
		}
		if err := updateExactlyOne(ctx, tx, `
			UPDATE artifacts SET step_run_id = ?, current_version_id = ?, updated_at = ?
			WHERE artifact_id = ? AND current_version_id = ?`,
			"完整剧本产物已经变化。", stepRunID, versionID, formatTime(now), artifactID, baseVersionID); err != nil {
			return runtimeStepOutcome{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifact_versions(
			artifact_version_id, artifact_id, version, status, payload_json, schema_id,
			schema_version, created_by_kind, actor_ref, creation_reason,
			base_version_id, created_at, confirmed_at
		) VALUES(?, ?, ?, 'confirmed', ?, 'scripts', '1.0.0', 'runtime',
			'runtime.aggregate_scripts', ?, NULLIF(?, ''), ?, ?)`,
		versionID,
		artifactID,
		versionNumber,
		string(payload),
		creationReason,
		baseVersionID,
		formatTime(now),
		formatTime(now),
	); err != nil {
		return runtimeStepOutcome{}, err
	}
	for _, input := range inputs {
		if err := s.insertArtifactDependencyTx(ctx, tx, ArtifactDependency{
			ProjectID:                   run.ProjectID,
			RunID:                       run.RunID,
			DownstreamArtifactVersionID: versionID,
			UpstreamKind:                "artifact_version",
			UpstreamRefID:               input.ArtifactVersionID,
			Relation:                    "aggregates",
			UpstreamScope:               dependencyScope("episode", input.ScopeKey),
			DownstreamScope:             dependencyScope("artifact", "singleton"),
			ImpactPolicyID:              "aggregate_member",
		}, now); err != nil {
			return runtimeStepOutcome{}, err
		}
	}
	candidate, createdCandidate, err := s.createScriptCandidateTx(ctx, tx, run, versionID, now)
	if err != nil {
		return runtimeStepOutcome{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE step_runs
		SET status = 'completed', attempt_count = 1,
			started_at = COALESCE(started_at, ?), ended_at = ?
		WHERE step_run_id = ? AND status = 'pending'`,
		"剧本聚合步骤已经变化。",
		formatTime(now),
		formatTime(now),
		stepRunID,
	); err != nil {
		return runtimeStepOutcome{}, err
	}
	nextStepRunID, completed, err := s.advanceRunAfterConfirmedArtifactTx(
		ctx,
		tx,
		run.RunID,
		stepRunID,
		versionID,
		now,
	)
	if err != nil {
		return runtimeStepOutcome{}, err
	}
	runRef, stepRef := run.RunID, stepRunID
	events := []struct {
		eventType   string
		subjectType string
		subjectID   string
		payload     any
	}{
		{"artifact.version_created", "artifact_version", versionID, map[string]any{
			"version":         versionNumber,
			"creation_reason": creationReason,
		}},
		{"script_candidate.updated", "script_candidate", candidate.CandidateID, map[string]any{
			"scripts_artifact_version_id": versionID,
			"source_capability_id":        run.CapabilityID,
		}},
		{"step.completed", "step_run", stepRunID, nil},
	}
	if createdArtifact {
		events = append([]struct {
			eventType   string
			subjectType string
			subjectID   string
			payload     any
		}{{"artifact.created", "artifact", artifactID, map[string]any{"artifact_type": "scripts"}}}, events...)
	}
	if createdCandidate {
		for index := range events {
			if events[index].eventType == "script_candidate.updated" {
				events[index].eventType = "script_candidate.created"
			}
		}
	}
	for _, event := range events {
		if _, err := s.appendEvent(
			ctx,
			tx,
			run.ProjectID,
			&runRef,
			&stepRef,
			event.eventType,
			event.subjectType,
			event.subjectID,
			event.payload,
		); err != nil {
			return runtimeStepOutcome{}, err
		}
	}
	return runtimeStepOutcome{NextStepRunID: nextStepRunID, Completed: completed}, nil
}

func (s *Store) executeVolumeFitReviewTx(
	ctx context.Context,
	tx *sql.Tx,
	run Run,
	stepRunID string,
	step capability.CompiledStep,
	stepInputJSON string,
	now time.Time,
) (runtimeStepOutcome, error) {
	var inputVersions []struct {
		ArtifactVersionID string `json:"artifact_version_id"`
	}
	if err := json.Unmarshal([]byte(stepInputJSON), &inputVersions); err != nil ||
		len(inputVersions) != 1 ||
		inputVersions[0].ArtifactVersionID == "" {
		return runtimeStepOutcome{}, domainError(
			"CONTEXT_REQUIRED_UPSTREAM_MISSING",
			"体量判断缺少已确认的上游产物。",
		)
	}
	configPayload, err := stepConfigPayloadTx(ctx, tx, run, step)
	if err != nil {
		return runtimeStepOutcome{}, err
	}
	targetCount, err := targetEpisodeCount(configPayload)
	if err != nil {
		return runtimeStepOutcome{}, err
	}
	durationMinutes, err := episodeDurationMinutes(configPayload)
	if err != nil {
		return runtimeStepOutcome{}, err
	}
	sourceCharacters, err := s.sourceCharacterCountForVersionTx(
		ctx,
		tx,
		run.ProjectID,
		run.RunID,
		inputVersions[0].ArtifactVersionID,
	)
	if err != nil {
		return runtimeStepOutcome{}, err
	}
	requiredCharacters := int(float64(targetCount) *
		durationMinutes *
		minimumSourceCharactersPerEpisodeMinute)
	sufficient := sourceCharacters >= requiredCharacters
	cursorPayload := map[string]any{
		"volume_fit": map[string]any{
			"assessment_version":         "1.0.0",
			"source_characters":          sourceCharacters,
			"required_source_characters": requiredCharacters,
			"target_episode_count":       targetCount,
			"episode_duration_minutes":   durationMinutes,
			"result": map[bool]string{
				true:  "sufficient",
				false: "insufficient",
			}[sufficient],
		},
	}
	cursorJSON, err := json.Marshal(cursorPayload)
	if err != nil {
		return runtimeStepOutcome{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE step_runs
		SET task_cursor_json = ?, started_at = COALESCE(started_at, ?)
		WHERE step_run_id = ? AND status = 'pending'`,
		string(cursorJSON),
		formatTime(now),
		stepRunID,
	); err != nil {
		return runtimeStepOutcome{}, err
	}

	runRef, stepRef := run.RunID, stepRunID
	if sufficient {
		if err := updateExactlyOne(ctx, tx, `
			UPDATE step_runs SET status = 'completed', ended_at = ?
			WHERE step_run_id = ? AND status = 'pending'`,
			"体量判断步骤已经变化。",
			formatTime(now), stepRunID,
		); err != nil {
			return runtimeStepOutcome{}, err
		}
		nextStepRunID, completed, err := s.advanceRunAfterConfirmedArtifactTx(
			ctx,
			tx,
			run.RunID,
			stepRunID,
			inputVersions[0].ArtifactVersionID,
			now,
			"volume_fit_sufficient",
		)
		if err != nil {
			return runtimeStepOutcome{}, err
		}
		if _, err := s.appendEvent(
			ctx,
			tx,
			run.ProjectID,
			&runRef,
			&stepRef,
			"volume_fit.assessed",
			"step_run",
			stepRunID,
			cursorPayload,
		); err != nil {
			return runtimeStepOutcome{}, err
		}
		return runtimeStepOutcome{NextStepRunID: nextStepRunID, Completed: completed}, nil
	}

	approvalID := s.newID("apr")
	optionsJSON, err := json.Marshal(step.Approval.AllowedActions)
	if err != nil {
		return runtimeStepOutcome{}, err
	}
	subjectHash := approvalSubjectHash(stepRunID, 1)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO approvals(
			approval_request_id, project_id, run_id, step_run_id, scope, status,
			version, title, reason, options_json, subject_kind, subject_ref_id,
			subject_version, subject_snapshot_hash, requested_at
		) VALUES(?, ?, ?, ?, 'transition', 'pending', 1, ?, ?, ?,
			'transition', ?, 1, ?, ?)`,
		approvalID,
		run.ProjectID,
		run.RunID,
		stepRunID,
		"确认扩写策略",
		"现有材料体量明显不足以支撑目标集数和单集时长，请确认后再继续。",
		string(optionsJSON),
		stepRunID,
		subjectHash,
		formatTime(now),
	); err != nil {
		return runtimeStepOutcome{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE step_runs SET status = 'waiting_approval'
		WHERE step_run_id = ? AND status = 'pending'`,
		"体量判断步骤已经变化。",
		stepRunID,
	); err != nil {
		return runtimeStepOutcome{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE runs SET status = 'waiting_approval', updated_at = ?
		WHERE run_id = ? AND status = 'paused'`,
		"生成任务状态已经变化。",
		formatTime(now), run.RunID,
	); err != nil {
		return runtimeStepOutcome{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE projects
		SET version = version + 1, status = 'waiting_approval', updated_at = ?
		WHERE project_id = ? AND active_write_run_id = ?`,
		"作品写锁已经变化。",
		formatTime(now), run.ProjectID, run.RunID,
	); err != nil {
		return runtimeStepOutcome{}, err
	}
	for _, event := range []struct {
		eventType   string
		subjectType string
		subjectID   string
		payload     any
	}{
		{"volume_fit.assessed", "step_run", stepRunID, cursorPayload},
		{"approval.requested", "approval", approvalID, cursorPayload},
		{"run.waiting_approval", "run", run.RunID, nil},
	} {
		if _, err := s.appendEvent(
			ctx,
			tx,
			run.ProjectID,
			&runRef,
			&stepRef,
			event.eventType,
			event.subjectType,
			event.subjectID,
			event.payload,
		); err != nil {
			return runtimeStepOutcome{}, err
		}
	}
	return runtimeStepOutcome{WaitingApproval: true}, nil
}

func (s *Store) sourceCharacterCountForVersionTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	runID string,
	versionID string,
) (int, error) {
	var sourcePayload string
	err := tx.QueryRowContext(ctx, `
		WITH RECURSIVE ancestors(version_id, depth) AS (
			SELECT ?, 0
			UNION ALL
			SELECT d.upstream_ref_id, ancestors.depth + 1
			FROM artifact_dependencies d
			JOIN ancestors
				ON d.downstream_artifact_version_id = ancestors.version_id
			WHERE d.upstream_kind = 'artifact_version'
				AND ancestors.depth < 32
		)
		SELECT av.payload_json
		FROM ancestors
		JOIN artifact_versions av ON av.artifact_version_id = ancestors.version_id
		JOIN artifacts a ON a.artifact_id = av.artifact_id
		WHERE a.project_id = ? AND a.run_id = ? AND a.artifact_type = 'source_input'
		ORDER BY ancestors.depth ASC
		LIMIT 1`,
		versionID,
		projectID,
		runID,
	).Scan(&sourcePayload)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, domainError(
			"DEPENDENCY_INCOMPLETE",
			"体量判断找不到来源材料。",
		)
	}
	if err != nil {
		return 0, err
	}
	var source struct {
		Assets []struct {
			AssetID string `json:"asset_id"`
		} `json:"assets"`
		ArtifactVersions []struct {
			ArtifactVersionID string `json:"artifact_version_id"`
		} `json:"artifact_versions"`
	}
	if err := json.Unmarshal([]byte(sourcePayload), &source); err != nil {
		return 0, domainError("DEPENDENCY_INCOMPLETE", "来源材料产物无法读取。")
	}
	total := 0
	for _, sourceVersion := range source.ArtifactVersions {
		var payloadJSON string
		if err := tx.QueryRowContext(ctx, `
			SELECT av.payload_json
			FROM artifact_versions av
			JOIN artifacts a ON a.artifact_id = av.artifact_id
			WHERE av.artifact_version_id = ? AND a.project_id = ?`,
			sourceVersion.ArtifactVersionID,
			projectID,
		).Scan(&payloadJSON); err != nil {
			return 0, err
		}
		var payload any
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
			return 0, domainError("DEPENDENCY_INCOMPLETE", "来源产物无法读取。")
		}
		total += textualPayloadCharacterCount(payload)
	}
	for _, asset := range source.Assets {
		var storageRef string
		if err := tx.QueryRowContext(ctx, `
			SELECT ab.storage_ref
			FROM assets a
			JOIN asset_blobs ab ON ab.blob_id = a.original_blob_id
			WHERE a.asset_id = ? AND a.project_id = ? AND a.deleted_at IS NULL
				AND a.kind = 'text' AND a.status = 'available'
				AND ab.status = 'available'`,
			asset.AssetID,
			projectID,
		).Scan(&storageRef); err != nil {
			return 0, err
		}
		path, err := resolveDataPath(s.dataRoot, storageRef)
		if err != nil {
			return 0, err
		}
		content, err := os.ReadFile(path)
		if err != nil || !utf8.Valid(content) {
			return 0, domainError(
				"CONTEXT_ASSET_UNAVAILABLE",
				"体量判断无法读取有效的 UTF-8 来源文本。",
			)
		}
		total += utf8.RuneCount(content)
	}
	return total, nil
}

func textualPayloadCharacterCount(value any) int {
	switch typed := value.(type) {
	case string:
		return utf8.RuneCountInString(typed)
	case []any:
		total := 0
		for _, item := range typed {
			total += textualPayloadCharacterCount(item)
		}
		return total
	case map[string]any:
		total := 0
		for _, item := range typed {
			total += textualPayloadCharacterCount(item)
		}
		return total
	default:
		return 0
	}
}

func episodeDurationMinutes(config json.RawMessage) (float64, error) {
	var envelope struct {
		Payload struct {
			EpisodeDurationMinutes float64 `json:"episode_duration_minutes"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(config, &envelope); err != nil ||
		envelope.Payload.EpisodeDurationMinutes <= 0 {
		return 0, domainError("RUN_STATE_CONFLICT", "生成配置缺少单集时长。")
	}
	return envelope.Payload.EpisodeDurationMinutes, nil
}
