package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"content-agent/backend/internal/capability"
)

func (s *Store) requestedScriptSnapshotTx(ctx context.Context, tx *sql.Tx, workspaceID string, command BeginAgentToolCallCommand) (activeSkillScript, error) {
	arguments, err := decodeScriptToolArguments(command.Arguments)
	if err != nil {
		return activeSkillScript{}, err
	}
	var capabilityID, version string
	var entry capability.Entry
	var found bool
	if command.SkillSnapshot != nil {
		capabilityID, version = command.SkillSnapshot.CapabilityID, command.SkillSnapshot.Version
		if capabilityID == "" || version == "" || command.SkillSnapshot.ContentHash == "" {
			return activeSkillScript{}, domainError("SKILL_SCRIPT_SNAPSHOT_MISMATCH", "Skill 版本快照不完整。")
		}
		if arguments.CapabilityID != "" && arguments.CapabilityID != capabilityID {
			return activeSkillScript{}, domainError("SKILL_SCRIPT_SNAPSHOT_MISMATCH", "脚本参数与选定 Skill 的身份不一致。")
		}
	}
	if command.SkillInvocationID != "" {
		var invocationCapability, invocationVersion string
		if err := tx.QueryRowContext(ctx, `SELECT capability_id, capability_version FROM skill_invocations WHERE skill_invocation_id = ? AND project_id = ?`, command.SkillInvocationID, command.ProjectID).Scan(&invocationCapability, &invocationVersion); err != nil {
			return activeSkillScript{}, err
		}
		if version != "" && (capabilityID != invocationCapability || version != invocationVersion) {
			return activeSkillScript{}, domainError("SKILL_SCRIPT_SNAPSHOT_MISMATCH", "脚本版本与 Skill 任务的固定版本不一致。")
		}
		if arguments.CapabilityID != "" && arguments.CapabilityID != invocationCapability {
			return activeSkillScript{}, domainError("SKILL_SCRIPT_SNAPSHOT_MISMATCH", "脚本参数与 Skill 任务的身份不一致。")
		}
		capabilityID, version = invocationCapability, invocationVersion
		entry, found, err = s.capabilityEntryForInvocationQuery(ctx, tx, command.SkillInvocationID)
	} else if capabilityID != "" {
		entry, found, err = s.capabilityEntryForProjectVersionQuery(ctx, tx, command.ProjectID, capabilityID, version)
	} else {
		registry, selectionErr := s.buildSelectionRegistry(ctx, tx, workspaceID, command.ProjectID)
		if selectionErr != nil {
			return activeSkillScript{}, selectionErr
		}
		matchedName := false
		for _, candidate := range registry.Entries() {
			if candidate.Skill == nil || candidate.Skill.Name != arguments.SkillName ||
				(arguments.CapabilityID != "" && candidate.Skill.CapabilityID != arguments.CapabilityID) {
				continue
			}
			matchedName = true
			for _, script := range candidate.Skill.Scripts {
				if script.ID != arguments.ScriptID {
					continue
				}
				if found {
					return activeSkillScript{}, domainError("SKILL_SCRIPT_IDENTITY_AMBIGUOUS", "多个同名 Skill 声明了该脚本，请提供确切的 capability_id。")
				}
				entry, found = candidate, true
				break
			}
		}
		if matchedName && !found {
			return activeSkillScript{}, domainError("SKILL_SCRIPT_NOT_DECLARED", "请求的脚本未在选定 Skill 中声明。")
		}
	}
	if err != nil {
		return activeSkillScript{}, err
	}
	if !found || entry.Skill == nil || entry.Status != capability.Available || entry.Skill.Name != arguments.SkillName {
		return activeSkillScript{}, domainError("SKILL_SCRIPT_SKILL_UNAVAILABLE", "请求的 Skill 未安装、未启用或不在当前可见范围。")
	}
	if err := authorizePersonalSkillExecution(ctx, entry); err != nil {
		return activeSkillScript{}, err
	}
	_, snapshotID, err := s.pinExecutionSkillTx(ctx, tx, command.ProjectID, entry)
	if err != nil {
		return activeSkillScript{}, err
	}
	if snapshotID == nil {
		return activeSkillScript{}, domainError("SKILL_SCRIPT_SKILL_UNAVAILABLE", "脚本执行需要不可变的 Skill 副本。")
	}
	selected, err := s.resolveExecutionSkillScriptTx(ctx, tx, command.ProjectID, arguments.SkillName, arguments.ScriptID, entry.Skill.CapabilityID, *snapshotID)
	if err != nil {
		return activeSkillScript{}, err
	}
	if command.SkillSnapshot != nil && selected.contentHash != command.SkillSnapshot.ContentHash {
		return activeSkillScript{}, domainError("SKILL_SCRIPT_SNAPSHOT_MISMATCH", "脚本内容与选定 Skill 的哈希不一致。")
	}
	return selected, nil
}

