package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"content-agent/backend/internal/agenttool"
)

const agentSubtaskSchema = `
CREATE TABLE IF NOT EXISTS agent_subtask_reads (
	agent_tool_call_id TEXT PRIMARY KEY REFERENCES agent_tool_calls(agent_tool_call_id) ON DELETE CASCADE,
	parent_tool_call_id TEXT NOT NULL REFERENCES agent_tool_calls(agent_tool_call_id) ON DELETE CASCADE,
	artifact_id TEXT,
	artifact_version_id TEXT
);
CREATE INDEX IF NOT EXISTS idx_agent_subtask_reads_parent ON agent_subtask_reads(parent_tool_call_id);
CREATE TABLE IF NOT EXISTS agent_subtask_results (
	agent_tool_call_id TEXT PRIMARY KEY REFERENCES agent_tool_calls(agent_tool_call_id) ON DELETE CASCADE,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	result_json TEXT NOT NULL,
	content_hash TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_agent_subtask_results_project ON agent_subtask_results(project_id);
`

type SubtaskArtifactReference struct {
	ArtifactID        string `json:"artifact_id"`
	ArtifactVersionID string `json:"artifact_version_id"`
}

type AgentSubtaskResult struct {
	SchemaVersion      string                     `json:"schema_version"`
	Text               string                     `json:"text"`
	ReadCallIDs        []string                   `json:"read_call_ids"`
	InspectedArtifacts []SubtaskArtifactReference `json:"inspected_artifacts"`
}

type AgentSubtaskView struct {
	AgentSubtaskResult
	AgentToolCallID string `json:"agent_tool_call_id"`
	ProjectID       string `json:"project_id"`
}

