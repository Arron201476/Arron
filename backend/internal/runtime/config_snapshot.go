package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

func latestRunConfigSnapshotTx(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
	configRef string,
) (RunConfigSnapshot, error) {
	var snapshot RunConfigSnapshot
	var payloadJSON, createdAt, sealedAt string
	err := tx.QueryRowContext(ctx, `
		SELECT config_snapshot_id, run_id, config_ref, version, status,
			payload_json, snapshot_hash, created_at, sealed_at
		FROM run_config_snapshots
		WHERE run_id = ? AND config_ref = ? AND status = 'sealed'
		ORDER BY version DESC LIMIT 1`, runID, configRef).Scan(
		&snapshot.ConfigSnapshotID,
		&snapshot.RunID,
		&snapshot.ConfigRef,
		&snapshot.Version,
		&snapshot.Status,
		&payloadJSON,
		&snapshot.SnapshotHash,
		&createdAt,
		&sealedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return RunConfigSnapshot{}, domainError(
			"CONFIG_SNAPSHOT_NOT_FOUND",
			"当前步骤缺少已确认的配置快照。",
		)
	}
	if err != nil {
		return RunConfigSnapshot{}, err
	}
	snapshot.Payload = json.RawMessage(payloadJSON)
	snapshot.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return RunConfigSnapshot{}, err
	}
	snapshot.SealedAt, err = parseTime(sealedAt)
	return snapshot, err
}

func createRunConfigSnapshotTx(
	ctx context.Context,
	tx *sql.Tx,
	id string,
	runID string,
	configRef string,
	payload json.RawMessage,
	now time.Time,
) (RunConfigSnapshot, error) {
	if id == "" || runID == "" || configRef == "" || !jsonObject(payload) {
		return RunConfigSnapshot{}, domainError(
			"CAPABILITY_CONFIG_INVALID",
			"配置快照内容无效。",
		)
	}
	var nextVersion int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(version), 0) + 1
		FROM run_config_snapshots
		WHERE run_id = ? AND config_ref = ?`, runID, configRef).Scan(&nextVersion); err != nil {
		return RunConfigSnapshot{}, err
	}
	hash := sha256Hex(payload)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO run_config_snapshots(
			config_snapshot_id, run_id, config_ref, version, status,
			payload_json, snapshot_hash, created_at, sealed_at
		) VALUES(?, ?, ?, ?, 'sealed', ?, ?, ?, ?)`,
		id, runID, configRef, nextVersion, string(payload), hash,
		formatTime(now), formatTime(now),
	); err != nil {
		return RunConfigSnapshot{}, err
	}
	return RunConfigSnapshot{
		ConfigSnapshotID: id,
		RunID:            runID,
		ConfigRef:        configRef,
		Version:          nextVersion,
		Status:           "sealed",
		Payload:          payload,
		SnapshotHash:     hash,
		CreatedAt:        now,
		SealedAt:         now,
	}, nil
}