func scriptSnapshotForCallTx(ctx context.Context, tx *sql.Tx, callID string) (AgentToolSkillSnapshot, string, string, error) {
	var snapshot AgentToolSkillSnapshot
	var versionID, snapshotID, descriptorJSON string
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(si.capability_id, ''), COALESCE(sv.version, ''), COALESCE(sv.content_hash, ''), COALESCE(sv.skill_version_id, ''), COALESCE(binding.skill_snapshot_id, ''), COALESCE(execution.descriptor_json, '')
		FROM agent_tool_skill_snapshots binding
		LEFT JOIN skill_versions sv ON sv.skill_version_id = binding.skill_version_id
		LEFT JOIN skill_installations si ON si.skill_installation_id = sv.skill_installation_id
		LEFT JOIN skill_execution_snapshots execution ON execution.skill_snapshot_id = binding.skill_snapshot_id
		WHERE binding.agent_tool_call_id = ?`, callID).Scan(&snapshot.CapabilityID, &snapshot.Version, &snapshot.ContentHash, &versionID, &snapshotID, &descriptorJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, "", "", domainError("SKILL_SCRIPT_SNAPSHOT_UNAVAILABLE", "这次脚本审批缺少版本快照，请重新发起脚本调用并确认。")
	}
	if err == nil && snapshotID != "" {
		var descriptor capability.SkillPackage
		if err := json.Unmarshal([]byte(descriptorJSON), &descriptor); err != nil {
			return snapshot, "", "", err
		}
		snapshot = AgentToolSkillSnapshot{CapabilityID: descriptor.CapabilityID, Version: descriptor.Version, ContentHash: descriptor.ContentHash}
	}
	return snapshot, versionID, snapshotID, err
}

func (s *Store) resolveExecutionSkillScriptTx(ctx context.Context, tx *sql.Tx, projectID, skillName, scriptID, capabilityID, snapshotID string) (activeSkillScript, error) {
	entry, ok, err := s.capabilityEntryForExecutionSnapshotQuery(ctx, tx, projectID, capabilityID, snapshotID)
	if err != nil {
		return activeSkillScript{}, err
	}
	if !ok || entry.Skill == nil || entry.Skill.Name != skillName || entry.Status != capability.Available {
		return activeSkillScript{}, domainError("SKILL_SCRIPT_SKILL_UNAVAILABLE", "执行绑定的 Skill 已不可用。")
	}
	selected := activeSkillScript{snapshotID: snapshotID, versionID: entry.Skill.ManagedVersionID,
		skillName: skillName, capabilityID: capabilityID, version: entry.Skill.Version,
		executionMode: entry.Skill.ExecutionMode, contentHash: entry.Skill.ContentHash}
	if err := tx.QueryRowContext(ctx, `SELECT package_ref FROM skill_execution_snapshots WHERE skill_snapshot_id = ?`, snapshotID).Scan(&selected.packageRef); err != nil {
		return activeSkillScript{}, err
	}
	if selected.versionID != "" {
		if err := tx.QueryRowContext(ctx, `SELECT skill_installation_id FROM skill_versions WHERE skill_version_id = ?`, selected.versionID).Scan(&selected.installationID); err != nil {
			return activeSkillScript{}, err
		}
	}
	for _, script := range entry.Skill.Scripts {
		if script.ID == scriptID {
			selected.script = script
			return selected, nil
		}
	}
	return activeSkillScript{}, domainError("SKILL_SCRIPT_NOT_DECLARED", "请求的脚本未在绑定的 Skill 中声明。")
}

func scriptSnapshotStillEnabledTx(ctx context.Context, tx *sql.Tx, callID string) bool {
	var enabled bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM agent_tool_skill_snapshots binding
		JOIN agent_tool_calls call ON call.agent_tool_call_id = binding.agent_tool_call_id
		JOIN workspace_script_policies policy ON policy.workspace_id = call.workspace_id AND policy.enabled = 1
		LEFT JOIN skill_versions version ON version.skill_version_id = binding.skill_version_id
		LEFT JOIN skill_installations installation ON installation.skill_installation_id = version.skill_installation_id
		LEFT JOIN skill_execution_snapshots snapshot ON snapshot.skill_snapshot_id = binding.skill_snapshot_id
		WHERE binding.agent_tool_call_id = ? AND (
			(binding.skill_version_id IS NOT NULL AND version.status = 'installed' AND installation.status = 'installed' AND installation.enabled = 1)
			OR (binding.skill_version_id IS NULL AND snapshot.skill_snapshot_id IS NOT NULL AND NOT EXISTS(
				SELECT 1 FROM skill_installations revoked WHERE revoked.workspace_id = snapshot.workspace_id
				AND revoked.scope = json_extract(snapshot.descriptor_json, '$.Scope')
				AND revoked.scope_ref = json_extract(snapshot.descriptor_json, '$.ScopeRef')
				AND revoked.capability_id = json_extract(snapshot.descriptor_json, '$.CapabilityID')
				AND (revoked.enabled = 0 OR revoked.status != 'installed')))))`, callID).Scan(&enabled)
	return err == nil && enabled
}
