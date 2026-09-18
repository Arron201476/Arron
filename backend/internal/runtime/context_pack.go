package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"content-agent/backend/internal/capability"
)

const (
	stepExecutionContextVersion = "1.0.0"
	maxContextInputCharacters   = 400_000
	maxGemini3InputCharacters   = 3_000_000
	maxContextDocumentBytes     = 2 << 20
)

func contextInputCharacterLimit(modelID string) int {
	normalized := strings.ToLower(strings.TrimSpace(modelID))
	if strings.Contains(normalized, "gemini-3") {
		return maxGemini3InputCharacters
	}
	return maxContextInputCharacters
}

type stepContextBuildRequest struct {
	ProjectID         string
	ConversationID    string
	RunID             string
	RunKind           string
	CapabilityID      string
	CapabilityVersion string
	StepRunID         string
	StepID            string
	TaskItemID        string
	ItemKey           string
	ProviderID        string
	ModelID           string
	ConfigSnapshot    json.RawMessage
	ConfigSnapshotRef *RunConfigSnapshot
	TaskInputSnapshot json.RawMessage
	TaskCursor        json.RawMessage
	CreatedAt         time.Time
}

type taskInputVersionSnapshot struct {
	RunInputSnapshotVersionID string `json:"run_input_snapshot_version_id"`
	StepInputVersions         []struct {
		ArtifactID        string `json:"artifact_id"`
		ArtifactVersionID string `json:"artifact_version_id"`
		Version           int    `json:"version"`
		Status            string `json:"status"`
	} `json:"step_input_versions"`
}

type sourceInputPayload struct {
	UserRequestMessageID string                   `json:"user_request_message_id"`
	SourceKind           string                   `json:"source_kind"`
	Assets               []assetInputReference    `json:"assets"`
	ArtifactVersions     []artifactInputReference `json:"artifact_versions"`
}

