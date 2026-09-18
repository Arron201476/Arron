package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
	"content-agent/backend/internal/scriptsandbox"
)

var scriptEnvironmentAllowlist = []string{
	"CONTENT_AGENT_INPUT",
	"CONTENT_AGENT_OUTPUT",
	"LANG",
	"PATH",
	"PYTHONHASHSEED",
}

type scriptToolArguments struct {
	CapabilityID string          `json:"capability_id,omitempty"`
	SkillName    string          `json:"skill_name"`
	ScriptID     string          `json:"script_id"`
	InputJSON    string          `json:"input_json"`
	Input        json.RawMessage `json:"-"`
}

type activeSkillScript struct {
	installationID string
	versionID      string
	snapshotID     string
	skillName      string
	capabilityID   string
	version        string
	executionMode  string
	contentHash    string
	packageRef     string
	script         capability.SkillScript
}

func (s *Store) GetScriptSandboxPolicy(ctx context.Context) (ScriptSandboxPolicy, error) {
	return s.getScriptSandboxPolicyForWorkspace(ctx, identity.WorkspaceIDFromContext(ctx))
}

func (s *Store) getScriptSandboxPolicyForWorkspace(
	ctx context.Context,
	workspaceID string,
) (ScriptSandboxPolicy, error) {
	return s.getScriptSandboxPolicyForWorkspaceQuery(ctx, s.db, workspaceID)
}

func (s *Store) getScriptSandboxPolicyForWorkspaceQuery(
	ctx context.Context,
	queryer rowQueryer,
	workspaceID string,
) (ScriptSandboxPolicy, error) {
	if !validWorkspaceID(workspaceID) {
		return ScriptSandboxPolicy{}, domainError("WORKSPACE_ACCESS_DENIED", "工作区范围无效。")
	}
	policy := ScriptSandboxPolicy{
		WorkspaceID:          workspaceID,
		Limits:               scriptsandbox.DefaultLimits(),
		EnvironmentAllowlist: append([]string(nil), scriptEnvironmentAllowlist...),
		Sandbox:              s.scriptSandbox.Status(),
	}
	var enabled int
	var limitsJSON, updatedAt string
	err := queryer.QueryRowContext(ctx, `
		SELECT enabled, version, limits_json, updated_by, updated_at
		FROM workspace_script_policies WHERE workspace_id = ?`, workspaceID,
	).Scan(&enabled, &policy.Version, &limitsJSON, &policy.UpdatedBy, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return policy, nil
	}
	if err != nil {
		return ScriptSandboxPolicy{}, err
	}
	if err := json.Unmarshal([]byte(limitsJSON), &policy.Limits); err != nil {
		return ScriptSandboxPolicy{}, fmt.Errorf("decode script sandbox limits: %w", err)
	}
	policy.Enabled = enabled == 1
	parsed, err := parseTime(updatedAt)
	if err != nil {
		return ScriptSandboxPolicy{}, err
	}
	policy.UpdatedAt = &parsed
	return policy, nil
}

