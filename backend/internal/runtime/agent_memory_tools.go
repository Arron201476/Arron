package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/identity"
)

const prepareAgentMemoryPublicationTool = "runtime:prepare_agent_memory_publication"

func memoryGenerationToolPolicy(phase string, descriptor agenttool.Descriptor) bool {
	if (phase != "extraction" && phase != "consolidation") || descriptor.Kind != agenttool.KindRuntimeFunction || descriptor.ServerID != "" || descriptor.Name != strings.TrimPrefix(descriptor.ID, "runtime:") {
		return false
	}
	switch descriptor.ID {
	case nativeWorkspaceExecTool, nativeWorkspacePatchTool, nativeWorkspaceStdinTool:
		return descriptor.Approval == agenttool.ApprovalAlways
	case prepareAgentMemoryPublicationTool:
		return phase == "consolidation" && descriptor.Approval == agenttool.ApprovalNever && descriptor.Access == agenttool.AccessRead
	case publishAgentMemoryTool:
		return phase == "consolidation" && descriptor.Approval == agenttool.ApprovalAlways && descriptor.Access == agenttool.AccessWrite
	}
	return false
}

// Private generation tools do not resolve MCP credentials or hosted services.
func (s *Store) memoryGenerationToolRegistryTx(ctx context.Context, tx *sql.Tx) (*agenttool.Registry, error) {
	activity, active := AgentActivityFromContext(ctx)
	principal, authenticated := identity.FromContext(ctx)
	if !active || activity.MemoryGenerationID == "" || !authenticated || principal.Kind != identity.KindService {
		return nil, domainError("AGENT_ACTIVITY_REQUIRED", "Memory tool catalog requires its admitted execution.")
	}
	_, err := s.resolveAgentActivityPrincipalTx(ctx, tx, activity)
	if err != nil {
		return nil, err
	}
	if s.agentTools == nil {
		return nil, domainError("AGENT_TOOL_REGISTRY_UNAVAILABLE", "Agent tool registry is unavailable.")
	}
	// Native descriptors are operator-defined, not workspace MCP/hosted switches.
	return s.agentTools, nil
}

func (s *Store) memoryGenerationToolCatalog(ctx context.Context) (agenttool.Catalog, error) {
	result := agenttool.Catalog{SchemaVersion: agenttool.SchemaVersion, Tools: []agenttool.Descriptor{}}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	registry, err := s.memoryGenerationToolRegistryTx(ctx, tx)
	if err != nil {
		return result, err
	}
	activity, _ := AgentActivityFromContext(ctx)
	job, err := memoryGenerationQuery(ctx, tx, activity.MemoryGenerationID)
	if err != nil {
		return result, err
	}
	ids := []string{nativeWorkspaceExecTool, nativeWorkspacePatchTool, nativeWorkspaceStdinTool}
	if job.Phase == "consolidation" {
		ids = append(ids, prepareAgentMemoryPublicationTool, publishAgentMemoryTool)
	}
	for _, id := range ids {
		descriptor, ok := registry.Get(id)
		if !ok {
			continue
		}
		if !memoryGenerationToolPolicy(job.Phase, descriptor) {
			return result, domainError("AGENT_TOOL_CONFIGURATION_CHANGED", "Memory native tool policy is inconsistent.")
		}
		result.Tools = append(result.Tools, descriptor)
	}
	return result, nil
}

