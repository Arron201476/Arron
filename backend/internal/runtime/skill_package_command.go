package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

type SkillPackageCommand struct {
	Action         string
	InstallationID string
	Expected       *SkillLifecycleSnapshot
	Target         SkillInstallTarget
	CapabilityID   string
	Version        string
	ContentHash    string
	SourceName     string
	IdempotencyKey string
}

type SkillPackageReceipt struct {
	SkillLifecycleReceipt
	WorkspaceID  string `json:"workspace_id"`
	Scope        string `json:"scope"`
	ScopeRef     string `json:"scope_ref"`
	SkillName    string `json:"skill_name"`
	CapabilityID string `json:"capability_id"`
	Version      string `json:"version"`
	ContentHash  string `json:"content_hash"`
	SourceHash   string `json:"source_hash"`
}

type SkillPackageResult struct {
	Receipt      SkillPackageReceipt `json:"receipt"`
	Installation SkillInstallation   `json:"installation"`
}

type skillPackageGuard struct {
	command    SkillPackageCommand
	target     managedSkillScope
	actor      string
	sourceHash string
	meta       CommandMeta
	receipt    *SkillPackageReceipt
}

type skillPackageCommitGuard struct {
	command        *skillPackageGuard
	installationID string
	expected       *SkillLifecycleSnapshot
}

// All public package writes use the existing quarantine/install transaction.
// The receipt is resolved before mutable discovery, package or version checks.
func (s *Store) ExecuteSkillPackageCommand(ctx context.Context, command SkillPackageCommand, source io.Reader) (SkillPackageResult, error) {
	upgrade := command.Action == "upgrade_zip" || command.Action == "update_directory"
	zip := command.Action == "install_zip" || command.Action == "upgrade_zip"
	if (!zip && command.Action != "adopt_directory" && command.Action != "update_directory") || upgrade != (command.InstallationID != "") || (!upgrade && command.Expected != nil) {
		return SkillPackageResult{}, domainError("REQUEST_VALIDATION_FAILED", "Skill 安装操作或目标无效。")
	}
	if strings.TrimSpace(command.IdempotencyKey) == "" || len(command.IdempotencyKey) > 200 {
		return SkillPackageResult{}, domainError("IDEMPOTENCY_KEY_REQUIRED", "Skill 安装必须保留原请求键。")
	}
	var installation SkillInstallation
	var err error
	if upgrade {
		if command.Expected == nil || command.Expected.Enabled == nil || command.Expected.EventCount == nil || *command.Expected.EventCount < 1 || command.Expected.ActiveVersionID == "" || command.Expected.Status == "" {
			return SkillPackageResult{}, domainError("REQUEST_VALIDATION_FAILED", "Skill 升级缺少完整安装状态，请刷新后重试。")
		}
		installation, err = s.PreviewSkillLifecycle(ctx, command.InstallationID)
		if err != nil {
			return SkillPackageResult{}, err
		}
		command.Target = SkillInstallTarget{Scope: capability.SkillScope(installation.Scope)}
		if command.Target.Scope == capability.SkillScopeProject {
			command.Target.ProjectID = installation.ScopeRef
		}
	}
	target, err := s.resolveSkillInstallTarget(ctx, []SkillInstallTarget{command.Target})
	if err != nil {
		return SkillPackageResult{}, err
	}
	var archive []byte
	sourceHash := ""
	if zip {
		if source == nil {
			return SkillPackageResult{}, domainError("SKILL_ARCHIVE_REQUIRED", "请上传 Skill ZIP。")
		}
		archive, err = io.ReadAll(io.LimitReader(source, maxSkillArchiveBytes+1))
		if err != nil {
			return SkillPackageResult{}, err
		}
		if len(archive) > maxSkillArchiveBytes {
			return SkillPackageResult{}, domainError("SKILL_ARCHIVE_TOO_LARGE", "Skill ZIP 超过 20 MiB 限制。")
		}
		sourceHash = fmt.Sprintf("%x", sha256.Sum256(archive))
		command.SourceName = safeSkillSourceName(command.SourceName, "skill.zip")
		if command.CapabilityID != "" || command.Version != "" || command.ContentHash != "" {
			return SkillPackageResult{}, domainError("REQUEST_VALIDATION_FAILED", "ZIP 元数据必须来自安装包。")
		}
	} else if command.Version == "" || command.ContentHash == "" || (command.Action == "adopt_directory" && command.CapabilityID == "") || command.SourceName != "" {
		return SkillPackageResult{}, domainError("REQUEST_VALIDATION_FAILED", "请确认目录 Skill 的版本和哈希，不接受本地路径。")
	}
	actor := identity.ActorRefFromContext(ctx)
	canonical, err := json.Marshal([]any{target.workspaceID, target.scope, target.ref, actor, command.Action, command.InstallationID, command.Expected, command.CapabilityID, command.Version, command.ContentHash, command.SourceName, sourceHash})
	if err != nil {
		return SkillPackageResult{}, err
	}
	guard := &skillPackageGuard{command: command, target: target, actor: actor, sourceHash: sourceHash, meta: CommandMeta{
		Scope: target.workspaceID + ":skill-package:" + actor, CommandType: "skill_package", IdempotencyKey: command.IdempotencyKey, RequestHash: fmt.Sprintf("%x", sha256.Sum256(canonical)),
	}}
	if hit, err := guard.preflight(ctx, s); err != nil {
		return SkillPackageResult{}, err
	} else if hit {
		return guard.result(ctx, s)
	}
	var prepare func(string) (*capability.SkillPackage, error)
	sourceType, sourceName := "zip", command.SourceName
	if zip {
		prepare = func(root string) (*capability.SkillPackage, error) {
			return prepareSkillZIP(s.registry.ProjectRoot(), root, sourceName, bytes.NewReader(archive))
		}
	} else {
		var discovered *capability.SkillPackage
		if upgrade {
			discovered, err = s.directoryUpdateSource(ctx, installation)
		} else {
			projectID := ""
			if target.scope == capability.SkillScopeProject {
				projectID = target.ref
			}
			registry, lookupErr := s.buildSelectionRegistry(ctx, s.db, target.workspaceID, projectID)
			err = lookupErr
			if err == nil {
				if entry, ok := registry.Get(command.CapabilityID); ok {
					discovered = entry.Skill
				}
			}
		}
		if err != nil {
			return SkillPackageResult{}, err
		}
		if discovered == nil {
			return SkillPackageResult{}, domainError("SKILL_NOT_FOUND", "目录 Skill 不存在，请重新扫描。")
		}
		if discovered.Version != command.Version || discovered.ContentHash != command.ContentHash {
			return SkillPackageResult{}, domainError("SKILL_DISCOVERY_CHANGED", "目录 Skill 已变化，请重新检查更新。")
		}
		sourceType, sourceName = "directory", safeSkillSourceName(filepath.Base(discovered.Directory), "skill-directory")
		prepare = func(root string) (*capability.SkillPackage, error) {
			copied, err := prepareSkillDirectory(s.registry.ProjectRoot(), root, discovered.Directory)
			if err != nil {
				return nil, err
			}
			if copied.Name != discovered.Name || copied.CapabilityID != discovered.CapabilityID || copied.Version != command.Version || copied.ContentHash != command.ContentHash {
				return nil, domainError("SKILL_DISCOVERY_CHANGED", "目录在安装前发生变化，请重新检查。")
			}
			return copied, nil
		}
	}
	expectedVersion := ""
	if upgrade {
		expectedVersion = command.Expected.ActiveVersionID
	}
	_, err = s.installSkillGuarded(ctx, sourceType, sourceName, actor, command.InstallationID, expectedVersion, prepare, nil, guard, command.Target)
	if err != nil {
		return SkillPackageResult{}, err
	}
	return guard.result(ctx, s)
}

