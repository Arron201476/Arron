package runtime

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"unicode"

	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/identity"
)

func migrateMCPConnections(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS mcp_connections (
		workspace_id TEXT NOT NULL REFERENCES workspaces(workspace_id),
		owner_user_id TEXT NOT NULL,
		server_id TEXT NOT NULL,
		version INTEGER NOT NULL CHECK(version > 0),
		status TEXT NOT NULL CHECK(status IN ('active', 'deleted')),
		ciphertext BLOB,
		request_id TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY(workspace_id, owner_user_id, server_id),
		CHECK((status = 'active' AND ciphertext IS NOT NULL) OR (status = 'deleted' AND ciphertext IS NULL))
	);`)
	if err != nil {
		return err
	}
	present, err := tableHasColumn(db, "agent_tool_config_snapshots", "credential_user_id")
	if err != nil || present {
		return err
	}
	_, err = db.Exec(`ALTER TABLE agent_tool_config_snapshots ADD COLUMN credential_user_id TEXT NOT NULL DEFAULT ''`)
	return err
}

// Configure once at startup, before serving requests. Missing keys disable writes
// and secret resolution; encrypted records never fall back to plaintext storage.
func (s *Store) ConfigureMCPCredentials(encodedKey string) error {
	if encodedKey == "" {
		return nil
	}
	key, err := base64.StdEncoding.Strict().DecodeString(encodedKey)
	if err != nil || len(key) != 32 || s.mcpCredentialCipher != nil {
		return errors.New("MCP credential storage requires one base64-encoded 32-byte encryption key")
	}
	block, err := aes.NewCipher(key)
	clear(key)
	if err != nil {
		return errors.New("MCP credential encryption initialization failed")
	}
	s.mcpCredentialCipher, err = cipher.NewGCM(block)
	return err
}

type MCPConnectionState struct {
	Scope     string `json:"scope"`
	Version   int    `json:"version"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type MCPConnectionOption struct {
	ServerID       string             `json:"server_id"`
	DisplayName    string             `json:"display_name"`
	Fields         []string           `json:"fields"`
	Enabled        bool               `json:"enabled"`
	EffectiveScope string             `json:"effective_scope"`
	Personal       MCPConnectionState `json:"personal"`
	Workspace      MCPConnectionState `json:"workspace"`
}

type MCPConnectionInventory struct {
	StorageAvailable   bool                  `json:"storage_available"`
	CanManagePersonal  bool                  `json:"can_manage_personal"`
	CanManageWorkspace bool                  `json:"can_manage_workspace"`
	Items              []MCPConnectionOption `json:"items"`
}

type UpdateMCPConnectionCommand struct {
	ServerID        string            `json:"server_id"`
	Scope           string            `json:"scope"`
	ExpectedVersion int               `json:"expected_version"`
	RequestID       string            `json:"request_id"`
	Values          map[string]string `json:"values,omitempty"`
	Delete          bool              `json:"delete,omitempty"`
}

type mcpConnectionRecord struct {
	MCPConnectionState
	WorkspaceID string
	OwnerUserID string
	ServerID    string
	Ciphertext  []byte
	RequestID   string
}

func mcpConnectionQuery(ctx context.Context, query rowQueryer, workspaceID, userID, serverID string) (mcpConnectionRecord, error) {
	record := mcpConnectionRecord{WorkspaceID: workspaceID, OwnerUserID: userID, ServerID: serverID,
		MCPConnectionState: MCPConnectionState{Scope: "workspace", Status: "missing"}}
	if userID != "" {
		record.Scope = "user"
	}
	err := query.QueryRowContext(ctx, `SELECT version, status, ciphertext, request_id, updated_at FROM mcp_connections
		WHERE workspace_id = ? AND owner_user_id = ? AND server_id = ?`, workspaceID, userID, serverID).
		Scan(&record.Version, &record.Status, &record.Ciphertext, &record.RequestID, &record.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return record, err
}

func mcpConnectionPair(ctx context.Context, query rowQueryer, workspaceID, userID, serverID string) (mcpConnectionRecord, mcpConnectionRecord, error) {
	personal, err := mcpConnectionQuery(ctx, query, workspaceID, userID, serverID)
	if err != nil {
		return personal, mcpConnectionRecord{}, err
	}
	shared, err := mcpConnectionQuery(ctx, query, workspaceID, "", serverID)
	return personal, shared, err
}

func mcpConnectionBinding(personal, shared mcpConnectionRecord) string {
	// Tombstone versions prevent an old approval from becoming valid again after
	// a credential is removed and the connection falls back to workspace scope.
	raw, _ := json.Marshal([]any{personal.WorkspaceID, personal.OwnerUserID, personal.ServerID,
		personal.Version, personal.Status, shared.Version, shared.Status})
	return "sha256:" + sha256Hex(raw)
}

func (s *Store) bindMCPConnections(ctx context.Context, query rowQueryer, registry *agenttool.Registry, workspaceID, userID string) error {
	bindings := map[string]string{}
	unavailable := map[string]bool{}
	for _, server := range registry.PrivateCatalog().MCPServers {
		if len(server.CredentialFields()) == 0 {
			continue
		}
		personal, shared, err := mcpConnectionPair(ctx, query, workspaceID, userID, server.ID)
		if err != nil {
			return err
		}
		bindings[server.ID] = mcpConnectionBinding(personal, shared)
		unavailable[server.ID] = s.mcpCredentialCipher == nil || (personal.Status != "active" && shared.Status != "active")
	}
	registry.BindMCPCredentials(bindings, unavailable)
	return nil
}

func (s *Store) ListMCPConnections(ctx context.Context) (MCPConnectionInventory, error) {
	principal, ok := identity.FromContext(ctx)
	if !ok || !principal.ValidUser() {
		return MCPConnectionInventory{}, domainError("AUTHENTICATION_REQUIRED", "需要用户身份。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MCPConnectionInventory{}, err
	}
	defer tx.Rollback()
	principal, err = resolvePrincipalQuery(ctx, tx, principal)
	if err != nil {
		return MCPConnectionInventory{}, err
	}
	registry, err := s.agentToolRegistryQuery(ctx, tx, principal.WorkspaceID)
	if err != nil {
		return MCPConnectionInventory{}, err
	}
	result := MCPConnectionInventory{StorageAvailable: s.mcpCredentialCipher != nil,
		CanManageWorkspace: principal.Allows(identity.RoleAdmin), CanManagePersonal: principal.Allows(identity.RoleEditor), Items: []MCPConnectionOption{}}
	for _, server := range registry.PrivateCatalog().MCPServers {
		fields := server.CredentialFields()
		if len(fields) == 0 {
			continue
		}
		personal, shared, err := mcpConnectionPair(ctx, tx, principal.WorkspaceID, principal.UserID, server.ID)
		if err != nil {
			return MCPConnectionInventory{}, err
		}
		effective := "none"
		if shared.Status == "active" {
			effective = "workspace"
		}
		if personal.Status == "active" {
			effective = "user"
		}
		result.Items = append(result.Items, MCPConnectionOption{ServerID: server.ID, DisplayName: server.ID,
			Fields: fields, Enabled: server.Enabled, Personal: personal.MCPConnectionState, Workspace: shared.MCPConnectionState, EffectiveScope: effective})
	}
	return result, nil
}

func (record mcpConnectionRecord) associatedData() []byte {
	raw, _ := json.Marshal([]any{"mcp-connection-v1", record.WorkspaceID, record.OwnerUserID, record.ServerID, record.Version})
	return raw
}

func (s *Store) decryptMCPConnection(record mcpConnectionRecord) (map[string]string, error) {
	if s.mcpCredentialCipher == nil {
		return nil, domainError("MCP_CREDENTIAL_STORAGE_UNAVAILABLE", "平台尚未配置凭据加密密钥。")
	}
	nonceSize := s.mcpCredentialCipher.NonceSize()
	if len(record.Ciphertext) < nonceSize {
		return nil, domainError("MCP_CREDENTIAL_UNAVAILABLE", "连接凭据不可读取，请重新配置。")
	}
	raw, err := s.mcpCredentialCipher.Open(nil, record.Ciphertext[:nonceSize], record.Ciphertext[nonceSize:], record.associatedData())
	if err != nil {
		return nil, domainError("MCP_CREDENTIAL_UNAVAILABLE", "连接凭据不可读取，请重新配置。")
	}
	defer clear(raw)
	var values map[string]string
	if json.Unmarshal(raw, &values) != nil {
		return nil, domainError("MCP_CREDENTIAL_UNAVAILABLE", "连接凭据不可读取，请重新配置。")
	}
	return values, nil
}

func validateMCPCredentialValues(server agenttool.MCPServerConfig, values map[string]string) error {
	fields := server.CredentialFields()
	if len(fields) == 0 || len(values) != len(fields) {
		return domainError("REQUEST_VALIDATION_FAILED", "凭据字段必须与当前连接配置一致。")
	}
	for _, field := range fields {
		value := values[field]
		if strings.TrimSpace(value) == "" || len(value) > 4096 || strings.ContainsFunc(value, unicode.IsControl) {
			return domainError("REQUEST_VALIDATION_FAILED", "凭据值为空、过长或包含控制字符。")
		}
	}
	return nil
}

func (s *Store) UpdateMCPConnection(ctx context.Context, command UpdateMCPConnectionCommand) (MCPConnectionState, error) {
	principal, ok := identity.FromContext(ctx)
	if !ok || !principal.ValidUser() {
		return MCPConnectionState{}, domainError("AUTHENTICATION_REQUIRED", "需要用户身份。")
	}
	if (command.Scope != "user" && command.Scope != "workspace") || command.ExpectedVersion < 0 ||
		strings.TrimSpace(command.RequestID) == "" || len(command.RequestID) > 128 || strings.ContainsFunc(command.RequestID, unicode.IsControl) {
		return MCPConnectionState{}, domainError("REQUEST_VALIDATION_FAILED", "连接更新请求无效。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MCPConnectionState{}, err
	}
	defer tx.Rollback()
	principal, err = resolvePrincipalQuery(ctx, tx, principal)
	if err != nil {
		return MCPConnectionState{}, err
	}
	if !principal.Allows(identity.RoleEditor) || (command.Scope == "workspace" && !principal.Allows(identity.RoleAdmin)) {
		return MCPConnectionState{}, domainError("WORKSPACE_ACCESS_DENIED", "当前用户无权修改此范围的连接凭据。")
	}
	registry, err := s.agentToolRegistryQuery(ctx, tx, principal.WorkspaceID)
	if err != nil {
		return MCPConnectionState{}, err
	}
	var server agenttool.MCPServerConfig
	for _, option := range registry.PrivateCatalog().MCPServers {
		if option.ID == command.ServerID {
			server = option
			break
		}
	}
	if server.ID == "" || len(server.CredentialFields()) == 0 {
		return MCPConnectionState{}, domainError("RESOURCE_NOT_FOUND", "连接不存在或未开放用户凭据配置。")
	}
	if command.Delete {
		if len(command.Values) != 0 {
			return MCPConnectionState{}, domainError("REQUEST_VALIDATION_FAILED", "撤销凭据不能包含新值。")
		}
	} else {
		if s.mcpCredentialCipher == nil {
			return MCPConnectionState{}, domainError("MCP_CREDENTIAL_STORAGE_UNAVAILABLE", "平台尚未配置凭据加密密钥。")
		}
		if err := validateMCPCredentialValues(server, command.Values); err != nil {
			return MCPConnectionState{}, err
		}
	}
	userID := principal.UserID
	if command.Scope == "workspace" {
		userID = ""
	}
	record, err := mcpConnectionQuery(ctx, tx, principal.WorkspaceID, userID, server.ID)
	if err != nil {
		return MCPConnectionState{}, err
	}
	if record.RequestID == command.RequestID {
		if record.Version != command.ExpectedVersion+1 {
			return MCPConnectionState{}, domainError("IDEMPOTENCY_CONFLICT", "此请求标识已用于其他版本的凭据更新。")
		}
		if command.Delete && record.Status == "deleted" {
			return record.MCPConnectionState, nil
		}
		if !command.Delete && record.Status == "active" {
			values, err := s.decryptMCPConnection(record)
			if err != nil {
				return MCPConnectionState{}, err
			}
			if maps.Equal(values, command.Values) {
				return record.MCPConnectionState, nil
			}
		}
		return MCPConnectionState{}, domainError("IDEMPOTENCY_CONFLICT", "此请求标识已用于不同的凭据更新。")
	}
	if record.Version != command.ExpectedVersion {
		return MCPConnectionState{}, domainError("MCP_CREDENTIAL_CONFLICT", "凭据已变化，请刷新后重试。")
	}
	record.Version++
	record.Status, record.Ciphertext = "deleted", nil
	if !command.Delete {
		raw, err := json.Marshal(command.Values)
		if err != nil {
			return MCPConnectionState{}, err
		}
		defer clear(raw)
		nonce := make([]byte, s.mcpCredentialCipher.NonceSize())
		if _, err := rand.Read(nonce); err != nil {
			return MCPConnectionState{}, fmt.Errorf("generate MCP credential nonce: %w", err)
		}
		record.Status = "active"
		record.Ciphertext = s.mcpCredentialCipher.Seal(nonce, nonce, raw, record.associatedData())
	}
	record.UpdatedAt = formatTime(s.now())
	_, err = tx.ExecContext(ctx, `INSERT INTO mcp_connections(workspace_id, owner_user_id, server_id, version, status, ciphertext, request_id, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(workspace_id, owner_user_id, server_id) DO UPDATE SET
		version = excluded.version, status = excluded.status, ciphertext = excluded.ciphertext, request_id = excluded.request_id, updated_at = excluded.updated_at`,
		principal.WorkspaceID, userID, server.ID, record.Version, record.Status, record.Ciphertext, command.RequestID, record.UpdatedAt)
	if err != nil {
		return MCPConnectionState{}, err
	}
	if err := tx.Commit(); err != nil {
		return MCPConnectionState{}, err
	}
	return record.MCPConnectionState, nil
}

type ResolveMCPCredentialsCommand struct {
	ServerID          string `json:"server_id"`
	CredentialBinding string `json:"credential_binding"`
}

type ResolvedMCPCredentials struct {
	ServerID          string            `json:"server_id"`
	CredentialBinding string            `json:"credential_binding"`
	Values            map[string]string `json:"values"`
}

func (s *Store) ResolveMCPCredentials(ctx context.Context, command ResolveMCPCredentialsCommand) (ResolvedMCPCredentials, error) {
	transport, authenticated := identity.FromContext(ctx)
	activity, active := AgentActivityFromContext(ctx)
	if !authenticated || transport.Kind != identity.KindService || !active || activity.AllowTerminal {
		return ResolvedMCPCredentials{}, domainError("AGENT_ACTIVITY_FORBIDDEN", "连接凭据只向有效的内部 Agent 执行提供。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ResolvedMCPCredentials{}, err
	}
	defer tx.Rollback()
	principal, err := s.resolveAgentActivityPrincipalTx(ctx, tx, activity)
	if err != nil {
		return ResolvedMCPCredentials{}, err
	}
	if !principal.Allows(identity.RoleEditor) {
		return ResolvedMCPCredentials{}, domainError("WORKSPACE_ACCESS_DENIED", "当前执行用户无权使用连接。")
	}
	ctx = identity.WithDelegatedUser(ctx, principal)
	registry, err := s.agentToolRegistryQuery(ctx, tx, principal.WorkspaceID)
	if err != nil {
		return ResolvedMCPCredentials{}, err
	}
	var server agenttool.MCPServerConfig
	for _, option := range registry.PrivateCatalog().MCPServers {
		if option.ID == command.ServerID && option.Enabled {
			server = option
			break
		}
	}
	if server.ID == "" || len(server.CredentialFields()) == 0 || command.CredentialBinding == "" || server.CredentialBinding != command.CredentialBinding {
		return ResolvedMCPCredentials{}, domainError("AGENT_TOOL_CONFIGURATION_CHANGED", "连接配置或凭据已变化，请重新加载工具目录。")
	}
	personal, shared, err := mcpConnectionPair(ctx, tx, principal.WorkspaceID, principal.UserID, server.ID)
	if err != nil {
		return ResolvedMCPCredentials{}, err
	}
	selected := shared
	if personal.Status == "active" {
		selected = personal
	}
	if selected.Status != "active" {
		return ResolvedMCPCredentials{}, domainError("MCP_CREDENTIAL_REQUIRED", "此连接尚未配置有效凭据。")
	}
	values, err := s.decryptMCPConnection(selected)
	if err != nil {
		return ResolvedMCPCredentials{}, err
	}
	if err := validateMCPCredentialValues(server, values); err != nil {
		return ResolvedMCPCredentials{}, domainError("MCP_CREDENTIAL_UNAVAILABLE", "凭据字段与当前连接配置不匹配，请重新配置。")
	}
	return ResolvedMCPCredentials{ServerID: server.ID, CredentialBinding: server.CredentialBinding, Values: values}, nil
}