func (s *Store) UpdateScriptSandboxPolicy(
	ctx context.Context,
	command UpdateScriptSandboxPolicyCommand,
) (ScriptSandboxPolicy, error) {
	workspaceID := identity.WorkspaceIDFromContext(ctx)
	current, err := s.getScriptSandboxPolicyForWorkspace(ctx, workspaceID)
	if err != nil {
		return ScriptSandboxPolicy{}, err
	}
	if command.ExpectedVersion != current.Version {
		return ScriptSandboxPolicy{}, domainError("SCRIPT_SANDBOX_POLICY_VERSION_CONFLICT", "脚本沙箱策略已更新，请刷新后重试。")
	}
	limits := current.Limits
	if command.Limits != nil {
		limits = *command.Limits
	}
	if err := limits.Validate(); err != nil {
		return ScriptSandboxPolicy{}, domainError("SCRIPT_SANDBOX_LIMITS_INVALID", err.Error())
	}
	if command.Enabled && !current.Sandbox.Available {
		return ScriptSandboxPolicy{}, domainError("SCRIPT_SANDBOX_UNAVAILABLE", current.Sandbox.UserMessage)
	}
	actorRef := strings.TrimSpace(command.ActorRef)
	if _, ok := identity.FromContext(ctx); ok || actorRef == "" {
		actorRef = identity.ActorRefFromContext(ctx)
	}
	limitsJSON, err := json.Marshal(limits)
	if err != nil {
		return ScriptSandboxPolicy{}, err
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ScriptSandboxPolicy{}, err
	}
	defer tx.Rollback()
	if current.Version == 0 {
		result, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO workspace_script_policies(
				workspace_id, enabled, version, limits_json, updated_by, updated_at
			) VALUES(?, ?, 1, ?, ?, ?)`,
			workspaceID, command.Enabled, string(limitsJSON), actorRef, formatTime(now),
		)
		if err != nil {
			return ScriptSandboxPolicy{}, err
		}
		if err := requireOneRow(result, "SCRIPT_SANDBOX_POLICY_VERSION_CONFLICT", "脚本沙箱策略已更新，请刷新后重试。"); err != nil {
			return ScriptSandboxPolicy{}, err
		}
	} else {
		result, err := tx.ExecContext(ctx, `
			UPDATE workspace_script_policies
			SET enabled = ?, version = version + 1, limits_json = ?, updated_by = ?, updated_at = ?
			WHERE workspace_id = ? AND version = ?`,
			command.Enabled, string(limitsJSON), actorRef, formatTime(now),
			workspaceID, current.Version,
		)
		if err != nil {
			return ScriptSandboxPolicy{}, err
		}
		if err := requireOneRow(result, "SCRIPT_SANDBOX_POLICY_VERSION_CONFLICT", "脚本沙箱策略已更新，请刷新后重试。"); err != nil {
			return ScriptSandboxPolicy{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ScriptSandboxPolicy{}, err
	}
	return s.getScriptSandboxPolicyForWorkspace(ctx, workspaceID)
}

func (s *Store) ExecuteSkillScript(
	ctx context.Context,
	command ExecuteSkillScriptCommand,
) (SkillScriptExecution, error) {
	command.AgentToolCallID = strings.TrimSpace(command.AgentToolCallID)
	command.ExpectedSDKToolCallID = strings.TrimSpace(command.ExpectedSDKToolCallID)
	if command.AgentToolCallID == "" || command.ExpectedSDKToolCallID == "" || len(command.Arguments) == 0 {
		return SkillScriptExecution{}, domainError("REQUEST_VALIDATION_FAILED", "脚本执行请求不完整。")
	}
	_, argumentHash, _, err := summarizeAgentToolPayload(command.Arguments, true)
	if err != nil {
		return SkillScriptExecution{}, err
	}
	arguments, err := decodeScriptToolArguments(command.Arguments)
	if err != nil {
		return SkillScriptExecution{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SkillScriptExecution{}, err
	}
	defer tx.Rollback()
	call, err := getAgentToolCallTx(ctx, tx, command.AgentToolCallID)
	if err != nil {
		return SkillScriptExecution{}, err
	}
	if call.ToolID != "runtime:execute_skill_script" || call.SDKToolCallID != command.ExpectedSDKToolCallID ||
		call.ArgumentsHash != argumentHash {
		return SkillScriptExecution{}, domainError("SKILL_SCRIPT_TOOL_CALL_MISMATCH", "脚本请求与已确认的工具调用不一致。")
	}
	if call.Status != "running" || call.ApprovalStatus != "approved" || call.ApprovalConsumedAt == nil {
		return SkillScriptExecution{}, domainError("SKILL_SCRIPT_CONFIRMATION_REQUIRED", "脚本执行必须先经过本次工具调用的用户确认。")
	}
	if err := s.validateAgentTaskToolCallTx(ctx, tx, call.AgentToolCallID); err != nil {
		return SkillScriptExecution{}, err
	}
	existing, err := scanSkillScriptExecution(tx.QueryRowContext(
		ctx, skillScriptExecutionSelect+` WHERE agent_tool_call_id = ?`, call.AgentToolCallID,
	))
	if err == nil {
		if existing.Status == "running" {
			return SkillScriptExecution{}, domainError("SKILL_SCRIPT_EXECUTION_IN_PROGRESS", "该脚本调用正在执行。")
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return SkillScriptExecution{}, err
	}
	policy, err := s.getScriptSandboxPolicyForWorkspaceQuery(ctx, tx, call.WorkspaceID)
	if err != nil {
		return SkillScriptExecution{}, err
	}
	if !policy.Enabled {
		return SkillScriptExecution{}, domainError("SKILL_SCRIPT_POLICY_DISABLED", "工作区管理员尚未启用 Skill 脚本执行。")
	}
	if !policy.Sandbox.Available {
		return SkillScriptExecution{}, domainError("SCRIPT_SANDBOX_UNAVAILABLE", policy.Sandbox.UserMessage)
	}
	snapshot, snapshotVersionID, snapshotID, err := scriptSnapshotForCallTx(ctx, tx, call.AgentToolCallID)
	if err != nil {
		return SkillScriptExecution{}, err
	}
	if arguments.CapabilityID != "" && arguments.CapabilityID != snapshot.CapabilityID {
		return SkillScriptExecution{}, domainError("SKILL_SCRIPT_SNAPSHOT_MISMATCH", "脚本参数与已确认的 Skill 身份不一致。")
	}
	var selected activeSkillScript
	if snapshotID != "" {
		selected, err = s.resolveExecutionSkillScriptTx(ctx, tx, call.ProjectID, arguments.SkillName, arguments.ScriptID, snapshot.CapabilityID, snapshotID)
	} else {
		selected, err = s.resolvePinnedSkillScriptTx(ctx, tx, call.WorkspaceID, arguments.SkillName, arguments.ScriptID, snapshotVersionID)
	}
	if err != nil {
		return SkillScriptExecution{}, err
	}
	if selected.versionID != snapshotVersionID || selected.contentHash != snapshot.ContentHash {
		return SkillScriptExecution{}, domainError("SKILL_SCRIPT_SNAPSHOT_MISMATCH", "脚本执行版本与已确认的快照不一致。")
	}
	packagePath, err := s.verifyManagedSkillPackage(
		selected.packageRef, selected.skillName, selected.capabilityID, selected.version,
		selected.executionMode, selected.contentHash,
	)
	if err != nil {
		return SkillScriptExecution{}, err
	}
	executionID := s.newID("sse")
	executionRoot := filepath.Join(s.dataRoot, "script-executions", call.WorkspaceID, executionID)
	outputPath := filepath.Join(executionRoot, "output")
	outputRef, err := filepath.Rel(s.dataRoot, outputPath)
	if err != nil {
		return SkillScriptExecution{}, err
	}
	limitsJSON, _ := json.Marshal(policy.Limits)
	environmentJSON, _ := json.Marshal(scriptEnvironmentAllowlist)
	requestedBy := "confirmed_user"
	if approval, approvalErr := getAgentToolApprovalForCallTx(ctx, tx, call.AgentToolCallID); approvalErr == nil && approval.ActorRef != nil {
		requestedBy = *approval.ActorRef
	}
	now := s.now()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO skill_script_executions(
			skill_script_execution_id, workspace_id, project_id, conversation_id,
			agent_tool_call_id, skill_installation_id, skill_version_id, skill_snapshot_id,
			script_id, script_path, runtime, adapter, engine, image, status,
			input_hash, limits_json, environment_names_json, output_storage_ref,
			requested_by, started_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'running', ?, ?, ?, ?, ?, ?, ?)`,
		executionID, call.WorkspaceID, call.ProjectID, call.ConversationID,
		call.AgentToolCallID, optionalSkillID(selected.installationID), optionalSkillID(selected.versionID), optionalSkillID(selected.snapshotID),
		selected.script.ID, selected.script.Path, selected.script.Runtime,
		policy.Sandbox.Adapter, policy.Sandbox.Engine, sandboxImage(s.scriptSandbox, selected.script.Runtime),
		sha256Hex(arguments.Input), string(limitsJSON), string(environmentJSON), filepath.ToSlash(outputRef),
		requestedBy, formatTime(now), formatTime(now),
	); err != nil {
		return SkillScriptExecution{}, err
	}
	if _, err := s.appendEvent(
		ctx, tx, call.ProjectID, nil, nil, "skill.script_execution.started",
		"skill_script_execution", executionID,
		map[string]any{"skill_name": selected.skillName, "script_id": selected.script.ID},
	); err != nil {
		return SkillScriptExecution{}, err
	}
	if err := tx.Commit(); err != nil {
		return SkillScriptExecution{}, err
	}

	executionCtx, stopExecution := context.WithCancel(ctx)
	defer stopExecution()
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-executionCtx.Done():
				return
			case <-ticker.C:
				if !s.scriptToolCallActive(executionCtx, call.AgentToolCallID) {
					stopExecution()
					return
				}
			}
		}
	}()
	result, executionErr := s.scriptSandbox.Execute(executionCtx, scriptsandbox.Request{
		ExecutionID: executionID,
		SkillRoot:   packagePath,
		ScriptPath:  selected.script.Path,
		Runtime:     selected.script.Runtime,
		Input:       arguments.Input,
		WorkRoot:    filepath.Join(executionRoot, "work"),
		OutputRoot:  outputPath,
		Limits:      policy.Limits,
	})
	stopExecution()
	<-monitorDone
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	return s.finishSkillScriptExecution(finishCtx, executionID, result, executionErr)
}

