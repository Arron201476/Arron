package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

const managedSkillRootPriority = 250

type managedSkillSwap struct {
	root             string
	activePath       string
	previousPath     string
	installedCurrent bool
	previousExists   bool
}

func (s *Store) initializeSkillInstallations(ctx context.Context) error {
	if err := s.cleanUncommittedSkillSnapshots(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE skill_install_attempts
		SET status = 'failed', failure_code = 'SKILL_INSTALL_INTERRUPTED',
			diagnostics_json = '[{"code":"SKILL_INSTALL_INTERRUPTED"}]',
			completed_at = ?
		WHERE status = 'validating'`, formatTime(s.now())); err != nil {
		return err
	}
	quarantineRoot := filepath.Join(s.skillDataRoot, "quarantine")
	entries, err := os.ReadDir(quarantineRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := removeManagedSkillPath(s.skillDataRoot, filepath.Join(quarantineRoot, entry.Name())); err != nil {
			return err
		}
	}
	activationRoot := filepath.Join(s.skillDataRoot, "activation")
	entries, err = os.ReadDir(activationRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := removeManagedSkillPath(s.skillDataRoot, filepath.Join(activationRoot, entry.Name())); err != nil {
			return err
		}
	}

	type activeRecord struct {
		workspaceID    string
		scope          capability.SkillScope
		scopeRef       string
		installationID string
		capabilityID   string
		skillName      string
		version        string
		contentHash    string
		executionMode  string
		packageRef     string
		status         string
		enabled        bool
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT si.workspace_id, si.scope, si.scope_ref, si.skill_installation_id, si.capability_id, si.skill_name,
			sv.version, sv.content_hash, sv.execution_mode, sv.package_ref,
			si.status, si.enabled
		FROM skill_installations si
		JOIN skill_versions sv ON sv.skill_version_id = si.active_version_id
		WHERE si.status != 'uninstalled'
		ORDER BY si.workspace_id, si.skill_name, si.skill_installation_id`)
	if err != nil {
		return err
	}
	records := make([]activeRecord, 0)
	for rows.Next() {
		var record activeRecord
		if err := rows.Scan(
			&record.workspaceID, &record.scope, &record.scopeRef, &record.installationID, &record.capabilityID, &record.skillName,
			&record.version, &record.contentHash, &record.executionMode, &record.packageRef,
			&record.status, &record.enabled,
		); err != nil {
			rows.Close()
			return err
		}
		records = append(records, record)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}

	expectedActive := make(map[string]map[string]struct{})
	for _, record := range records {
		if !validWorkspaceID(record.workspaceID) {
			return fmt.Errorf("invalid workspace id %q in Skill installation", record.workspaceID)
		}
		target := managedSkillScope{record.workspaceID, record.scope, record.scopeRef}
		activeRoot, err := s.scopedSkillActiveRoot(target)
		if err != nil {
			return err
		}
		if !managedSkillExecutionReady(record.executionMode) {
			if _, err := s.db.ExecContext(ctx, `
				UPDATE skill_installations
				SET status = 'pending_runtime_support', enabled = 0, updated_at = ?
				WHERE skill_installation_id = ?`,
				formatTime(s.now()), record.installationID,
			); err != nil {
				return err
			}
			continue
		}
		packagePath, err := resolveDataPath(s.dataRoot, record.packageRef)
		if err != nil {
			return err
		}
		archiveSkill, err := capability.InspectSkillPackage(
			s.registry.ProjectRoot(),
			capability.SkillRoot{
				Scope: capability.SkillScopeWorkspace,
				Path:  filepath.Dir(packagePath), Priority: managedSkillRootPriority,
			},
			packagePath,
		)
		archiveSkill, snapshotMatches := archiveSkill.MatchSnapshot(record.version, record.contentHash)
		if err != nil || !snapshotMatches || archiveSkill.Name != record.skillName ||
			archiveSkill.CapabilityID != record.capabilityID ||
			archiveSkill.Version != record.version ||
			archiveSkill.ExecutionMode != record.executionMode ||
			archiveSkill.ContentHash != record.contentHash {
			if _, updateErr := s.db.ExecContext(ctx, `
				UPDATE skill_installations
				SET status = 'broken', enabled = 0, updated_at = ?
				WHERE skill_installation_id = ?`,
				formatTime(s.now()), record.installationID,
			); updateErr != nil {
				return updateErr
			}
			continue
		}
		if record.status != "installed" {
			if _, updateErr := s.db.ExecContext(ctx, `
				UPDATE skill_installations SET status = 'installed', updated_at = ?
				WHERE skill_installation_id = ?`,
				formatTime(s.now()), record.installationID,
			); updateErr != nil {
				return updateErr
			}
		}
		swap, err := s.activateManagedSkillPackage(
			record.workspaceID, record.skillName, packagePath, s.newID("startup"), target,
		)
		if err != nil {
			return err
		}
		if err := swap.complete(s.skillDataRoot); err != nil {
			return err
		}
		if expectedActive[activeRoot] == nil {
			expectedActive[activeRoot] = map[string]struct{}{}
		}
		expectedActive[activeRoot][record.skillName] = struct{}{}
	}
	activeBase := filepath.Join(s.skillDataRoot, "active")
	workspaceEntries, err := os.ReadDir(activeBase)
	if err != nil {
		return err
	}
	for _, workspaceEntry := range workspaceEntries {
		if !workspaceEntry.IsDir() || !validWorkspaceID(workspaceEntry.Name()) {
			continue
		}
		activeRoot := s.managedSkillActiveRoot(workspaceEntry.Name())
		activeEntries, readErr := os.ReadDir(activeRoot)
		if readErr != nil {
			return readErr
		}
		for _, entry := range activeEntries {
			if _, expected := expectedActive[activeRoot][entry.Name()]; expected {
				continue
			}
			if err := removeManagedSkillPath(s.skillDataRoot, filepath.Join(activeRoot, entry.Name())); err != nil {
				return err
			}
		}
	}
	if err := s.cleanScopedSkillActivations(expectedActive); err != nil {
		return err
	}
	s.registryMu.Lock()
	s.workspaceRegistries = make(map[string]*capability.Registry)
	s.registryMu.Unlock()
	workspaceRows, err := s.db.QueryContext(ctx, `SELECT workspace_id FROM workspaces WHERE status = 'active' ORDER BY workspace_id`)
	if err != nil {
		return err
	}
	workspaceIDs := make([]string, 0)
	for workspaceRows.Next() {
		var workspaceID string
		if err := workspaceRows.Scan(&workspaceID); err != nil {
			workspaceRows.Close()
			return err
		}
		workspaceIDs = append(workspaceIDs, workspaceID)
	}
	if err := workspaceRows.Close(); err != nil {
		return err
	}
	for _, workspaceID := range workspaceIDs {
		if _, err := s.CapabilityRegistryForWorkspace(ctx, workspaceID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) InstallSkillZIP(
	ctx context.Context,
	sourceName string,
	source io.Reader,
	actorRef string,
	targets ...SkillInstallTarget,
) (SkillInstallation, error) {
	sourceName = safeSkillSourceName(sourceName, "skill.zip")
	return s.installSkill(ctx, "zip", sourceName, actorRef, "", func(quarantineRoot string) (*capability.SkillPackage, error) {
		return prepareSkillZIP(s.registry.ProjectRoot(), quarantineRoot, sourceName, source)
	}, targets...)
}

func (s *Store) UpgradeSkillZIP(
	ctx context.Context,
	installationID string,
	sourceName string,
	source io.Reader,
	actorRef string,
) (SkillInstallation, error) {
	sourceName = safeSkillSourceName(sourceName, "skill.zip")
	return s.installSkill(ctx, "zip", sourceName, actorRef, installationID, func(quarantineRoot string) (*capability.SkillPackage, error) {
		return prepareSkillZIP(s.registry.ProjectRoot(), quarantineRoot, sourceName, source)
	})
}

func (s *Store) InstallSkillDirectory(
	ctx context.Context,
	sourceDirectory string,
	actorRef string,
	targets ...SkillInstallTarget,
) (SkillInstallation, error) {
	sourceName := safeSkillSourceName(filepath.Base(sourceDirectory), "skill-directory")
	return s.installSkill(ctx, "directory", sourceName, actorRef, "", func(quarantineRoot string) (*capability.SkillPackage, error) {
		return prepareSkillDirectory(s.registry.ProjectRoot(), quarantineRoot, sourceDirectory)
	}, targets...)
}

func (s *Store) installSkill(
	ctx context.Context,
	sourceType string,
	sourceName string,
	actorRef string,
	expectedInstallationID string,
	prepare func(string) (*capability.SkillPackage, error),
	targets ...SkillInstallTarget,
) (SkillInstallation, error) {
	return s.installSkillChecked(ctx, sourceType, sourceName, actorRef, expectedInstallationID, "", prepare, targets...)
}

func (s *Store) installSkillChecked(
	ctx context.Context,
	sourceType, sourceName, actorRef, expectedInstallationID, expectedActiveVersionID string,
	prepare func(string) (*capability.SkillPackage, error),
	targets ...SkillInstallTarget,
) (SkillInstallation, error) {
	return s.installSkillCheckedWithDraft(ctx, sourceType, sourceName, actorRef, expectedInstallationID, expectedActiveVersionID, prepare, nil, targets...)
}

func (s *Store) installSkillCheckedWithDraft(
	ctx context.Context,
	sourceType, sourceName, actorRef, expectedInstallationID, expectedActiveVersionID string,
	prepare func(string) (*capability.SkillPackage, error),
	draft *projectSkillInstallGuard,
	targets ...SkillInstallTarget,
) (SkillInstallation, error) {
	return s.installSkillGuarded(ctx, sourceType, sourceName, actorRef, expectedInstallationID, expectedActiveVersionID, prepare, draft, nil, targets...)
}

func (s *Store) installSkillGuarded(
	ctx context.Context,
	sourceType, sourceName, actorRef, expectedInstallationID, expectedActiveVersionID string,
	prepare func(string) (*capability.SkillPackage, error),
	draft *projectSkillInstallGuard,
	guard *skillPackageGuard,
	targets ...SkillInstallTarget,
) (SkillInstallation, error) {
	if actorRef = strings.TrimSpace(actorRef); actorRef == "" {
		actorRef = identity.ActorRefFromContext(ctx)
	}
	workspaceID := identity.WorkspaceIDFromContext(ctx)
	var expectedSnapshot *SkillLifecycleSnapshot
	if expectedInstallationID != "" {
		existing, err := s.GetSkillInstallation(ctx, expectedInstallationID)
		if err != nil {
			return SkillInstallation{}, err
		}
		snapshot := skillLifecycleSnapshot(existing)
		expectedSnapshot = &snapshot
		if expectedActiveVersionID != "" {
			expectedSnapshot.ActiveVersionID = expectedActiveVersionID
		}
		target := SkillInstallTarget{Scope: capability.SkillScope(existing.Scope)}
		if target.Scope == capability.SkillScopeProject {
			target.ProjectID = existing.ScopeRef
		}
		targets = []SkillInstallTarget{target}
	}
	target, err := s.resolveSkillInstallTarget(ctx, targets)
	if err != nil {
		return SkillInstallation{}, err
	}
	attemptID := s.newID("ski")
	quarantineRoot := filepath.Join(s.skillDataRoot, "quarantine", attemptID)
	if err := os.Mkdir(quarantineRoot, 0o700); err != nil {
		return SkillInstallation{}, fmt.Errorf("create Skill quarantine: %w", err)
	}
	defer removeManagedSkillPath(s.skillDataRoot, quarantineRoot)
	now := s.now()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO skill_install_attempts(
			skill_install_attempt_id, workspace_id, scope, scope_ref, source_type, source_name,
			status, diagnostics_json, created_by, created_at
		) VALUES(?, ?, ?, ?, ?, ?, 'validating', '[]', ?, ?)`,
		attemptID, workspaceID, target.scope, target.ref, sourceType, sourceName, actorRef, formatTime(now),
	); err != nil {
		return SkillInstallation{}, err
	}

	skill, err := prepare(quarantineRoot)
	if err != nil {
		s.recordSkillInstallFailure(ctx, attemptID, err)
		return SkillInstallation{}, err
	}
	s.skillMu.Lock()
	defer s.skillMu.Unlock()
	if hit, err := guard.preflight(ctx, s); err != nil {
		s.recordSkillInstallFailure(ctx, attemptID, err)
		return SkillInstallation{}, err
	} else if hit {
		if _, err := s.db.ExecContext(ctx, `UPDATE skill_install_attempts SET status='completed', completed_at=? WHERE skill_install_attempt_id=?`, formatTime(s.now()), attemptID); err != nil {
			return SkillInstallation{}, err
		}
		return s.GetSkillInstallation(ctx, guard.receipt.InstallationID)
	}
	if err := s.authorizeSkillScopeMutation(ctx, target); err != nil {
		s.recordSkillInstallFailure(ctx, attemptID, err)
		return SkillInstallation{}, err
	}
	if expectedInstallationID != "" {
		existing, lookupErr := s.GetSkillInstallation(ctx, expectedInstallationID)
		if lookupErr != nil {
			s.recordSkillInstallFailure(ctx, attemptID, lookupErr)
			return SkillInstallation{}, lookupErr
		}
		if existing.Status == "uninstalled" {
			err := domainError("SKILL_INSTALLATION_STATE_CONFLICT", "已卸载的 Skill 不能直接升级。")
			s.recordSkillInstallFailure(ctx, attemptID, err)
			return SkillInstallation{}, err
		}
		if expectedActiveVersionID != "" && (existing.ActiveVersionID == nil || *existing.ActiveVersionID != expectedActiveVersionID) {
			err := domainError("SKILL_DISCOVERY_CHANGED", "当前 Skill 版本已变化，请重新检查更新。")
			s.recordSkillInstallFailure(ctx, attemptID, err)
			return SkillInstallation{}, err
		}
		if existing.SkillName != skill.Name || existing.CapabilityID != skill.CapabilityID {
			err := domainError("SKILL_UPGRADE_ID_MISMATCH", "升级包与目标 Skill 的名称或 capability_id 不一致。")
			s.recordSkillInstallFailure(ctx, attemptID, err)
			return SkillInstallation{}, err
		}
	}
	installation, err := s.persistSkillPackage(
		ctx, target, attemptID, sourceType, sourceName, actorRef, skill, draft, &skillPackageCommitGuard{command: guard, installationID: expectedInstallationID, expected: expectedSnapshot},
	)
	if err != nil {
		s.recordSkillInstallFailure(ctx, attemptID, err)
		return SkillInstallation{}, err
	}
	return installation, nil
}

func (s *Store) persistSkillPackage(
	ctx context.Context,
	target managedSkillScope,
	attemptID string,
	sourceType string,
	sourceName string,
	actorRef string,
	skill *capability.SkillPackage,
	draft *projectSkillInstallGuard,
	guards ...*skillPackageCommitGuard,
) (SkillInstallation, error) {
	workspaceID := target.workspaceID
	registry, err := s.lifecycleSkillRegistry(ctx, target)
	if err != nil {
		return SkillInstallation{}, err
	}
	if entry, exists := registry.Get(skill.CapabilityID); exists && entry.Skill == nil {
		return SkillInstallation{}, domainError("SKILL_CAPABILITY_COLLISION", "Skill ID 与内置业务能力冲突。")
	}
	skill.Scope, skill.ScopeRef, skill.WorkspaceID = target.scope, target.ref, workspaceID
	manifest := skill.Public(false)
	manifest.Path = skill.Name + "/SKILL.md"
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return SkillInstallation{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SkillInstallation{}, err
	}
	defer tx.Rollback()
	if err := authorizeSkillScopeMutationQuery(ctx, tx, target); err != nil {
		return SkillInstallation{}, err
	}
	if draft != nil {
		if err := draft.validateCommitTx(ctx, s, tx, target, skill); err != nil {
			return SkillInstallation{}, err
		}
	}
	var packageGuard *skillPackageGuard
	if len(guards) != 0 && guards[0] != nil {
		check := guards[0]
		packageGuard = check.command
		if hit, err := packageGuard.begin(ctx, s, tx, skill); err != nil {
			return SkillInstallation{}, err
		} else if hit {
			if _, err := tx.ExecContext(ctx, `UPDATE skill_install_attempts SET status='completed',completed_at=? WHERE skill_install_attempt_id=?`, formatTime(s.now()), attemptID); err != nil {
				return SkillInstallation{}, err
			}
			if err := tx.Commit(); err != nil {
				return SkillInstallation{}, err
			}
			return s.GetSkillInstallation(ctx, packageGuard.receipt.InstallationID)
		}
		if check.expected != nil {
			if err := checkSkillLifecycleSnapshotTx(ctx, tx, check.installationID, *check.expected); err != nil {
				return SkillInstallation{}, err
			}
		}
	}
	if err := s.enforceWorkspaceQuotaTx(ctx, tx, workspaceID, "installed_skills", 1); err != nil {
		var existing int
		lookupErr := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM skill_installations
			WHERE workspace_id = ? AND scope = ? AND scope_ref = ? AND skill_name = ? AND status != 'uninstalled'`,
			workspaceID, target.scope, target.ref, skill.Name,
		).Scan(&existing)
		if lookupErr != nil || existing == 0 {
			return SkillInstallation{}, err
		}
	}
	now := s.now()
	installationID := ""
	status := "installed"
	enabled := managedSkillExecutionReady(skill.ExecutionMode)
	if !enabled {
		status = "pending_runtime_support"
	}
	var existingCapabilityID, existingStatus string
	var existingEnabled bool
	err = tx.QueryRowContext(ctx, `
		SELECT skill_installation_id, capability_id, status, enabled
		FROM skill_installations
		WHERE workspace_id = ? AND scope = ? AND scope_ref = ? AND skill_name = ?`,
		workspaceID, target.scope, target.ref, skill.Name,
	).Scan(&installationID, &existingCapabilityID, &existingStatus, &existingEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		installationID = s.newID("ski")
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO skill_installations(
				skill_installation_id, workspace_id, scope, scope_ref, skill_name,
				capability_id, status, enabled, created_by, created_at, updated_at
			) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			installationID, workspaceID, target.scope, target.ref, skill.Name,
			skill.CapabilityID, status, enabled, actorRef, formatTime(now), formatTime(now),
		); err != nil {
			return SkillInstallation{}, mapSkillConstraintError(err)
		}
	} else if err != nil {
		return SkillInstallation{}, err
	} else if existingCapabilityID != skill.CapabilityID {
		return SkillInstallation{}, domainError("SKILL_ID_CHANGED", "同名 Skill 的 capability_id 不允许变更。")
	} else if existingStatus != "uninstalled" && !existingEnabled {
		enabled = false
	}

	var existingVersionID, existingHash, existingPackageRef string
	err = tx.QueryRowContext(ctx, `
		SELECT skill_version_id, content_hash, package_ref
		FROM skill_versions
		WHERE skill_installation_id = ? AND version = ?`,
		installationID, skill.Version,
	).Scan(&existingVersionID, &existingHash, &existingPackageRef)
	versionID := existingVersionID
	packageRef := existingPackageRef
	packageCreated := false
	packageCommitted := false
	defer func() {
		if !packageCreated || packageCommitted || packageRef == "" {
			return
		}
		if packagePath, resolveErr := resolveDataPath(s.dataRoot, packageRef); resolveErr == nil {
			_ = removeManagedSkillPath(s.skillDataRoot, packagePath)
		}
	}()
	if err == nil && existingHash != skill.ContentHash {
		return SkillInstallation{}, domainError("SKILL_VERSION_IMMUTABLE", "同一 Skill 版本已存在且内容哈希不同。")
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return SkillInstallation{}, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		versionID = s.newID("skv")
		packageRef, packageCreated, err = s.archiveSkillPackage(workspaceID, skill)
		if err != nil {
			return SkillInstallation{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO skill_versions(
				skill_version_id, skill_installation_id, version, content_hash,
				execution_mode, package_ref, source_type, source_name, manifest_json,
				status, installed_by, created_at
			) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 'installed', ?, ?)`,
			versionID, installationID, skill.Version, skill.ContentHash,
			skill.ExecutionMode, packageRef, sourceType, sourceName, string(manifestJSON),
			actorRef, formatTime(now),
		); err != nil {
			return SkillInstallation{}, mapSkillConstraintError(err)
		}
	}

	packagePath, err := resolveDataPath(s.dataRoot, packageRef)
	if err != nil {
		return SkillInstallation{}, err
	}
	if _, err := s.verifyManagedSkillPackage(
		packageRef, skill.Name, skill.CapabilityID, skill.Version,
		skill.ExecutionMode, skill.ContentHash,
	); err != nil {
		return SkillInstallation{}, err
	}
	var swap *managedSkillSwap
	if enabled {
		swap, err = s.activateManagedSkillPackage(
			workspaceID, skill.Name, packagePath, attemptID, target,
		)
	} else {
		swap, err = s.deactivateManagedSkillPackage(workspaceID, skill.Name, attemptID, target)
	}
	if err != nil {
		return SkillInstallation{}, err
	}
	rollbackSwap := true
	defer func() {
		if rollbackSwap {
			_ = swap.rollback(s.skillDataRoot)
		}
	}()

	result, err := tx.ExecContext(ctx, `
		UPDATE skill_installations
		SET status = ?, enabled = ?, active_version_id = ?, updated_at = ?, uninstalled_at = NULL
		WHERE skill_installation_id = ?`,
		status, enabled, versionID, formatTime(now), installationID,
	)
	if err != nil {
		return SkillInstallation{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return SkillInstallation{}, err
	} else if affected != 1 {
		return SkillInstallation{}, domainError("SKILL_INSTALLATION_STATE_CONFLICT", "Skill 安装状态未能提交，请刷新后重试。")
	}
	eventType := "skill.version.installed"
	if existingVersionID != "" {
		eventType = "skill.version.reused"
	}
	var eventID string
	if err := s.appendSkillInstallationEvent(
		ctx, tx, installationID, &versionID, eventType, actorRef,
		packageGuard.eventPayload(map[string]any{"content_hash": skill.ContentHash, "enabled": enabled}), now, &eventID,
	); err != nil {
		return SkillInstallation{}, err
	}
	if err := packageGuard.complete(ctx, tx, installationID, versionID, eventID, skill, now); err != nil {
		return SkillInstallation{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE skill_install_attempts
		SET status = 'completed', diagnostics_json = '[]', completed_at = ?
		WHERE skill_install_attempt_id = ?`, formatTime(now), attemptID,
	); err != nil {
		return SkillInstallation{}, err
	}
	if draft != nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO project_skill_install_receipts(receipt_id,project_id,arguments_hash,skill_installation_id,skill_version_id,agent_tool_call_id,created_at) VALUES(?,?,?,?,?,?,?)`, draft.receiptID, draft.projectID, draft.argumentHash, installationID, versionID, optionalSkillID(draft.callID), formatTime(now)); err != nil {
			return SkillInstallation{}, err
		}
	}
	if enabled {
		err = refreshManagedSkill(registry, skill.CapabilityID, skill.Version, skill.ContentHash, true)
	} else {
		registry.ClearSkillState(skill.CapabilityID)
		err = registry.RefreshSkills()
	}
	if err != nil {
		_ = swap.rollback(s.skillDataRoot)
		_ = tx.Rollback()
		rollbackSwap = false
		_ = s.initializeSkillInstallations(context.Background())
		return SkillInstallation{}, err
	}
	if err := tx.Commit(); err != nil {
		_ = swap.rollback(s.skillDataRoot)
		_ = tx.Rollback()
		_ = s.initializeSkillInstallations(context.Background())
		rollbackSwap = false
		return SkillInstallation{}, err
	}
	packageCommitted = true
	rollbackSwap = false
	_ = swap.complete(s.skillDataRoot)
	return s.GetSkillInstallation(ctx, installationID)
}

func (s *Store) ListSkillInstallations(
	ctx context.Context,
	includeUninstalled bool,
) ([]SkillInstallation, error) {
	query := `SELECT skill_installation_id FROM skill_installations managed WHERE ` + visibleManagedSkillSQL
	if !includeUninstalled {
		query += ` AND status != 'uninstalled'`
	}
	query += ` ORDER BY updated_at DESC, skill_installation_id`
	rows, err := s.db.QueryContext(ctx, query, skillVisibilityArgs(ctx)...)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	items := make([]SkillInstallation, 0, len(ids))
	for _, id := range ids {
		item, err := s.GetSkillInstallation(ctx, id)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Store) GetSkillInstallation(
	ctx context.Context,
	installationID string,
) (SkillInstallation, error) {
	var item SkillInstallation
	var enabled int
	var activeVersionID, uninstalledAt sql.NullString
	var createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT skill_installation_id, workspace_id, scope, scope_ref, skill_name,
			capability_id, status, enabled, active_version_id, created_by,
			created_at, updated_at, uninstalled_at
		FROM skill_installations
		WHERE skill_installation_id = ? AND workspace_id = ?`,
		installationID, identity.WorkspaceIDFromContext(ctx),
	).Scan(
		&item.SkillInstallationID, &item.WorkspaceID, &item.Scope, &item.ScopeRef,
		&item.SkillName, &item.CapabilityID, &item.Status, &enabled,
		&activeVersionID, &item.CreatedBy, &createdAt, &updatedAt, &uninstalledAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return SkillInstallation{}, domainError("SKILL_INSTALLATION_NOT_FOUND", "Skill 安装记录不存在。")
	}
	if err != nil {
		return SkillInstallation{}, err
	}
	visible, err := s.skillScopeVisible(ctx, installationScope(item))
	if err != nil {
		return SkillInstallation{}, err
	}
	if !visible {
		return SkillInstallation{}, domainError("SKILL_INSTALLATION_NOT_FOUND", "Skill 安装记录不存在。")
	}
	item.Enabled = enabled != 0
	if activeVersionID.Valid {
		item.ActiveVersionID = &activeVersionID.String
	}
	item.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return SkillInstallation{}, err
	}
	item.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return SkillInstallation{}, err
	}
	item.UninstalledAt, err = optionalTime(uninstalledAt)
	if err != nil {
		return SkillInstallation{}, err
	}
	item.Versions, err = s.listSkillVersions(ctx, installationID)
	if err != nil {
		return SkillInstallation{}, err
	}
	item.Events, err = s.listSkillInstallationEvents(ctx, installationID)
	if err != nil {
		return SkillInstallation{}, err
	}
	s.decorateSkillRegistryStatus(ctx, &item)
	return item, nil
}

