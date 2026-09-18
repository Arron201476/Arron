package runtime

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"

	"content-agent/backend/internal/identity"
)

type AgentActivityIdentity struct {
	ProjectID               string
	AgentTurnID             string
	AgentTaskAttemptID      string
	ExecutionAttemptID      string
	MemoryGenerationID      string
	MemoryGenerationAttempt int
	AttemptToken            string
	AllowTerminal           bool
}

type agentActivityContextKey struct{}

func WithAgentActivity(ctx context.Context, activity AgentActivityIdentity) context.Context {
	return context.WithValue(ctx, agentActivityContextKey{}, activity)
}

func AgentActivityFromContext(ctx context.Context) (AgentActivityIdentity, bool) {
	activity, ok := ctx.Value(agentActivityContextKey{}).(AgentActivityIdentity)
	return activity, ok
}

func migrateExecutionOwners(db *sql.DB) error {
	for _, table := range []string{"skill_invocations", "runs"} {
		present, err := tableHasColumn(db, table, "user_id")
		if err != nil {
			return err
		}
		if !present {
			if _, err := db.Exec("ALTER TABLE " + table + " ADD COLUMN user_id TEXT NOT NULL DEFAULT ''"); err != nil {
				return err
			}
		}
		present, err = tableHasColumn(db, table, "skill_version_id")
		if err != nil {
			return err
		}
		if !present {
			if _, err := db.Exec("ALTER TABLE " + table + " ADD COLUMN skill_version_id TEXT REFERENCES skill_versions(skill_version_id)"); err != nil {
				return err
			}
		}
	}
	// Pre-scope records have no personal packages. Prefer the exact creator turn;
	// retain project ownership only as a legacy fallback when no turn was stored.
	_, err := db.Exec(`UPDATE skill_invocations SET user_id = COALESCE(
		(SELECT user_id FROM agent_turns WHERE user_message_id = skill_invocations.user_message_id AND project_id = skill_invocations.project_id LIMIT 1),
		(SELECT owner_user_id FROM projects WHERE project_id = skill_invocations.project_id), '')
		WHERE user_id = '';
		UPDATE runs SET user_id = COALESCE(
		(SELECT user_id FROM skill_invocations WHERE run_id = runs.run_id AND project_id = runs.project_id LIMIT 1),
		(SELECT owner_user_id FROM projects WHERE project_id = runs.project_id), '')
		WHERE user_id = '';`)
	if err != nil {
		return err
	}
	var previousVersion int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&previousVersion); err != nil {
		return err
	}
	if previousVersion >= 35 {
		return nil
	}
	// Before scoped installations existed, workspace + capability + version was
	// unambiguous. Persist that package identity before adding scoped overrides.
	for _, table := range []string{"skill_invocations", "runs"} {
		if _, err := db.Exec(`UPDATE ` + table + ` SET skill_version_id = (
			SELECT version.skill_version_id FROM skill_versions version
			JOIN skill_installations installation ON installation.skill_installation_id = version.skill_installation_id
			JOIN projects project ON project.workspace_id = installation.workspace_id
			WHERE project.project_id = ` + table + `.project_id
			AND installation.scope = 'workspace' AND installation.scope_ref = project.workspace_id
			AND installation.capability_id = ` + table + `.capability_id AND version.version = ` + table + `.capability_version
		) WHERE skill_version_id IS NULL`); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ResolveAgentActivityPrincipal(ctx context.Context, activity AgentActivityIdentity) (identity.Principal, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return identity.Principal{}, err
	}
	defer tx.Rollback()
	return s.resolveAgentActivityPrincipalTx(ctx, tx, activity)
}

