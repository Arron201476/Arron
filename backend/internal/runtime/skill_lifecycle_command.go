package runtime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"content-agent/backend/internal/identity"
)

type SkillLifecycleSnapshot struct {
	ActiveVersionID string `json:"active_version_id"`
	Status          string `json:"status"`
	Enabled         *bool  `json:"enabled"`
	EventCount      *int   `json:"event_count"`
}

type SkillLifecycleCommand struct {
	InstallationID string
	Action         string
	Version        string
	IdempotencyKey string
	Expected       *SkillLifecycleSnapshot
}

type SkillLifecycleReceipt struct {
	RequestID      string `json:"request_id"`
	InstallationID string `json:"skill_installation_id"`
	EventID        string `json:"skill_installation_event_id"`
	VersionID      string `json:"skill_version_id"`
	Action         string `json:"action"`
	ActorRef       string `json:"actor_ref"`
}

type SkillLifecycleResult struct {
	Receipt      SkillLifecycleReceipt `json:"receipt"`
	Installation SkillInstallation     `json:"installation"`
}

type skillLifecycleGuard struct {
	command SkillLifecycleCommand
	meta    CommandMeta
	actor   string
	receipt *SkillLifecycleReceipt
}

func (s *Store) PreviewSkillLifecycle(ctx context.Context, installationID string) (SkillInstallation, error) {
	installation, err := s.GetSkillInstallation(ctx, installationID)
	if err != nil {
		return SkillInstallation{}, err
	}
	return installation, s.authorizeSkillScopeMutation(ctx, installationScope(installation))
}

func (s *Store) ControlSkillInstallation(ctx context.Context, command SkillLifecycleCommand) (SkillLifecycleResult, error) {
	if command.InstallationID == "" || command.Expected == nil || command.Expected.Enabled == nil || command.Expected.EventCount == nil || *command.Expected.EventCount < 1 || command.Expected.ActiveVersionID == "" || command.Expected.Status == "" {
		return SkillLifecycleResult{}, domainError("REQUEST_VALIDATION_FAILED", "Skill 操作缺少完整的当前安装状态，请刷新后重试。")
	}
	if strings.TrimSpace(command.IdempotencyKey) == "" || len(command.IdempotencyKey) > 200 {
		return SkillLifecycleResult{}, domainError("IDEMPOTENCY_KEY_REQUIRED", "Skill 操作必须保留原请求键。")
	}
	if command.Action != "enable" && command.Action != "disable" && command.Action != "activate" && command.Action != "uninstall" || (command.Action == "activate") != (command.Version != "") {
		return SkillLifecycleResult{}, domainError("REQUEST_VALIDATION_FAILED", "Skill 操作或目标版本无效。")
	}
	actor := identity.ActorRefFromContext(ctx)
	canonical, err := json.Marshal([]any{identity.WorkspaceIDFromContext(ctx), actor, command.InstallationID, command.Action, command.Version, command.Expected})
	if err != nil {
		return SkillLifecycleResult{}, err
	}
	guard := &skillLifecycleGuard{command: command, actor: actor, meta: CommandMeta{
		Scope:       identity.WorkspaceIDFromContext(ctx) + ":skill:" + command.InstallationID + ":" + actor,
		CommandType: "skill_lifecycle", IdempotencyKey: command.IdempotencyKey,
		RequestHash: fmt.Sprintf("%x", sha256.Sum256(canonical)),
	}}
	var installation SkillInstallation
	switch command.Action {
	case "enable", "disable":
		installation, err = s.setSkillInstallationEnabled(ctx, command.InstallationID, command.Action == "enable", actor, guard)
	case "activate":
		installation, err = s.activateSkillVersion(ctx, command.InstallationID, command.Version, actor, guard)
	case "uninstall":
		installation, err = s.uninstallSkill(ctx, command.InstallationID, actor, guard)
	}
	if err != nil {
		return SkillLifecycleResult{}, err
	}
	if guard.receipt == nil {
		return SkillLifecycleResult{}, domainError("IDEMPOTENCY_RESULT_INVALID", "Skill 操作缺少持久回执。")
	}
	return SkillLifecycleResult{Receipt: *guard.receipt, Installation: installation}, nil
}