func (s *Store) listSkillVersions(ctx context.Context, installationID string) ([]SkillVersion, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT skill_version_id, skill_installation_id, version, content_hash,
			execution_mode, package_ref, source_type, source_name, manifest_json,
			status, installed_by, created_at
		FROM skill_versions
		WHERE skill_installation_id = ?
		ORDER BY created_at DESC, skill_version_id DESC`, installationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	versions := make([]SkillVersion, 0)
	for rows.Next() {
		var item SkillVersion
		var manifest, createdAt string
		if err := rows.Scan(
			&item.SkillVersionID, &item.SkillInstallationID, &item.Version,
			&item.ContentHash, &item.ExecutionMode, &item.packageRef, &item.SourceType,
			&item.SourceName, &manifest, &item.Status, &item.InstalledBy, &createdAt,
		); err != nil {
			return nil, err
		}
		item.Manifest = json.RawMessage(manifest)
		item.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		versions = append(versions, item)
	}
	return versions, rows.Err()
}

func (s *Store) listSkillInstallationEvents(
	ctx context.Context,
	installationID string,
) ([]SkillInstallationEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT skill_installation_event_id, skill_installation_id, skill_version_id,
			event_type, actor_ref, payload_json, created_at
		FROM skill_installation_events
		WHERE skill_installation_id = ?
		ORDER BY created_at, skill_installation_event_id`, installationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]SkillInstallationEvent, 0)
	for rows.Next() {
		var item SkillInstallationEvent
		var versionID sql.NullString
		var payload, createdAt string
		if err := rows.Scan(
			&item.SkillInstallationEventID, &item.SkillInstallationID, &versionID,
			&item.EventType, &item.ActorRef, &payload, &createdAt,
		); err != nil {
			return nil, err
		}
		if versionID.Valid {
			item.SkillVersionID = &versionID.String
		}
		item.Payload = json.RawMessage(payload)
		item.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListSkillInstallAttempts(
	ctx context.Context,
	limit int,
) ([]SkillInstallAttempt, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	args := append(skillVisibilityArgs(ctx), limit)
	rows, err := s.db.QueryContext(ctx, `
		SELECT skill_install_attempt_id, workspace_id, scope, scope_ref, source_type, source_name,
			status, failure_code, diagnostics_json, created_by, created_at, completed_at
		FROM skill_install_attempts managed
		WHERE `+visibleManagedSkillSQL+`
		ORDER BY created_at DESC, skill_install_attempt_id DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]SkillInstallAttempt, 0)
	for rows.Next() {
		var item SkillInstallAttempt
		var failureCode, completedAt sql.NullString
		var diagnostics, createdAt string
		if err := rows.Scan(
			&item.SkillInstallAttemptID, &item.WorkspaceID, &item.Scope, &item.ScopeRef, &item.SourceType,
			&item.SourceName, &item.Status, &failureCode, &diagnostics,
			&item.CreatedBy, &createdAt, &completedAt,
		); err != nil {
			return nil, err
		}
		if failureCode.Valid {
			item.FailureCode = failureCode.String
		}
		if err := json.Unmarshal([]byte(diagnostics), &item.Diagnostics); err != nil {
			return nil, fmt.Errorf("decode Skill install diagnostics: %w", err)
		}
		item.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		item.CompletedAt, err = optionalTime(completedAt)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) SetSkillInstallationEnabled(
	ctx context.Context,
	installationID string,
	enabled bool,
	actorRef string,
) (SkillInstallation, error) {
	return s.setSkillInstallationEnabled(ctx, installationID, enabled, actorRef, nil)
}

func (s *Store) setSkillInstallationEnabled(ctx context.Context, installationID string, enabled bool, actorRef string, guard *skillLifecycleGuard) (SkillInstallation, error) {
	if actorRef = strings.TrimSpace(actorRef); actorRef == "" {
		actorRef = identity.ActorRefFromContext(ctx)
	}
	s.skillMu.Lock()
	defer s.skillMu.Unlock()
	installation, err := s.GetSkillInstallation(ctx, installationID)
	if err != nil {
		return SkillInstallation{}, err
	}
	if err := s.authorizeSkillScopeMutation(ctx, installationScope(installation)); err != nil {
		return SkillInstallation{}, err
	}
	if hit, err := guard.preflight(ctx, s, installation); err != nil {
		return SkillInstallation{}, err
	} else if hit {
		return s.GetSkillInstallation(ctx, installationID)
	}
	if installation.Status == "uninstalled" {
		return SkillInstallation{}, domainError("SKILL_INSTALLATION_STATE_CONFLICT", "已卸载的 Skill 不能直接启用或禁用。")
	}
	registry, err := s.lifecycleSkillRegistry(ctx, installationScope(installation))
	if err != nil {
		return SkillInstallation{}, err
	}
	activeVersion, err := activeSkillVersion(installation)
	if err != nil {
		return SkillInstallation{}, err
	}
	if enabled && !managedSkillExecutionReady(activeVersion.ExecutionMode) {
		return SkillInstallation{}, domainError("SKILL_EXECUTION_MODE_UNAVAILABLE", "该 Skill 的执行模式将在通用执行器接通后才能启用。")
	}

	var packagePath string
	if enabled {
		packagePath, err = s.verifyManagedSkillPackage(
			activeVersion.packageRef, installation.SkillName, installation.CapabilityID,
			activeVersion.Version, activeVersion.ExecutionMode, activeVersion.ContentHash,
		)
		if err != nil {
			return SkillInstallation{}, err
		}
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SkillInstallation{}, err
	}
	defer tx.Rollback()
	if err := authorizeSkillScopeMutationQuery(ctx, tx, installationScope(installation)); err != nil {
		return SkillInstallation{}, err
	}
	if hit, err := guard.begin(ctx, s, tx, installation); err != nil {
		return SkillInstallation{}, err
	} else if hit {
		_ = tx.Rollback()
		return s.GetSkillInstallation(ctx, installationID)
	}
	var swap *managedSkillSwap
	if enabled {
		swap, err = s.activateManagedSkillPackage(
			installation.WorkspaceID, installation.SkillName, packagePath, s.newID("enable"), installationScope(installation),
		)
		if err != nil {
			return SkillInstallation{}, err
		}
		defer swap.complete(s.skillDataRoot)
	}
	status := installation.Status
	if enabled {
		status = "installed"
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE skill_installations SET enabled = ?, status = ?, updated_at = ?
		WHERE skill_installation_id = ?`, enabled, status, formatTime(now), installationID,
	); err != nil {
		if swap != nil {
			_ = swap.rollback(s.skillDataRoot)
		}
		return SkillInstallation{}, err
	}
	eventType := "skill.disabled"
	if enabled {
		eventType = "skill.enabled"
	}
	var eventID string
	if err := s.appendSkillInstallationEvent(
		ctx, tx, installationID, installation.ActiveVersionID, eventType, actorRef, guard.eventPayload(nil), now, &eventID,
	); err != nil {
		if swap != nil {
			_ = swap.rollback(s.skillDataRoot)
		}
		return SkillInstallation{}, err
	}
	if err := guard.complete(ctx, tx, eventID, installation.ActiveVersionID, now); err != nil {
		if swap != nil {
			_ = swap.rollback(s.skillDataRoot)
		}
		return SkillInstallation{}, err
	}
	if enabled {
		err = refreshManagedSkill(
			registry, installation.CapabilityID, activeVersion.Version, activeVersion.ContentHash, true,
		)
	} else if installation.RegistryStatus != "absent" &&
		installation.Status != "pending_runtime_support" &&
		!registry.SetSkillEnabled(installation.CapabilityID, false) {
		err = errors.New("managed Skill is missing from registry")
	}
	if err != nil {
		if swap != nil {
			_ = swap.rollback(s.skillDataRoot)
		}
		_ = tx.Rollback()
		_ = s.initializeSkillInstallations(context.Background())
		return SkillInstallation{}, err
	}
	if err := tx.Commit(); err != nil {
		if swap != nil {
			_ = swap.rollback(s.skillDataRoot)
		}
		_ = tx.Rollback()
		_ = s.initializeSkillInstallations(context.Background())
		return SkillInstallation{}, err
	}
	return s.GetSkillInstallation(ctx, installationID)
}

func (s *Store) ActivateSkillVersion(
	ctx context.Context,
	installationID string,
	version string,
	actorRef string,
) (SkillInstallation, error) {
	return s.activateSkillVersion(ctx, installationID, version, actorRef, nil)
}

func (s *Store) activateSkillVersion(ctx context.Context, installationID, version, actorRef string, guard *skillLifecycleGuard) (SkillInstallation, error) {
	if actorRef = strings.TrimSpace(actorRef); actorRef == "" {
		actorRef = identity.ActorRefFromContext(ctx)
	}
	s.skillMu.Lock()
	defer s.skillMu.Unlock()
	installation, err := s.GetSkillInstallation(ctx, installationID)
	if err != nil {
		return SkillInstallation{}, err
	}
	if err := s.authorizeSkillScopeMutation(ctx, installationScope(installation)); err != nil {
		return SkillInstallation{}, err
	}
	if hit, err := guard.preflight(ctx, s, installation); err != nil {
		return SkillInstallation{}, err
	} else if hit {
		return s.GetSkillInstallation(ctx, installationID)
	}
	if installation.Status == "uninstalled" {
		return SkillInstallation{}, domainError("SKILL_INSTALLATION_STATE_CONFLICT", "已卸载的 Skill 不能切换版本。")
	}
	registry, err := s.lifecycleSkillRegistry(ctx, installationScope(installation))
	if err != nil {
		return SkillInstallation{}, err
	}
	var selected *SkillVersion
	for index := range installation.Versions {
		if installation.Versions[index].Version == version {
			selected = &installation.Versions[index]
			break
		}
	}
	if selected == nil {
		return SkillInstallation{}, domainError("SKILL_VERSION_NOT_FOUND", "指定的 Skill 版本不存在。")
	}
	executionReady := managedSkillExecutionReady(selected.ExecutionMode)
	enabled := installation.Enabled && executionReady
	status := "installed"
	if !executionReady {
		status = "pending_runtime_support"
	}
	packagePath, err := s.verifyManagedSkillPackage(
		selected.packageRef, installation.SkillName, installation.CapabilityID,
		selected.Version, selected.ExecutionMode, selected.ContentHash,
	)
	if err != nil {
		return SkillInstallation{}, err
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SkillInstallation{}, err
	}
	defer tx.Rollback()
	if err := authorizeSkillScopeMutationQuery(ctx, tx, installationScope(installation)); err != nil {
		return SkillInstallation{}, err
	}
	if hit, err := guard.begin(ctx, s, tx, installation); err != nil {
		return SkillInstallation{}, err
	} else if hit {
		_ = tx.Rollback()
		return s.GetSkillInstallation(ctx, installationID)
	}
	var swap *managedSkillSwap
	if executionReady {
		swap, err = s.activateManagedSkillPackage(
			installation.WorkspaceID, installation.SkillName, packagePath, s.newID("activate"), installationScope(installation),
		)
	} else {
		swap, err = s.deactivateManagedSkillPackage(
			installation.WorkspaceID, installation.SkillName, s.newID("deactivate"), installationScope(installation),
		)
	}
	if err != nil {
		return SkillInstallation{}, err
	}
	defer swap.complete(s.skillDataRoot)
	if _, err := tx.ExecContext(ctx, `
		UPDATE skill_installations
		SET active_version_id = ?, enabled = ?, status = ?, updated_at = ?
		WHERE skill_installation_id = ?`,
		selected.SkillVersionID, enabled, status, formatTime(now), installationID,
	); err != nil {
		_ = swap.rollback(s.skillDataRoot)
		return SkillInstallation{}, err
	}
	var eventID string
	if err := s.appendSkillInstallationEvent(
		ctx, tx, installationID, &selected.SkillVersionID, "skill.version.activated",
		actorRef, guard.eventPayload(map[string]any{"version": selected.Version, "enabled": enabled}), now, &eventID,
	); err != nil {
		_ = swap.rollback(s.skillDataRoot)
		return SkillInstallation{}, err
	}
	if err := guard.complete(ctx, tx, eventID, &selected.SkillVersionID, now); err != nil {
		_ = swap.rollback(s.skillDataRoot)
		return SkillInstallation{}, err
	}
	if executionReady {
		err = refreshManagedSkill(
			registry, installation.CapabilityID, selected.Version, selected.ContentHash, enabled,
		)
	} else {
		registry.ClearSkillState(installation.CapabilityID)
		err = registry.RefreshSkills()
	}
	if err != nil {
		_ = swap.rollback(s.skillDataRoot)
		_ = tx.Rollback()
		_ = s.initializeSkillInstallations(context.Background())
		return SkillInstallation{}, err
	}
	if err := tx.Commit(); err != nil {
		_ = swap.rollback(s.skillDataRoot)
		_ = tx.Rollback()
		_ = s.initializeSkillInstallations(context.Background())
		return SkillInstallation{}, err
	}
	return s.GetSkillInstallation(ctx, installationID)
}

func (s *Store) UninstallSkill(
	ctx context.Context,
	installationID string,
	actorRef string,
) (SkillInstallation, error) {
	return s.uninstallSkill(ctx, installationID, actorRef, nil)
}

func (s *Store) uninstallSkill(ctx context.Context, installationID, actorRef string, guard *skillLifecycleGuard) (SkillInstallation, error) {
	if actorRef = strings.TrimSpace(actorRef); actorRef == "" {
		actorRef = identity.ActorRefFromContext(ctx)
	}
	s.skillMu.Lock()
	defer s.skillMu.Unlock()
	installation, err := s.GetSkillInstallation(ctx, installationID)
	if err != nil {
		return SkillInstallation{}, err
	}
	if err := s.authorizeSkillScopeMutation(ctx, installationScope(installation)); err != nil {
		return SkillInstallation{}, err
	}
	if hit, err := guard.preflight(ctx, s, installation); err != nil {
		return SkillInstallation{}, err
	} else if hit {
		return s.GetSkillInstallation(ctx, installationID)
	}
	if installation.Status == "uninstalled" {
		return installation, nil
	}
	registry, err := s.lifecycleSkillRegistry(ctx, installationScope(installation))
	if err != nil {
		return SkillInstallation{}, err
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SkillInstallation{}, err
	}
	defer tx.Rollback()
	if err := authorizeSkillScopeMutationQuery(ctx, tx, installationScope(installation)); err != nil {
		return SkillInstallation{}, err
	}
	if hit, err := guard.begin(ctx, s, tx, installation); err != nil {
		return SkillInstallation{}, err
	} else if hit {
		_ = tx.Rollback()
		return s.GetSkillInstallation(ctx, installationID)
	}
	swap, err := s.deactivateManagedSkillPackage(
		installation.WorkspaceID, installation.SkillName, s.newID("uninstall"), installationScope(installation),
	)
	if err != nil {
		return SkillInstallation{}, err
	}
	defer swap.complete(s.skillDataRoot)
	if _, err := tx.ExecContext(ctx, `
		UPDATE skill_installations
		SET status = 'uninstalled', enabled = 0, active_version_id = NULL,
			updated_at = ?, uninstalled_at = ?
		WHERE skill_installation_id = ?`,
		formatTime(now), formatTime(now), installationID,
	); err != nil {
		_ = swap.rollback(s.skillDataRoot)
		return SkillInstallation{}, err
	}
	var eventID string
	if err := s.appendSkillInstallationEvent(
		ctx, tx, installationID, installation.ActiveVersionID, "skill.uninstalled",
		actorRef, guard.eventPayload(nil), now, &eventID,
	); err != nil {
		_ = swap.rollback(s.skillDataRoot)
		return SkillInstallation{}, err
	}
	if err := guard.complete(ctx, tx, eventID, installation.ActiveVersionID, now); err != nil {
		_ = swap.rollback(s.skillDataRoot)
		return SkillInstallation{}, err
	}
	registry.ClearSkillState(installation.CapabilityID)
	if err := registry.RefreshSkills(); err != nil {
		_ = swap.rollback(s.skillDataRoot)
		_ = tx.Rollback()
		_ = s.initializeSkillInstallations(context.Background())
		return SkillInstallation{}, err
	}
	// Uninstalling a managed override must not reactivate a directory fallback.
	_ = registry.SetSkillEnabled(installation.CapabilityID, false)
	if err := tx.Commit(); err != nil {
		_ = swap.rollback(s.skillDataRoot)
		_ = tx.Rollback()
		_ = s.initializeSkillInstallations(context.Background())
		return SkillInstallation{}, err
	}
	return s.GetSkillInstallation(ctx, installationID)
}

func (s *Store) archiveSkillPackage(workspaceID string, skill *capability.SkillPackage) (string, bool, error) {
	hash := strings.TrimPrefix(skill.ContentHash, "sha256:")
	parent := filepath.Join(
		s.skillDataRoot, "packages", workspaceID,
		skill.Name, skill.Version, hash,
	)
	target := filepath.Join(parent, skill.Name)
	created := false
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		existing, inspectErr := capability.InspectSkillPackage(
			s.registry.ProjectRoot(),
			capability.SkillRoot{
				Scope: capability.SkillScopeWorkspace, Path: parent, Priority: managedSkillRootPriority,
			},
			target,
		)
		if inspectErr != nil || existing.ContentHash != skill.ContentHash {
			return "", false, domainError("SKILL_PACKAGE_STORAGE_CONFLICT", "不可变 Skill 包目录已存在但内容不一致。")
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	} else {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return "", false, err
		}
		if err := renameManagedSkillPath(skill.Directory, target); err != nil {
			return "", false, fmt.Errorf("commit immutable Skill package: %w", err)
		}
		created = true
	}
	reference, err := filepath.Rel(s.dataRoot, target)
	if err != nil {
		return "", false, err
	}
	return filepath.ToSlash(reference), created, nil
}