func (s *Store) buildStepExecutionContextPack(
	ctx context.Context,
	tx *sql.Tx,
	request stepContextBuildRequest,
) (StepExecutionContextPack, error) {
	entry, ok, registryErr := s.capabilityEntryForRunQuery(
		ctx, tx, request.RunID, request.CapabilityID,
	)
	if registryErr != nil {
		return StepExecutionContextPack{}, registryErr
	}
	if !ok || entry.Status != capability.Available || entry.Definition == nil ||
		entry.Definition.Version != request.CapabilityVersion {
		return StepExecutionContextPack{}, domainError(
			"CONTEXT_CAPABILITY_MISMATCH",
			"Context Pack 对应的能力版本不可用。",
		)
	}
	var step *capability.CompiledStep
	for index := range entry.Definition.Steps {
		if entry.Definition.Steps[index].ID == request.StepID {
			step = &entry.Definition.Steps[index]
			break
		}
	}
	isQualityReview := step != nil && isScriptQualityReviewStep(*step)
	if step == nil || step.PromptRef == nil ||
		(!isQualityReview && len(step.OutputRefs) != 1 && !isScriptBundleStep(*step) &&
			!(entry.Skill != nil && capability.SupportsDirectSkillOutput(*step))) {
		return StepExecutionContextPack{}, domainError(
			"CONTEXT_BUILD_VERSION_MISSING",
			"当前步骤缺少可执行 Prompt 或输出合同。",
		)
	}

	var taskSnapshot taskInputVersionSnapshot
	if err := json.Unmarshal(request.TaskInputSnapshot, &taskSnapshot); err != nil {
		return StepExecutionContextPack{}, domainError(
			"CONTEXT_VERSION_NOT_FOUND",
			"Task 输入版本快照无法读取。",
		)
	}
	if taskSnapshot.RunInputSnapshotVersionID == "" ||
		(len(taskSnapshot.StepInputVersions) == 0 && len(step.InputRefs) != 0) {
		return StepExecutionContextPack{}, domainError(
			"CONTEXT_REQUIRED_UPSTREAM_MISSING",
			"Task 没有绑定完整的步骤输入版本。",
		)
	}
	var sealedInputSnapshotJSON string
	if err := tx.QueryRowContext(ctx, `
		SELECT payload_json
		FROM run_input_snapshot_versions
		WHERE run_input_snapshot_version_id = ? AND run_id = ? AND status = 'sealed'`,
		taskSnapshot.RunInputSnapshotVersionID,
		request.RunID,
	).Scan(&sealedInputSnapshotJSON); errors.Is(err, sql.ErrNoRows) {
		return StepExecutionContextPack{}, domainError(
			"CONTEXT_RUN_MISMATCH",
			"Task 绑定的 Run Input Snapshot 不存在或尚未封存。",
		)
	} else if err != nil {
		return StepExecutionContextPack{}, err
	}

	upstream := make([]ContextUpstreamArtifact, 0, len(taskSnapshot.StepInputVersions))
	assets := make([]ContextAsset, 0)
	requestMessageID := ""
	sourceInputSeen := false
	resolvedSourceArtifacts := 0
	var managedInput managedWorkflowInputSnapshot
	managedSnapshot := json.Unmarshal([]byte(sealedInputSnapshotJSON), &managedInput) == nil &&
		managedInput.SnapshotKind == managedWorkflowInputSnapshotKind
	if managedSnapshot {
		requestMessageID = managedInput.UserRequestMessageID
		resolvedAssets, err := s.resolveContextAssets(
			ctx, tx, request.ProjectID, managedInput.Assets, request.CreatedAt,
		)
		if err != nil {
			return StepExecutionContextPack{}, err
		}
		assets = append(assets, filterContextAssetsForTask(resolvedAssets, request.TaskCursor)...)
		sourceArtifacts, err := s.resolveExplicitSourceArtifacts(
			ctx, tx, request.ProjectID, managedInput.ArtifactVersions,
		)
		if err != nil {
			return StepExecutionContextPack{}, err
		}
		upstream = append(upstream, sourceArtifacts...)
		resolvedSourceArtifacts += len(sourceArtifacts)
	}
	for _, versionRef := range taskSnapshot.StepInputVersions {
		var artifactID, artifactType, scopeKey, versionStatus, payloadJSON string
		var storedVersion int
		err := tx.QueryRowContext(ctx, `
			SELECT a.artifact_id, a.artifact_type, a.scope_key, av.version,
				av.status, av.payload_json
			FROM artifacts a
			JOIN artifact_versions av ON av.artifact_id = a.artifact_id
			WHERE a.artifact_id = ? AND av.artifact_version_id = ?
				AND a.project_id = ? AND a.run_id = ?`,
			versionRef.ArtifactID,
			versionRef.ArtifactVersionID,
			request.ProjectID,
			request.RunID,
		).Scan(&artifactID, &artifactType, &scopeKey, &storedVersion, &versionStatus, &payloadJSON)
		if errors.Is(err, sql.ErrNoRows) {
			return StepExecutionContextPack{}, domainError(
				"CONTEXT_PROJECT_MISMATCH",
				"步骤输入版本不属于当前作品和 Run。",
			)
		}
		if err != nil {
			return StepExecutionContextPack{}, err
		}
		var newerConfirmed int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM artifact_versions
			WHERE artifact_id = ? AND version > ? AND status = 'confirmed'`,
			artifactID,
			storedVersion,
		).Scan(&newerConfirmed); err != nil {
			return StepExecutionContextPack{}, err
		}
		if (versionStatus != "confirmed" && versionStatus != "superseded") ||
			versionRef.Status != "confirmed" || newerConfirmed != 0 {
			return StepExecutionContextPack{}, domainError(
				"CONTEXT_UPSTREAM_VERSION_STALE",
				"步骤输入不是上游最新确认版本，任务必须重新规划。",
			)
		}
		if !stepAcceptsInput(step.InputRefs, artifactType) {
			return StepExecutionContextPack{}, domainError(
				"CONTEXT_LINEAGE_CONFLICT",
				"步骤输入版本类型不符合能力定义。",
			)
		}
		if !contextScopeMatchesTask(scopeKey, request.ItemKey) {
			continue
		}
		payload := json.RawMessage(payloadJSON)
		upstream = append(upstream, ContextUpstreamArtifact{
			ArtifactID:        artifactID,
			ArtifactVersionID: versionRef.ArtifactVersionID,
			ArtifactType:      artifactType,
			ScopeKey:          scopeKey,
			Status:            "confirmed",
			SelectionPolicy:   "approval_snapshot",
			Content:           payload,
			ContentHash:       sha256Hex(payload),
		})
		if artifactType == "source_input" {
			sourceInputSeen = true
			var source sourceInputPayload
			if err := json.Unmarshal(payload, &source); err != nil {
				return StepExecutionContextPack{}, domainError(
					"CONTEXT_REQUIRED_UPSTREAM_MISSING",
					"来源材料产物无法读取。",
				)
			}
			requestMessageID = source.UserRequestMessageID
			resolvedAssets, err := s.resolveContextAssets(
				ctx,
				tx,
				request.ProjectID,
				source.Assets,
				request.CreatedAt,
			)
			if err != nil {
				return StepExecutionContextPack{}, err
			}
			resolvedAssets = filterContextAssetsForTask(resolvedAssets, request.TaskCursor)
			assets = append(assets, resolvedAssets...)
			sourceArtifacts, err := s.resolveExplicitSourceArtifacts(
				ctx, tx, request.ProjectID, source.ArtifactVersions,
			)
			if err != nil {
				return StepExecutionContextPack{}, err
			}
			upstream = append(upstream, sourceArtifacts...)
			resolvedSourceArtifacts += len(sourceArtifacts)
		}
	}
	declaredUpstream := declaredContextUpstream(upstream)
	if err := validateContextUpstreamCoverage(step.InputRefs, declaredUpstream); err != nil {
		return StepExecutionContextPack{}, err
	}
	_, isHandoffRefresh := parseScriptHandoffRefreshCursor(request.TaskCursor)
	if step.StateTransition != nil && !isHandoffRefresh {
		continuity, err := s.adjacentStateOutputContextTx(ctx, tx, request, *step)
		if err != nil {
			return StepExecutionContextPack{}, err
		}
		upstream = append(upstream, continuity...)
	}
	if (sourceInputSeen || managedSnapshot) && len(assets) == 0 && resolvedSourceArtifacts == 0 {
		return StepExecutionContextPack{}, domainError(
			"CONTEXT_REQUIRED_UPSTREAM_MISSING",
			"当前步骤缺少可发送给 Worker 的来源内容。",
		)
	}
	if requestMessageID != "" {
		var requestMessageCount int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM messages
			WHERE message_id = ? AND project_id = ? AND conversation_id = ?`,
			requestMessageID,
			request.ProjectID,
			request.ConversationID,
		).Scan(&requestMessageCount); err != nil {
			return StepExecutionContextPack{}, err
		}
		if requestMessageCount != 1 {
			return StepExecutionContextPack{}, domainError(
				"CONTEXT_CONVERSATION_MISMATCH",
				"来源请求消息不属于当前作品会话。",
			)
		}
	}
	decisions := make([]ContextDecisionSnapshot, 0)
	decisionRows, err := tx.QueryContext(ctx, `
		SELECT decision_snapshot_id, decision_type, source_kind, source_ref_id,
			version, payload_json, snapshot_hash
		FROM run_decision_snapshots
		WHERE run_id = ? AND status = 'sealed'
		ORDER BY created_at ASC, decision_snapshot_id ASC`, request.RunID)
	if err != nil {
		return StepExecutionContextPack{}, err
	}
	for decisionRows.Next() {
		var decision ContextDecisionSnapshot
		var payloadJSON string
		if err := decisionRows.Scan(
			&decision.DecisionSnapshotID,
			&decision.DecisionType,
			&decision.SourceKind,
			&decision.SourceRefID,
			&decision.Version,
			&payloadJSON,
			&decision.SnapshotHash,
		); err != nil {
			decisionRows.Close()
			return StepExecutionContextPack{}, err
		}
		decision.Payload = json.RawMessage(payloadJSON)
		decisions = append(decisions, decision)
	}
	if err := decisionRows.Close(); err != nil {
		return StepExecutionContextPack{}, err
	}

	promptRef := *step.PromptRef
	var output capability.ArtifactOutput
	outputArtifactType := ""
	outputSchemaRef := ""
	operation := "generate_artifact"
	if isQualityReview {
		cursor, err := parseQualityReviewCursor(request.TaskCursor)
		if err != nil {
			return StepExecutionContextPack{}, err
		}
		operation = qualityReviewOperation
		if cursor.Review.Phase == "episode_batch" {
			outputArtifactType = "task_checkpoint:quality_review_batch"
			outputSchemaRef = "../../schemas/v1/quality-review-batch.schema.json"
		} else {
			promptRef = "../../design/prompts/shared/quality-review-global.v1.md"
			outputArtifactType = "task_checkpoint:quality_review_global"
			outputSchemaRef = "../../schemas/v1/quality-review.schema.json"
			checkpoints, err := s.qualityReviewBatchCheckpointsTx(ctx, tx, request.StepRunID)
			if err != nil {
				return StepExecutionContextPack{}, err
			}
			if len(checkpoints) == 0 {
				return StepExecutionContextPack{}, domainError(
					"CONTEXT_REQUIRED_UPSTREAM_MISSING",
					"质量审核全局任务缺少分集审核检查点。",
				)
			}
			upstream = append(upstream, checkpoints...)
		}
	} else {
		output = step.OutputRefs[0]
		outputArtifactType = output.ArtifactType
		outputSchemaRef = output.SchemaRef
	}
	if refreshCursor, refresh := parseScriptHandoffRefreshCursor(request.TaskCursor); refresh {
		if !isScriptBundleStep(*step) || len(step.OutputRefs) != 2 {
			return StepExecutionContextPack{}, domainError(
				"CONTEXT_LINEAGE_CONFLICT",
				"交接刷新游标不属于剧本生成步骤。",
			)
		}
		var artifactID, artifactType, scopeKey, currentVersionID, status, payloadJSON string
		err := tx.QueryRowContext(ctx, `
			SELECT a.artifact_id, a.artifact_type, a.scope_key,
				a.current_version_id, av.status, av.payload_json
			FROM artifacts a
			JOIN artifact_versions av
				ON av.artifact_version_id = a.current_version_id
			WHERE a.run_id = ? AND a.step_run_id = ?
				AND a.artifact_type = 'script_unit' AND a.scope_key = ?
				AND av.artifact_version_id = ?`,
			request.RunID,
			request.StepRunID,
			request.ItemKey,
			refreshCursor.ScriptArtifactVersionID,
		).Scan(
			&artifactID,
			&artifactType,
			&scopeKey,
			&currentVersionID,
			&status,
			&payloadJSON,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return StepExecutionContextPack{}, domainError(
				"ARTIFACT_VERSION_CONFLICT",
				"待刷新的剧本版本已经变化。",
			)
		}
		if err != nil {
			return StepExecutionContextPack{}, err
		}
		if currentVersionID != refreshCursor.ScriptArtifactVersionID ||
			status != "pending_approval" {
			return StepExecutionContextPack{}, domainError(
				"ARTIFACT_VERSION_CONFLICT",
				"待刷新的剧本不是当前待确认版本。",
			)
		}
		scriptPayload := json.RawMessage(payloadJSON)
		upstream = append(upstream, ContextUpstreamArtifact{
			ArtifactID:        artifactID,
			ArtifactVersionID: currentVersionID,
			ArtifactType:      artifactType,
			ScopeKey:          scopeKey,
			Status:            status,
			SelectionPolicy:   "current_manual_edit",
			Content:           scriptPayload,
			ContentHash:       sha256Hex(scriptPayload),
		})
		promptRef = "../../design/prompts/shared/script-handoff-refresh.v1.md"
		output = step.OutputRefs[1]
		outputArtifactType = output.ArtifactType
		outputSchemaRef = output.SchemaRef
		operation = "refresh_script_handoff"
	}
	if batchStage, ok := batchStageForCursor(step.Batch, request.TaskCursor); ok {
		promptRef = batchStage.PromptRef
		outputSchemaRef = batchStage.SchemaRef
		for index := range assets {
			assets[index].Content = ""
		}
		if batchStage.ResultMode != "artifact" {
			outputArtifactType = "task_checkpoint:" + batchStage.ID
			operation = "generate_task_checkpoint"
		}
	}
	prompt, err := loadContextDocument(
		entry.ContentRoot,
		entry.SourceFile,
		promptRef,
		"prompt:"+request.StepID,
	)
	if err != nil {
		return StepExecutionContextPack{}, err
	}
	var skillInstructions *ContextDocument
	if managedSnapshot && managedInput.SkillInstructions != "" {
		document := ContextDocument{
			ID:          "skill:" + managedInput.SkillName,
			Ref:         "skill:" + managedInput.SkillName,
			Content:     managedInput.SkillInstructions,
			ContentHash: sha256Hex([]byte(managedInput.SkillInstructions)),
		}
		skillInstructions = &document
	}
	rules := make([]ContextDocument, 0, len(step.RuleRefs))
	for _, ruleRef := range step.RuleRefs {
		document, err := loadContextDocument(
			entry.ContentRoot,
			entry.SourceFile,
			ruleRef,
			"rule:"+strings.TrimSuffix(filepath.Base(ruleRef), filepath.Ext(ruleRef)),
		)
		if err != nil {
			return StepExecutionContextPack{}, err
		}
		rules = append(rules, document)
	}
	outputContracts := make([]ContextOutputContract, 0, max(1, len(step.OutputRefs)))
	if _, internal := batchStageForCursor(step.Batch, request.TaskCursor); internal {
		outputContracts = make([]ContextOutputContract, 0, 1)
	}
	outputSchema, err := loadContextSchema(
		entry.ContentRoot,
		entry.SourceFile,
		outputSchemaRef,
	)
	if err != nil {
		return StepExecutionContextPack{}, err
	}
	outputContracts = append(outputContracts, ContextOutputContract{
		ArtifactType:  outputArtifactType,
		SchemaRef:     outputSchemaRef,
		SchemaVersion: "1.0.0",
		Schema:        outputSchema,
	})
	if !isQualityReview && len(step.OutputRefs) > 1 && len(outputContracts) == 1 &&
		outputArtifactType == step.OutputRefs[0].ArtifactType {
		for _, additional := range step.OutputRefs[1:] {
			schema, err := loadContextSchema(
				entry.ContentRoot,
				entry.SourceFile,
				additional.SchemaRef,
			)
			if err != nil {
				return StepExecutionContextPack{}, err
			}
			outputContracts = append(outputContracts, ContextOutputContract{
				ArtifactType:  additional.ArtifactType,
				SchemaRef:     additional.SchemaRef,
				SchemaVersion: "1.0.0",
				Schema:        schema,
			})
		}
	}
	var providerResultContract *ContextOutputContract
	if isQualityReview {
		providerResultContract = &ContextOutputContract{
			ArtifactType:  "provider_result",
			SchemaRef:     outputSchemaRef,
			SchemaVersion: "1.0.0",
			Schema:        outputSchema,
		}
	} else if step.ProviderResultSchemaRef != nil {
		resultSchema, err := loadContextSchema(
			entry.ContentRoot,
			entry.SourceFile,
			*step.ProviderResultSchemaRef,
		)
		if err != nil {
			return StepExecutionContextPack{}, err
		}
		providerResultContract = &ContextOutputContract{
			ArtifactType:  "provider_result",
			SchemaRef:     *step.ProviderResultSchemaRef,
			SchemaVersion: "1.0.0",
			Schema:        resultSchema,
		}
	}
	if entry.Skill != nil && capability.SupportsDirectSkillOutput(*step) {
		providerResultContract, err = directSkillStepResponseContract(outputContracts, providerResultContract)
		if err != nil {
			return StepExecutionContextPack{}, err
		}
	}

	taskCursor, err := s.withPreviousBatchResultsTx(ctx, tx, request, *step)
	if err != nil {
		return StepExecutionContextPack{}, err
	}
	if isVideoScriptExtractionStep(*step) {
		taskCursor, err = s.videoCharacterContinuityCursorTx(ctx, tx, request)
		if err != nil {
			return StepExecutionContextPack{}, err
		}
	}
	structuredRunState, err := s.materializeStructuredRunStateTx(
		ctx, tx, request, entry, *step,
	)
	if err != nil {
		return StepExecutionContextPack{}, err
	}

	requiredCharacters := utf8.RuneCount(request.ConfigSnapshot) +
		utf8.RuneCount(request.TaskInputSnapshot) +
		utf8.RuneCount(taskCursor) +
		utf8.RuneCountInString(prompt.Content)
	if managedSnapshot {
		requiredCharacters += utf8.RuneCount(managedInput.Input)
	}
	if skillInstructions != nil {
		requiredCharacters += utf8.RuneCountInString(skillInstructions.Content)
	}
	for _, contract := range outputContracts {
		requiredCharacters += utf8.RuneCount(contract.Schema)
	}
	if providerResultContract != nil {
		requiredCharacters += utf8.RuneCount(providerResultContract.Schema)
	}
	for _, asset := range assets {
		requiredCharacters += utf8.RuneCountInString(asset.Content)
	}
	for _, rule := range rules {
		requiredCharacters += utf8.RuneCountInString(rule.Content)
	}
	for _, item := range upstream {
		requiredCharacters += utf8.RuneCount(item.Content)
	}
	for _, decision := range decisions {
		requiredCharacters += utf8.RuneCount(decision.Payload)
	}
	if structuredRunState != nil {
		requiredCharacters += utf8.RuneCount(structuredRunState.Content)
	}
	maxInputCharacters := contextInputCharacterLimit(request.ModelID)
	if requiredCharacters > maxInputCharacters {
		return StepExecutionContextPack{}, domainError(
			"CONTEXT_REQUIRED_INPUT_EXCEEDS_BUDGET",
			fmt.Sprintf(
				"当前步骤需要 %d 字符，超过模型 Context Pack 上限 %d，必须拆分 Source Units。",
				requiredCharacters,
				maxInputCharacters,
			),
		)
	}

	provenance := make([]ContextProvenance, 0, len(assets)+len(upstream)+len(rules)+4)
	provenance = append(provenance, ContextProvenance{
		Kind:        "run_input_snapshot",
		Ref:         taskSnapshot.RunInputSnapshotVersionID,
		ContentHash: sha256Hex([]byte(sealedInputSnapshotJSON)),
	})
	for _, asset := range assets {
		provenance = append(provenance, ContextProvenance{
			Kind:        "asset_snapshot",
			Ref:         asset.AssetSnapshotID,
			ContentHash: asset.ContentHash,
		})
	}
	if managedSnapshot && managedInput.SkillContentHash != "" {
		provenance = append(provenance, ContextProvenance{
			Kind:        "skill_package",
			Ref:         "skill:" + managedInput.SkillName,
			ContentHash: managedInput.SkillContentHash,
		})
	}
	for _, item := range upstream {
		provenance = append(provenance, ContextProvenance{
			Kind:        "artifact_version",
			Ref:         item.ArtifactVersionID,
			ContentHash: item.ContentHash,
		})
	}
	if request.ConfigSnapshotRef != nil {
		provenance = append(provenance, ContextProvenance{
			Kind:        "config_snapshot",
			Ref:         request.ConfigSnapshotRef.ConfigSnapshotID,
			ContentHash: request.ConfigSnapshotRef.SnapshotHash,
		})
	}
	if structuredRunState != nil {
		provenance = append(provenance, ContextProvenance{
			Kind:        "artifact_version",
			Ref:         structuredRunState.ArtifactVersionID,
			ContentHash: structuredRunState.ContentHash,
		})
	}
	for _, decision := range decisions {
		provenance = append(provenance, ContextProvenance{
			Kind:        "decision_snapshot",
			Ref:         decision.DecisionSnapshotID,
			ContentHash: decision.SnapshotHash,
		})
	}
	for _, checkpoint := range previousBatchResultProvenance(taskCursor) {
		provenance = append(provenance, checkpoint)
	}
	provenance = append(provenance, ContextProvenance{
		Kind:        "prompt",
		Ref:         prompt.Ref,
		ContentHash: prompt.ContentHash,
	})
	if skillInstructions != nil {
		provenance = append(provenance, ContextProvenance{
			Kind:        "skill_instructions",
			Ref:         skillInstructions.Ref,
			ContentHash: skillInstructions.ContentHash,
		})
	}
	for _, rule := range rules {
		provenance = append(provenance, ContextProvenance{
			Kind:        "rule",
			Ref:         rule.Ref,
			ContentHash: rule.ContentHash,
		})
	}
	for _, contract := range outputContracts {
		provenance = append(provenance, ContextProvenance{
			Kind:        "output_schema",
			Ref:         contract.SchemaRef,
			ContentHash: sha256Hex(contract.Schema),
		})
	}
	if providerResultContract != nil {
		provenance = append(provenance, ContextProvenance{
			Kind:        "provider_result_schema",
			Ref:         providerResultContract.SchemaRef,
			ContentHash: sha256Hex(providerResultContract.Schema),
		})
	}

	pack := StepExecutionContextPack{
		ContextPackID:      s.newID("ctx"),
		ContextPackVersion: stepExecutionContextVersion,
		PackType:           "step_execution",
		ProjectID:          request.ProjectID,
		ConversationID:     request.ConversationID,
		RequestMessageID:   optionalContextString(requestMessageID),
		Capability: ContextCapabilityRef{
			CapabilityID:      request.CapabilityID,
			CapabilityVersion: request.CapabilityVersion,
		},
		Run: ContextRunRef{
			RunID:   request.RunID,
			RunKind: request.RunKind,
			Status:  "running",
		},
		Step: ContextStepRef{
			StepRunID:  request.StepRunID,
			StepID:     request.StepID,
			TaskItemID: request.TaskItemID,
		},
		Intent: ContextIntent{
			Operation:    operation,
			ArtifactType: outputArtifactType,
		},
		Target: ContextTarget{
			ScopeKey: request.ItemKey,
		},
		RunInputSnapshot:       managedInput.Input,
		InputVersionSnapshot:   request.TaskInputSnapshot,
		TaskCursor:             taskCursor,
		AssetContext:           assets,
		UpstreamContext:        upstream,
		StructuredRunState:     structuredRunState,
		ConfigSnapshotRef:      request.ConfigSnapshotRef,
		ConfigSnapshot:         request.ConfigSnapshot,
		DecisionSnapshots:      decisions,
		SkillInstructions:      skillInstructions,
		Prompt:                 prompt,
		Rules:                  rules,
		OutputContract:         outputContracts[0],
		OutputContracts:        outputContracts,
		ProviderResultContract: providerResultContract,
		Budget: ContextBudget{
			ProviderID:         request.ProviderID,
			ModelID:            optionalContextString(request.ModelID),
			MaxInputCharacters: maxInputCharacters,
			RequiredCharacters: requiredCharacters,
			Estimator:          "conservative_chars",
			TruncationApplied:  false,
		},
		Provenance: provenance,
		CreatedAt:  request.CreatedAt,
	}
	contextHash, err := calculateStepExecutionContextHash(pack)
	if err != nil {
		return StepExecutionContextPack{}, err
	}
	pack.ContextHash = contextHash
	return pack, nil
}

