package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"content-agent/backend/internal/capability"
)

// A bundled provider schema applies to all outputs together, not to a projected
// fragment. Siblings are read-only inputs; only OutputKey may be replaced.
type ArtifactEditContract struct {
	ResponseSchema json.RawMessage            `json:"response_schema"`
	ContextHash    string                     `json:"context_hash"`
	OutputKey      string                     `json:"output_key,omitempty"`
	OutputBundle   map[string]json.RawMessage `json:"output_bundle,omitempty"`
}

func (contract *ArtifactEditContract) validate(payload json.RawMessage) error {
	if contract == nil {
		return nil
	}
	if contract.OutputKey != "" {
		bundle := make(map[string]json.RawMessage, len(contract.OutputBundle))
		for key, value := range contract.OutputBundle {
			bundle[key] = value
		}
		bundle[contract.OutputKey] = payload
		var err error
		payload, err = json.Marshal(bundle)
		if err != nil {
			return err
		}
	}
	if err := validateEmbeddedJSONSchema(contract.ResponseSchema, payload); err != nil {
		return domainError("OUTPUT_SCHEMA_VALIDATION_FAILED", fmt.Sprintf("修改后的 Skill 产物未通过输出合同校验：%v", err))
	}
	return nil
}

func (s *Store) validateArtifactEditTx(ctx context.Context, tx *sql.Tx, artifact Artifact, payload json.RawMessage) error {
	contract, err := s.artifactEditContractTx(ctx, tx, artifact)
	if err != nil {
		return err
	}
	return contract.validate(payload)
}

