package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

func migrateWorkspaceAgentTools(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS workspace_agent_tool_settings (
		workspace_id TEXT PRIMARY KEY REFERENCES workspaces(workspace_id),
		version INTEGER NOT NULL CHECK(version > 0),
		settings_json TEXT NOT NULL,
		updated_by TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS agent_tool_config_snapshots (
		agent_tool_call_id TEXT PRIMARY KEY REFERENCES agent_tool_calls(agent_tool_call_id) ON DELETE CASCADE,
		configuration_hash TEXT NOT NULL
	);`)
	return err
}

type AgentToolConfiguration struct {
	WorkspaceID string                          `json:"workspace_id"`
	Version     int                             `json:"version"`
	Options     []agenttool.ConfigurationOption `json:"options"`
}

type UpdateAgentToolConfigurationCommand struct {
	ExpectedVersion int             `json:"expected_version"`
	Enabled         map[string]bool `json:"enabled"`
}

func (s *Store) agentToolSettingsQuery(ctx context.Context, query rowQueryer, workspaceID string) (agenttool.WorkspaceSettings, int, error) {
	settings := agenttool.WorkspaceSettings{Enabled: map[string]bool{}}
	var raw string
	var version int
	err := query.QueryRowContext(ctx, `SELECT settings_json, version FROM workspace_agent_tool_settings WHERE workspace_id = ?`, workspaceID).Scan(&raw, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, 0, nil
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &settings)
	}
	settings.Revision = version
	return settings, version, err
}

func (s *Store) agentToolRegistryQuery(ctx context.Context, query rowQueryer, workspaceID string) (*agenttool.Registry, error) {
	return s.agentToolRegistryForUserQuery(ctx, query, workspaceID, identity.UserIDFromContext(ctx))
}

func (s *Store) agentToolRegistryForUserQuery(ctx context.Context, query rowQueryer, workspaceID, userID string) (*agenttool.Registry, error) {
	if s.agentTools == nil {
		return nil, domainError("AGENT_TOOL_REGISTRY_UNAVAILABLE", "平台工具目录尚未配置。")
	}
	settings, _, err := s.agentToolSettingsQuery(ctx, query, workspaceID)
	if err != nil {
		return nil, err
	}
	registry, err := s.agentTools.ForWorkspace(workspaceID, settings)
	if err != nil {
		return nil, err
	}
	if err := s.bindMCPConnections(ctx, query, registry, workspaceID, userID); err != nil {
		return nil, err
	}
	return registry, nil
}

func (s *Store) AgentToolCatalog(ctx context.Context, private bool) (agenttool.Catalog, error) {
	if activity, active := AgentActivityFromContext(ctx); active && activity.MemoryGenerationID != "" {
		return s.memoryGenerationToolCatalog(ctx)
	}
	registry, err := s.agentToolRegistryQuery(ctx, s.db, identity.WorkspaceIDFromContext(ctx))
	if err != nil {
		return agenttool.Catalog{}, err
	}
	if private {
		return registry.PrivateCatalog(), nil
	}
	return registry.PublicCatalog(), nil
}

func (s *Store) GetAgentToolConfiguration(ctx context.Context) (AgentToolConfiguration, error) {
	workspaceID := identity.WorkspaceIDFromContext(ctx)
	settings, version, err := s.agentToolSettingsQuery(ctx, s.db, workspaceID)
	if err != nil {
		return AgentToolConfiguration{}, err
	}
	if s.agentTools == nil {
		return AgentToolConfiguration{}, domainError("AGENT_TOOL_REGISTRY_UNAVAILABLE", "平台工具目录尚未配置。")
	}
	registry, err := s.agentTools.ForWorkspace(workspaceID, settings)
	if err != nil {
		return AgentToolConfiguration{}, err
	}
	return AgentToolConfiguration{WorkspaceID: workspaceID, Version: version, Options: registry.ConfigurationOptions()}, nil
}

func (s *Store) UpdateAgentToolConfiguration(ctx context.Context, command UpdateAgentToolConfigurationCommand) (AgentToolConfiguration, error) {
	principal, ok := identity.FromContext(ctx)
	if !ok || !principal.ValidUser() || !principal.Allows(identity.RoleAdmin) {
		return AgentToolConfiguration{}, domainError("WORKSPACE_ACCESS_DENIED", "只有工作区管理员可修改工具配置。")
	}
	if len(command.Enabled) == 0 || len(command.Enabled) > 128 || command.ExpectedVersion < 0 {
		return AgentToolConfiguration{}, domainError("REQUEST_VALIDATION_FAILED", "工具配置更新为空或超出限制。")
	}
	if s.agentTools == nil {
		return AgentToolConfiguration{}, domainError("AGENT_TOOL_REGISTRY_UNAVAILABLE", "平台工具目录尚未配置。")
	}
	workspaceID := principal.WorkspaceID
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentToolConfiguration{}, err
	}
	defer tx.Rollback()
	var role identity.Role
	if err := tx.QueryRowContext(ctx, `SELECT membership.role FROM workspace_memberships membership JOIN workspaces workspace ON workspace.workspace_id = membership.workspace_id
		WHERE membership.workspace_id = ? AND membership.user_id = ? AND membership.status = 'active' AND workspace.status = 'active'`, workspaceID, principal.UserID).Scan(&role); err != nil || (role != identity.RoleAdmin && role != identity.RoleOwner) {
		return AgentToolConfiguration{}, domainError("WORKSPACE_ACCESS_DENIED", "当前用户无权修改工作区工具配置。")
	}
	settings, version, err := s.agentToolSettingsQuery(ctx, tx, workspaceID)
	if err != nil {
		return AgentToolConfiguration{}, err
	}
	if version != command.ExpectedVersion {
		return AgentToolConfiguration{}, domainError("AGENT_TOOL_CONFIGURATION_CONFLICT", "工具配置已变化，请刷新后重试。")
	}
	available, err := s.agentTools.ForWorkspace(workspaceID, agenttool.WorkspaceSettings{})
	if err != nil {
		return AgentToolConfiguration{}, err
	}
	options := make(map[string]agenttool.ConfigurationOption)
	for _, option := range available.ConfigurationOptions() {
		options[option.ID] = option
	}
	if settings.Enabled == nil {
		settings.Enabled = map[string]bool{}
	}
	for id, enabled := range command.Enabled {
		option, exists := options[id]
		if !exists || (enabled && !option.Configurable) {
			return AgentToolConfiguration{}, domainError("AGENT_TOOL_CONFIGURATION_INVALID", "工具未获工作区授权或缺少平台配置。")
		}
		settings.Enabled[id] = enabled
	}
	registry, err := s.agentTools.ForWorkspace(workspaceID, settings)
	if err != nil {
		return AgentToolConfiguration{}, domainError("AGENT_TOOL_CONFIGURATION_INVALID", "工具配置未通过校验。")
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return AgentToolConfiguration{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workspace_agent_tool_settings(workspace_id, version, settings_json, updated_by, updated_at) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id) DO UPDATE SET version = excluded.version, settings_json = excluded.settings_json, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		workspaceID, version+1, string(encoded), principal.UserID, formatTime(s.now()))
	if err != nil {
		return AgentToolConfiguration{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentToolConfiguration{}, err
	}
	return AgentToolConfiguration{WorkspaceID: workspaceID, Version: version + 1, Options: registry.ConfigurationOptions()}, nil
}

func (s *Store) validateAgentToolConfigurationTx(ctx context.Context, tx *sql.Tx, call AgentToolCall, expected string) error {
	if call.ToolKind == string(agenttool.KindRuntimeFunction) {
		return nil
	}
	var bound, userID string
	if err := tx.QueryRowContext(ctx, `SELECT configuration_hash, credential_user_id FROM agent_tool_config_snapshots WHERE agent_tool_call_id = ?`, call.AgentToolCallID).Scan(&bound, &userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domainError("AGENT_TOOL_CONFIGURATION_CHANGED", "旧工具审批没有配置快照，请重新发起调用。")
		}
		return err
	}
	registry, err := s.agentToolRegistryForUserQuery(ctx, tx, call.WorkspaceID, userID)
	if err != nil {
		return err
	}
	descriptor, ok := registry.Get(call.ToolID)
	if !ok || !descriptor.Enabled || bound == "" || bound != descriptor.ConfigurationHash || expected != bound {
		return domainError("AGENT_TOOL_CONFIGURATION_CHANGED", "工具配置已变更或禁用，请重新发起调用与审批。")
	}
	return nil
}

func (s *Store) applyWorkspaceToolDependencies(ctx context.Context, query rowQueryer, workspaceID string, registry *capability.Registry) error {
	if s.agentTools == nil {
		return nil
	}
	tools, err := s.agentToolRegistryQuery(ctx, query, workspaceID)
	if err != nil {
		return fmt.Errorf("load workspace tool dependencies: %w", err)
	}
	return registry.SetSkillDependencyResolver(tools)
}

func (s *Store) executionSkillEntryWithTools(ctx context.Context, query rowQueryer, workspaceID string, skill *capability.SkillPackage) (capability.Entry, bool, error) {
	registry, err := s.registry.ForkWithSnapshots([]*capability.SkillPackage{skill})
	if err != nil {
		return capability.Entry{}, false, err
	}
	if err := s.applyWorkspaceToolDependencies(ctx, query, workspaceID, registry); err != nil {
		return capability.Entry{}, false, err
	}
	entry, ok := registry.Get(skill.CapabilityID)
	return entry, ok, nil
}