func migrateAgentMemoryTools(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS agent_memory_tool_calls (
		agent_tool_call_id TEXT PRIMARY KEY REFERENCES agent_tool_calls(agent_tool_call_id) ON DELETE CASCADE,
		generation_id TEXT NOT NULL REFERENCES agent_memory_generations(generation_id) ON DELETE CASCADE,
		phase TEXT NOT NULL CHECK(phase IN ('extraction','consolidation','publication')),
		original_attempt INTEGER NOT NULL CHECK(original_attempt>0)
	);
	CREATE INDEX IF NOT EXISTS idx_memory_tool_generation ON agent_memory_tool_calls(generation_id,phase,agent_tool_call_id);`)
	if err != nil {
		return err
	}
	present, err := tableHasColumn(db, "agent_memory_tool_calls", "arguments_json")
	if err != nil {
		return err
	}
	if !present {
		_, err = db.Exec(`ALTER TABLE agent_memory_tool_calls ADD COLUMN arguments_json TEXT NOT NULL DEFAULT ''`)
	}
	return err
}

func validateMemoryToolBeginIdentity(ctx context.Context, command BeginAgentToolCallCommand) error {
	activity, active := AgentActivityFromContext(ctx)
	if command.MemoryGenerationID == "" && command.MemoryGenerationAttempt == 0 && (!active || activity.MemoryGenerationID == "") {
		return nil
	}
	principal, authenticated := identity.FromContext(ctx)
	if !authenticated || principal.Kind != identity.KindService || !active || command.MemoryGenerationID == "" ||
		activity.MemoryGenerationID != command.MemoryGenerationID || command.MemoryGenerationAttempt < 1 ||
		activity.MemoryGenerationAttempt != command.MemoryGenerationAttempt || activity.ProjectID != command.ProjectID ||
		command.AttemptToken == "" || activity.AttemptToken != command.AttemptToken ||
		command.AgentTurnID != "" || command.AgentTaskAttemptID != "" || command.ExecutionAttemptID != "" ||
		command.SkillInvocationID != "" || command.SkillSnapshot != nil {
		return domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "Memory tool registration requires its exact independent execution identity.")
	}
	switch command.ToolID {
	case nativeWorkspaceExecTool, nativeWorkspacePatchTool, nativeWorkspaceStdinTool, prepareAgentMemoryPublicationTool, publishAgentMemoryTool:
		return nil
	default:
		return domainError("AGENT_TOOL_EXECUTION_MODE_UNSUPPORTED", "This tool is not admitted for memory generation.")
	}
}

// Must run in the same transaction as the shared tool-call insert and approval.
func (s *Store) bindMemoryGenerationToolTx(ctx context.Context, tx *sql.Tx, callID string, arguments json.RawMessage) error {
	activity, active := AgentActivityFromContext(ctx)
	principal, authenticated := identity.FromContext(ctx)
	if !active || activity.MemoryGenerationID == "" || !authenticated || principal.Kind != identity.KindService {
		return domainError("AGENT_ACTIVITY_REQUIRED", "Memory tools require their own admitted execution.")
	}
	if _, err := s.resolveAgentActivityPrincipalTx(ctx, tx, activity); err != nil {
		return err
	}
	job, err := memoryGenerationQuery(ctx, tx, activity.MemoryGenerationID)
	if err != nil {
		return err
	}
	var valid bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_tool_calls c
		WHERE c.agent_tool_call_id=? AND c.project_id=? AND c.workspace_id=? AND c.agent_turn_id IS NULL AND c.skill_invocation_id IS NULL
		AND (EXISTS(SELECT 1 FROM agent_turns a WHERE 'turn:'||a.agent_turn_id=? AND a.conversation_id=c.conversation_id)
		OR EXISTS(SELECT 1 FROM agent_task_attempts a JOIN agent_tasks t ON t.agent_task_id=a.agent_task_id WHERE 'background:'||a.agent_task_attempt_id=? AND t.conversation_id=c.conversation_id)
		OR EXISTS(SELECT 1 FROM execution_attempts a JOIN runs r ON r.run_id=a.run_id WHERE 'execution:'||a.attempt_id=? AND r.conversation_id=c.conversation_id))
		AND NOT EXISTS(SELECT 1 FROM agent_task_tool_calls t WHERE t.agent_tool_call_id=c.agent_tool_call_id)
		AND NOT EXISTS(SELECT 1 FROM execution_tool_calls e WHERE e.agent_tool_call_id=c.agent_tool_call_id))`, callID, job.ProjectID, job.WorkspaceID, job.ActivityKey, job.ActivityKey, job.ActivityKey).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "Memory tool call has a conflicting execution owner.")
	}
	_, hash, _, err := summarizeAgentToolPayload(arguments, true)
	if err != nil {
		return err
	}
	var originalHash string
	if err := tx.QueryRowContext(ctx, `SELECT arguments_hash FROM agent_tool_calls WHERE agent_tool_call_id=?`, callID).Scan(&originalHash); err != nil {
		return err
	}
	if originalHash != hash {
		return domainError("AGENT_TOOL_CALL_ID_CONFLICT", "Private tool arguments do not match their original audit hash.")
	}
	if err := s.enforceWorkspaceQuotaTx(ctx, tx, job.WorkspaceID, "storage_bytes", int64(len(arguments)+512)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_memory_tool_calls(agent_tool_call_id,generation_id,phase,original_attempt,arguments_json) VALUES(?,?,?,?,?)`, callID, job.GenerationID, job.Phase, job.Attempt, string(arguments)); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE agent_tool_calls SET arguments_summary_json='{"private_memory_tool":true}' WHERE agent_tool_call_id=?`, callID)
	return err
}

func memoryToolBoundQuery(ctx context.Context, query rowQueryer, callID string) (bool, error) {
	var bound bool
	err := query.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_memory_tool_calls WHERE agent_tool_call_id=?)`, callID).Scan(&bound)
	return bound, err
}

func (s *Store) validateMemoryGenerationToolCallTx(ctx context.Context, tx *sql.Tx, callID string) (bool, error) {
	var generationID, phase string
	err := tx.QueryRowContext(ctx, `SELECT generation_id,phase FROM agent_memory_tool_calls WHERE agent_tool_call_id=?`, callID).Scan(&generationID, &phase)
	activity, active := AgentActivityFromContext(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		if active && activity.MemoryGenerationID != "" {
			return false, domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "Tool call is not bound to this memory generation.")
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	principal, authenticated := identity.FromContext(ctx)
	if !authenticated || principal.Kind != identity.KindService || !active || activity.MemoryGenerationID != generationID {
		return true, domainError("AGENT_ACTIVITY_SCOPE_MISMATCH", "Memory tool call requires its current generation execution.")
	}
	if _, err := s.resolveAgentActivityPrincipalTx(ctx, tx, activity); err != nil {
		return true, err
	}
	job, err := memoryGenerationQuery(ctx, tx, generationID)
	if err != nil {
		return true, err
	}
	if job.Phase != phase {
		return true, domainError("AGENT_ACTIVITY_STALE", "Memory tool call belongs to an earlier generation phase.")
	}
	return true, validateAgentActivityToolCallQuery(ctx, tx, activity, callID)
}