func (s *Store) resolveExplicitSourceArtifacts(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	references []artifactInputReference,
) ([]ContextUpstreamArtifact, error) {
	sorted := append([]artifactInputReference(nil), references...)
	sort.Slice(sorted, func(left, right int) bool {
		if sorted[left].Order == sorted[right].Order {
			return sorted[left].ArtifactVersionID < sorted[right].ArtifactVersionID
		}
		return sorted[left].Order < sorted[right].Order
	})
	result := make([]ContextUpstreamArtifact, 0, len(sorted))
	for _, reference := range sorted {
		var artifactID, artifactType, scopeKey, currentVersionID, status, payloadJSON string
		err := tx.QueryRowContext(ctx, `
			SELECT a.artifact_id, a.artifact_type, a.scope_key,
				a.current_version_id, av.status, av.payload_json
			FROM artifacts a
			JOIN artifact_versions av ON av.artifact_version_id = a.current_version_id
			WHERE a.project_id = ? AND av.artifact_version_id = ?`,
			projectID, reference.ArtifactVersionID,
		).Scan(&artifactID, &artifactType, &scopeKey, &currentVersionID, &status, &payloadJSON)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domainError(
				"CONTEXT_REQUIRED_UPSTREAM_MISSING",
				"来源产物版本不存在或已被替换。",
			)
		}
		if err != nil {
			return nil, err
		}
		if currentVersionID != reference.ArtifactVersionID || status != "confirmed" ||
			(artifactType != "generic_document" && artifactType != "generic_table") {
			return nil, domainError(
				"CONTEXT_UPSTREAM_VERSION_STALE",
				"来源产物不是当前已确认版本，任务必须重新配置。",
			)
		}
		payload := json.RawMessage(payloadJSON)
		result = append(result, ContextUpstreamArtifact{
			ArtifactID: artifactID, ArtifactVersionID: reference.ArtifactVersionID,
			ArtifactType: artifactType, ScopeKey: scopeKey, Status: "confirmed",
			SelectionPolicy: "explicit_source", Content: payload,
			ContentHash: sha256Hex(payload),
		})
	}
	return result, nil
}