func (s *Store) managedSkillActiveRoot(workspaceID string) string {
	return filepath.Join(s.skillDataRoot, "active", workspaceID)
}

func (s *Store) activateManagedSkillPackage(
	workspaceID string,
	skillName string,
	packagePath string,
	operationID string,
	targets ...managedSkillScope,
) (*managedSkillSwap, error) {
	target := managedSkillScope{workspaceID, capability.SkillScopeWorkspace, workspaceID}
	if len(targets) == 1 {
		target = targets[0]
	}
	activeRoot, err := s.scopedSkillActiveRoot(target)
	if err != nil {
		return nil, err
	}
	operationRoot := filepath.Join(s.skillDataRoot, "activation", operationID)
	nextPath := filepath.Join(operationRoot, "next", skillName)
	previousPath := filepath.Join(operationRoot, "previous", skillName)
	if err := copySkillTree(packagePath, nextPath); err != nil {
		_ = removeManagedSkillPath(s.skillDataRoot, operationRoot)
		return nil, err
	}
	activePath := filepath.Join(activeRoot, skillName)
	if err := os.MkdirAll(filepath.Dir(activePath), 0o755); err != nil {
		_ = removeManagedSkillPath(s.skillDataRoot, operationRoot)
		return nil, err
	}
	swap := &managedSkillSwap{
		root: operationRoot, activePath: activePath, previousPath: previousPath,
	}
	if _, err := os.Stat(activePath); err == nil {
		if err := os.MkdirAll(filepath.Dir(previousPath), 0o755); err != nil {
			_ = removeManagedSkillPath(s.skillDataRoot, operationRoot)
			return nil, err
		}
		if err := renameManagedSkillPath(activePath, previousPath); err != nil {
			_ = removeManagedSkillPath(s.skillDataRoot, operationRoot)
			return nil, err
		}
		swap.previousExists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = removeManagedSkillPath(s.skillDataRoot, operationRoot)
		return nil, err
	}
	if err := renameManagedSkillPath(nextPath, activePath); err != nil {
		_ = swap.rollback(s.skillDataRoot)
		return nil, err
	}
	swap.installedCurrent = true
	return swap, nil
}

