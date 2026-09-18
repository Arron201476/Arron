package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"content-agent/backend/internal/capability"
)

type scriptContextInput struct {
	ArtifactID        string `json:"artifact_id"`
	ArtifactVersionID string `json:"artifact_version_id"`
	Version           int    `json:"version"`
	Status            string `json:"status"`
	ScopeKey          string `json:"scope_key"`
}

type scriptContextEpisodeCard struct {
	EpisodeNo       int            `json:"episode_no"`
	OpeningState    string         `json:"opening_state"`
	MainConflict    string         `json:"main_conflict"`
	KeyEvents       []string       `json:"key_events"`
	Payoff          string         `json:"payoff_or_reversal"`
	CharacterTurn   string         `json:"character_turn"`
	EndingHook      map[string]any `json:"ending_hook"`
	SourceBasis     map[string]any `json:"source_basis"`
	AdaptationNotes []string       `json:"adaptation_suggestions"`
	RiskNotes       []string       `json:"risk_notes"`
	Raw             map[string]any `json:"-"`
}

func (s *Store) executeScriptContextsBuildTx(
	ctx context.Context,
	tx *sql.Tx,
	run Run,
	definition *capability.CompiledDefinition,
	stepRunID string,
	step capability.CompiledStep,
	stepInputJSON string,
	now time.Time,
) (runtimeStepOutcome, error) {
	if len(step.OutputRefs) != 1 ||
		step.OutputRefs[0].ArtifactType != "script_context" ||
		step.OutputRefs[0].Cardinality != "many" ||
		step.OutputRefs[0].InitialStatus != "confirmed" ||
		step.Approval.Required {
		return runtimeStepOutcome{}, domainError(
			"CAPABILITY_VERSION_UNAVAILABLE",
			"剧本上下文步骤合同无效。",
		)
	}
	var inputs []scriptContextInput
	if err := json.Unmarshal([]byte(stepInputJSON), &inputs); err != nil ||
		len(inputs) != len(step.InputRefs) {
		return runtimeStepOutcome{}, domainError(
			"CONTEXT_REQUIRED_UPSTREAM_MISSING",
			"剧本上下文缺少完整的已确认输入。",
		)
	}

	var episodeCardsPayload json.RawMessage
	for _, input := range inputs {
		var artifactType, currentVersionID, versionStatus, payloadJSON string
		if err := tx.QueryRowContext(ctx, `
			SELECT a.artifact_type, a.current_version_id, av.status, av.payload_json
			FROM artifacts a
			JOIN artifact_versions av ON av.artifact_id = a.artifact_id
			WHERE a.artifact_id = ? AND av.artifact_version_id = ?
				AND a.project_id = ? AND a.run_id = ?`,
			input.ArtifactID,
			input.ArtifactVersionID,
			run.ProjectID,
			run.RunID,
		).Scan(
			&artifactType,
			&currentVersionID,
			&versionStatus,
			&payloadJSON,
		); err != nil {
			return runtimeStepOutcome{}, err
		}
		if input.Status != "confirmed" ||
			(versionStatus != "confirmed" && versionStatus != "superseded") {
			return runtimeStepOutcome{}, domainError(
				"CONTEXT_REQUIRED_UPSTREAM_MISSING",
				"剧本上下文输入版本尚未确认。",
			)
		}
		if artifactType == "episode_cards" {
			if currentVersionID != input.ArtifactVersionID ||
				versionStatus != "confirmed" {
				return runtimeStepOutcome{}, domainError(
					"CONTEXT_LINEAGE_CONFLICT",
					"分集卡当前版本已经变化。",
				)
			}
			episodeCardsPayload = json.RawMessage(payloadJSON)
		}
	}
	if len(episodeCardsPayload) == 0 {
		return runtimeStepOutcome{}, domainError(
			"CONTEXT_REQUIRED_UPSTREAM_MISSING",
			"剧本上下文缺少分集卡。",
		)
	}

	var cardsEnvelope struct {
		Episodes        []json.RawMessage `json:"episodes"`
		ContinuityDelta map[string]any    `json:"continuity_delta"`
	}
	if err := json.Unmarshal(episodeCardsPayload, &cardsEnvelope); err != nil {
		return runtimeStepOutcome{}, domainError(
			"CONTEXT_REQUIRED_UPSTREAM_MISSING",
			"分集卡产物无法读取。",
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
	if len(cardsEnvelope.Episodes) != targetCount {
		return runtimeStepOutcome{}, domainError(
			"BATCH_COVERAGE_INVALID",
			"分集卡数量与目标集数不一致。",
		)
	}
	generationConfig, userRequirements, err := normalizedScriptGenerationConfig(
		configPayload,
	)
	if err != nil {
		return runtimeStepOutcome{}, err
	}
	if definition == nil || strings.TrimSpace(definition.InputBinding.SourceType) == "" ||
		step.ResponseAdapterRef == nil || strings.TrimSpace(*step.ResponseAdapterRef) == "" {
		return runtimeStepOutcome{}, domainError(
			"CAPABILITY_VERSION_UNAVAILABLE",
			"剧本上下文步骤缺少来源或响应适配合同。",
		)
	}
	sourceKind := strings.TrimSpace(definition.InputBinding.SourceType)
	contextAdapterID := strings.TrimSpace(*step.ResponseAdapterRef)

	versionIDs := make([]string, 0, targetCount)
	for index, rawCard := range cardsEnvelope.Episodes {
		var card scriptContextEpisodeCard
		if err := json.Unmarshal(rawCard, &card); err != nil ||
			card.EpisodeNo != index+1 {
			return runtimeStepOutcome{}, domainError(
				"BATCH_COVERAGE_INVALID",
				"分集卡集号不连续。",
			)
		}
		if err := json.Unmarshal(rawCard, &card.Raw); err != nil {
			return runtimeStepOutcome{}, err
		}
		payload, err := buildScriptContextPayload(
			sourceKind,
			card,
			cardsEnvelope.ContinuityDelta,
			generationConfig,
			userRequirements,
		)
		if err != nil {
			return runtimeStepOutcome{}, err
		}
		wrappedPayload, err := json.Marshal(map[string]any{
			"script_context": json.RawMessage(payload),
		})
		if err != nil {
			return runtimeStepOutcome{}, err
		}
		payload, err = s.contracts.AdaptAndValidate(
			contextAdapterID,
			step.OutputRefs[0].SchemaRef,
			"script_context",
			wrappedPayload,
		)
		if err != nil {
			return runtimeStepOutcome{}, domainError(
				"OUTPUT_SCHEMA_VALIDATION_FAILED",
				fmt.Sprintf("第 %d 集剧本上下文未通过合同校验：%v", card.EpisodeNo, err),
			)
		}

		scopeKey := fmt.Sprintf("episode:%d", card.EpisodeNo)
		artifactID := s.newID("art")
		versionID := s.newID("av")
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO artifacts(
				artifact_id, project_id, run_id, step_run_id, capability_id,
				artifact_type, scope_key, current_version_id, created_at, updated_at
			) VALUES(?, ?, ?, ?, ?, 'script_context', ?, ?, ?, ?)`,
			artifactID,
			run.ProjectID,
			run.RunID,
			stepRunID,
			run.CapabilityID,
			scopeKey,
			versionID,
			formatTime(now),
			formatTime(now),
		); err != nil {
			if isUniqueConstraint(err) {
				return runtimeStepOutcome{}, domainError(
					"ARTIFACT_COMMIT_CONFLICT",
					"该集剧本上下文已经存在。",
				)
			}
			return runtimeStepOutcome{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO artifact_versions(
				artifact_version_id, artifact_id, version, status, payload_json,
				schema_id, schema_version, created_by_kind, actor_ref,
				creation_reason, created_at, confirmed_at
			) VALUES(?, ?, 1, 'confirmed', ?, 'script_context', '1.0.0',
				'system', 'content_agent_runtime', 'initial', ?, ?)`,
			versionID,
			artifactID,
			string(payload),
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
				Relation:                    "derived_from",
				UpstreamScope: dependencyScope(
					scopeKind(input.ScopeKey),
					input.ScopeKey,
				),
				DownstreamScope: dependencyScope("episode", scopeKey),
				ImpactPolicyID:  "scope_intersection",
			}, now); err != nil {
				return runtimeStepOutcome{}, err
			}
		}
		versionIDs = append(versionIDs, versionID)

		runRef, stepRef := run.RunID, stepRunID
		if _, err := s.appendEvent(
			ctx,
			tx,
			run.ProjectID,
			&runRef,
			&stepRef,
			"artifact.created",
			"artifact",
			artifactID,
			map[string]any{
				"artifact_type": "script_context",
				"scope_key":     scopeKey,
			},
		); err != nil {
			return runtimeStepOutcome{}, err
		}
		if _, err := s.appendEvent(
			ctx,
			tx,
			run.ProjectID,
			&runRef,
			&stepRef,
			"artifact.version_created",
			"artifact_version",
			versionID,
			map[string]any{
				"version":   1,
				"scope_key": scopeKey,
			},
		); err != nil {
			return runtimeStepOutcome{}, err
		}
	}

	if err := updateExactlyOne(ctx, tx, `
		UPDATE step_runs
		SET status = 'completed', attempt_count = 1,
			started_at = COALESCE(started_at, ?), ended_at = ?
		WHERE step_run_id = ? AND status = 'pending'`,
		"剧本上下文步骤已经变化。",
		formatTime(now),
		formatTime(now),
		stepRunID,
	); err != nil {
		return runtimeStepOutcome{}, err
	}
	nextStepRunID, completed, err := s.advanceRunAfterConfirmedArtifactsTx(
		ctx,
		tx,
		run.RunID,
		stepRunID,
		versionIDs,
		now,
	)
	if err != nil {
		return runtimeStepOutcome{}, err
	}
	runRef, stepRef := run.RunID, stepRunID
	if _, err := s.appendEvent(
		ctx,
		tx,
		run.ProjectID,
		&runRef,
		&stepRef,
		"step.completed",
		"step_run",
		stepRunID,
		map[string]any{"script_context_count": len(versionIDs)},
	); err != nil {
		return runtimeStepOutcome{}, err
	}
	return runtimeStepOutcome{NextStepRunID: nextStepRunID, Completed: completed}, nil
}