func decodeScriptToolArguments(raw json.RawMessage) (scriptToolArguments, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var arguments scriptToolArguments
	if err := decoder.Decode(&arguments); err != nil {
		return scriptToolArguments{}, domainError("SKILL_SCRIPT_ARGUMENTS_INVALID", "脚本工具参数无效。")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return scriptToolArguments{}, domainError("SKILL_SCRIPT_ARGUMENTS_INVALID", "脚本工具参数只能包含一个 JSON 对象。")
	}
	arguments.SkillName = strings.TrimSpace(arguments.SkillName)
	arguments.ScriptID = strings.TrimSpace(arguments.ScriptID)
	arguments.CapabilityID = strings.TrimSpace(arguments.CapabilityID)
	if arguments.SkillName == "" || arguments.ScriptID == "" {
		return scriptToolArguments{}, domainError("SKILL_SCRIPT_ARGUMENTS_INVALID", "skill_name 和 script_id 必填。")
	}
	arguments.InputJSON = strings.TrimSpace(arguments.InputJSON)
	if arguments.InputJSON == "" {
		arguments.InputJSON = `{}`
	}
	arguments.Input = json.RawMessage(arguments.InputJSON)
	if !json.Valid(arguments.Input) || len(arguments.Input) > maxAgentToolArgumentsBytes {
		return scriptToolArguments{}, domainError("SKILL_SCRIPT_INPUT_INVALID", "脚本输入必须是合法且不超过 256 KiB 的 JSON。")
	}
	return arguments, nil
}

