package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"content-agent/backend/internal/identity"
)

const agentToolReconciliationSchema = `
CREATE TABLE IF NOT EXISTS agent_tool_reconciliations (
	agent_tool_call_id TEXT PRIMARY KEY REFERENCES agent_tool_calls(agent_tool_call_id) ON DELETE CASCADE,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	subject_snapshot_hash TEXT NOT NULL,
	resolution_json TEXT NOT NULL,
	content_hash TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_agent_tool_reconciliations_project ON agent_tool_reconciliations(project_id);
CREATE TABLE IF NOT EXISTS agent_tool_reconciliation_inputs (
	agent_tool_call_id TEXT PRIMARY KEY REFERENCES agent_tool_reconciliations(agent_tool_call_id) ON DELETE CASCADE,
	input_id TEXT NOT NULL UNIQUE,
	execution_mode TEXT NOT NULL,
	execution_id TEXT NOT NULL,
	input_owner_id TEXT NOT NULL,
	content_hash TEXT NOT NULL,
	content_size_bytes INTEGER NOT NULL,
	created_at TEXT NOT NULL
);
`

type ToolOutcomeResolution struct {
	RequestID   string `json:"request_id"`
	Outcome     string `json:"outcome"`
	Evidence    string `json:"evidence"`
	ActorUserID string `json:"actor_user_id"`
	CreatedAt   string `json:"created_at"`
}

type AgentToolOutcomeReview struct {
	AgentToolCallID     string                 `json:"agent_tool_call_id"`
	ProjectID           string                 `json:"project_id"`
	SDKToolCallID       string                 `json:"sdk_tool_call_id"`
	ToolID              string                 `json:"tool_id"`
	ArgumentsHash       string                 `json:"arguments_hash"`
	ConfigurationHash   string                 `json:"configuration_hash"`
	UserID              string                 `json:"user_id"`
	ExecutionMode       string                 `json:"execution_mode"`
	ExecutionID         string                 `json:"execution_id"`
	ExecutionStatus     string                 `json:"execution_status"`
	SubjectSnapshotHash string                 `json:"subject_snapshot_hash"`
	CanResolve          bool                   `json:"can_resolve"`
	Resolution          *ToolOutcomeResolution `json:"resolution,omitempty"`
}

type ResolveAgentToolOutcomeCommand struct {
	AgentToolCallID     string `json:"-"`
	SubjectSnapshotHash string `json:"subject_snapshot_hash"`
	RequestID           string `json:"request_id"`
	Outcome             string `json:"outcome"`
	Evidence            string `json:"evidence"`
}