func (s *Store) artifactEditContractTx(ctx context.Context, tx *sql.Tx, artifact Artifact) (*ArtifactEditContract, error) {
	if artifact.CapabilityID == "agent_shell" || artifact.RunID == "" || artifact.StepRunID == "" {
		return nil, nil
	}
	entry, ok, err := s.capabilityEntryForRunQuery(ctx, tx, artifact.RunID, artifact.CapabilityID)
	if err != nil {
		return nil, err
	}
	if !ok || entry.Definition == nil {
		return nil, domainError("CAPABILITY_VERSION_UNAVAILABLE", "产物对应的冻结能力版本不可用。")
	}
	if entry.Skill == nil {
		return nil, nil
	}
	step, err := s.compiledStepForRunTx(ctx, tx, artifact.RunID, artifact.StepRunID)
	if err != nil {
		return nil, err
	}
	if !capability.SupportsDirectSkillOutput(step) {
		return nil, nil
	}
	// Follow the current version's ancestry, not the task's mutable current
	// attempt pointer. Edits must keep the contract that produced their base.
	var encoded, packHash, inputHash, taskID, taskKey string
	err = tx.QueryRowContext(ctx, `WITH RECURSIVE lineage AS (
		SELECT artifact_version_id, artifact_id, base_version_id, created_by_kind, actor_ref, version
		FROM artifact_versions WHERE artifact_version_id=? AND artifact_id=?
		UNION
		SELECT av.artifact_version_id, av.artifact_id, av.base_version_id, av.created_by_kind, av.actor_ref, av.version
		FROM artifact_versions av JOIN lineage child ON child.base_version_id=av.artifact_version_id
		WHERE av.artifact_id=child.artifact_id AND av.version<child.version
	), origin AS (
		SELECT * FROM lineage WHERE created_by_kind='model' ORDER BY version DESC LIMIT 1
	) SELECT cp.payload_json, cp.context_hash, ea.input_snapshot_hash, ti.task_item_id, ti.item_key
		FROM origin av JOIN execution_attempts ea ON ea.attempt_id=av.actor_ref
		JOIN task_items ti ON ti.task_item_id=ea.task_item_id
		JOIN context_packs cp ON cp.attempt_id=ea.attempt_id
		WHERE ea.status='succeeded' AND ti.run_id=? AND ti.step_run_id=?
		AND ea.run_id=ti.run_id AND ea.step_run_id=ti.step_run_id`, artifact.CurrentVersionID, artifact.ArtifactID,
		artifact.RunID, artifact.StepRunID).Scan(&encoded, &packHash, &inputHash, &taskID, &taskKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domainError("CONTEXT_LINEAGE_CONFLICT", "Skill 产物缺少生成基线的冻结输出合同。")
	}
	if err != nil {
		return nil, err
	}
	var pack StepExecutionContextPack
	if err := json.Unmarshal([]byte(encoded), &pack); err != nil {
		return nil, domainError("CONTEXT_PACK_INVALID", "Skill 输出的冻结编辑合同无法读取。")
	}
	calculated, err := calculateStepExecutionContextHash(pack)
	if err != nil {
		return nil, err
	}
	if calculated != packHash || pack.ContextHash != packHash || inputHash != packHash {
		return nil, domainError("CONTEXT_PACK_HASH_MISMATCH", "Skill 输出的冻结编辑合同完整性校验失败。")
	}
	outputs := pack.OutputContracts
	if len(outputs) == 0 {
		outputs = []ContextOutputContract{pack.OutputContract}
	}
	expectedKey := "step:" + step.ID
	if step.Batch != nil {
		expectedKey = artifact.ScopeKey
	}
	if pack.ProjectID != artifact.ProjectID || pack.Run.RunID != artifact.RunID || pack.Step.StepRunID != artifact.StepRunID ||
		pack.Step.TaskItemID != taskID || pack.Target.ScopeKey != taskKey || taskKey != expectedKey ||
		pack.SkillInstructions == nil || len(outputs) != len(step.OutputRefs) ||
		(step.Batch == nil && artifact.ScopeKey != "singleton") {
		return nil, domainError("CONTEXT_LINEAGE_CONFLICT", "Skill 输出编辑与冻结任务不一致。")
	}
	found := false
	for i, output := range outputs {
		if output.ArtifactType != step.OutputRefs[i].ArtifactType || output.SchemaRef != step.OutputRefs[i].SchemaRef {
			return nil, domainError("CONTEXT_LINEAGE_CONFLICT", "Skill 输出合同与冻结步骤声明不一致。")
		}
		found = found || output.ArtifactType == artifact.ArtifactType
	}
	if !found {
		return nil, domainError("CONTEXT_LINEAGE_CONFLICT", "编辑目标不属于冻结的 Skill 产物集合。")
	}
	response, err := directSkillStepResponseContract(outputs, pack.ProviderResultContract)
	if err != nil {
		return nil, err
	}
	contract := &ArtifactEditContract{ResponseSchema: response.Schema, ContextHash: packHash}
	if step.Batch != nil {
		episodeNo, err := episodeNumberFromScope(artifact.ScopeKey)
		if err != nil {
			return nil, err
		}
		contract.ResponseSchema, err = json.Marshal(map[string]any{"allOf": []any{
			response.Schema, map[string]any{"properties": map[string]any{"episode_no": map[string]any{"const": episodeNo}}, "required": []string{"episode_no"}},
		}})
		if err != nil {
			return nil, err
		}
	}
	if len(outputs) > 1 {
		contract.OutputKey = artifact.ArtifactType
		contract.OutputBundle = make(map[string]json.RawMessage, len(outputs))
		for _, output := range outputs {
			var payload string
			if err := tx.QueryRowContext(ctx, `SELECT av.payload_json FROM artifacts a
				JOIN artifact_versions av ON av.artifact_version_id=a.current_version_id AND av.artifact_id=a.artifact_id
				WHERE a.project_id=? AND a.run_id=? AND a.step_run_id=? AND a.artifact_type=? AND a.scope_key=?`,
				artifact.ProjectID, artifact.RunID, artifact.StepRunID, output.ArtifactType, artifact.ScopeKey).Scan(&payload); err != nil {
				return nil, err
			}
			contract.OutputBundle[output.ArtifactType] = json.RawMessage(payload)
		}
	}
	return contract, nil
}