func (s *Store) resolvePinnedSkillScriptTx(
	ctx context.Context,
	tx *sql.Tx,
	workspaceID string,
	skillName string,
	scriptID string,
	versionID string,
) (activeSkillScript, error) {
	var selected activeSkillScript
	var manifestJSON string
	err := tx.QueryRowContext(ctx, `
		SELECT si.skill_installation_id, sv.skill_version_id, si.skill_name,
			si.capability_id, sv.version, sv.execution_mode, sv.content_hash,
			sv.package_ref, sv.manifest_json
		FROM skill_installations si
		JOIN skill_versions sv ON sv.skill_installation_id = si.skill_installation_id
		WHERE si.workspace_id = ? AND si.skill_name = ?
			AND si.status = 'installed' AND si.enabled = 1 AND sv.status = 'installed'
			AND sv.skill_version_id = ?`,
		workspaceID, skillName, versionID,
	).Scan(
		&selected.installationID, &selected.versionID, &selected.skillName,
		&selected.capabilityID, &selected.version, &selected.executionMode,
		&selected.contentHash, &selected.packageRef, &manifestJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return activeSkillScript{}, domainError("SKILL_SCRIPT_SKILL_UNAVAILABLE", "请求的 Skill 未安装、未启用或没有活动版本。")
	}
	if err != nil {
		return activeSkillScript{}, err
	}
	var manifest capability.PublicSkill
	if err := json.Unmarshal([]byte(manifestJSON), &manifest); err != nil {
		return activeSkillScript{}, fmt.Errorf("decode installed Skill manifest: %w", err)
	}
	for _, script := range manifest.Scripts {
		if script.ID == scriptID {
			selected.script = script
			return selected, nil
		}
	}
	return activeSkillScript{}, domainError("SKILL_SCRIPT_NOT_DECLARED", "请求的脚本未在活动 Skill manifest 中声明。")
}