func subtaskReadParentTx(ctx context.Context, tx *sql.Tx, command BeginAgentToolCallCommand, descriptor agenttool.Descriptor) (string, error) {
	if !strings.HasPrefix(command.SDKToolCallID, "subtask:") {
		return "", nil
	}
	invalid := domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "子任务读取必须绑定当前执行中的父调用和可信只读工具。")
	parts := strings.Split(command.SDKToolCallID, ":")
	if len(parts) != 3 || len(parts[1]) != 24 || len(parts[2]) != 24 || descriptor.Access != agenttool.AccessRead || descriptor.Approval != agenttool.ApprovalNever {
		return "", invalid
	}
	switch command.ToolID {
	case "runtime:inspect_project", "runtime:inspect_project_goal", "runtime:list_project_assets", "runtime:inspect_text_asset",
		"runtime:search_artifacts", "runtime:inspect_current_artifact", "runtime:inspect_artifact_version",
		"runtime:inspect_recent_conversation", "runtime:search_conversation_history", "runtime:list_workspace_files",
		"runtime:read_workspace_file", "runtime:get_saved_instructions":
	default:
		return "", invalid
	}
	query := `SELECT c.agent_tool_call_id,c.sdk_tool_call_id FROM agent_tool_calls c
		WHERE c.project_id=? AND c.conversation_id=? AND c.tool_id=? AND c.status='running'
		AND COALESCE(c.agent_turn_id,'')=?
		AND COALESCE((SELECT agent_task_attempt_id FROM agent_task_tool_calls WHERE agent_tool_call_id=c.agent_tool_call_id),'')=?
		AND COALESCE((SELECT execution_attempt_id FROM execution_tool_calls WHERE agent_tool_call_id=c.agent_tool_call_id),'')=? LIMIT 8`
	rows, err := tx.QueryContext(ctx, query, command.ProjectID, command.ConversationID, delegateSubtaskTool,
		command.AgentTurnID, command.AgentTaskAttemptID, command.ExecutionAttemptID)
	if err != nil {
		return "", err
	}
	parentID := ""
	for rows.Next() {
		var id, sdkID string
		if err := rows.Scan(&id, &sdkID); err != nil {
			rows.Close()
			return "", err
		}
		if sha256Hex([]byte(sdkID))[:24] == parts[1] {
			parentID = id
		}
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if parentID == "" {
		return "", invalid
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_subtask_reads WHERE parent_tool_call_id=?`, parentID).Scan(&count); err != nil {
		return "", err
	}
	if count >= 64 {
		return "", domainError("AGENT_SUBTASK_LIMIT", "单个子任务最多读取 64 次，请汇总已有信息。")
	}
	return parentID, nil
}

func subtaskResultObject(raw json.RawMessage) json.RawMessage {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return json.RawMessage(text)
	}
	return raw
}

func (s *Store) completeSubtaskTx(ctx context.Context, tx *sql.Tx, call AgentToolCall, raw json.RawMessage) error {
	if call.ParentToolCallID != "" {
		var parentStatus string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM agent_tool_calls WHERE agent_tool_call_id=?`, call.ParentToolCallID).Scan(&parentStatus); err != nil {
			return err
		}
		if parentStatus != "running" {
			return domainError("AGENT_TOOL_CALL_STATE_CONFLICT", "父子任务已经结束，不能追加读取结果。")
		}
		if call.ToolID == "runtime:inspect_current_artifact" || call.ToolID == "runtime:inspect_artifact_version" {
			var payload map[string]json.RawMessage
			var ref SubtaskArtifactReference
			if json.Unmarshal(subtaskResultObject(raw), &payload) != nil {
				return domainError("AGENT_SUBTASK_RESULT_INVALID", "子任务读取结果缺少版本信息。")
			}
			version := payload["data"]
			if call.ToolID == "runtime:inspect_current_artifact" {
				version = payload["version"]
			}
			if json.Unmarshal(version, &ref) != nil || ref.ArtifactID == "" || ref.ArtifactVersionID == "" {
				return domainError("AGENT_SUBTASK_RESULT_INVALID", "子任务读取结果缺少准确版本。")
			}
			var args struct {
				ArtifactID        string `json:"artifact_id"`
				ArtifactVersionID string `json:"artifact_version_id"`
			}
			if err := json.Unmarshal(call.ArgumentsSummary, &args); err != nil {
				return err
			}
			if (call.ToolID == "runtime:inspect_current_artifact" && args.ArtifactID != ref.ArtifactID) ||
				(call.ToolID == "runtime:inspect_artifact_version" && args.ArtifactVersionID != ref.ArtifactVersionID) {
				return domainError("AGENT_SUBTASK_RESULT_INVALID", "子任务读取结果与请求版本不一致。")
			}
			var valid bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artifact_versions v JOIN artifacts a ON a.artifact_id=v.artifact_id WHERE v.artifact_version_id=? AND v.artifact_id=? AND a.project_id=?)`, ref.ArtifactVersionID, ref.ArtifactID, call.ProjectID).Scan(&valid); err != nil {
				return err
			}
			if !valid {
				return domainError("AGENT_SUBTASK_RESULT_INVALID", "子任务不能引用其他作品的版本。")
			}
			_, err := tx.ExecContext(ctx, `UPDATE agent_subtask_reads SET artifact_id=?,artifact_version_id=? WHERE agent_tool_call_id=?`, ref.ArtifactID, ref.ArtifactVersionID, call.AgentToolCallID)
			return err
		}
		return nil
	}
	if call.ToolID != delegateSubtaskTool {
		return nil
	}
	if _, err := projectFilesWorkspace(ctx, tx, call.ProjectID, false); err != nil {
		return err
	}
	invalid := domainError("AGENT_SUBTASK_RESULT_INVALID", "子任务结果必须包含有效结论和本子任务实际读取的引用。")
	var result AgentSubtaskResult
	decoder := json.NewDecoder(bytes.NewReader(subtaskResultObject(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || !json.Valid(subtaskResultObject(raw)) || result.SchemaVersion != "agent_subtask.v1" ||
		strings.TrimSpace(result.Text) == "" || strings.ContainsRune(result.Text, 0) || utf8.RuneCountInString(result.Text) > 32000 ||
		result.ReadCallIDs == nil || result.InspectedArtifacts == nil || len(result.ReadCallIDs) > 64 || len(result.InspectedArtifacts) > 64 {
		return invalid
	}
	seen := make(map[string]bool)
	versions := make(map[SubtaskArtifactReference]bool)
	for _, id := range result.ReadCallIDs {
		if seen[id] || len(id) > 256 {
			return invalid
		}
		seen[id] = true
		var status string
		var artifactID, versionID sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT c.status,r.artifact_id,r.artifact_version_id FROM agent_subtask_reads r JOIN agent_tool_calls c ON c.agent_tool_call_id=r.agent_tool_call_id WHERE r.parent_tool_call_id=? AND r.agent_tool_call_id=?`, call.AgentToolCallID, id).Scan(&status, &artifactID, &versionID)
		if errors.Is(err, sql.ErrNoRows) {
			return invalid
		}
		if err != nil {
			return err
		}
		if status != "completed" && status != "failed" && status != "cancelled" {
			return invalid
		}
		if status == "completed" && artifactID.Valid && versionID.Valid {
			versions[SubtaskArtifactReference{artifactID.String, versionID.String}] = true
		}
	}
	for _, ref := range result.InspectedArtifacts {
		if !versions[ref] {
			return invalid
		}
		delete(versions, ref)
	}
	var readCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_subtask_reads WHERE parent_tool_call_id=?`, call.AgentToolCallID).Scan(&readCount); err != nil {
		return err
	}
	if readCount != len(seen) {
		return invalid
	}
	content, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if err := s.enforceWorkspaceQuotaTx(ctx, tx, call.WorkspaceID, "storage_bytes", int64(len(content))); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_subtask_results(agent_tool_call_id,project_id,result_json,content_hash,created_at) VALUES(?,?,?,?,?)`, call.AgentToolCallID, call.ProjectID, string(content), sha256Hex(content), formatTime(s.now()))
	return err
}

func (s *Store) GetAgentSubtaskResult(ctx context.Context, callID string) (AgentSubtaskView, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return AgentSubtaskView{}, err
	}
	defer tx.Rollback()
	call, err := getAgentToolCallTx(ctx, tx, callID)
	if err != nil {
		return AgentSubtaskView{}, err
	}
	if _, err := projectFilesWorkspace(ctx, tx, call.ProjectID, false); err != nil {
		return AgentSubtaskView{}, err
	}
	if call.ToolID != delegateSubtaskTool || call.Status != "completed" {
		return AgentSubtaskView{}, domainError("AGENT_SUBTASK_RESULT_NOT_FOUND", "子任务尚无已保存结果。")
	}
	var content, hash string
	err = tx.QueryRowContext(ctx, `SELECT result_json,content_hash FROM agent_subtask_results WHERE agent_tool_call_id=? AND project_id=?`, callID, call.ProjectID).Scan(&content, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentSubtaskView{}, domainError("AGENT_SUBTASK_RESULT_NOT_FOUND", "子任务尚无已保存结果。")
	}
	if err != nil {
		return AgentSubtaskView{}, err
	}
	var result AgentSubtaskResult
	if sha256Hex([]byte(content)) != hash || json.Unmarshal([]byte(content), &result) != nil {
		return AgentSubtaskView{}, domainError("AGENT_SUBTASK_RESULT_INVALID", "保存的子任务结果校验失败。")
	}
	return AgentSubtaskView{AgentSubtaskResult: result, AgentToolCallID: call.AgentToolCallID, ProjectID: call.ProjectID}, nil
}
