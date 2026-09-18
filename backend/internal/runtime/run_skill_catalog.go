package runtime

import (
	"context"
	"database/sql"
	"errors"

	"content-agent/backend/internal/capability"
)

func migrateRunSkillCatalogs(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS run_skills (
		run_id TEXT NOT NULL REFERENCES runs(run_id) ON DELETE CASCADE,
		capability_id TEXT NOT NULL,
		skill_snapshot_id TEXT REFERENCES skill_execution_snapshots(skill_snapshot_id),
		descriptor_json TEXT NOT NULL,
		PRIMARY KEY(run_id, capability_id)
	);`); err != nil {
		return err
	}
	present, err := tableHasColumn(db, "runs", "skill_catalog_ready")
	if err == nil && !present {
		_, err = db.Exec("ALTER TABLE runs ADD COLUMN skill_catalog_ready INTEGER NOT NULL DEFAULT 0")
	}
	return err
}

func (s *Store) freezeRunSkillsTx(ctx context.Context, tx *sql.Tx, command StartRunCommand, runID string, primary capability.Entry) error {
	var workspaceID string
	if err := tx.QueryRowContext(ctx, `SELECT workspace_id FROM projects WHERE project_id = ? AND deleted_at IS NULL`, command.ProjectID).Scan(&workspaceID); err != nil {
		return err
	}
	// Inherit an initiating turn only for the same author. A collaborator's new
	// run must not gain access to the other author's personal Skill catalog.
	var turnID string
	err := tx.QueryRowContext(ctx, `SELECT turn.agent_turn_id FROM skill_invocations invocation
		JOIN agent_turns turn ON turn.agent_turn_id = invocation.agent_turn_id
		WHERE invocation.proposed_action_id = ? AND invocation.project_id = ?
		AND turn.project_id = ? AND turn.workspace_id = ? AND turn.user_id = ? AND turn.skill_snapshot_ready = 1`,
		command.ProposedActionID, command.ProjectID, command.ProjectID, workspaceID,
		selectedSkillScope(ctx, workspaceID, command.ProjectID).userID).Scan(&turnID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var registry *capability.Registry
	if turnID != "" {
		originCtx := WithAgentActivity(ctx, AgentActivityIdentity{ProjectID: command.ProjectID, AgentTurnID: turnID})
		registry, err = s.buildSelectionRegistry(originCtx, tx, workspaceID, command.ProjectID)
	} else {
		registry, err = s.buildLiveSelectionRegistry(ctx, tx, workspaceID, command.ProjectID)
	}
	if err != nil {
		return err
	}
	entries := registry.Entries()
	if primary.Skill != nil {
		// The confirmed invocation, not a later higher-priority installation, owns
		// the workflow's primary Skill binding.
		filtered := make([]capability.Entry, 0, len(entries)+1)
		for _, entry := range entries {
			if entry.Skill == nil || entry.Skill.CapabilityID != command.CapabilityID {
				filtered = append(filtered, entry)
			}
		}
		entries = append(filtered, primary)
	}
	for _, entry := range entries {
		if entry.Skill == nil {
			continue
		}
		snapshotID, descriptor, err := s.freezeCatalogSkillTx(ctx, tx, command.ProjectID, entry, entry.Skill.CapabilityID == command.CapabilityID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO run_skills(run_id, capability_id, skill_snapshot_id, descriptor_json) VALUES(?, ?, ?, ?)`,
			runID, entry.Skill.CapabilityID, snapshotID, string(descriptor)); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE runs SET skill_catalog_ready = 1 WHERE run_id = ?`, runID)
	return err
}

func (s *Store) runSkillRegistry(ctx context.Context, query projectWorkspaceQuery, workspaceID, projectID string, activity AgentActivityIdentity) (*capability.Registry, bool, error) {
	if activity.ProjectID != projectID || activity.AgentTurnID != "" || activity.AgentTaskAttemptID != "" {
		return nil, true, domainError("AGENT_ACTIVITY_INVALID", "Skill 执行身份与任务不匹配。")
	}
	var runID, userID string
	var ready bool
	err := query.QueryRowContext(ctx, `SELECT run.run_id, run.user_id, run.skill_catalog_ready
		FROM execution_attempts attempt JOIN runs run ON run.run_id = attempt.run_id
		JOIN projects project ON project.project_id = run.project_id
		WHERE attempt.attempt_id = ? AND run.project_id = ? AND project.workspace_id = ? AND project.deleted_at IS NULL`,
		activity.ExecutionAttemptID, projectID, workspaceID).Scan(&runID, &userID, &ready)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && userID != selectedSkillScope(ctx, workspaceID, projectID).userID) {
		return nil, true, domainError("AGENT_ACTIVITY_INVALID", "Skill 执行身份与任务不匹配。")
	}
	if err != nil {
		return nil, true, err
	}
	if !ready {
		return nil, true, domainError("SKILL_CATALOG_NOT_FROZEN", "旧任务没有冻结 Skill 目录，不能使用动态 Skill 工具；请明确确认新任务。")
	}
	rows, err := query.QueryContext(ctx, `SELECT COALESCE(skill_snapshot_id, ''), descriptor_json FROM run_skills WHERE run_id = ? ORDER BY capability_id`, runID)
	if err != nil {
		return nil, true, err
	}
	registry, err := s.skillRegistryFromSnapshotRows(ctx, query, workspaceID, projectID, rows)
	return registry, true, err
}
