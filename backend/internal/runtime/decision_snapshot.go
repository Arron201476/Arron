package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

func (s *Store) createRunDecisionSnapshotTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	runID string,
	stepRunID string,
	decisionType string,
	sourceKind string,
	sourceRefID string,
	payload json.RawMessage,
	now time.Time,
) (ContextDecisionSnapshot, error) {
	if projectID == "" || runID == "" || stepRunID == "" || decisionType == "" ||
		sourceKind == "" || sourceRefID == "" || !jsonObject(payload) {
		return ContextDecisionSnapshot{}, domainError(
			"REQUEST_VALIDATION_FAILED",
			"决策快照内容无效。",
		)
	}
	var nextVersion int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(version), 0) + 1
		FROM run_decision_snapshots
		WHERE run_id = ? AND decision_type = ?`, runID, decisionType).Scan(&nextVersion); err != nil {
		return ContextDecisionSnapshot{}, err
	}
	id := s.newID("dcs")
	hash := sha256Hex(payload)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO run_decision_snapshots(
			decision_snapshot_id, project_id, run_id, step_run_id,
			decision_type, source_kind, source_ref_id, version, status,
			payload_json, snapshot_hash, created_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, 'sealed', ?, ?, ?)`,
		id, projectID, runID, stepRunID, decisionType, sourceKind, sourceRefID,
		nextVersion, string(payload), hash, formatTime(now),
	); err != nil {
		return ContextDecisionSnapshot{}, err
	}
	return ContextDecisionSnapshot{
		DecisionSnapshotID: id,
		DecisionType:       decisionType,
		SourceKind:         sourceKind,
		SourceRefID:        sourceRefID,
		Version:            nextVersion,
		Payload:            payload,
		SnapshotHash:       hash,
	}, nil
}