func (s *Store) resolveAgentActivityPrincipalTx(ctx context.Context, tx *sql.Tx, activity AgentActivityIdentity) (identity.Principal, error) {
	count := 0
	for _, id := range []string{activity.AgentTurnID, activity.AgentTaskAttemptID, activity.ExecutionAttemptID, activity.MemoryGenerationID} {
		if id != "" {
			count++
		}
	}
	if activity.ProjectID == "" || count != 1 || (activity.AgentTurnID != "" && activity.AttemptToken != "") ||
		len(activity.ProjectID) > 256 || len(activity.AgentTurnID) > 256 || len(activity.AgentTaskAttemptID) > 256 || len(activity.ExecutionAttemptID) > 256 || len(activity.MemoryGenerationID) > 256 || len(activity.AttemptToken) > 512 ||
		(activity.MemoryGenerationID == "" && activity.MemoryGenerationAttempt != 0) || (activity.MemoryGenerationID != "" && activity.MemoryGenerationAttempt < 1) {
		return identity.Principal{}, domainError("AGENT_ACTIVITY_INVALID", "Agent 执行身份引用无效。")
	}
	var err error
	var userID, workspaceID, projectID string
	if activity.MemoryGenerationID != "" {
		job, loadErr := memoryGenerationQuery(ctx, tx, activity.MemoryGenerationID)
		if loadErr != nil {
			return identity.Principal{}, loadErr
		}
		lease, leaseErr := parseTime(job.LeaseUntil)
		if activity.AllowTerminal || leaseErr != nil || !lease.After(s.now()) || job.Status != "running" || !job.Started || job.Attempt != activity.MemoryGenerationAttempt || subtle.ConstantTimeCompare([]byte(job.TokenHash), []byte(sha256Hex([]byte(activity.AttemptToken)))) != 1 {
			return identity.Principal{}, domainError("AGENT_ACTIVITY_STALE", "Memory generation execution lease is no longer valid.")
		}
		if _, sourceErr := s.memoryGenerationSourceTx(ctx, tx, job); sourceErr != nil {
			return identity.Principal{}, sourceErr
		}
		userID, workspaceID, projectID = job.UserID, job.WorkspaceID, job.ProjectID
	} else if activity.AgentTurnID != "" {
		var status string
		err = tx.QueryRowContext(ctx, `SELECT turn.user_id, project.workspace_id, turn.project_id, turn.status
			FROM agent_turns turn JOIN projects project ON project.project_id = turn.project_id
			WHERE turn.agent_turn_id = ? AND project.deleted_at IS NULL`, activity.AgentTurnID).Scan(&userID, &workspaceID, &projectID, &status)
		if err == nil && status != "running" && status != "pausing" && !activity.AllowTerminal {
			return identity.Principal{}, domainError("AGENT_ACTIVITY_STALE", "Agent 对话当前不可执行。")
		}
	} else if activity.ExecutionAttemptID != "" {
		state, loadErr := loadExecutionToolStateTx(ctx, tx, activity.ExecutionAttemptID)
		if loadErr != nil {
			return identity.Principal{}, loadErr
		}
		if err := validateExecutionToolToken(state, activity.AttemptToken); err != nil {
			return identity.Principal{}, err
		}
		if state.ProjectDeleted {
			return identity.Principal{}, domainError("AGENT_ACTIVITY_STALE", "状态化任务所属作品已删除。")
		}
		if !activity.AllowTerminal {
			if err := validateExecutionToolState(state, s.now()); err != nil {
				return identity.Principal{}, err
			}
		}
		userID, workspaceID, projectID = state.UserID, state.WorkspaceID, state.ProjectID
	} else {
		state, loadErr := loadAgentTaskAttemptStateTx(ctx, tx, activity.AgentTaskAttemptID)
		if loadErr != nil {
			return identity.Principal{}, loadErr
		}
		if activity.AllowTerminal {
			if state.ProjectDeleted || subtle.ConstantTimeCompare([]byte(state.TokenHash), []byte(sha256Hex([]byte(activity.AttemptToken)))) != 1 {
				return identity.Principal{}, domainError("AGENT_ACTIVITY_STALE", "Agent 后台执行身份已失效。")
			}
		} else {
			if err := validateActiveAgentTaskAttempt(state, activity.AttemptToken, s.now()); err != nil {
				return identity.Principal{}, err
			}
		}
		projectID = state.ProjectID
		err = tx.QueryRowContext(ctx, `SELECT invocation.user_id, project.workspace_id
			FROM skill_invocations invocation JOIN projects project ON project.project_id = invocation.project_id
			WHERE invocation.skill_invocation_id = ?`, state.SkillInvocationID).Scan(&userID, &workspaceID)
	}
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (userID == "" || projectID != activity.ProjectID)) {
		return identity.Principal{}, domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "Agent 执行身份与目标作品不匹配。")
	}
	if err != nil {
		return identity.Principal{}, err
	}
	user, err := resolvePrincipalQuery(ctx, tx, identity.Principal{Kind: identity.KindUser, UserID: userID, WorkspaceID: workspaceID, Role: identity.RoleViewer, AuthMethod: "agent_activity"})
	if err != nil {
		return identity.Principal{}, err
	}
	// Terminal cleanup must remain possible after memory revocation.
	if !activity.AllowTerminal {
		if err := validateAgentMemorySnapshotQuery(ctx, tx, activity, user); err != nil {
			return identity.Principal{}, err
		}
	}
	return user, nil
}