func agentToolOutcomeReviewTx(ctx context.Context, tx *sql.Tx, callID string) (AgentToolOutcomeReview, error) {
	view := AgentToolOutcomeReview{AgentToolCallID: callID}
	call, err := getAgentToolCallTx(ctx, tx, callID)
	if err != nil {
		return view, err
	}
	if _, err := projectFilesWorkspace(ctx, tx, call.ProjectID, false); err != nil {
		return view, err
	}
	if call.ToolKind != "mcp" || call.AccessMode == "read" || call.StartedAt == nil || (call.Status != "failed" && call.Status != "cancelled") {
		return view, domainError("AGENT_TOOL_OUTCOME_REVIEW_NOT_FOUND", "该调用没有可核对的未确认外部写入。")
	}
	var turn, background, execution string
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(c.agent_turn_id,''),COALESCE(b.agent_task_attempt_id,''),COALESCE(e.execution_attempt_id,''),
		COALESCE(t.user_id,i.user_id,r.user_id,''),COALESCE(t.status,ba.status,ea.status,''),COALESCE(config.configuration_hash,'')
		FROM agent_tool_calls c LEFT JOIN agent_turns t ON t.agent_turn_id=c.agent_turn_id
		LEFT JOIN agent_task_tool_calls b ON b.agent_tool_call_id=c.agent_tool_call_id
		LEFT JOIN agent_task_attempts ba ON ba.agent_task_attempt_id=b.agent_task_attempt_id
		LEFT JOIN agent_tasks task ON task.agent_task_id=ba.agent_task_id
		LEFT JOIN skill_invocations i ON i.skill_invocation_id=task.skill_invocation_id
		LEFT JOIN execution_tool_calls e ON e.agent_tool_call_id=c.agent_tool_call_id
		LEFT JOIN execution_attempts ea ON ea.attempt_id=e.execution_attempt_id
		LEFT JOIN runs r ON r.run_id=ea.run_id
		LEFT JOIN agent_tool_config_snapshots config ON config.agent_tool_call_id=c.agent_tool_call_id
		WHERE c.agent_tool_call_id=?`, callID).Scan(&turn, &background, &execution, &view.UserID, &view.ExecutionStatus, &view.ConfigurationHash)
	if err != nil {
		return view, err
	}
	count := 0
	for _, binding := range []struct{ mode, id string }{{"conversation", turn}, {"background_task", background}, {"stateful_workflow", execution}} {
		if binding.id != "" {
			count++
			view.ExecutionMode, view.ExecutionID = binding.mode, binding.id
		}
	}
	if count != 1 || view.UserID == "" || view.ExecutionStatus == "" {
		return view, domainError("AGENT_TOOL_OUTCOME_REVIEW_NOT_FOUND", "原调用缺少确切执行身份，不能核对。")
	}
	view.ProjectID, view.SDKToolCallID, view.ToolID, view.ArgumentsHash = call.ProjectID, call.SDKToolCallID, call.ToolID, call.ArgumentsHash
	// Snapshot immutable call facts, not the execution's changing pause status.
	subject, err := json.Marshal([]any{call.AgentToolCallID, call.ProjectID, view.UserID, view.ExecutionMode, view.ExecutionID,
		call.SDKToolCallID, call.ToolID, call.ArgumentsHash, view.ConfigurationHash, formatTime(*call.StartedAt), call.Status, call.ErrorCode, call.ResultHash})
	if err != nil {
		return view, err
	}
	view.SubjectSnapshotHash = sha256Hex(subject)
	var raw, hash, snapshot string
	err = tx.QueryRowContext(ctx, `SELECT resolution_json,content_hash,subject_snapshot_hash FROM agent_tool_reconciliations WHERE agent_tool_call_id=? AND project_id=?`, callID, call.ProjectID).Scan(&raw, &hash, &snapshot)
	if err == nil {
		var resolution ToolOutcomeResolution
		if snapshot != view.SubjectSnapshotHash || sha256Hex([]byte(raw)) != hash || json.Unmarshal([]byte(raw), &resolution) != nil ||
			resolution.ActorUserID != view.UserID || (resolution.Outcome != "applied" && resolution.Outcome != "not_applied") {
			return view, domainError("AGENT_TOOL_OUTCOME_REVIEW_CORRUPT", "外部操作核对记录校验失败。")
		}
		view.Resolution = &resolution
	} else if !errors.Is(err, sql.ErrNoRows) {
		return view, err
	}
	if principal, ok := identity.FromContext(ctx); ok && principal.Kind == identity.KindUser && principal.UserID == view.UserID {
		_, active := AgentActivityFromContext(ctx)
		view.CanResolve = !active && principal.Allows(identity.RoleEditor) && view.Resolution == nil &&
			(view.ExecutionStatus == "paused" || view.ExecutionStatus == "failed" || view.ExecutionStatus == "cancelled")
	}
	return view, nil
}

func (s *Store) GetAgentToolOutcomeReview(ctx context.Context, callID string) (AgentToolOutcomeReview, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return AgentToolOutcomeReview{}, err
	}
	defer tx.Rollback()
	return agentToolOutcomeReviewTx(ctx, tx, callID)
}

func (s *Store) ResolveAgentToolOutcome(ctx context.Context, command ResolveAgentToolOutcomeCommand) (AgentToolOutcomeReview, error) {
	if command.AgentToolCallID == "" || len(command.AgentToolCallID) > 256 || command.RequestID == "" || len(command.RequestID) > 256 ||
		strings.ContainsRune(command.RequestID, 0) || len(command.SubjectSnapshotHash) != 64 ||
		(command.Outcome != "applied" && command.Outcome != "not_applied") || strings.TrimSpace(command.Evidence) == "" ||
		len(command.Evidence) > 4096 || !utf8.ValidString(command.Evidence) || strings.ContainsRune(command.Evidence, 0) {
		return AgentToolOutcomeReview{}, domainError("REQUEST_VALIDATION_FAILED", "请选择核对结果并填写实际检查依据。")
	}
	principal, ok := identity.FromContext(ctx)
	_, active := AgentActivityFromContext(ctx)
	if !ok || principal.Kind != identity.KindUser || active {
		return AgentToolOutcomeReview{}, domainError("ROLE_FORBIDDEN", "外部操作结果只能由发起用户本人核对，Agent 不能代为确认。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentToolOutcomeReview{}, err
	}
	defer tx.Rollback()
	principal, err = resolvePrincipalQuery(ctx, tx, principal)
	if err != nil {
		return AgentToolOutcomeReview{}, err
	}
	ctx = identity.WithPrincipal(ctx, principal)
	view, err := agentToolOutcomeReviewTx(ctx, tx, command.AgentToolCallID)
	if err != nil {
		return view, err
	}
	if principal.UserID != view.UserID || !principal.Allows(identity.RoleEditor) {
		return view, domainError("ROLE_FORBIDDEN", "只有原执行的发起用户可以核对外部操作结果。")
	}
	if command.SubjectSnapshotHash != view.SubjectSnapshotHash {
		return view, domainError("AGENT_TOOL_OUTCOME_REVIEW_CONFLICT", "原操作快照已变化，请重新读取后核对。")
	}
	if view.Resolution != nil {
		if view.Resolution.RequestID == command.RequestID && view.Resolution.Outcome == command.Outcome && view.Resolution.Evidence == command.Evidence {
			return view, nil
		}
		return view, domainError("AGENT_TOOL_OUTCOME_REVIEW_CONFLICT", "该操作已核对，不能覆盖原核对记录。")
	}
	if !view.CanResolve {
		return view, domainError("AGENT_TOOL_OUTCOME_REVIEW_CONFLICT", "请等待执行暂停或结束后再核对原操作。")
	}
	now := s.now()
	resolution := ToolOutcomeResolution{RequestID: command.RequestID, Outcome: command.Outcome, Evidence: command.Evidence, ActorUserID: principal.UserID, CreatedAt: formatTime(now)}
	raw, err := json.Marshal(resolution)
	if err != nil {
		return view, err
	}
	if err := s.enforceWorkspaceQuotaTx(ctx, tx, principal.WorkspaceID, "storage_bytes", int64(len(raw))); err != nil {
		return view, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_tool_reconciliations(agent_tool_call_id,project_id,subject_snapshot_hash,resolution_json,content_hash,created_at) VALUES(?,?,?,?,?,?)`,
		view.AgentToolCallID, view.ProjectID, view.SubjectSnapshotHash, string(raw), sha256Hex(raw), formatTime(now)); err != nil {
		return view, err
	}
	if _, err := s.appendEvent(ctx, tx, view.ProjectID, nil, nil, "agent.tool_outcome.reconciled", "agent_tool_call", view.AgentToolCallID,
		map[string]any{"outcome": command.Outcome, "subject_snapshot_hash": view.SubjectSnapshotHash, "actor_user_id": principal.UserID}); err != nil {
		return view, err
	}
	if err := tx.Commit(); err != nil {
		return view, err
	}
	view.CanResolve, view.Resolution = false, &resolution
	return view, nil
}
