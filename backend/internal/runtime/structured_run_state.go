package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"content-agent/backend/internal/capability"
)

type structuredStateSource struct {
	StepIndex    int
	ItemOrder    int
	StepID       string
	ScopeKey     string
	ArtifactType string
	Kind         string
	Ref          string
	Payload      json.RawMessage
	ContentHash  string
}

type structuredStateSnapshotPayload struct {
	StateVersion   int                        `json:"state_version"`
	State          map[string]any             `json:"state"`
	LastTransition structuredStateTransition  `json:"last_transition"`
	SourceRefs     []structuredStateSourceRef `json:"source_refs"`
}

type structuredStateTransition struct {
	StepID       string `json:"step_id"`
	ScopeKey     string `json:"scope_key"`
	ArtifactType string `json:"artifact_type"`
}

type structuredStateSourceRef struct {
	Kind        string `json:"kind"`
	Ref         string `json:"ref"`
	ContentHash string `json:"content_hash"`
}

func (s *Store) materializeStructuredRunStateTx(
	ctx context.Context,
	tx *sql.Tx,
	request stepContextBuildRequest,
	entry capability.Entry,
	currentStep capability.CompiledStep,
) (*ContextStructuredRunState, error) {
	contract := entry.Definition.StateContract
	if contract == nil || currentStep.StateTransition == nil {
		return nil, nil
	}
	sources, err := s.structuredStateSourcesTx(ctx, tx, request, *entry.Definition)
	if err != nil {
		return nil, err
	}
	state := map[string]any{}
	refs := make([]structuredStateSourceRef, 0, len(sources))
	last := structuredStateTransition{}
	for _, source := range sources {
		step := compiledStepByID(*entry.Definition, source.StepID)
		if step == nil || step.StateTransition == nil ||
			step.StateTransition.DeltaArtifactType != source.ArtifactType {
			continue
		}
		delta, err := structuredStateDelta(source.Payload, step.StateTransition.DeltaPointer)
		if err != nil {
			return nil, domainError(
				"STATE_DELTA_INVALID",
				fmt.Sprintf("%s 的状态增量无法读取。", source.ScopeKey),
			)
		}
		applyStructuredStateMappings(state, delta, step.StateTransition.FieldMappings)
		refs = append(refs, structuredStateSourceRef{
			Kind: source.Kind, Ref: source.Ref, ContentHash: source.ContentHash,
		})
		last = structuredStateTransition{
			StepID: source.StepID, ScopeKey: source.ScopeKey, ArtifactType: source.ArtifactType,
		}
	}
	payload := structuredStateSnapshotPayload{
		StateVersion: 1, State: state, LastTransition: last, SourceRefs: refs,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if utf8.RuneCount(encoded) > contract.MaxSnapshotCharacters {
		return nil, domainError(
			"STATE_SNAPSHOT_EXCEEDS_BUDGET",
			fmt.Sprintf("结构化运行状态需要 %d 字符，超过合同上限 %d。", utf8.RuneCount(encoded), contract.MaxSnapshotCharacters),
		)
	}
	schema, err := loadContextSchema(entry.ContentRoot, entry.SourceFile, contract.SnapshotSchemaRef)
	if err != nil {
		return nil, err
	}
	if err := validateEmbeddedJSONSchema(schema, encoded); err != nil {
		return nil, domainError("STATE_SNAPSHOT_INVALID", fmt.Sprintf("结构化运行状态未通过合同校验：%v", err))
	}
	return s.persistStructuredRunStateTx(
		ctx, tx, request, contract.SnapshotSchemaRef, encoded, time.Now().UTC(),
	)
}

func (s *Store) structuredStateSourcesTx(
	ctx context.Context,
	tx *sql.Tx,
	request stepContextBuildRequest,
	definition capability.CompiledDefinition,
) ([]structuredStateSource, error) {
	stepIndexes := make(map[string]int, len(definition.Steps))
	transitions := make(map[string]capability.CompiledStep)
	currentIndex := -1
	for index, step := range definition.Steps {
		stepIndexes[step.ID] = index
		if step.StateTransition != nil {
			transitions[step.ID] = step
		}
		if step.ID == request.StepID {
			currentIndex = index
		}
	}
	if currentIndex < 0 {
		return nil, domainError("STATE_CONTRACT_MISMATCH", "当前步骤不属于结构化运行状态合同。")
	}
	currentItemOrder, err := currentTaskItemOrderTx(ctx, tx, request.TaskItemID)
	if err != nil {
		return nil, err
	}
	sources := make([]structuredStateSource, 0)
	checkpointRows, err := tx.QueryContext(ctx, `
		SELECT sr.step_id, ti.item_key, ti.item_order,
			trc.task_result_checkpoint_id, trc.artifact_type,
			trc.payload_json, trc.payload_hash
		FROM task_result_checkpoints trc
		JOIN task_items ti ON ti.task_item_id = trc.task_item_id
		JOIN step_runs sr ON sr.step_run_id = ti.step_run_id
		WHERE sr.run_id = ? AND ti.status = 'succeeded'`, request.RunID)
	if err != nil {
		return nil, err
	}
	for checkpointRows.Next() {
		var source structuredStateSource
		var payloadJSON string
		if err := checkpointRows.Scan(
			&source.StepID, &source.ScopeKey, &source.ItemOrder, &source.Ref,
			&source.ArtifactType, &payloadJSON, &source.ContentHash,
		); err != nil {
			checkpointRows.Close()
			return nil, err
		}
		source.Payload = json.RawMessage(payloadJSON)
		step, ok := transitions[source.StepID]
		index := stepIndexes[source.StepID]
		if !ok || index > currentIndex ||
			(index == currentIndex && source.ItemOrder >= currentItemOrder) ||
			step.StateTransition.DeltaArtifactType != source.ArtifactType {
			continue
		}
		source.StepIndex = index
		source.Kind = "task_result_checkpoint"
		sources = append(sources, source)
	}
	if err := checkpointRows.Close(); err != nil {
		return nil, err
	}

	artifactRows, err := tx.QueryContext(ctx, `
		SELECT sr.step_id, a.scope_key, ti.item_order, a.artifact_type,
			av.artifact_version_id, av.payload_json
		FROM artifacts a
		JOIN artifact_versions av ON av.artifact_version_id = a.current_version_id
		JOIN step_runs sr ON sr.step_run_id = a.step_run_id
		JOIN task_items ti ON ti.step_run_id = a.step_run_id AND ti.item_key = a.scope_key
		WHERE a.run_id = ? AND ti.status = 'succeeded'
			AND av.status IN ('pending_approval', 'confirmed')`, request.RunID)
	if err != nil {
		return nil, err
	}
	defer artifactRows.Close()
	for artifactRows.Next() {
		var source structuredStateSource
		var payloadJSON string
		if err := artifactRows.Scan(
			&source.StepID, &source.ScopeKey, &source.ItemOrder, &source.ArtifactType,
			&source.Ref, &payloadJSON,
		); err != nil {
			return nil, err
		}
		source.Payload = json.RawMessage(payloadJSON)
		step, ok := transitions[source.StepID]
		index := stepIndexes[source.StepID]
		if !ok || index > currentIndex ||
			(index == currentIndex && source.ItemOrder >= currentItemOrder) ||
			step.StateTransition.DeltaArtifactType != source.ArtifactType {
			continue
		}
		// Multi-item outputs are represented by their artifacts. Batched singleton
		// outputs are represented by checkpoints so the final merge is not replayed twice.
		if step.Batch != nil && len(step.OutputRefs) == 1 && step.OutputRefs[0].Cardinality == "one" {
			continue
		}
		source.StepIndex = index
		source.Kind = "artifact_version"
		source.ContentHash = sha256Hex(source.Payload)
		sources = append(sources, source)
	}
	if err := artifactRows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(sources, func(i, j int) bool {
		if sources[i].StepIndex != sources[j].StepIndex {
			return sources[i].StepIndex < sources[j].StepIndex
		}
		if sources[i].ItemOrder != sources[j].ItemOrder {
			return sources[i].ItemOrder < sources[j].ItemOrder
		}
		return sources[i].Ref < sources[j].Ref
	})
	return sources, nil
}

func currentTaskItemOrderTx(ctx context.Context, tx *sql.Tx, taskItemID string) (int, error) {
	var order int
	if err := tx.QueryRowContext(ctx, `SELECT item_order FROM task_items WHERE task_item_id = ?`, taskItemID).Scan(&order); err != nil {
		return 0, fmt.Errorf("read current state task order: %w", err)
	}
	return order, nil
}

func compiledStepByID(definition capability.CompiledDefinition, stepID string) *capability.CompiledStep {
	for index := range definition.Steps {
		if definition.Steps[index].ID == stepID {
			return &definition.Steps[index]
		}
	}
	return nil
}

func structuredStateDelta(payload json.RawMessage, pointer string) (map[string]any, error) {
	var document any
	if err := json.Unmarshal(payload, &document); err != nil {
		return nil, err
	}
	value, err := resolveJSONPointer(document, pointer)
	if err != nil {
		return nil, err
	}
	delta, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("state delta is not an object")
	}
	return delta, nil
}