func stepConfigPayloadTx(
	ctx context.Context,
	tx *sql.Tx,
	run Run,
	step capability.CompiledStep,
) (json.RawMessage, error) {
	if step.ConfigRef == nil {
		return run.ConfigSnapshot, nil
	}
	configSnapshot, err := latestRunConfigSnapshotTx(
		ctx,
		tx,
		run.RunID,
		*step.ConfigRef,
	)
	if err != nil {
		return nil, err
	}
	return configSnapshot.Payload, nil
}

func normalizedScriptGenerationConfig(
	raw json.RawMessage,
) (map[string]any, []string, error) {
	var envelope struct {
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil ||
		envelope.Payload == nil {
		return nil, nil, domainError(
			"RUN_STATE_CONFLICT",
			"生成配置无法读取。",
		)
	}
	targetCount, ok := jsonNumberAsInt(envelope.Payload["target_episode_count"])
	if !ok || targetCount <= 0 {
		return nil, nil, domainError(
			"RUN_STATE_CONFLICT",
			"生成配置缺少目标集数。",
		)
	}
	duration, ok := envelope.Payload["episode_duration_minutes"].(float64)
	if !ok || duration <= 0 {
		return nil, nil, domainError(
			"RUN_STATE_CONFLICT",
			"生成配置缺少单集时长。",
		)
	}
	requirements := stringSliceValue(envelope.Payload["user_requirements"])
	config := map[string]any{
		"target_episode_count":     targetCount,
		"episode_duration_minutes": duration,
		"preserve_existing_episode_marks": scriptContextBoolValue(
			envelope.Payload["preserve_existing_episode_marks"],
			true,
		),
		"expansion_policy":  "confirm_if_needed",
		"user_requirements": requirements,
	}
	for _, key := range []string{
		"retention_profile",
		"adaptation_freedom",
		"compliance_mode",
		"policy_pack_version",
	} {
		if value, exists := envelope.Payload[key]; exists {
			config[key] = value
		}
	}
	return config, requirements, nil
}

func buildScriptContextPayload(
	sourceKind string,
	card scriptContextEpisodeCard,
	continuityDelta map[string]any,
	generationConfig map[string]any,
	userRequirements []string,
) (json.RawMessage, error) {
	mustFollow := appendUniqueStrings(
		nil,
		card.OpeningState,
		card.MainConflict,
	)
	mustFollow = append(mustFollow, card.KeyEvents...)
	mustFollow = appendUniqueStrings(
		mustFollow,
		card.Payoff,
		card.CharacterTurn,
		stringValue(card.EndingHook["hook_text"]),
	)
	allowedAdditions := append([]string{}, card.AdaptationNotes...)
	if generated, ok := card.SourceBasis["generated_additions"]; ok {
		allowedAdditions = append(
			allowedAdditions,
			stringSliceValue(generated)...,
		)
	}
	if continuityDelta == nil {
		continuityDelta = map[string]any{}
	}
	payload, err := json.Marshal(map[string]any{
		"source_kind":       sourceKind,
		"episode_no":        card.EpisodeNo,
		"generation_config": generationConfig,
		"must_follow_facts": mustFollow,
		"allowed_additions": allowedAdditions,
		"forbidden_changes": []string{
			"不得改变已确认的分集卡主冲突、关键事件与结尾钩子。",
		},
		"character_state":    map[string]any{},
		"relationship_state": map[string]any{},
		"continuity_state": map[string]any{
			"episode_card_delta": continuityDelta,
			"risk_notes":         card.RiskNotes,
		},
		"source_material": map[string]any{
			"text":         "",
			"refs":         []any{},
			"basis":        card.SourceBasis,
			"episode_card": card.Raw,
		},
		"style_constraints": []string{},
		"user_notes":        userRequirements,
	})
	if err != nil {
		return nil, err
	}
	return json.RawMessage(payload), nil
}

func jsonNumberAsInt(value any) (int, bool) {
	number, ok := value.(float64)
	if !ok || number != float64(int(number)) {
		return 0, false
	}
	return int(number), true
}

func scriptContextBoolValue(value any, fallback bool) bool {
	result, ok := value.(bool)
	if !ok {
		return fallback
	}
	return result
}

func stringSliceValue(value any) []string {
	items, ok := value.([]any)
	if !ok {
		if strings, ok := value.([]string); ok {
			return append([]string{}, strings...)
		}
		return []string{}
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok && text != "" {
			result = append(result, text)
		}
	}
	return result
}