// Resolve a committed request before checking the present package or state. A
// later uninstall, upgrade, or damaged package must not replay the original write.
func (guard *skillLifecycleGuard) preflight(ctx context.Context, s *Store, installation SkillInstallation) (bool, error) {
	if guard == nil {
		return false, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err := authorizeSkillScopeMutationQuery(ctx, tx, installationScope(installation)); err != nil {
		return false, err
	}
	raw, hit, err := readSkillCommandReceipt(ctx, tx, guard.meta)
	if err != nil {
		return false, err
	}
	if hit {
		return true, guard.restoreReceipt(ctx, tx, raw)
	}
	loaded := skillLifecycleSnapshot(installation)
	if loaded.ActiveVersionID != guard.command.Expected.ActiveVersionID || loaded.Status != guard.command.Expected.Status || *loaded.Enabled != *guard.command.Expected.Enabled || *loaded.EventCount != *guard.command.Expected.EventCount {
		return false, domainError("SKILL_INSTALLATION_STATE_CONFLICT", "Skill 安装状态在读取期间发生变化，请刷新后重试。")
	}
	return false, guard.checkSnapshot(ctx, tx)
}

func (guard *skillLifecycleGuard) begin(ctx context.Context, s *Store, tx *sql.Tx, installation SkillInstallation) (bool, error) {
	if guard == nil {
		return false, checkSkillLifecycleSnapshotTx(ctx, tx, installation.SkillInstallationID, skillLifecycleSnapshot(installation))
	}
	raw, hit, err := s.beginIdempotency(ctx, tx, guard.meta)
	if err != nil {
		return false, err
	}
	if hit {
		return true, guard.restoreReceipt(ctx, tx, raw)
	}
	return false, guard.checkSnapshot(ctx, tx)
}

func (guard *skillLifecycleGuard) checkSnapshot(ctx context.Context, tx *sql.Tx) error {
	return checkSkillLifecycleSnapshotTx(ctx, tx, guard.command.InstallationID, *guard.command.Expected)
}

func skillLifecycleSnapshot(installation SkillInstallation) SkillLifecycleSnapshot {
	count, enabled := len(installation.Events), installation.Enabled
	version := ""
	if installation.ActiveVersionID != nil {
		version = *installation.ActiveVersionID
	}
	return SkillLifecycleSnapshot{ActiveVersionID: version, Status: installation.Status, Enabled: &enabled, EventCount: &count}
}

func checkSkillLifecycleSnapshotTx(ctx context.Context, tx *sql.Tx, installationID string, expected SkillLifecycleSnapshot) error {
	var version, status string
	var enabled bool
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(active_version_id,''),status,enabled,
		(SELECT COUNT(*) FROM skill_installation_events e WHERE e.skill_installation_id=i.skill_installation_id)
		FROM skill_installations i WHERE skill_installation_id=? AND workspace_id=?`, installationID, identity.WorkspaceIDFromContext(ctx)).Scan(&version, &status, &enabled, &count)
	if err != nil {
		return err
	}
	if version != expected.ActiveVersionID || status != expected.Status || enabled != *expected.Enabled || count != *expected.EventCount {
		return domainError("SKILL_INSTALLATION_STATE_CONFLICT", "Skill 的版本或启停状态已变化，请刷新后重新确认。")
	}
	return nil
}

func (guard *skillLifecycleGuard) restoreReceipt(ctx context.Context, tx *sql.Tx, raw json.RawMessage) error {
	receipt, err := decodeIdempotentResult[SkillLifecycleReceipt](raw)
	if err != nil {
		return err
	}
	invalid := func() error {
		return domainError("IDEMPOTENCY_RESULT_INVALID", "Skill 操作回执与原请求或审计记录不一致。")
	}
	if receipt.RequestID != guard.command.IdempotencyKey || receipt.InstallationID != guard.command.InstallationID || receipt.Action != guard.command.Action || receipt.ActorRef != guard.actor || receipt.EventID == "" || receipt.VersionID == "" {
		return invalid()
	}
	var eventType, actor, versionID, version, payload string
	err = tx.QueryRowContext(ctx, `SELECT e.event_type,e.actor_ref,e.skill_version_id,v.version,e.payload_json
		FROM skill_installation_events e JOIN skill_versions v ON v.skill_version_id=e.skill_version_id AND v.skill_installation_id=e.skill_installation_id
		WHERE e.skill_installation_event_id=? AND e.skill_installation_id=?`, receipt.EventID, receipt.InstallationID).Scan(&eventType, &actor, &versionID, &version, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return invalid()
	}
	if err != nil {
		return err
	}
	if eventType != skillLifecycleEventType(receipt.Action) || actor != guard.actor || versionID != receipt.VersionID || (receipt.Action == "activate" && version != guard.command.Version) || (receipt.Action != "activate" && versionID != guard.command.Expected.ActiveVersionID) {
		return invalid()
	}
	var binding struct {
		RequestID   string `json:"request_id"`
		RequestHash string `json:"request_hash"`
	}
	if json.Unmarshal([]byte(payload), &binding) != nil || binding.RequestID != guard.command.IdempotencyKey || binding.RequestHash != guard.meta.RequestHash {
		return invalid()
	}
	guard.receipt = &receipt
	return nil
}

func (guard *skillLifecycleGuard) complete(ctx context.Context, tx *sql.Tx, eventID string, versionID *string, now time.Time) error {
	if guard == nil {
		return nil
	}
	if versionID == nil || *versionID == "" || eventID == "" {
		return domainError("IDEMPOTENCY_RESULT_INVALID", "Skill 操作缺少审计版本。")
	}
	receipt := SkillLifecycleReceipt{RequestID: guard.command.IdempotencyKey, InstallationID: guard.command.InstallationID, EventID: eventID, VersionID: *versionID, Action: guard.command.Action, ActorRef: guard.actor}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if err := guard.restoreReceipt(ctx, tx, raw); err != nil {
		return err
	}
	return completeIdempotency(ctx, tx, guard.meta, receipt, now)
}

func (guard *skillLifecycleGuard) eventPayload(payload map[string]any) map[string]any {
	if payload == nil {
		payload = map[string]any{}
	}
	if guard == nil {
		return payload
	}
	payload["request_id"], payload["request_hash"] = guard.command.IdempotencyKey, guard.meta.RequestHash
	return payload
}

func skillLifecycleEventType(action string) string {
	switch action {
	case "enable":
		return "skill.enabled"
	case "disable":
		return "skill.disabled"
	case "activate":
		return "skill.version.activated"
	case "uninstall":
		return "skill.uninstalled"
	}
	return ""
}
