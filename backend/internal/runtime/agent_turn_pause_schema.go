package runtime

import (
	"context"
	"database/sql"
	"strings"
)

func migrateAgentTurnPauseStates(db *sql.DB) error {
	var tableSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='agent_turns'`).Scan(&tableSQL); err != nil {
		return err
	}
	if strings.Contains(tableSQL, "'pausing'") && strings.Contains(tableSQL, "'paused'") {
		return nil
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// All earlier migrations, including the frozen Skill catalog column, run first.
	_, err = tx.ExecContext(ctx, `
		CREATE TABLE agent_turns_v45 (
			agent_turn_id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			user_id TEXT NOT NULL DEFAULT 'user_local_default',
			project_id TEXT NOT NULL REFERENCES projects(project_id),
			conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
			idempotency_key TEXT NOT NULL, request_hash TEXT NOT NULL, request_json TEXT NOT NULL,
			status TEXT NOT NULL CHECK(status IN ('accepted','running','waiting_approval','pausing','paused','cancel_requested','committing','committed','failed','cancelled')),
			error_code TEXT, error_message TEXT, exchange_json TEXT,
			user_message_id TEXT REFERENCES messages(message_id),
			agent_message_id TEXT REFERENCES messages(message_id), terminal_event_id TEXT UNIQUE,
			created_at TEXT NOT NULL, started_at TEXT, completed_at TEXT,
			provider_id TEXT, model_id TEXT, release_id TEXT,
			trace_refs_json TEXT NOT NULL DEFAULT '[]', response_ids_json TEXT NOT NULL DEFAULT '[]', request_ids_json TEXT NOT NULL DEFAULT '[]',
			usage_json TEXT NOT NULL DEFAULT '{}', latency_json TEXT NOT NULL DEFAULT '{}',
			failure_stage TEXT, cancel_reason TEXT, skill_invocation_id TEXT, agent_task_id TEXT, run_id TEXT,
			agent_tool_call_ids_json TEXT NOT NULL DEFAULT '[]', updated_at TEXT NOT NULL,
			skill_snapshot_ready INTEGER NOT NULL DEFAULT 0,
			UNIQUE(project_id, idempotency_key)
		);
		INSERT INTO agent_turns_v45 (
			agent_turn_id,workspace_id,user_id,project_id,conversation_id,idempotency_key,request_hash,request_json,status,
			error_code,error_message,exchange_json,user_message_id,agent_message_id,terminal_event_id,created_at,started_at,completed_at,
			provider_id,model_id,release_id,trace_refs_json,response_ids_json,request_ids_json,usage_json,latency_json,
			failure_stage,cancel_reason,skill_invocation_id,agent_task_id,run_id,agent_tool_call_ids_json,updated_at,skill_snapshot_ready
		) SELECT agent_turn_id,workspace_id,user_id,project_id,conversation_id,idempotency_key,request_hash,request_json,status,
			error_code,error_message,exchange_json,user_message_id,agent_message_id,terminal_event_id,created_at,started_at,completed_at,
			provider_id,model_id,release_id,trace_refs_json,response_ids_json,request_ids_json,usage_json,latency_json,
			failure_stage,cancel_reason,skill_invocation_id,agent_task_id,run_id,agent_tool_call_ids_json,updated_at,skill_snapshot_ready FROM agent_turns;
		DROP TABLE agent_turns;
		ALTER TABLE agent_turns_v45 RENAME TO agent_turns;
		CREATE INDEX idx_agent_turns_project ON agent_turns(project_id,created_at DESC,agent_turn_id);
		CREATE INDEX idx_agent_turns_conversation_status ON agent_turns(conversation_id,status,created_at,agent_turn_id);
		CREATE INDEX idx_agent_turns_workspace_user ON agent_turns(workspace_id,user_id,created_at DESC,agent_turn_id);
	`)
	if err != nil {
		return err
	}
	return tx.Commit()
}