func (g *skillPackageGuard) result(ctx context.Context, s *Store) (SkillPackageResult, error) {
	if g.receipt == nil {
		return SkillPackageResult{}, domainError("IDEMPOTENCY_RESULT_INVALID", "Skill 安装缺少持久回执。")
	}
	installation, err := s.GetSkillInstallation(ctx, g.receipt.InstallationID)
	return SkillPackageResult{Receipt: *g.receipt, Installation: installation}, err
}

func (g *skillPackageGuard) preflight(ctx context.Context, s *Store) (bool, error) {
	if g == nil {
		return false, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err := authorizeSkillScopeMutationQuery(ctx, tx, g.target); err != nil {
		return false, err
	}
	raw, hit, err := readSkillCommandReceipt(ctx, tx, g.meta)
	if err != nil {
		return false, err
	}
	if hit {
		return true, g.restore(ctx, tx, raw)
	}
	if g.command.Expected != nil {
		return false, checkSkillLifecycleSnapshotTx(ctx, tx, g.command.InstallationID, *g.command.Expected)
	}
	return false, nil
}

func readSkillCommandReceipt(ctx context.Context, tx *sql.Tx, meta CommandMeta) (json.RawMessage, bool, error) {
	var hash, status string
	var raw sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT request_hash,status,response_json FROM idempotency_records WHERE scope=? AND command_type=? AND idempotency_key=?`, meta.Scope, meta.CommandType, meta.IdempotencyKey).Scan(&hash, &status, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if hash != meta.RequestHash {
		return nil, false, domainError("IDEMPOTENCY_KEY_REUSED", "该 Idempotency-Key 已用于不同请求。")
	}
	if status != "completed" {
		return nil, false, domainError("COMMAND_IN_PROGRESS", "原 Skill 操作结果尚未确定。")
	}
	if !raw.Valid {
		return nil, false, domainError("IDEMPOTENCY_RESULT_INVALID", "Skill 操作回执缺失。")
	}
	return json.RawMessage(raw.String), true, nil
}

func (g *skillPackageGuard) begin(ctx context.Context, s *Store, tx *sql.Tx, skill *capability.SkillPackage) (bool, error) {
	if g == nil {
		return false, nil
	}
	raw, hit, err := s.beginIdempotency(ctx, tx, g.meta)
	if err != nil {
		return false, err
	}
	if hit {
		return true, g.restore(ctx, tx, raw)
	}
	if g.command.Expected != nil {
		return false, checkSkillLifecycleSnapshotTx(ctx, tx, g.command.InstallationID, *g.command.Expected)
	}
	var count int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM skill_installations WHERE workspace_id=? AND scope=? AND scope_ref=? AND skill_name=? AND status!='uninstalled'`, g.target.workspaceID, g.target.scope, g.target.ref, skill.Name).Scan(&count)
	if err == nil && count != 0 {
		err = domainError("SKILL_INSTALLATION_STATE_CONFLICT", "同名 Skill 已安装，请选择现有安装并上传新版本。")
	}
	return false, err
}

