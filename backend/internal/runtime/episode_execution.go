package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"content-agent/backend/internal/capability"
)

const (
	episodeExecutionModeContinuous = "continuous"
	episodeExecutionModeReviewEach = "review_each"
	episodeExecutionConfigRef      = "execution_control"
)

type queryRowContext interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func validEpisodeExecutionMode(mode string) bool {
	return mode == episodeExecutionModeContinuous || mode == episodeExecutionModeReviewEach
}

func initialEpisodeExecutionMode(config json.RawMessage) string {
	var envelope struct {
		Payload struct {
			Mode string `json:"episode_execution_mode"`
		} `json:"payload"`
	}
	if json.Unmarshal(config, &envelope) == nil && validEpisodeExecutionMode(envelope.Payload.Mode) {
		return envelope.Payload.Mode
	}
	return episodeExecutionModeContinuous
}

func episodeExecutionModeForRun(
	ctx context.Context,
	db queryRowContext,
	runID string,
	fallbackConfig json.RawMessage,
) (string, error) {
	var payloadJSON string
	err := db.QueryRowContext(ctx, `
		SELECT payload_json FROM run_config_snapshots
		WHERE run_id = ? AND config_ref = ? AND status = 'sealed'
		ORDER BY version DESC LIMIT 1`, runID, episodeExecutionConfigRef).Scan(&payloadJSON)
	if errors.Is(err, sql.ErrNoRows) {
		if len(fallbackConfig) == 0 {
			var configJSON string
			if queryErr := db.QueryRowContext(ctx, `SELECT config_snapshot_json FROM runs WHERE run_id = ?`, runID).Scan(&configJSON); queryErr != nil {
				return "", queryErr
			}
			fallbackConfig = json.RawMessage(configJSON)
		}
		return initialEpisodeExecutionMode(fallbackConfig), nil
	}
	if err != nil {
		return "", err
	}
	var payload struct {
		Mode string `json:"episode_execution_mode"`
	}
	if json.Unmarshal([]byte(payloadJSON), &payload) != nil || !validEpisodeExecutionMode(payload.Mode) {
		return "", domainError("CAPABILITY_CONFIG_INVALID", "逐集生成方式配置无效。")
	}
	return payload.Mode, nil
}

func (s *Store) enrichRunEpisodeExecutionMode(ctx context.Context, db queryRowContext, run *Run) error {
	mode, err := episodeExecutionModeForRun(ctx, db, run.RunID, run.ConfigSnapshot)
	if err != nil {
		return err
	}
	run.EpisodeExecutionMode = mode
	return nil
}

func (s *Store) SetEpisodeExecutionMode(
	ctx context.Context,
	command SetEpisodeExecutionModeCommand,
) (RunSnapshot, error) {
	if !validEpisodeExecutionMode(command.Mode) {
		return RunSnapshot{}, domainError("REQUEST_VALIDATION_FAILED", "逐集生成方式只能是 continuous 或 review_each。")
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunSnapshot{}, err
	}
	defer tx.Rollback()
	run, err := getRunTx(ctx, tx, command.RunID)
	if err != nil {
		return RunSnapshot{}, err
	}
	if command.Scope == "" {
		command.Scope = run.ProjectID
	}
	cached, hit, err := s.beginExecutionControlCommand(ctx, tx, command.CommandMeta, run.ProjectID, "run", run.RunID)
	if err != nil {
		return RunSnapshot{}, err
	}
	if hit {
		return decodeIdempotentResult[RunSnapshot](cached)
	}
	if _, err := s.setEpisodeExecutionModeTx(ctx, tx, run, command.Mode, now); err != nil {
		return RunSnapshot{}, err
	}
	snapshot, err := s.getRunSnapshotTx(ctx, tx, run.RunID)
	if err != nil {
		return RunSnapshot{}, err
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, snapshot, now); err != nil {
		return RunSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunSnapshot{}, err
	}
	return snapshot, nil
}

// The caller owns authorization, idempotency and the enclosing transaction.
func (s *Store) setEpisodeExecutionModeTx(ctx context.Context, tx *sql.Tx, run Run, mode string, now time.Time) (RunConfigSnapshot, error) {
	if !validEpisodeExecutionMode(mode) {
		return RunConfigSnapshot{}, domainError("REQUEST_VALIDATION_FAILED", "逐集生成方式只能是 continuous 或 review_each。")
	}
	entry, ok, registryErr := s.capabilityEntryForRunQuery(
		ctx, tx, run.RunID, run.CapabilityID,
	)
	if registryErr != nil {
		return RunConfigSnapshot{}, registryErr
	}
	if !ok || entry.Status != capability.Available || entry.Definition == nil ||
		entry.Definition.Version != run.CapabilityVersion ||
		!supportsEpisodeExecutionMode(entry.Definition) {
		return RunConfigSnapshot{}, domainError("RUN_STATE_CONFLICT", "当前 Skill 不支持逐集生成方式。")
	}
	if run.Status == "completed" || run.Status == "cancelled" || run.Status == "failed" {
		return RunConfigSnapshot{}, domainError("RUN_STATE_CONFLICT", "已结束的生成任务不能切换逐集生成方式。")
	}
	payload, err := json.Marshal(map[string]any{"episode_execution_mode": mode})
	if err != nil {
		return RunConfigSnapshot{}, err
	}
	snapshot, err := createRunConfigSnapshotTx(
		ctx, tx, s.newID("cfg"), run.RunID, episodeExecutionConfigRef, payload, now,
	)
	if err != nil {
		return RunConfigSnapshot{}, err
	}
	if err := updateExactlyOne(ctx, tx, `UPDATE runs SET updated_at = ? WHERE run_id = ? AND status = ?`, "生成任务状态已经变化。", formatTime(now), run.RunID, run.Status); err != nil {
		return RunConfigSnapshot{}, err
	}
	runRef := run.RunID
	if _, err := s.appendEvent(ctx, tx, run.ProjectID, &runRef, run.CurrentStepRunID,
		"run.episode_execution_mode_changed", "run", run.RunID,
		map[string]any{"episode_execution_mode": mode, "config_snapshot_id": snapshot.ConfigSnapshotID}); err != nil {
		return RunConfigSnapshot{}, err
	}
	return snapshot, nil
}

func supportsEpisodeExecutionMode(definition *capability.CompiledDefinition) bool {
	if definition == nil || !slices.Contains(definition.Commands, "set_episode_execution_mode") {
		return false
	}
	for _, step := range definition.Steps {
		if isScriptBundleStep(step) {
			return true
		}
	}
	return false
}