func (s *Store) deactivateManagedSkillPackage(
	workspaceID string,
	skillName string,
	operationID string,
	targets ...managedSkillScope,
) (*managedSkillSwap, error) {
	target := managedSkillScope{workspaceID, capability.SkillScopeWorkspace, workspaceID}
	if len(targets) == 1 {
		target = targets[0]
	}
	activeRoot, err := s.scopedSkillActiveRoot(target)
	if err != nil {
		return nil, err
	}
	operationRoot := filepath.Join(s.skillDataRoot, "activation", operationID)
	activePath := filepath.Join(activeRoot, skillName)
	previousPath := filepath.Join(operationRoot, "previous", skillName)
	swap := &managedSkillSwap{
		root: operationRoot, activePath: activePath, previousPath: previousPath,
	}
	if _, err := os.Stat(activePath); errors.Is(err, os.ErrNotExist) {
		return swap, nil
	} else if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(previousPath), 0o755); err != nil {
		return nil, err
	}
	if err := renameManagedSkillPath(activePath, previousPath); err != nil {
		_ = removeManagedSkillPath(s.skillDataRoot, operationRoot)
		return nil, err
	}
	swap.previousExists = true
	return swap, nil
}

func (swap *managedSkillSwap) rollback(skillDataRoot string) error {
	if swap == nil {
		return nil
	}
	if swap.installedCurrent {
		if err := removeManagedSkillPath(skillDataRoot, swap.activePath); err != nil {
			return err
		}
	}
	if swap.previousExists {
		if err := os.MkdirAll(filepath.Dir(swap.activePath), 0o755); err != nil {
			return err
		}
		if err := renameManagedSkillPath(swap.previousPath, swap.activePath); err != nil {
			return err
		}
	}
	return removeManagedSkillPath(skillDataRoot, swap.root)
}