func (s *Store) scriptToolCallActive(ctx context.Context, callID string) bool {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false
	}
	defer tx.Rollback()
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT c.status = 'running' AND p.deleted_at IS NULL FROM agent_tool_calls c
		JOIN projects p ON p.project_id = c.project_id WHERE c.agent_tool_call_id = ?`, callID).Scan(&active); err != nil || !active {
		return false
	}
	if !scriptSnapshotStillEnabledTx(ctx, tx, callID) {
		return false
	}
	return s.validateAgentTaskToolCallTx(ctx, tx, callID) == nil
}

func (s *Store) finishSkillScriptExecution(
	ctx context.Context,
	executionID string,
	result scriptsandbox.Result,
	executionErr error,
) (SkillScriptExecution, error) {
	status := "completed"
	var errorCode, errorMessage any
	eventType := "skill.script_execution.completed"
	if executionErr != nil {
		status = "failed"
		eventType = "skill.script_execution.failed"
		errorCode = scriptsandbox.ErrorCode(executionErr)
		errorMessage = truncateUTF8(executionErr.Error(), 2048)
	}
	artifactsJSON, err := json.Marshal(result.Artifacts)
	if err != nil {
		return SkillScriptExecution{}, err
	}
	completedAt := result.CompletedAt
	if completedAt.IsZero() {
		completedAt = s.now()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SkillScriptExecution{}, err
	}
	defer tx.Rollback()
	var projectID string
	if err := tx.QueryRowContext(ctx, `
		SELECT project_id FROM skill_script_executions WHERE skill_script_execution_id = ?`, executionID,
	).Scan(&projectID); err != nil {
		return SkillScriptExecution{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE skill_script_executions
		SET status = ?, adapter = ?, engine = ?, image = ?, stdout_summary = ?,
			stderr_summary = ?, exit_code = ?, artifacts_json = ?, error_code = ?,
			error_message = ?, completed_at = ?, updated_at = ?
		WHERE skill_script_execution_id = ? AND status = 'running'`,
		status, result.Adapter, result.Engine, result.Image,
		truncateUTF8(result.StdoutSummary, maxAgentToolSummaryBytes),
		truncateUTF8(result.StderrSummary, maxAgentToolSummaryBytes),
		result.ExitCode, string(artifactsJSON), errorCode, errorMessage,
		formatTime(completedAt), formatTime(completedAt), executionID,
	); err != nil {
		return SkillScriptExecution{}, err
	}
	if _, err := s.appendEvent(
		ctx, tx, projectID, nil, nil, eventType, "skill_script_execution", executionID,
		map[string]any{
			"status": status, "exit_code": result.ExitCode,
			"artifact_count": len(result.Artifacts), "error_code": errorCode,
		},
	); err != nil {
		return SkillScriptExecution{}, err
	}
	execution, err := scanSkillScriptExecution(tx.QueryRowContext(
		ctx, skillScriptExecutionSelect+` WHERE skill_script_execution_id = ?`, executionID,
	))
	if err != nil {
		return SkillScriptExecution{}, err
	}
	if err := tx.Commit(); err != nil {
		return SkillScriptExecution{}, err
	}
	return execution, nil
}

