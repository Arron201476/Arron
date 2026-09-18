package runtime

import "database/sql"

const scriptSnapshotSchema = `
CREATE TABLE IF NOT EXISTS agent_tool_skill_snapshots (
	agent_tool_call_id TEXT PRIMARY KEY REFERENCES agent_tool_calls(agent_tool_call_id) ON DELETE CASCADE,
	skill_version_id TEXT REFERENCES skill_versions(skill_version_id),
	skill_snapshot_id TEXT REFERENCES skill_execution_snapshots(skill_snapshot_id),
	CHECK(skill_version_id IS NOT NULL OR skill_snapshot_id IS NOT NULL)
);
CREATE TABLE IF NOT EXISTS skill_script_executions (
	skill_script_execution_id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(workspace_id),
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
	agent_tool_call_id TEXT NOT NULL UNIQUE REFERENCES agent_tool_calls(agent_tool_call_id),
	skill_installation_id TEXT REFERENCES skill_installations(skill_installation_id),
	skill_version_id TEXT REFERENCES skill_versions(skill_version_id),
	skill_snapshot_id TEXT REFERENCES skill_execution_snapshots(skill_snapshot_id),
	script_id TEXT NOT NULL,
	script_path TEXT NOT NULL,
	runtime TEXT NOT NULL,
	adapter TEXT NOT NULL,
	engine TEXT NOT NULL,
	image TEXT NOT NULL,
	status TEXT NOT NULL,
	input_hash TEXT NOT NULL,
	limits_json TEXT NOT NULL,
	environment_names_json TEXT NOT NULL,
	output_storage_ref TEXT NOT NULL,
	stdout_summary TEXT NOT NULL DEFAULT '',
	stderr_summary TEXT NOT NULL DEFAULT '',
	exit_code INTEGER,
	artifacts_json TEXT NOT NULL DEFAULT '[]',
	error_code TEXT,
	error_message TEXT,
	requested_by TEXT NOT NULL,
	started_at TEXT NOT NULL,
	completed_at TEXT,
	updated_at TEXT NOT NULL,
	CHECK(skill_version_id IS NOT NULL OR skill_snapshot_id IS NOT NULL),
	CHECK((skill_installation_id IS NULL) = (skill_version_id IS NULL))
);
CREATE INDEX IF NOT EXISTS idx_skill_script_executions_project
ON skill_script_executions(project_id, started_at DESC, skill_script_execution_id);
CREATE INDEX IF NOT EXISTS idx_skill_script_executions_workspace_status
ON skill_script_executions(workspace_id, status, updated_at, skill_script_execution_id);
`

func migrateScriptExecutionSnapshots(db *sql.DB) error {
	for _, table := range []struct{ name, columns string }{
		{"agent_tool_skill_snapshots", "agent_tool_call_id, skill_version_id"},
		{"skill_script_executions", `skill_script_execution_id, workspace_id, project_id, conversation_id,
			agent_tool_call_id, skill_installation_id, skill_version_id, script_id, script_path, runtime,
			adapter, engine, image, status, input_hash, limits_json, environment_names_json,
			output_storage_ref, stdout_summary, stderr_summary, exit_code, artifacts_json,
			error_code, error_message, requested_by, started_at, completed_at, updated_at`},
	} {
		present, err := tableHasColumn(db, table.name, "skill_snapshot_id")
		if err != nil {
			return err
		}
		if present {
			continue
		}
		// These leaf tables have no incoming foreign keys. Rebuild transactionally
		// to make managed installation IDs optional without disabling FK checks.
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		statements := []string{
			"ALTER TABLE " + table.name + " RENAME TO " + table.name + "_v36",
			scriptSnapshotSchema,
			"INSERT INTO " + table.name + "(" + table.columns + ") SELECT " + table.columns + " FROM " + table.name + "_v36",
			"DROP TABLE " + table.name + "_v36",
			scriptSnapshotSchema,
		}
		for _, statement := range statements {
			if _, err := tx.Exec(statement); err != nil {
				tx.Rollback()
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