func (s *Store) withPreviousBatchResultsTx(
	ctx context.Context,
	tx *sql.Tx,
	request stepContextBuildRequest,
	step capability.CompiledStep,
) (json.RawMessage, error) {
	if step.Batch == nil || step.Batch.MaxItemsPerTask <= 1 ||
		step.Batch.Execution != "sequential" || !strings.HasPrefix(request.ItemKey, "episode:") {
		return request.TaskCursor, nil
	}

	var cursor map[string]any
	if err := json.Unmarshal(request.TaskCursor, &cursor); err != nil {
		return nil, domainError("CONTEXT_VERSION_NOT_FOUND", "批处理任务游标无法读取。")
	}
	batch, ok := cursor["batch"].(map[string]any)
	if !ok {
		return request.TaskCursor, nil
	}

	previousTaskFilter := ""
	if step.StateTransition != nil && step.StateTransition.PreviousOutputPolicy == "adjacent" {
		// Stateful batches receive their long-term state separately. Only the
		// adjacent batch remains in full to preserve local continuity.
		previousTaskFilter = "AND ti.item_order = current.item_order - 1"
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT trc.task_result_checkpoint_id, ti.item_key, ti.item_order, trc.artifact_type,
			trc.payload_json, trc.payload_hash
		FROM task_items current
		JOIN task_items ti
			ON ti.step_run_id = current.step_run_id
			AND ti.item_order < current.item_order
		JOIN task_result_checkpoints trc ON trc.task_item_id = ti.task_item_id
		WHERE current.task_item_id = ?
			AND current.step_run_id = ?
			AND ti.status = 'succeeded'
			AND ti.item_key LIKE 'episode:%'
			`+previousTaskFilter+`
		ORDER BY ti.item_order ASC, trc.task_result_checkpoint_id ASC`,
		request.TaskItemID,
		request.StepRunID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	previous := make([]map[string]any, 0)
	for rows.Next() {
		var checkpointID, itemKey, artifactType, payloadJSON, payloadHash string
		var itemOrder int
		if err := rows.Scan(&checkpointID, &itemKey, &itemOrder, &artifactType, &payloadJSON, &payloadHash); err != nil {
			return nil, err
		}
		var payload any
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
			return nil, domainError("CONTEXT_VERSION_NOT_FOUND", "前序批次结果无法读取。")
		}
		previous = append(previous, map[string]any{
			"task_result_checkpoint_id": checkpointID,
			"item_key":                  itemKey,
			"item_order":                itemOrder,
			"artifact_type":             artifactType,
			"payload":                   payload,
			"payload_hash":              payloadHash,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(previous) == 0 {
		return request.TaskCursor, nil
	}

	batch["previous_batch_results"] = previous
	cursor["batch"] = batch
	return json.Marshal(cursor)
}

func previousBatchResultProvenance(cursor json.RawMessage) []ContextProvenance {
	var payload struct {
		Batch struct {
			Previous []struct {
				CheckpointID string `json:"task_result_checkpoint_id"`
				PayloadHash  string `json:"payload_hash"`
			} `json:"previous_batch_results"`
		} `json:"batch"`
	}
	if json.Unmarshal(cursor, &payload) != nil {
		return nil
	}
	result := make([]ContextProvenance, 0, len(payload.Batch.Previous))
	for _, checkpoint := range payload.Batch.Previous {
		if checkpoint.CheckpointID == "" || checkpoint.PayloadHash == "" {
			continue
		}
		result = append(result, ContextProvenance{
			Kind:        "task_result_checkpoint",
			Ref:         checkpoint.CheckpointID,
			ContentHash: checkpoint.PayloadHash,
		})
	}
	return result
}

func (s *Store) adjacentStateOutputContextTx(
	ctx context.Context,
	tx *sql.Tx,
	request stepContextBuildRequest,
	step capability.CompiledStep,
) ([]ContextUpstreamArtifact, error) {
	if step.StateTransition == nil || step.StateTransition.PreviousOutputPolicy != "adjacent" {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT a.artifact_id, a.current_version_id, a.artifact_type,
			a.scope_key, av.status, av.payload_json
		FROM task_items current
		JOIN task_items previous
			ON previous.step_run_id = current.step_run_id
			AND previous.item_order = current.item_order - 1
			AND previous.status = 'succeeded'
		JOIN artifacts a
			ON a.step_run_id = previous.step_run_id AND a.scope_key = previous.item_key
		JOIN artifact_versions av ON av.artifact_version_id = a.current_version_id
		WHERE current.task_item_id = ? AND current.step_run_id = ?
			AND av.status IN ('pending_approval', 'confirmed')
		ORDER BY a.artifact_type ASC`, request.TaskItemID, request.StepRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ContextUpstreamArtifact, 0, len(step.OutputRefs))
	for rows.Next() {
		var item ContextUpstreamArtifact
		var payloadJSON string
		if err := rows.Scan(
			&item.ArtifactID, &item.ArtifactVersionID, &item.ArtifactType,
			&item.ScopeKey, &item.Status, &payloadJSON,
		); err != nil {
			return nil, err
		}
		declared := false
		for _, output := range step.OutputRefs {
			declared = declared || output.ArtifactType == item.ArtifactType
		}
		if !declared {
			continue
		}
		item.SelectionPolicy = "state_adjacent"
		item.Content = json.RawMessage(payloadJSON)
		item.ContentHash = sha256Hex(item.Content)
		result = append(result, item)
	}
	return result, rows.Err()
}