func (s *Store) ValidateAgentActivityToolCall(ctx context.Context, activity AgentActivityIdentity, callID string) error {
	return validateAgentActivityToolCallQuery(ctx, s.db, activity, callID)
}

func validateAgentActivityToolCallQuery(ctx context.Context, query rowQueryer, activity AgentActivityIdentity, callID string) error {
	if activity.MemoryGenerationID != "" {
		var valid bool
		err := query.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_memory_tool_calls b
			JOIN agent_memory_generations g ON g.generation_id=b.generation_id
			JOIN agent_tool_calls c ON c.agent_tool_call_id=b.agent_tool_call_id
			WHERE b.agent_tool_call_id=? AND g.generation_id=? AND g.project_id=? AND b.phase=g.phase
			AND c.project_id=g.project_id AND c.workspace_id=g.workspace_id AND c.agent_turn_id IS NULL AND c.skill_invocation_id IS NULL
			AND NOT EXISTS(SELECT 1 FROM agent_task_tool_calls t WHERE t.agent_tool_call_id=c.agent_tool_call_id)
			AND NOT EXISTS(SELECT 1 FROM execution_tool_calls e WHERE e.agent_tool_call_id=c.agent_tool_call_id))`, callID, activity.MemoryGenerationID, activity.ProjectID).Scan(&valid)
		if err != nil {
			return err
		}
		if !valid {
			return domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "Memory generation cannot use a business execution tool call.")
		}
		return nil
	}
	var memoryBound bool
	if err := query.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_memory_tool_calls WHERE agent_tool_call_id=?)`, callID).Scan(&memoryBound); err != nil {
		return err
	}
	if memoryBound {
		return domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "Memory tool call cannot be used by a business execution.")
	}
	var projectID, turnID, attemptID, executionID string
	err := query.QueryRowContext(ctx, `SELECT call.project_id, COALESCE(call.agent_turn_id, ''), COALESCE(task.agent_task_attempt_id, ''), COALESCE(execution.execution_attempt_id, '')
		FROM agent_tool_calls call LEFT JOIN agent_task_tool_calls task ON task.agent_tool_call_id = call.agent_tool_call_id
		LEFT JOIN execution_tool_calls execution ON execution.agent_tool_call_id = call.agent_tool_call_id
		WHERE call.agent_tool_call_id = ?`, callID).Scan(&projectID, &turnID, &attemptID, &executionID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (projectID != activity.ProjectID || turnID != activity.AgentTurnID || attemptID != activity.AgentTaskAttemptID || executionID != activity.ExecutionAttemptID)) {
		return domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "工具调用不属于当前 Agent 执行。")
	}
	return err
}