func (swap *managedSkillSwap) complete(skillDataRoot string) error {
	if swap == nil || swap.root == "" {
		return nil
	}
	return removeManagedSkillPath(skillDataRoot, swap.root)
}

func copySkillTree(sourceRoot string, targetRoot string) error {
	return filepath.WalkDir(sourceRoot, func(sourcePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return domainError("SKILL_PACKAGE_SYMLINK_FORBIDDEN", "不可变 Skill 包包含符号链接。")
		}
		relative, err := filepath.Rel(sourceRoot, sourcePath)
		if err != nil {
			return err
		}
		target := filepath.Join(targetRoot, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return domainError("SKILL_PACKAGE_ENTRY_UNSAFE", "不可变 Skill 包包含非普通文件。")
		}
		return copyRegularFile(sourcePath, target, info.Size())
	})
}

func removeManagedSkillPath(skillDataRoot, target string) error {
	root, err := filepath.Abs(skillDataRoot)
	if err != nil {
		return err
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("refusing to remove path outside managed Skill root")
	}
	return os.RemoveAll(target)
}

func renameManagedSkillPath(source, target string) error {
	delays := [...]time.Duration{0, 10 * time.Millisecond, 25 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond}
	var err error
	for _, delay := range delays {
		if delay > 0 {
			time.Sleep(delay)
		}
		err = os.Rename(source, target)
		if err == nil {
			return nil
		}
		if !errors.Is(err, os.ErrPermission) {
			return err
		}
	}
	return err
}