func validateScriptExecutionContext(
	step capability.CompiledStep,
	pack StepExecutionContextPack,
) error {
	episodeNo, err := episodeNumberFromScope(pack.Target.ScopeKey)
	if err != nil {
		return err
	}
	declared := make([]ContextUpstreamArtifact, 0, len(pack.UpstreamContext))
	handoffCount := 0
	previousScriptCount := 0
	previousScope := fmt.Sprintf("episode:%d", episodeNo-1)
	for _, upstream := range pack.UpstreamContext {
		if upstream.SelectionPolicy != "sdk_context_candidate" && upstream.SelectionPolicy != "state_adjacent" {
			declared = append(declared, upstream)
			continue
		}
		switch upstream.ArtifactType {
		case "script_handoff":
			candidateEpisode, candidateErr := episodeNumberFromScope(upstream.ScopeKey)
			if candidateErr != nil || candidateEpisode >= episodeNo {
				return domainError(
					"CONTEXT_LINEAGE_CONFLICT",
					"剧集连续性候选包含无效的交接版本。",
				)
			}
			if upstream.ScopeKey == previousScope {
				handoffCount++
			}
		case "script_unit":
			candidateEpisode, candidateErr := episodeNumberFromScope(upstream.ScopeKey)
			if candidateErr != nil || candidateEpisode >= episodeNo {
				return domainError(
					"CONTEXT_LINEAGE_CONFLICT",
					"剧集连续性候选包含无效的剧本版本。",
				)
			}
			if upstream.ScopeKey == previousScope {
				previousScriptCount++
			}
		default:
			return domainError(
				"CONTEXT_LINEAGE_CONFLICT",
				"剧集连续性上下文包含不受支持的产物类型。",
			)
		}
	}
	expectedHandoffs := 0
	expectedPreviousScripts := 0
	if episodeNo > 1 {
		expectedHandoffs = 1
		expectedPreviousScripts = 1
	}
	if handoffCount != expectedHandoffs || previousScriptCount != expectedPreviousScripts {
		return domainError(
			"CONTEXT_REQUIRED_UPSTREAM_MISSING",
			"剧集连续性上下文缺少前序交接或上一集剧本。",
		)
	}
	return validateContextUpstreamCoverage(step.InputRefs, declared)
}

