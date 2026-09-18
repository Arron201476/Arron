package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

func migrateSkillExecutionSnapshots(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS skill_execution_snapshots (
		skill_snapshot_id TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL,
		snapshot_key TEXT NOT NULL,
		skill_version_id TEXT REFERENCES skill_versions(skill_version_id),
		descriptor_json TEXT NOT NULL,
		package_ref TEXT NOT NULL,
		created_at TEXT NOT NULL,
		UNIQUE(workspace_id, snapshot_key)
	);
	CREATE TABLE IF NOT EXISTS agent_turn_skills (
		agent_turn_id TEXT NOT NULL REFERENCES agent_turns(agent_turn_id) ON DELETE CASCADE,
		capability_id TEXT NOT NULL,
		skill_snapshot_id TEXT REFERENCES skill_execution_snapshots(skill_snapshot_id),
		descriptor_json TEXT NOT NULL,
		PRIMARY KEY(agent_turn_id, capability_id)
	);`); err != nil {
		return err
	}
	for _, table := range []string{"skill_invocations", "runs"} {
		present, err := tableHasColumn(db, table, "skill_snapshot_id")
		if err != nil {
			return err
		}
		if !present {
			if _, err := db.Exec("ALTER TABLE " + table + " ADD COLUMN skill_snapshot_id TEXT REFERENCES skill_execution_snapshots(skill_snapshot_id)"); err != nil {
				return err
			}
		}
	}
	present, err := tableHasColumn(db, "skill_invocations", "agent_turn_id")
	if err != nil {
		return err
	}
	if !present {
		if _, err := db.Exec("ALTER TABLE skill_invocations ADD COLUMN agent_turn_id TEXT REFERENCES agent_turns(agent_turn_id)"); err != nil {
			return err
		}
	}
	present, err = tableHasColumn(db, "agent_turns", "skill_snapshot_ready")
	if err != nil {
		return err
	}
	if !present {
		_, err = db.Exec("ALTER TABLE agent_turns ADD COLUMN skill_snapshot_ready INTEGER NOT NULL DEFAULT 0")
	}
	return err
}

// Only package identity is persisted here. Instructions and resources remain in
// integrity-checked archives, not in a second serialized implementation of Skill.
func executionSkillDescriptor(skill *capability.SkillPackage) *capability.SkillPackage {
	return &capability.SkillPackage{
		Name: skill.Name, Description: skill.Description, CapabilityID: skill.CapabilityID,
		Version: skill.Version, ContentHash: skill.ContentHash, ExecutionMode: skill.ExecutionMode,
		Scope: skill.Scope, ScopeRef: skill.ScopeRef, WorkspaceID: skill.WorkspaceID, Priority: skill.Priority,
		Disabled: skill.Disabled, UnavailableReasonCode: skill.UnavailableReasonCode, UnavailableMessage: skill.UnavailableMessage,
	}
}

func (s *Store) pinExecutionSkillTx(ctx context.Context, tx *sql.Tx, projectID string, entry capability.Entry) (*string, *string, error) {
	if entry.Skill == nil {
		return nil, nil, nil
	}
	if entry.Status != capability.Available {
		return nil, nil, domainError("CAPABILITY_UNAVAILABLE", "Skill 当前不可执行。")
	}
	if err := authorizePersonalSkillExecution(ctx, entry); err != nil {
		return nil, nil, err
	}
	if id := entry.Skill.ExecutionSnapshotID; id != "" {
		bound, ok, err := s.capabilityEntryForExecutionSnapshotQuery(ctx, tx, projectID, entry.Skill.CapabilityID, id)
		if err != nil {
			return nil, nil, err
		}
		if !ok || bound.Status != capability.Available {
			return nil, nil, domainError("CAPABILITY_UNAVAILABLE", "执行绑定的 Skill 已不可用。")
		}
		return optionalSkillID(bound.Skill.ManagedVersionID), &id, nil
	}
	var workspaceID string
	if err := tx.QueryRowContext(ctx, `SELECT workspace_id FROM projects WHERE project_id = ? AND deleted_at IS NULL`, projectID).Scan(&workspaceID); err != nil {
		return nil, nil, err
	}
	descriptor := executionSkillDescriptor(entry.Skill)
	if descriptor.Scope != capability.SkillScopeSystem {
		descriptor.WorkspaceID = workspaceID
		if descriptor.ScopeRef == "" && descriptor.Scope == capability.SkillScopeWorkspace {
			descriptor.ScopeRef = workspaceID
		}
		if descriptor.ScopeRef == "" && descriptor.Scope == capability.SkillScopeUser {
			descriptor.ScopeRef = identity.DefaultUserID
		}
	}
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		return nil, nil, err
	}
	versionID, err := selectedManagedSkillVersionQuery(ctx, tx, projectID, entry)
	if err != nil {
		return nil, nil, err
	}
	if entry.Skill.ManagedVersionID != "" && versionID == nil {
		return nil, nil, domainError("CAPABILITY_UNAVAILABLE", "执行绑定的 Skill 已禁用或卸载。")
	}
	keyData, err := json.Marshal([]any{json.RawMessage(encoded), entry.Skill.Directory, versionID})
	if err != nil {
		return nil, nil, err
	}
	key := sha256Hex(keyData)
	var snapshotID string
	err = tx.QueryRowContext(ctx, `SELECT skill_snapshot_id FROM skill_execution_snapshots WHERE workspace_id = ? AND snapshot_key = ?`, workspaceID, key).Scan(&snapshotID)
	if err == nil {
		bound, ok, err := s.capabilityEntryForExecutionSnapshotQuery(ctx, tx, projectID, entry.Skill.CapabilityID, snapshotID)
		if err != nil {
			return nil, nil, err
		}
		if !ok || bound.Status != capability.Available {
			return nil, nil, domainError("SKILL_PACKAGE_UNAVAILABLE", "Skill 执行副本缺失或校验失败。")
		}
		return versionID, &snapshotID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, err
	}
	snapshotID = s.newID("sks")
	var packageRef string
	if versionID != nil {
		if err := tx.QueryRowContext(ctx, `SELECT package_ref FROM skill_versions WHERE skill_version_id = ?`, *versionID).Scan(&packageRef); err != nil {
			return nil, nil, err
		}
	} else {
		// A distinct execution archive cannot be removed by a concurrent failed
		// managed installation. Never rename or mutate the user's source directory.
		root := filepath.Join(s.skillDataRoot, "execution", workspaceID, snapshotID)
		if err := os.MkdirAll(root, 0o700); err != nil {
			return nil, nil, err
		}
		copied, err := prepareSkillDirectory(s.registry.ProjectRoot(), root, entry.Skill.Directory)
		if err == nil {
			var matches bool
			copied, matches = copied.MatchSnapshot(entry.Skill.Version, entry.Skill.ContentHash)
			if !matches || copied.CapabilityID != entry.Skill.CapabilityID || copied.Name != entry.Skill.Name {
				err = domainError("SKILL_DIRECTORY_CHANGED", "Skill 目录在读取时发生变化，请刷新后重试。")
			}
		}
		if err != nil {
			_ = removeManagedSkillPath(s.skillDataRoot, root)
			return nil, nil, err
		}
		packageRef, err = filepath.Rel(s.dataRoot, copied.Directory)
		if err != nil {
			_ = removeManagedSkillPath(s.skillDataRoot, root)
			return nil, nil, err
		}
		packageRef = filepath.ToSlash(packageRef)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO skill_execution_snapshots(skill_snapshot_id, workspace_id, snapshot_key, skill_version_id, descriptor_json, package_ref, created_at) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		snapshotID, workspaceID, key, versionID, string(encoded), packageRef, formatTime(s.now())); err != nil {
		return nil, nil, err
	}
	return versionID, &snapshotID, nil
}

func optionalSkillID(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}

// Run only during Store startup, before accepting work. A rolled-back DB
// transaction may leave an unreferenced, immutable directory on disk.
func (s *Store) cleanUncommittedSkillSnapshots(ctx context.Context) error {
	root := filepath.Join(s.skillDataRoot, "execution")
	workspaces, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, workspace := range workspaces {
		if !workspace.IsDir() || workspace.Type()&os.ModeSymlink != 0 || !validWorkspaceID(workspace.Name()) {
			continue
		}
		workspaceRoot := filepath.Join(root, workspace.Name())
		snapshots, err := os.ReadDir(workspaceRoot)
		if err != nil {
			return err
		}
		for _, snapshot := range snapshots {
			if !snapshot.IsDir() || snapshot.Type()&os.ModeSymlink != 0 || !tenantIdentifierPattern.MatchString(snapshot.Name()) {
				continue
			}
			var committed bool
			if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM skill_execution_snapshots WHERE workspace_id = ? AND skill_snapshot_id = ?)`, workspace.Name(), snapshot.Name()).Scan(&committed); err != nil {
				return err
			}
			if !committed {
				if err := removeManagedSkillPath(s.skillDataRoot, filepath.Join(workspaceRoot, snapshot.Name())); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *Store) capabilityEntryForExecutionSnapshotQuery(ctx context.Context, query projectWorkspaceQuery, projectID, capabilityID, snapshotID string) (capability.Entry, bool, error) {
	var descriptorJSON, packageRef, versionID, workspaceID string
	err := query.QueryRowContext(ctx, `SELECT snapshot.descriptor_json, snapshot.package_ref, COALESCE(snapshot.skill_version_id, ''), snapshot.workspace_id
		FROM skill_execution_snapshots snapshot JOIN projects project ON project.workspace_id = snapshot.workspace_id
		WHERE snapshot.skill_snapshot_id = ? AND project.project_id = ? AND project.deleted_at IS NULL`, snapshotID, projectID).Scan(&descriptorJSON, &packageRef, &versionID, &workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return capability.Entry{}, false, nil
	}
	if err != nil {
		return capability.Entry{}, false, err
	}
	var descriptor capability.SkillPackage
	if err := json.Unmarshal([]byte(descriptorJSON), &descriptor); err != nil {
		return capability.Entry{}, false, err
	}
	if descriptor.CapabilityID != capabilityID || (descriptor.Scope == capability.SkillScopeProject && descriptor.ScopeRef != projectID) {
		return capability.Entry{}, false, nil
	}
	if versionID != "" {
		entry, ok, err := s.capabilityEntryForSkillVersionQuery(ctx, query, projectID, capabilityID, versionID)
		if ok && entry.Skill != nil {
			entry.Skill.ExecutionSnapshotID = snapshotID
		}
		return entry, ok, err
	}
	var disabled bool
	if err := query.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM skill_installations WHERE workspace_id = ? AND scope = ? AND scope_ref = ? AND capability_id = ? AND (enabled = 0 OR status != 'installed'))`,
		descriptor.WorkspaceID, descriptor.Scope, descriptor.ScopeRef, capabilityID).Scan(&disabled); err != nil {
		return capability.Entry{}, false, err
	}
	if disabled {
		return capability.Entry{}, false, nil
	}
	_, skill, err := s.inspectManagedSkillPackage(packageRef, descriptor.Name, capabilityID, descriptor.Version, descriptor.ExecutionMode, descriptor.ContentHash)
	if err != nil {
		return capability.Entry{}, false, err
	}
	skill.Scope, skill.ScopeRef, skill.WorkspaceID, skill.Priority = descriptor.Scope, descriptor.ScopeRef, descriptor.WorkspaceID, descriptor.Priority
	skill.ExecutionSnapshotID = snapshotID
	return s.executionSkillEntryWithTools(ctx, query, workspaceID, skill)
}

func (s *Store) freezeAgentTurnSkillsTx(ctx context.Context, tx *sql.Tx, turn AgentTurn) error {
	registry, err := s.buildLiveSelectionRegistry(ctx, tx, turn.WorkspaceID, turn.ProjectID)
	if err != nil {
		return err
	}
	for _, entry := range registry.Entries() {
		if entry.Skill == nil {
			continue
		}
		selected := turn.Request.CapabilityRef != nil && turn.Request.CapabilityRef.CapabilityID == entry.Skill.CapabilityID
		snapshotID, encoded, err := s.freezeCatalogSkillTx(ctx, tx, turn.ProjectID, entry, selected)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_turn_skills(agent_turn_id, capability_id, skill_snapshot_id, descriptor_json) VALUES(?, ?, ?, ?)`, turn.AgentTurnID, entry.Skill.CapabilityID, snapshotID, string(encoded)); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE agent_turns SET skill_snapshot_ready = 1 WHERE agent_turn_id = ?`, turn.AgentTurnID)
	return err
}

func (s *Store) freezeCatalogSkillTx(ctx context.Context, tx *sql.Tx, projectID string, entry capability.Entry, selected bool) (*string, []byte, error) {
	descriptor := executionSkillDescriptor(entry.Skill)
	var snapshotID *string
	if entry.Status == capability.Available {
		var err error
		_, snapshotID, err = s.pinExecutionSkillTx(ctx, tx, projectID, entry)
		if err != nil {
			var packageError *DomainError
			var fileError *os.PathError
			if ctx.Err() != nil || selected || (!errors.As(err, &packageError) && !errors.As(err, &fileError)) {
				return nil, nil, err
			}
			descriptor.Disabled = true
			descriptor.UnavailableReasonCode = "SKILL_PACKAGE_UNAVAILABLE"
			descriptor.UnavailableMessage = "Skill 目录无法生成一致的执行副本，请刷新或修复目录后重试。"
		}
	} else {
		descriptor.Disabled = true
		descriptor.UnavailableReasonCode = entry.ReasonCode
		descriptor.UnavailableMessage = entry.Message
	}
	encoded, err := json.Marshal(descriptor)
	return snapshotID, encoded, err
}

func (s *Store) agentTurnSkillRegistry(ctx context.Context, query projectWorkspaceQuery, workspaceID, projectID string) (*capability.Registry, bool, error) {
	activity, present := AgentActivityFromContext(ctx)
	if !present {
		return nil, false, nil
	}
	if projectID == "" {
		projectID = activity.ProjectID
	}
	if activity.ExecutionAttemptID != "" {
		return s.runSkillRegistry(ctx, query, workspaceID, projectID, activity)
	}
	turnID := activity.AgentTurnID
	if turnID == "" && activity.AgentTaskAttemptID != "" {
		err := query.QueryRowContext(ctx, `SELECT COALESCE(invocation.agent_turn_id, '')
			FROM agent_task_attempts attempt JOIN agent_tasks task ON task.agent_task_id = attempt.agent_task_id
			JOIN skill_invocations invocation ON invocation.skill_invocation_id = task.skill_invocation_id
			WHERE attempt.agent_task_attempt_id = ? AND invocation.project_id = ?`, activity.AgentTaskAttemptID, projectID).Scan(&turnID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, false, err
		}
	}
	if turnID == "" {
		return nil, false, nil
	}
	var ready bool
	var userID string
	err := query.QueryRowContext(ctx, `SELECT skill_snapshot_ready, user_id FROM agent_turns WHERE agent_turn_id = ? AND workspace_id = ? AND project_id = ?`, turnID, workspaceID, projectID).Scan(&ready, &userID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && userID != selectedSkillScope(ctx, workspaceID, projectID).userID) {
		return nil, false, domainError("AGENT_ACTIVITY_INVALID", "Skill 执行身份与任务不匹配。")
	}
	if err != nil || !ready {
		return nil, false, err
	}
	rows, err := query.QueryContext(ctx, `SELECT COALESCE(skill_snapshot_id, ''), descriptor_json FROM agent_turn_skills WHERE agent_turn_id = ? ORDER BY capability_id`, turnID)
	if err != nil {
		return nil, true, err
	}
	registry, err := s.skillRegistryFromSnapshotRows(ctx, query, workspaceID, projectID, rows)
	return registry, true, err
}

func (s *Store) skillRegistryFromSnapshotRows(ctx context.Context, query projectWorkspaceQuery, workspaceID, projectID string, rows *sql.Rows) (*capability.Registry, error) {
	type storedSkill struct{ id, descriptor string }
	var stored []storedSkill
	for rows.Next() {
		var item storedSkill
		if err := rows.Scan(&item.id, &item.descriptor); err != nil {
			rows.Close()
			return nil, err
		}
		stored = append(stored, item)
	}
	err := rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	snapshots := make([]*capability.SkillPackage, 0, len(stored))
	for _, item := range stored {
		var descriptor capability.SkillPackage
		if err := json.Unmarshal([]byte(item.descriptor), &descriptor); err != nil {
			return nil, err
		}
		if item.id != "" {
			entry, ok, err := s.capabilityEntryForExecutionSnapshotQuery(ctx, query, projectID, descriptor.CapabilityID, item.id)
			if err == nil && ok && entry.Skill != nil {
				snapshots = append(snapshots, entry.Skill)
				continue
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			descriptor.Disabled = true
			descriptor.UnavailableReasonCode = "SKILL_PACKAGE_UNAVAILABLE"
			descriptor.UnavailableMessage = "本次任务绑定的 Skill 已禁用、卸载或执行副本不可用。"
		}
		snapshots = append(snapshots, &descriptor)
	}
	snapshots, err = s.applyInstalledDraftsToSnapshot(ctx, query, projectID, snapshots)
	if err != nil {
		return nil, err
	}
	registry, err := s.registry.ForkWithSnapshots(snapshots)
	if err == nil {
		err = s.applyWorkspaceToolDependencies(ctx, query, workspaceID, registry)
	}
	return registry, err
}