func (s *Store) verifyManagedSkillPackage(
	packageRef string,
	skillName string,
	capabilityID string,
	version string,
	executionMode string,
	contentHash string,
) (string, error) {
	packagePath, _, err := s.inspectManagedSkillPackage(
		packageRef, skillName, capabilityID, version, executionMode, contentHash,
	)
	return packagePath, err
}

func (s *Store) inspectManagedSkillPackage(
	packageRef string,
	skillName string,
	capabilityID string,
	version string,
	executionMode string,
	contentHash string,
) (string, *capability.SkillPackage, error) {
	packagePath, err := resolveDataPath(s.dataRoot, packageRef)
	if err != nil {
		return "", nil, err
	}
	inspected, err := capability.InspectSkillPackage(
		s.registry.ProjectRoot(),
		capability.SkillRoot{
			Scope: capability.SkillScopeWorkspace, Path: filepath.Dir(packagePath), Priority: managedSkillRootPriority,
		},
		packagePath,
	)
	inspected, snapshotMatches := inspected.MatchSnapshot(version, contentHash)
	if err != nil || !snapshotMatches || inspected.Name != skillName || inspected.CapabilityID != capabilityID ||
		inspected.Version != version || inspected.ExecutionMode != executionMode ||
		inspected.ContentHash != contentHash {
		return "", nil, domainError("SKILL_PACKAGE_INTEGRITY_FAILED", "Skill 包完整性校验失败，未切换活动版本。")
	}
	return packagePath, inspected, nil
}