func contextScopeMatchesTask(scopeKey string, itemKey string) bool {
	if !strings.HasPrefix(scopeKey, "episode:") ||
		!strings.HasPrefix(itemKey, "episode:") {
		return true
	}
	if scopeKey == itemKey {
		return true
	}
	var episodeNo, start, end int
	if _, err := fmt.Sscanf(scopeKey, "episode:%d", &episodeNo); err != nil {
		return false
	}
	if _, err := fmt.Sscanf(itemKey, "episode:%d-%d", &start, &end); err != nil {
		return false
	}
	return episodeNo >= start && episodeNo <= end
}

func validateContextUpstreamCoverage(
	inputRefs []capability.ArtifactRef,
	upstream []ContextUpstreamArtifact,
) error {
	counts := make(map[string]int, len(inputRefs))
	allowed := make(map[string]capability.ArtifactRef, len(inputRefs))
	for _, ref := range inputRefs {
		allowed[ref.ArtifactType] = ref
	}
	for _, item := range upstream {
		if _, ok := allowed[item.ArtifactType]; !ok {
			return domainError(
				"CONTEXT_LINEAGE_CONFLICT",
				"步骤上下文包含未声明的输入产物。",
			)
		}
		counts[item.ArtifactType]++
	}
	for _, ref := range inputRefs {
		count := counts[ref.ArtifactType]
		switch ref.Cardinality {
		case "one":
			if count != 1 {
				return domainError(
					"CONTEXT_REQUIRED_UPSTREAM_MISSING",
					"步骤上下文缺少唯一输入产物。",
				)
			}
		case "many":
			if count == 0 {
				return domainError(
					"CONTEXT_REQUIRED_UPSTREAM_MISSING",
					"步骤上下文缺少批量输入产物。",
				)
			}
		default:
			return domainError(
				"CAPABILITY_VERSION_UNAVAILABLE",
				"步骤输入基数不受支持。",
			)
		}
	}
	return nil
}