func (s *Store) GetSkillScriptExecution(
	ctx context.Context,
	executionID string,
) (SkillScriptExecution, error) {
	execution, err := scanSkillScriptExecution(s.db.QueryRowContext(
		ctx, skillScriptExecutionSelect+` WHERE skill_script_execution_id = ? AND workspace_id = ?`,
		strings.TrimSpace(executionID), identity.WorkspaceIDFromContext(ctx),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return SkillScriptExecution{}, domainError("SKILL_SCRIPT_EXECUTION_NOT_FOUND", "脚本执行记录不存在。")
	}
	return execution, err
}

func (s *Store) ListSkillScriptExecutions(
	ctx context.Context,
	projectID string,
) ([]SkillScriptExecution, error) {
	if _, err := s.GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, skillScriptExecutionSelect+`
		WHERE project_id = ? AND workspace_id = ?
		ORDER BY started_at DESC, skill_script_execution_id DESC`,
		projectID, identity.WorkspaceIDFromContext(ctx),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]SkillScriptExecution, 0)
	for rows.Next() {
		item, err := scanSkillScriptExecution(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func getAgentToolApprovalForCallTx(
	ctx context.Context,
	tx *sql.Tx,
	callID string,
) (AgentToolApproval, error) {
	approval, err := scanAgentToolApproval(tx.QueryRowContext(
		ctx, agentToolApprovalSelect+` WHERE agent_tool_call_id = ?`, callID,
	))
	return approval, err
}

func sandboxImage(sandbox scriptsandbox.Sandbox, runtime string) string {
	if runtime == "python" {
		if provider, ok := sandbox.(interface{ PythonImage() string }); ok {
			return provider.PythonImage()
		}
	}
	return "configured:" + runtime
}

const skillScriptExecutionSelect = `
	SELECT skill_script_execution_id, workspace_id, project_id, conversation_id,
		agent_tool_call_id, COALESCE(skill_installation_id, ''), COALESCE(skill_version_id, ''), COALESCE(skill_snapshot_id, ''),
		script_id, script_path, runtime, adapter, engine, image, status,
		input_hash, limits_json, environment_names_json, output_storage_ref,
		stdout_summary, stderr_summary, exit_code, artifacts_json,
		error_code, error_message, requested_by, started_at, completed_at, updated_at
	FROM skill_script_executions`

func scanSkillScriptExecution(row rowScanner) (SkillScriptExecution, error) {
	var execution SkillScriptExecution
	var limitsJSON, environmentJSON, artifactsJSON string
	var exitCode sql.NullInt64
	var errorCode, errorMessage, completedAt sql.NullString
	var startedAt, updatedAt string
	if err := row.Scan(
		&execution.SkillScriptExecutionID, &execution.WorkspaceID, &execution.ProjectID,
		&execution.ConversationID, &execution.AgentToolCallID, &execution.SkillInstallationID,
		&execution.SkillVersionID, &execution.SkillSnapshotID, &execution.ScriptID, &execution.ScriptPath,
		&execution.Runtime, &execution.Adapter, &execution.Engine, &execution.Image,
		&execution.Status, &execution.InputHash, &limitsJSON, &environmentJSON,
		&execution.outputStorageRef, &execution.StdoutSummary, &execution.StderrSummary,
		&exitCode, &artifactsJSON, &errorCode, &errorMessage, &execution.RequestedBy,
		&startedAt, &completedAt, &updatedAt,
	); err != nil {
		return SkillScriptExecution{}, err
	}
	if err := json.Unmarshal([]byte(limitsJSON), &execution.Limits); err != nil {
		return SkillScriptExecution{}, err
	}
	if err := json.Unmarshal([]byte(environmentJSON), &execution.EnvironmentNames); err != nil {
		return SkillScriptExecution{}, err
	}
	if err := json.Unmarshal([]byte(artifactsJSON), &execution.Artifacts); err != nil {
		return SkillScriptExecution{}, err
	}
	if exitCode.Valid {
		value := int(exitCode.Int64)
		execution.ExitCode = &value
	}
	execution.ErrorCode = stringPointer(errorCode)
	execution.ErrorMessage = stringPointer(errorMessage)
	var err error
	execution.StartedAt, err = parseTime(startedAt)
	if err != nil {
		return SkillScriptExecution{}, err
	}
	execution.CompletedAt, err = optionalTime(completedAt)
	if err != nil {
		return SkillScriptExecution{}, err
	}
	execution.UpdatedAt, err = parseTime(updatedAt)
	return execution, err
}

func (s *Store) OpenSkillScriptArtifact(
	ctx context.Context,
	executionID string,
	artifactPath string,
) (io.ReadCloser, int64, error) {
	execution, err := s.GetSkillScriptExecution(ctx, executionID)
	if err != nil {
		return nil, 0, err
	}
	artifactPath = filepath.ToSlash(strings.TrimSpace(artifactPath))
	var expectedSize int64 = -1
	var expectedHash string
	for _, artifact := range execution.Artifacts {
		if artifact.Path == artifactPath {
			expectedSize = artifact.SizeBytes
			expectedHash = artifact.SHA256
			break
		}
	}
	if expectedSize < 0 {
		return nil, 0, domainError("SKILL_SCRIPT_ARTIFACT_NOT_FOUND", "脚本产物不存在。")
	}
	root, err := resolveDataPath(s.dataRoot, execution.outputStorageRef)
	if err != nil {
		return nil, 0, err
	}
	target, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(artifactPath)))
	if err != nil {
		return nil, 0, err
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, 0, domainError("SKILL_SCRIPT_OUTPUT_PATH_UNSAFE", "脚本产物路径越界。")
	}
	file, err := os.Open(target)
	if err != nil {
		return nil, 0, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != expectedSize {
		file.Close()
		return nil, 0, domainError("SKILL_SCRIPT_ARTIFACT_INTEGRITY_FAILED", "脚本产物完整性校验失败。")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != expectedHash {
		file.Close()
		return nil, 0, domainError("SKILL_SCRIPT_ARTIFACT_INTEGRITY_FAILED", "脚本产物完整性校验失败。")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		return nil, 0, err
	}
	return file, expectedSize, nil
}