func applyStructuredStateMappings(
	state map[string]any,
	delta map[string]any,
	mappings []capability.StateFieldMapping,
) {
	for _, mapping := range mappings {
		value, exists := delta[mapping.DeltaField]
		if !exists {
			continue
		}
		switch mapping.Operation {
		case "set_latest":
			state[mapping.StateField] = value
		case "append_unique":
			current := genericStringSlice(state[mapping.StateField])
			state[mapping.StateField] = appendUniqueStrings(current, genericStringSlice(value)...)
		case "remove":
			current := genericStringSlice(state[mapping.StateField])
			remove := make(map[string]bool)
			for _, item := range genericStringSlice(value) {
				remove[item] = true
			}
			kept := current[:0]
			for _, item := range current {
				if !remove[item] {
					kept = append(kept, item)
				}
			}
			state[mapping.StateField] = kept
		}
	}
}

func genericStringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				result = append(result, strings.TrimSpace(text))
			}
		}
		return result
	case string:
		if strings.TrimSpace(typed) != "" {
			return []string{strings.TrimSpace(typed)}
		}
	}
	return []string{}
}

func (s *Store) persistStructuredRunStateTx(
	ctx context.Context,
	tx *sql.Tx,
	request stepContextBuildRequest,
	schemaRef string,
	payload json.RawMessage,
	now time.Time,
) (*ContextStructuredRunState, error) {
	var artifactID, currentVersionID, currentPayload string
	var currentVersion int
	err := tx.QueryRowContext(ctx, `
		SELECT a.artifact_id, a.current_version_id, av.version, av.payload_json
		FROM artifacts a
		JOIN artifact_versions av ON av.artifact_version_id = a.current_version_id
		WHERE a.run_id = ? AND a.artifact_type = 'structured_run_state' AND a.scope_key = 'run'`,
		request.RunID,
	).Scan(&artifactID, &currentVersionID, &currentVersion, &currentPayload)
	if err == nil && structuredStateLogicalHash(json.RawMessage(currentPayload)) == structuredStateLogicalHash(payload) {
		content := json.RawMessage(currentPayload)
		return &ContextStructuredRunState{
			ArtifactID: artifactID, ArtifactVersionID: currentVersionID, Version: currentVersion,
			SchemaRef: schemaRef, Content: content, ContentHash: sha256Hex(content),
		}, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	isNew := errors.Is(err, sql.ErrNoRows)
	version := currentVersion + 1
	var versioned map[string]any
	if err := json.Unmarshal(payload, &versioned); err != nil {
		return nil, err
	}
	versioned["state_version"] = version
	payload, err = json.Marshal(versioned)
	if err != nil {
		return nil, err
	}
	contentHash := sha256Hex(payload)
	versionID := s.newID("av")
	if isNew {
		artifactID = s.newID("art")
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO artifacts(
				artifact_id, project_id, run_id, step_run_id, capability_id,
				artifact_type, scope_key, current_version_id, created_at, updated_at
			) VALUES(?, ?, ?, ?, ?, 'structured_run_state', 'run', ?, ?, ?)`,
			artifactID, request.ProjectID, request.RunID, request.StepRunID,
			request.CapabilityID, versionID, formatTime(now), formatTime(now),
		); err != nil {
			return nil, fmt.Errorf("create structured run state artifact: %w", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
			UPDATE artifacts SET current_version_id = ?, step_run_id = ?, updated_at = ?
			WHERE artifact_id = ? AND current_version_id = ?`,
			versionID, request.StepRunID, formatTime(now), artifactID, currentVersionID,
		); err != nil {
			return nil, fmt.Errorf("advance structured run state artifact: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE artifact_versions SET status = 'superseded'
			WHERE artifact_version_id = ? AND status = 'confirmed'`, currentVersionID); err != nil {
			return nil, fmt.Errorf("supersede structured run state version: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifact_versions(
			artifact_version_id, artifact_id, version, status, payload_json,
			schema_id, schema_version, created_by_kind, actor_ref,
			creation_reason, base_version_id, created_at, confirmed_at
		) VALUES(?, ?, ?, 'confirmed', ?, 'structured_run_state', '1.0.0',
			'runtime', 'structured_state_reducer.v1', 'state_reduction', ?, ?, ?)`,
		versionID, artifactID, version, string(payload), optionalString(currentVersionID),
		formatTime(now), formatTime(now),
	); err != nil {
		return nil, fmt.Errorf("create structured run state version: %w", err)
	}
	return &ContextStructuredRunState{
		ArtifactID: artifactID, ArtifactVersionID: versionID, Version: version,
		SchemaRef: schemaRef, Content: payload, ContentHash: contentHash,
	}, nil
}

func structuredStateLogicalHash(payload json.RawMessage) string {
	var value map[string]any
	if json.Unmarshal(payload, &value) != nil {
		return ""
	}
	delete(value, "state_version")
	encoded, _ := json.Marshal(value)
	return sha256Hex(encoded)
}

func optionalString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