func declaredContextUpstream(upstream []ContextUpstreamArtifact) []ContextUpstreamArtifact {
	declared := make([]ContextUpstreamArtifact, 0, len(upstream))
	for _, item := range upstream {
		if item.SelectionPolicy != "explicit_source" &&
			item.SelectionPolicy != "sdk_context_candidate" &&
			item.SelectionPolicy != "state_adjacent" {
			declared = append(declared, item)
		}
	}
	return declared
}

func calculateStepExecutionContextHash(pack StepExecutionContextPack) (string, error) {
	hashInput := pack
	hashInput.ContextPackID = ""
	hashInput.ContextHash = ""
	hashInput.CreatedAt = time.Time{}
	encoded, err := json.Marshal(hashInput)
	if err != nil {
		return "", err
	}
	return sha256Hex(encoded), nil
}

func (s *Store) resolveContextAssets(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	references []assetInputReference,
	now time.Time,
) ([]ContextAsset, error) {
	sorted := append([]assetInputReference(nil), references...)
	sort.Slice(sorted, func(left, right int) bool {
		if sorted[left].Order == sorted[right].Order {
			return sorted[left].AssetID < sorted[right].AssetID
		}
		return sorted[left].Order < sorted[right].Order
	})
	result := make([]ContextAsset, 0, len(sorted))
	for _, reference := range sorted {
		var assetProjectID, kind, filename, checksum, assetStatus string
		var snapshotStatus, storageRef, blobStatus string
		var expiresAt, deletedAt sql.NullString
		err := tx.QueryRowContext(ctx, `
			SELECT a.project_id, a.kind, a.original_filename, a.checksum, a.status,
				a.expires_at, a.deleted_at,
				ass.status, ab.storage_ref, ab.status
			FROM assets a
			JOIN asset_snapshots ass ON ass.asset_id = a.asset_id
			JOIN asset_blobs ab ON ab.blob_id = a.original_blob_id
			WHERE a.asset_id = ? AND ass.asset_snapshot_id = ?`,
			reference.AssetID,
			reference.AssetSnapshotID,
		).Scan(
			&assetProjectID,
			&kind,
			&filename,
			&checksum,
			&assetStatus,
			&expiresAt,
			&deletedAt,
			&snapshotStatus,
			&storageRef,
			&blobStatus,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domainError("CONTEXT_VERSION_NOT_FOUND", "材料快照不存在。")
		}
		if err != nil {
			return nil, err
		}
		if assetProjectID != projectID {
			return nil, domainError("CONTEXT_PROJECT_MISMATCH", "材料快照不属于当前作品。")
		}
		if assetStatus != "available" || snapshotStatus != "available" ||
			blobStatus != "available" || deletedAt.Valid {
			return nil, domainError("CONTEXT_ASSET_UNAVAILABLE", "材料当前不可用于模型执行。")
		}
		if expiresAt.Valid {
			expiry, err := parseTime(expiresAt.String)
			if err != nil {
				return nil, err
			}
			if !expiry.After(now) {
				return nil, domainError("CONTEXT_ASSET_EXPIRED", "材料已经过期，不能创建新的模型输入。")
			}
		}
		if kind != "text" && kind != "document" && kind != "video" {
			return nil, domainError(
				"CONTEXT_MODALITY_NOT_SUPPORTED",
				"当前内容生产步骤只接受文本或已解析文档。",
			)
		}
		var content []byte
		contentHash := ""
		if kind == "video" {
			contentHash = checksum
		} else if kind == "document" {
			var parseStatus, parsedText string
			if err := tx.QueryRowContext(ctx, `
				SELECT status, content_text, content_hash FROM asset_parse_results
				WHERE asset_id = ? AND asset_snapshot_id = ?`,
				reference.AssetID, reference.AssetSnapshotID,
			).Scan(&parseStatus, &parsedText, &contentHash); errors.Is(err, sql.ErrNoRows) {
				return nil, domainError("ASSET_PARSE_FAILED", "文档尚未生成可用的文本解析结果。")
			} else if err != nil {
				return nil, err
			}
			if parseStatus != "completed" || parsedText == "" || sha256Hex([]byte(parsedText)) != contentHash {
				return nil, domainError("ASSET_PARSE_FAILED", "文档文本解析失败或结果不完整。")
			}
			content = []byte(parsedText)
		} else {
			path, err := resolveDataPath(s.dataRoot, storageRef)
			if err != nil {
				return nil, err
			}
			content, err = os.ReadFile(path)
			if err != nil {
				return nil, domainError("CONTEXT_ASSET_UNAVAILABLE", "材料原文无法读取。")
			}
			contentHash = sha256Hex(content)
			if contentHash != checksum {
				return nil, domainError("CONTEXT_PROVENANCE_INCOMPLETE", "材料内容摘要与快照不一致。")
			}
		}
		if kind != "video" && !utf8.Valid(content) {
			return nil, domainError("CONTEXT_MODALITY_NOT_SUPPORTED", "文本材料不是有效 UTF-8。")
		}
		result = append(result, ContextAsset{
			AssetID:         reference.AssetID,
			AssetSnapshotID: reference.AssetSnapshotID,
			Kind:            kind,
			Filename:        filename,
			Checksum:        checksum,
			Role:            reference.Role,
			Order:           reference.Order,
			Content:         string(content),
			ContentHash:     contentHash,
		})
	}
	return result, nil
}

func filterContextAssetsForTask(assets []ContextAsset, cursor json.RawMessage) []ContextAsset {
	var task struct {
		Video struct {
			AssetID string `json:"asset_id"`
		} `json:"video"`
	}
	if json.Unmarshal(cursor, &task) != nil || task.Video.AssetID == "" {
		return assets
	}
	filtered := make([]ContextAsset, 0, 1)
	for _, asset := range assets {
		if asset.AssetID == task.Video.AssetID {
			filtered = append(filtered, asset)
			break
		}
	}
	return filtered
}