func (g *skillPackageGuard) restore(ctx context.Context, tx *sql.Tx, raw json.RawMessage) error {
	r, err := decodeIdempotentResult[SkillPackageReceipt](raw)
	if err != nil {
		return err
	}
	invalid := func() error {
		return domainError("IDEMPOTENCY_RESULT_INVALID", "Skill 包回执与原请求或审计记录不一致。")
	}
	if r.RequestID != g.command.IdempotencyKey || r.Action != g.command.Action || r.ActorRef != g.actor || r.WorkspaceID != g.target.workspaceID || r.Scope != string(g.target.scope) || r.ScopeRef != g.target.ref || r.SourceHash != g.sourceHash || r.InstallationID == "" || r.EventID == "" || r.VersionID == "" || (g.command.InstallationID != "" && r.InstallationID != g.command.InstallationID) || (g.command.CapabilityID != "" && r.CapabilityID != g.command.CapabilityID) || (g.command.Version != "" && (r.Version != g.command.Version || r.ContentHash != g.command.ContentHash)) {
		return invalid()
	}
	var name, capID, version, hash, eventType, actor, payload string
	err = tx.QueryRowContext(ctx, `SELECT i.skill_name,i.capability_id,v.version,v.content_hash,e.event_type,e.actor_ref,e.payload_json
		FROM skill_installations i JOIN skill_versions v ON v.skill_installation_id=i.skill_installation_id
		JOIN skill_installation_events e ON e.skill_installation_id=i.skill_installation_id AND e.skill_version_id=v.skill_version_id
		WHERE i.skill_installation_id=? AND i.workspace_id=? AND i.scope=? AND i.scope_ref=? AND v.skill_version_id=? AND e.skill_installation_event_id=?`, r.InstallationID, r.WorkspaceID, r.Scope, r.ScopeRef, r.VersionID, r.EventID).Scan(&name, &capID, &version, &hash, &eventType, &actor, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return invalid()
	}
	if err != nil {
		return err
	}
	var binding struct {
		RequestID   string `json:"request_id"`
		RequestHash string `json:"request_hash"`
		ContentHash string `json:"content_hash"`
		SourceHash  string `json:"source_hash"`
	}
	if name != r.SkillName || capID != r.CapabilityID || version != r.Version || hash != r.ContentHash || actor != r.ActorRef || (eventType != "skill.version.installed" && eventType != "skill.version.reused") || json.Unmarshal([]byte(payload), &binding) != nil || binding.RequestID != r.RequestID || binding.RequestHash != g.meta.RequestHash || binding.ContentHash != r.ContentHash || binding.SourceHash != r.SourceHash {
		return invalid()
	}
	g.receipt = &r
	return nil
}

func (g *skillPackageGuard) complete(ctx context.Context, tx *sql.Tx, installationID, versionID, eventID string, skill *capability.SkillPackage, now time.Time) error {
	if g == nil {
		return nil
	}
	r := SkillPackageReceipt{SkillLifecycleReceipt: SkillLifecycleReceipt{RequestID: g.command.IdempotencyKey, InstallationID: installationID, VersionID: versionID, EventID: eventID, Action: g.command.Action, ActorRef: g.actor}, WorkspaceID: g.target.workspaceID, Scope: string(g.target.scope), ScopeRef: g.target.ref, SkillName: skill.Name, CapabilityID: skill.CapabilityID, Version: skill.Version, ContentHash: skill.ContentHash, SourceHash: g.sourceHash}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := g.restore(ctx, tx, raw); err != nil {
		return err
	}
	return completeIdempotency(ctx, tx, g.meta, r, now)
}

func (g *skillPackageGuard) eventPayload(payload map[string]any) map[string]any {
	if g != nil {
		payload["request_id"], payload["request_hash"], payload["source_hash"] = g.command.IdempotencyKey, g.meta.RequestHash, g.sourceHash
	}
	return payload
}