func refreshManagedSkill(
	registry *capability.Registry, capabilityID, version, contentHash string, enabled bool,
) error {
	registry.ClearSkillState(capabilityID)
	if err := registry.RefreshSkills(); err != nil {
		return err
	}
	if !registry.PinSkillSnapshot(capabilityID, version, contentHash) {
		return domainError("SKILL_REGISTRY_REFRESH_FAILED", "Skill 版本固定失败。")
	}
	entry, ok := registry.Get(capabilityID)
	if !ok || entry.Skill == nil || entry.Definition == nil ||
		entry.Definition.Version != version || entry.Skill.ContentHash != contentHash {
		return domainError("SKILL_REGISTRY_REFRESH_FAILED", "Skill 已安装但动态注册未得到目标版本。")
	}
	if !enabled && !registry.SetSkillEnabled(capabilityID, false) {
		return domainError("SKILL_REGISTRY_REFRESH_FAILED", "Skill 禁用状态同步失败。")
	}
	return nil
}

func (s *Store) appendSkillInstallationEvent(
	ctx context.Context,
	tx *sql.Tx,
	installationID string,
	versionID *string,
	eventType string,
	actorRef string,
	payload any,
	now time.Time,
	eventIDs ...*string,
) error {
	if payload == nil {
		payload = map[string]any{}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	eventID := s.newID("ske")
	_, err = tx.ExecContext(ctx, `
		INSERT INTO skill_installation_events(
			skill_installation_event_id, skill_installation_id, skill_version_id,
			event_type, actor_ref, payload_json, created_at
		) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		eventID, installationID, versionID, eventType, actorRef,
		string(encoded), formatTime(now),
	)
	if err == nil {
		for _, target := range eventIDs {
			if target != nil {
				*target = eventID
			}
		}
	}
	return err
}

func (s *Store) recordSkillInstallFailure(_ context.Context, attemptID string, installErr error) {
	code := "SKILL_INSTALL_FAILED"
	message := "Skill 安装失败。"
	var domain *DomainError
	if errors.As(installErr, &domain) {
		code = domain.Code
		message = domain.Message
	}
	diagnostics, _ := json.Marshal([]map[string]string{{"code": code, "message": message}})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = s.db.ExecContext(ctx, `
		UPDATE skill_install_attempts
		SET status = 'failed', failure_code = ?, diagnostics_json = ?, completed_at = ?
		WHERE skill_install_attempt_id = ? AND status = 'validating'`,
		code, string(diagnostics), formatTime(s.now()), attemptID,
	)
}

func (s *Store) decorateSkillRegistryStatus(ctx context.Context, item *SkillInstallation) {
	if item.Status == "uninstalled" {
		item.RegistryStatus = "absent"
		item.RegistryReasonCode = "SKILL_UNINSTALLED"
		return
	}
	if item.Status == "pending_runtime_support" {
		item.RegistryStatus = "unavailable"
		item.RegistryReasonCode = "SKILL_EXECUTION_MODE_UNAVAILABLE"
		return
	}
	projectID := ""
	if item.Scope == string(capability.SkillScopeProject) {
		projectID = item.ScopeRef
	}
	registry, err := s.buildSelectionRegistry(ctx, s.db, item.WorkspaceID, projectID)
	if err != nil {
		item.RegistryStatus = "unavailable"
		item.RegistryReasonCode = "SKILL_REGISTRY_UNAVAILABLE"
		return
	}
	entry, ok := registry.Get(item.CapabilityID)
	if !ok || entry.Skill == nil {
		item.RegistryStatus = "unavailable"
		item.RegistryReasonCode = "SKILL_NOT_REGISTERED"
		return
	}
	if entry.Skill.Scope != capability.SkillScope(item.Scope) || entry.Skill.ScopeRef != item.ScopeRef {
		item.RegistryStatus, item.RegistryReasonCode = "shadowed", "SKILL_SCOPE_SHADOWED"
		return
	}
	item.RegistryStatus = string(entry.Status)
	item.RegistryReasonCode = entry.ReasonCode
}

func activeSkillVersion(installation SkillInstallation) (SkillVersion, error) {
	if installation.ActiveVersionID == nil {
		return SkillVersion{}, domainError("SKILL_VERSION_NOT_FOUND", "Skill 没有活动版本。")
	}
	for _, version := range installation.Versions {
		if version.SkillVersionID == *installation.ActiveVersionID {
			return version, nil
		}
	}
	return SkillVersion{}, domainError("SKILL_VERSION_NOT_FOUND", "Skill 活动版本记录不存在。")
}

func managedSkillExecutionReady(executionMode string) bool {
	switch executionMode {
	case "inline", "background_task", "stateful_workflow":
		return true
	default:
		return false
	}
}

func safeSkillSourceName(value, fallback string) string {
	value = filepath.Base(strings.ReplaceAll(strings.TrimSpace(value), "\\", "/"))
	if value == "" || value == "." {
		return fallback
	}
	const maxSourceNameBytes = 255
	if len(value) > maxSourceNameBytes {
		extension := filepath.Ext(value)
		stem := strings.TrimSuffix(value, extension)
		budget := maxSourceNameBytes - len(extension)
		if budget <= 0 {
			extension = ""
			budget = maxSourceNameBytes
		}
		for len(stem) > budget {
			_, size := utf8.DecodeLastRuneInString(stem)
			stem = stem[:len(stem)-size]
		}
		value = stem + extension
	}
	return value
}

func mapSkillConstraintError(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "unique constraint failed") {
		return domainError("SKILL_INSTALLATION_CONFLICT", "Skill 名称、ID、版本或哈希与现有安装冲突。")
	}
	return err
}