func stepAcceptsInput(inputRefs []capability.ArtifactRef, artifactType string) bool {
	for _, input := range inputRefs {
		if input.ArtifactType == artifactType {
			return true
		}
	}
	return false
}

func optionalContextString(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}

func loadContextDocument(projectRoot, sourceFile, ref, id string) (ContextDocument, error) {
	path, fragment, err := resolveProjectReference(projectRoot, sourceFile, ref)
	if err != nil {
		return ContextDocument{}, err
	}
	if fragment != "" {
		return ContextDocument{}, domainError(
			"CONTEXT_BUILD_VERSION_MISSING",
			"Prompt 或 Rule 引用不能包含 JSON Fragment。",
		)
	}
	content, err := readLimitedFile(path, maxContextDocumentBytes)
	if err != nil {
		return ContextDocument{}, domainError(
			"CONTEXT_BUILD_VERSION_MISSING",
			fmt.Sprintf("无法读取 Context 文档 %s。", ref),
		)
	}
	if !utf8.Valid(content) {
		return ContextDocument{}, domainError(
			"CONTEXT_BUILD_VERSION_MISSING",
			fmt.Sprintf("Context 文档 %s 不是有效 UTF-8。", ref),
		)
	}
	return ContextDocument{
		ID:          id,
		Ref:         ref,
		Content:     string(content),
		ContentHash: sha256Hex(content),
	}, nil
}

func loadContextSchema(projectRoot, sourceFile, ref string) (json.RawMessage, error) {
	path, fragment, err := resolveProjectReference(projectRoot, sourceFile, ref)
	if err != nil {
		return nil, err
	}
	content, err := readLimitedFile(path, maxContextDocumentBytes)
	if err != nil {
		return nil, domainError("CONTEXT_BUILD_VERSION_MISSING", "输出 Schema 无法读取。")
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, domainError("CONTEXT_BUILD_VERSION_MISSING", "输出 Schema 不是有效 JSON。")
	}
	if fragment != "" {
		value, err = resolveJSONPointer(value, fragment)
		if err != nil {
			return nil, domainError("CONTEXT_BUILD_VERSION_MISSING", "输出 Schema Fragment 不存在。")
		}
	}
	value, err = expandContextSchemaReferences(projectRoot, path, value, map[string]bool{})
	if err != nil {
		return nil, domainError("CONTEXT_BUILD_VERSION_MISSING", "输出 Schema 引用无法解析。")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(encoded), nil
}

func expandContextSchemaReferences(
	projectRoot string,
	currentFile string,
	value any,
	stack map[string]bool,
) (any, error) {
	switch typed := value.(type) {
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			expanded, err := expandContextSchemaReferences(projectRoot, currentFile, item, stack)
			if err != nil {
				return nil, err
			}
			result[index] = expanded
		}
		return result, nil
	case map[string]any:
		if reference, ok := typed["$ref"].(string); ok {
			targetFile := currentFile
			fragment := ""
			parts := strings.SplitN(reference, "#", 2)
			if parts[0] != "" {
				var err error
				targetFile, fragment, err = resolveProjectReference(projectRoot, currentFile, reference)
				if err != nil {
					return nil, err
				}
			} else if len(parts) == 2 {
				fragment = parts[1]
			}
			key := filepath.Clean(targetFile) + "#" + fragment
			if stack[key] {
				return nil, fmt.Errorf("cyclic schema reference %s", reference)
			}
			stack[key] = true
			content, err := readLimitedFile(targetFile, maxContextDocumentBytes)
			if err != nil {
				delete(stack, key)
				return nil, err
			}
			var document any
			decoder := json.NewDecoder(bytes.NewReader(content))
			decoder.UseNumber()
			if err := decoder.Decode(&document); err != nil {
				delete(stack, key)
				return nil, err
			}
			if fragment != "" {
				document, err = resolveJSONPointer(document, fragment)
				if err != nil {
					delete(stack, key)
					return nil, err
				}
			}
			expandedReference, err := expandContextSchemaReferences(projectRoot, targetFile, document, stack)
			delete(stack, key)
			if err != nil {
				return nil, err
			}
			if len(typed) == 1 {
				return expandedReference, nil
			}
			siblings := make(map[string]any, len(typed)-1)
			for name, item := range typed {
				if name != "$ref" {
					siblings[name] = item
				}
			}
			expandedSiblings, err := expandContextSchemaReferences(projectRoot, currentFile, siblings, stack)
			if err != nil {
				return nil, err
			}
			return map[string]any{"allOf": []any{expandedReference, expandedSiblings}}, nil
		}
		result := make(map[string]any, len(typed))
		for name, item := range typed {
			expanded, err := expandContextSchemaReferences(projectRoot, currentFile, item, stack)
			if err != nil {
				return nil, err
			}
			result[name] = expanded
		}
		return result, nil
	default:
		return value, nil
	}
}

func resolveProjectReference(projectRoot, sourceFile, ref string) (string, string, error) {
	parts := strings.SplitN(ref, "#", 2)
	target, err := filepath.Abs(filepath.Join(filepath.Dir(sourceFile), filepath.FromSlash(parts[0])))
	if err != nil {
		return "", "", err
	}
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", "", err
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(relative) {
		return "", "", domainError(
			"CONTEXT_BUILD_VERSION_MISSING",
			"Context 引用越过项目目录。",
		)
	}
	fragment := ""
	if len(parts) == 2 {
		fragment = parts[1]
	}
	return target, fragment, nil
}

func readLimitedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var buffer bytes.Buffer
	if _, err := buffer.ReadFrom(&limitedReader{source: file, remaining: limit + 1}); err != nil {
		return nil, err
	}
	if int64(buffer.Len()) > limit {
		return nil, errors.New("file exceeds limit")
	}
	return buffer.Bytes(), nil
}

type limitedReader struct {
	source    *os.File
	remaining int64
}

func (r *limitedReader) Read(buffer []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, errors.New("read limit exceeded")
	}
	if int64(len(buffer)) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	count, err := r.source.Read(buffer)
	r.remaining -= int64(count)
	return count, err
}

func resolveJSONPointer(document any, pointer string) (any, error) {
	if pointer == "" {
		return document, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, errors.New("invalid JSON pointer")
	}
	current := document
	for _, rawToken := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		token := strings.ReplaceAll(strings.ReplaceAll(rawToken, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			return nil, errors.New("JSON pointer does not resolve to an object field")
		}
		next, exists := object[token]
		if !exists {
			return nil, errors.New("JSON pointer field not found")
		}
		current = next
	}
	return current, nil
}
