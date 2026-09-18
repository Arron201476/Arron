package runtime

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/artifactcontract"
	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/documentparser"
	"content-agent/backend/internal/identity"
	"content-agent/backend/internal/scriptsandbox"

	_ "modernc.org/sqlite"
)

const schemaVersion = 78

const schema = `
CREATE TABLE IF NOT EXISTS workspaces (
	workspace_id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	status TEXT NOT NULL CHECK(status IN ('active','deleting','deleted')),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	deleted_at TEXT
);
CREATE TABLE IF NOT EXISTS users (
	user_id TEXT PRIMARY KEY,
	display_name TEXT NOT NULL,
	status TEXT NOT NULL CHECK(status IN ('active','disabled','deleted')),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	deleted_at TEXT
);
CREATE TABLE IF NOT EXISTS workspace_memberships (
	workspace_id TEXT NOT NULL REFERENCES workspaces(workspace_id),
	user_id TEXT NOT NULL REFERENCES users(user_id),
	role TEXT NOT NULL CHECK(role IN ('viewer','editor','admin','owner')),
	status TEXT NOT NULL CHECK(status IN ('active','disabled','deleted')),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(workspace_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_workspace_memberships_user
ON workspace_memberships(user_id, status, workspace_id);
CREATE TABLE IF NOT EXISTS workspace_quotas (
	workspace_id TEXT PRIMARY KEY REFERENCES workspaces(workspace_id),
	max_projects INTEGER NOT NULL DEFAULT 100,
	max_storage_bytes INTEGER NOT NULL DEFAULT 21474836480,
	max_active_agent_turns INTEGER NOT NULL DEFAULT 8,
	max_installed_skills INTEGER NOT NULL DEFAULT 100,
	audit_retention_days INTEGER NOT NULL DEFAULT 90,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS security_audit_events (
	audit_event_id TEXT PRIMARY KEY,
	workspace_id TEXT,
	user_id TEXT,
	principal_kind TEXT NOT NULL,
	action TEXT NOT NULL,
	resource_type TEXT,
	resource_id TEXT,
	outcome TEXT NOT NULL,
	status_code INTEGER NOT NULL,
	request_id TEXT NOT NULL,
	remote_addr TEXT,
	user_agent TEXT,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_security_audit_workspace
ON security_audit_events(workspace_id, created_at DESC, audit_event_id);
CREATE TABLE IF NOT EXISTS workspace_mcp_credentials (
	credential_id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL REFERENCES workspaces(workspace_id),
	server_id TEXT NOT NULL,
	credential_name TEXT NOT NULL,
	secret_ref TEXT NOT NULL,
	status TEXT NOT NULL CHECK(status IN ('active','disabled','deleted')),
	created_by_user_id TEXT NOT NULL REFERENCES users(user_id),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	deleted_at TEXT,
	UNIQUE(workspace_id, server_id, credential_name)
);
CREATE TABLE IF NOT EXISTS projects (
	project_id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL,
	owner_user_id TEXT NOT NULL DEFAULT 'user_local_default',
	title TEXT NOT NULL,
	version INTEGER NOT NULL,
	status TEXT NOT NULL,
	primary_conversation_id TEXT NOT NULL,
	active_write_run_id TEXT,
	current_capability_id TEXT,
	current_focus_artifact_version_id TEXT,
	project_event_seq INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	deleted_at TEXT
);
CREATE TABLE IF NOT EXISTS conversations (
	conversation_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	is_primary INTEGER NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_conversations_project ON conversations(project_id, is_primary DESC, conversation_id);
CREATE TABLE IF NOT EXISTS project_goals (
	goal_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
	title TEXT NOT NULL,
	success_criteria_json TEXT NOT NULL,
	status TEXT NOT NULL,
	version INTEGER NOT NULL,
	source_message_id TEXT NOT NULL REFERENCES messages(message_id),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	resolved_at TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_project_goals_one_active
ON project_goals(project_id) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_project_goals_history
ON project_goals(project_id, updated_at DESC, goal_id);
CREATE TABLE IF NOT EXISTS messages (
	message_id TEXT PRIMARY KEY,
	conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	role TEXT NOT NULL,
	content TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_conversation ON messages(conversation_id, created_at, message_id);
CREATE TABLE IF NOT EXISTS agent_turns (
	agent_turn_id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL,
	user_id TEXT NOT NULL DEFAULT 'user_local_default',
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
	idempotency_key TEXT NOT NULL,
	request_hash TEXT NOT NULL,
	request_json TEXT NOT NULL,
	status TEXT NOT NULL CHECK(status IN (
		'accepted','running','waiting_approval','pausing','paused','cancel_requested','committing',
		'committed','failed','cancelled'
	)),
	error_code TEXT,
	error_message TEXT,
	exchange_json TEXT,
	user_message_id TEXT REFERENCES messages(message_id),
	agent_message_id TEXT REFERENCES messages(message_id),
	terminal_event_id TEXT UNIQUE,
	created_at TEXT NOT NULL,
	started_at TEXT,
	completed_at TEXT,
	provider_id TEXT,
	model_id TEXT,
	release_id TEXT,
	trace_refs_json TEXT NOT NULL DEFAULT '[]',
	response_ids_json TEXT NOT NULL DEFAULT '[]',
	request_ids_json TEXT NOT NULL DEFAULT '[]',
	usage_json TEXT NOT NULL DEFAULT '{}',
	latency_json TEXT NOT NULL DEFAULT '{}',
	failure_stage TEXT,
	cancel_reason TEXT,
	skill_invocation_id TEXT,
	agent_task_id TEXT,
	run_id TEXT,
	agent_tool_call_ids_json TEXT NOT NULL DEFAULT '[]',
	updated_at TEXT NOT NULL,
	UNIQUE(project_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS idx_agent_turns_project
ON agent_turns(project_id, created_at DESC, agent_turn_id);
CREATE INDEX IF NOT EXISTS idx_agent_turns_conversation_status
ON agent_turns(conversation_id, status, created_at, agent_turn_id);
CREATE TABLE IF NOT EXISTS agent_turn_run_states (
	agent_turn_id TEXT PRIMARY KEY REFERENCES agent_turns(agent_turn_id) ON DELETE CASCADE,
	schema_version TEXT NOT NULL,
	state_json TEXT NOT NULL,
	state_hash TEXT NOT NULL,
	pending_sdk_tool_call_ids_json TEXT NOT NULL,
	checkpoint_version INTEGER NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS message_contexts (
	message_id TEXT PRIMARY KEY REFERENCES messages(message_id),
	capability_ref_json TEXT,
	attachment_refs_json TEXT NOT NULL,
	selection_snapshot_json TEXT,
	client_context_json TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS message_routing_contexts (
	message_id TEXT PRIMARY KEY REFERENCES messages(message_id),
	scope TEXT NOT NULL,
	invocation_id TEXT,
	run_id TEXT,
	capability_id TEXT,
	artifact_id TEXT
);
CREATE INDEX IF NOT EXISTS idx_message_routing_run ON message_routing_contexts(run_id, message_id);
CREATE INDEX IF NOT EXISTS idx_message_routing_capability ON message_routing_contexts(capability_id, message_id);
CREATE TABLE IF NOT EXISTS agent_decisions (
	agent_decision_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
	user_message_id TEXT NOT NULL REFERENCES messages(message_id),
	agent_message_id TEXT NOT NULL REFERENCES messages(message_id),
	intent TEXT NOT NULL,
	confidence REAL NOT NULL,
	decision_json TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_agent_decisions_conversation ON agent_decisions(conversation_id, created_at, agent_decision_id);
CREATE TABLE IF NOT EXISTS conversation_summaries (
	conversation_summary_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
	scope TEXT NOT NULL,
	covered_through_message_id TEXT,
	covered_message_count INTEGER NOT NULL,
	summary_text TEXT NOT NULL,
	source_hash TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE(conversation_id, scope)
);
CREATE INDEX IF NOT EXISTS idx_conversation_summaries_project
ON conversation_summaries(project_id, updated_at DESC, conversation_summary_id);
CREATE TABLE IF NOT EXISTS project_memory_entries (
	memory_entry_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	kind TEXT NOT NULL,
	scope TEXT NOT NULL,
	content TEXT NOT NULL,
	payload_json TEXT NOT NULL,
	source_kind TEXT NOT NULL,
	source_ref_id TEXT NOT NULL,
	source_hash TEXT NOT NULL,
	confidence REAL NOT NULL,
	status TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	invalidated_at TEXT,
	UNIQUE(project_id, kind, scope, source_kind, source_ref_id)
);
CREATE INDEX IF NOT EXISTS idx_project_memory_entries_lookup
ON project_memory_entries(project_id, status, kind, updated_at DESC, memory_entry_id);
CREATE TABLE IF NOT EXISTS skill_installations (
	skill_installation_id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL,
	scope TEXT NOT NULL,
	scope_ref TEXT NOT NULL,
	skill_name TEXT NOT NULL,
	capability_id TEXT NOT NULL,
	status TEXT NOT NULL,
	enabled INTEGER NOT NULL,
	active_version_id TEXT,
	created_by TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	uninstalled_at TEXT,
	UNIQUE(workspace_id, scope, scope_ref, skill_name),
	UNIQUE(workspace_id, scope, scope_ref, capability_id),
	FOREIGN KEY(active_version_id) REFERENCES skill_versions(skill_version_id)
);
CREATE INDEX IF NOT EXISTS idx_skill_installations_workspace
ON skill_installations(workspace_id, status, updated_at DESC, skill_installation_id);
CREATE TABLE IF NOT EXISTS skill_versions (
	skill_version_id TEXT PRIMARY KEY,
	skill_installation_id TEXT NOT NULL REFERENCES skill_installations(skill_installation_id),
	version TEXT NOT NULL,
	content_hash TEXT NOT NULL,
	execution_mode TEXT NOT NULL,
	package_ref TEXT NOT NULL,
	source_type TEXT NOT NULL,
	source_name TEXT NOT NULL,
	manifest_json TEXT NOT NULL,
	status TEXT NOT NULL,
	installed_by TEXT NOT NULL,
	created_at TEXT NOT NULL,
	UNIQUE(skill_installation_id, version)
);
CREATE INDEX IF NOT EXISTS idx_skill_versions_installation
ON skill_versions(skill_installation_id, created_at DESC, skill_version_id);
CREATE TABLE IF NOT EXISTS skill_install_attempts (
	skill_install_attempt_id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL,
	scope TEXT NOT NULL DEFAULT 'workspace',
	scope_ref TEXT NOT NULL DEFAULT '',
	source_type TEXT NOT NULL,
	source_name TEXT NOT NULL,
	status TEXT NOT NULL,
	failure_code TEXT,
	diagnostics_json TEXT NOT NULL,
	created_by TEXT NOT NULL,
	created_at TEXT NOT NULL,
	completed_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_skill_install_attempts_workspace
ON skill_install_attempts(workspace_id, created_at DESC, skill_install_attempt_id);
CREATE TABLE IF NOT EXISTS skill_installation_events (
	skill_installation_event_id TEXT PRIMARY KEY,
	skill_installation_id TEXT NOT NULL REFERENCES skill_installations(skill_installation_id),
	skill_version_id TEXT REFERENCES skill_versions(skill_version_id),
	event_type TEXT NOT NULL,
	actor_ref TEXT NOT NULL,
	payload_json TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_skill_installation_events_installation
ON skill_installation_events(skill_installation_id, created_at, skill_installation_event_id);
CREATE TABLE IF NOT EXISTS skill_invocations (
	skill_invocation_id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL DEFAULT '',
	skill_version_id TEXT REFERENCES skill_versions(skill_version_id),
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
	user_message_id TEXT NOT NULL UNIQUE REFERENCES messages(message_id),
	agent_message_id TEXT NOT NULL UNIQUE REFERENCES messages(message_id),
	capability_id TEXT NOT NULL,
	capability_version TEXT NOT NULL,
	execution_mode TEXT NOT NULL,
	status TEXT NOT NULL,
	proposed_action_id TEXT,
	agent_task_id TEXT,
	run_id TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_skill_invocations_project ON skill_invocations(project_id, created_at, skill_invocation_id);
CREATE INDEX IF NOT EXISTS idx_skill_invocations_run ON skill_invocations(run_id, skill_invocation_id);
CREATE TABLE IF NOT EXISTS proposed_actions (
	proposed_action_id TEXT PRIMARY KEY,
	agent_decision_id TEXT NOT NULL REFERENCES agent_decisions(agent_decision_id),
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
	confirmation_message_id TEXT NOT NULL REFERENCES messages(message_id),
	action_type TEXT NOT NULL,
	version INTEGER NOT NULL,
	status TEXT NOT NULL,
	capability_id TEXT,
	capability_version TEXT,
	input_json TEXT NOT NULL,
	config_json TEXT NOT NULL,
	snapshot_hash TEXT NOT NULL,
	requires_confirmation INTEGER NOT NULL,
	consumed_run_id TEXT,
	consumed_task_id TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_proposed_actions_project_status ON proposed_actions(project_id, status, created_at DESC, proposed_action_id);
CREATE TABLE IF NOT EXISTS agent_tasks (
	agent_task_id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
	skill_invocation_id TEXT NOT NULL UNIQUE REFERENCES skill_invocations(skill_invocation_id),
	proposed_action_id TEXT UNIQUE REFERENCES proposed_actions(proposed_action_id),
	capability_id TEXT NOT NULL,
	capability_version TEXT NOT NULL,
	skill_name TEXT NOT NULL,
	skill_description TEXT NOT NULL,
	skill_instructions TEXT NOT NULL,
	skill_content_hash TEXT NOT NULL,
	status TEXT NOT NULL,
	progress_current INTEGER NOT NULL DEFAULT 0,
	progress_total INTEGER NOT NULL DEFAULT 1,
	progress_message TEXT NOT NULL DEFAULT '',
	input_json TEXT NOT NULL,
	config_json TEXT NOT NULL,
	result_artifact_id TEXT,
	result_artifact_version_id TEXT,
	result_json TEXT,
	failure_code TEXT,
	failure_message TEXT,
	attempt_count INTEGER NOT NULL DEFAULT 0,
	max_attempts INTEGER NOT NULL DEFAULT 3,
	cancel_requested INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	queued_at TEXT NOT NULL,
	started_at TEXT,
	completed_at TEXT,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_agent_tasks_project
ON agent_tasks(project_id, created_at DESC, agent_task_id);
CREATE INDEX IF NOT EXISTS idx_agent_tasks_claim
ON agent_tasks(status, queued_at, agent_task_id);
CREATE TABLE IF NOT EXISTS agent_task_attempts (
	agent_task_attempt_id TEXT PRIMARY KEY,
	agent_task_id TEXT NOT NULL REFERENCES agent_tasks(agent_task_id),
	attempt_no INTEGER NOT NULL,
	worker_id TEXT NOT NULL,
	provider_id TEXT NOT NULL,
	model_id TEXT NOT NULL,
	input_snapshot_hash TEXT NOT NULL,
	status TEXT NOT NULL,
	token_hash TEXT NOT NULL,
	lease_until TEXT NOT NULL,
	result_json TEXT,
	usage_json TEXT NOT NULL DEFAULT '{}',
	trace_ref TEXT,
	error_code TEXT,
	error_message TEXT,
	started_at TEXT NOT NULL,
	ended_at TEXT,
	UNIQUE(agent_task_id, attempt_no)
);
CREATE INDEX IF NOT EXISTS idx_agent_task_attempts_task
ON agent_task_attempts(agent_task_id, attempt_no DESC);
CREATE INDEX IF NOT EXISTS idx_agent_task_attempts_lease
ON agent_task_attempts(status, lease_until, agent_task_attempt_id);
CREATE TABLE IF NOT EXISTS agent_tool_calls (
	agent_tool_call_id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
	skill_invocation_id TEXT REFERENCES skill_invocations(skill_invocation_id),
	agent_turn_id TEXT,
	sdk_tool_call_id TEXT NOT NULL,
	tool_id TEXT NOT NULL,
	tool_kind TEXT NOT NULL,
	server_id TEXT,
	tool_name TEXT NOT NULL,
	access_mode TEXT NOT NULL,
	approval_policy TEXT NOT NULL,
	max_result_bytes INTEGER NOT NULL,
	approval_status TEXT NOT NULL,
	status TEXT NOT NULL,
	arguments_summary_json TEXT NOT NULL,
	arguments_hash TEXT NOT NULL,
	result_summary_json TEXT,
	result_hash TEXT,
	result_size_bytes INTEGER,
	trace_ref TEXT,
	error_code TEXT,
	error_message TEXT,
	requested_at TEXT NOT NULL,
	started_at TEXT,
	completed_at TEXT,
	approval_consumed_at TEXT,
	updated_at TEXT NOT NULL,
	UNIQUE(conversation_id, sdk_tool_call_id)
);
CREATE INDEX IF NOT EXISTS idx_agent_tool_calls_project
ON agent_tool_calls(project_id, requested_at DESC, agent_tool_call_id);
CREATE INDEX IF NOT EXISTS idx_agent_tool_calls_status
ON agent_tool_calls(status, updated_at, agent_tool_call_id);
CREATE TABLE IF NOT EXISTS agent_task_tool_calls (
	agent_tool_call_id TEXT PRIMARY KEY REFERENCES agent_tool_calls(agent_tool_call_id) ON DELETE CASCADE,
	agent_task_attempt_id TEXT NOT NULL REFERENCES agent_task_attempts(agent_task_attempt_id)
);
CREATE INDEX IF NOT EXISTS idx_agent_task_tool_calls_attempt
ON agent_task_tool_calls(agent_task_attempt_id, agent_tool_call_id);
CREATE TABLE IF NOT EXISTS agent_task_run_states (
	agent_task_id TEXT PRIMARY KEY REFERENCES agent_tasks(agent_task_id) ON DELETE CASCADE,
	agent_task_attempt_id TEXT NOT NULL UNIQUE REFERENCES agent_task_attempts(agent_task_attempt_id),
	schema_version TEXT NOT NULL,
	state_json TEXT NOT NULL,
	state_hash TEXT NOT NULL,
	pending_sdk_tool_call_ids_json TEXT NOT NULL,
	checkpoint_version INTEGER NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS agent_tool_approvals (
	agent_tool_approval_id TEXT PRIMARY KEY,
	agent_tool_call_id TEXT NOT NULL UNIQUE REFERENCES agent_tool_calls(agent_tool_call_id),
	workspace_id TEXT NOT NULL,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
	status TEXT NOT NULL,
	version INTEGER NOT NULL,
	title TEXT NOT NULL,
	reason TEXT NOT NULL,
	options_json TEXT NOT NULL,
	subject_snapshot_hash TEXT NOT NULL,
	requested_at TEXT NOT NULL,
	resolved_at TEXT,
	resolution_json TEXT,
	actor_ref TEXT
);
CREATE INDEX IF NOT EXISTS idx_agent_tool_approvals_project_status
ON agent_tool_approvals(project_id, status, requested_at, agent_tool_approval_id);
CREATE TABLE IF NOT EXISTS workspace_script_policies (
	workspace_id TEXT PRIMARY KEY REFERENCES workspaces(workspace_id),
	enabled INTEGER NOT NULL DEFAULT 0,
	version INTEGER NOT NULL DEFAULT 1,
	limits_json TEXT NOT NULL,
	updated_by TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
` + scriptSnapshotSchema + `
CREATE TABLE IF NOT EXISTS target_resolutions (
	target_resolution_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
	request_message_id TEXT NOT NULL REFERENCES messages(message_id),
	status TEXT NOT NULL,
	source TEXT NOT NULL,
	artifact_id TEXT,
	artifact_version_id TEXT,
	artifact_type TEXT,
	scope_key TEXT,
	field_path TEXT,
	entity_json TEXT NOT NULL,
	text_range_json TEXT,
	display_json TEXT NOT NULL,
	candidates_json TEXT NOT NULL,
	target_hash TEXT,
	created_at TEXT NOT NULL,
	resolved_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_target_resolutions_project
ON target_resolutions(project_id, created_at DESC, target_resolution_id);
CREATE TABLE IF NOT EXISTS revision_requests (
	revision_request_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
	request_message_id TEXT NOT NULL REFERENCES messages(message_id),
	target_resolution_id TEXT NOT NULL REFERENCES target_resolutions(target_resolution_id),
	artifact_id TEXT,
	base_artifact_version_id TEXT,
	instruction TEXT NOT NULL,
	operation TEXT NOT NULL,
	status TEXT NOT NULL,
	execution_policy TEXT NOT NULL,
	version INTEGER NOT NULL,
	proposal_payload_json TEXT,
	proposal_summary TEXT,
	proposal_hash TEXT,
	failure_code TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	finished_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_revision_requests_project_status
ON revision_requests(project_id, status, created_at, revision_request_id);
CREATE TABLE IF NOT EXISTS revision_attempts (
	revision_attempt_id TEXT PRIMARY KEY,
	revision_request_id TEXT NOT NULL REFERENCES revision_requests(revision_request_id),
	attempt_no INTEGER NOT NULL,
	status TEXT NOT NULL,
	context_hash TEXT NOT NULL,
	context_payload_json TEXT NOT NULL,
	adapter_id TEXT NOT NULL,
	adapter_version TEXT NOT NULL,
	provider_id TEXT,
	trace_ref TEXT,
	response_hash TEXT,
	failure_code TEXT,
	created_at TEXT NOT NULL,
	finished_at TEXT,
	UNIQUE(revision_request_id, attempt_no)
);
CREATE INDEX IF NOT EXISTS idx_revision_attempts_request
ON revision_attempts(revision_request_id, attempt_no);
CREATE TABLE IF NOT EXISTS upload_sessions (
	upload_session_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	status TEXT NOT NULL,
	declared_item_count INTEGER NOT NULL,
	completed_item_count INTEGER NOT NULL,
	failed_item_count INTEGER NOT NULL,
	created_at TEXT NOT NULL,
	closed_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_upload_sessions_project ON upload_sessions(project_id, created_at DESC, upload_session_id);
CREATE TABLE IF NOT EXISTS upload_items (
	upload_item_id TEXT PRIMARY KEY,
	upload_session_id TEXT NOT NULL REFERENCES upload_sessions(upload_session_id),
	client_item_key TEXT NOT NULL,
	kind TEXT NOT NULL,
	original_filename TEXT NOT NULL,
	declared_mime_type TEXT NOT NULL,
	declared_size_bytes INTEGER NOT NULL,
	received_size_bytes INTEGER NOT NULL,
	status TEXT NOT NULL,
	staging_ref TEXT,
	checksum TEXT,
	asset_id TEXT,
	failure TEXT,
	created_at TEXT NOT NULL,
	completed_at TEXT,
	UNIQUE(upload_session_id, client_item_key)
);
CREATE INDEX IF NOT EXISTS idx_upload_items_session ON upload_items(upload_session_id, created_at, upload_item_id);
CREATE TABLE IF NOT EXISTS asset_blobs (
	blob_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	asset_id TEXT NOT NULL,
	role TEXT NOT NULL,
	storage_ref TEXT NOT NULL UNIQUE,
	size_bytes INTEGER NOT NULL,
	checksum_algorithm TEXT NOT NULL,
	checksum TEXT NOT NULL,
	status TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS assets (
	asset_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	current_snapshot_id TEXT NOT NULL,
	kind TEXT NOT NULL,
	source_type TEXT NOT NULL,
	display_name TEXT NOT NULL,
	original_filename TEXT NOT NULL,
	extension TEXT NOT NULL,
	declared_mime_type TEXT NOT NULL,
	detected_mime_type TEXT NOT NULL,
	size_bytes INTEGER NOT NULL,
	checksum_algorithm TEXT NOT NULL,
	checksum TEXT NOT NULL,
	status TEXT NOT NULL,
	parse_status TEXT NOT NULL,
	original_blob_id TEXT NOT NULL,
	metadata_json TEXT NOT NULL,
	retention_policy_id TEXT,
	uploaded_at TEXT NOT NULL,
	expires_at TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	deleted_at TEXT,
	delete_reason TEXT
);
CREATE INDEX IF NOT EXISTS idx_assets_project ON assets(project_id, updated_at DESC, asset_id);
CREATE TABLE IF NOT EXISTS asset_snapshots (
	asset_snapshot_id TEXT PRIMARY KEY,
	asset_id TEXT NOT NULL REFERENCES assets(asset_id),
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	snapshot_version INTEGER NOT NULL,
	status TEXT NOT NULL,
	payload_json TEXT NOT NULL,
	created_at TEXT NOT NULL,
	UNIQUE(asset_id, snapshot_version)
);
CREATE INDEX IF NOT EXISTS idx_asset_snapshots_asset ON asset_snapshots(asset_id, snapshot_version);
CREATE TABLE IF NOT EXISTS asset_parse_results (
	asset_parse_result_id TEXT PRIMARY KEY,
	asset_id TEXT NOT NULL REFERENCES assets(asset_id),
	asset_snapshot_id TEXT NOT NULL UNIQUE REFERENCES asset_snapshots(asset_snapshot_id),
	status TEXT NOT NULL,
	parser_id TEXT NOT NULL,
	parser_version TEXT NOT NULL,
	content_text TEXT NOT NULL,
	content_hash TEXT NOT NULL,
	error_code TEXT,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_asset_parse_results_asset ON asset_parse_results(asset_id, created_at DESC);
CREATE TABLE IF NOT EXISTS asset_sets (
	asset_set_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	purpose TEXT NOT NULL,
	display_name TEXT NOT NULL,
	status TEXT NOT NULL,
	current_version_id TEXT,
	current_version INTEGER NOT NULL,
	created_by_message_id TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	sealed_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_asset_sets_project ON asset_sets(project_id, updated_at DESC, asset_set_id);
CREATE TABLE IF NOT EXISTS asset_set_versions (
	asset_set_version_id TEXT PRIMARY KEY,
	asset_set_id TEXT NOT NULL REFERENCES asset_sets(asset_set_id),
	version INTEGER NOT NULL,
	status TEXT NOT NULL,
	member_count INTEGER NOT NULL,
	completeness_json TEXT NOT NULL,
	continuation_policy_json TEXT,
	created_at TEXT NOT NULL,
	UNIQUE(asset_set_id, version)
);
CREATE INDEX IF NOT EXISTS idx_asset_set_versions_set ON asset_set_versions(asset_set_id, version DESC);
CREATE TABLE IF NOT EXISTS asset_set_members (
	asset_set_member_id TEXT PRIMARY KEY,
	asset_set_version_id TEXT NOT NULL REFERENCES asset_set_versions(asset_set_version_id),
	asset_id TEXT NOT NULL REFERENCES assets(asset_id),
	episode_order INTEGER NOT NULL,
	episode_no INTEGER,
	episode_label TEXT,
	episode_source TEXT NOT NULL,
	filename_candidate_json TEXT NOT NULL,
	included INTEGER NOT NULL,
	exclusion_reason TEXT,
	UNIQUE(asset_set_version_id, asset_id),
	UNIQUE(asset_set_version_id, episode_order)
);
CREATE INDEX IF NOT EXISTS idx_asset_set_members_version ON asset_set_members(asset_set_version_id, episode_order, asset_set_member_id);
CREATE TABLE IF NOT EXISTS retention_jobs (
	retention_job_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	asset_id TEXT NOT NULL REFERENCES assets(asset_id),
	policy_id TEXT NOT NULL,
	action TEXT NOT NULL,
	due_at TEXT NOT NULL,
	status TEXT NOT NULL,
	attempt_count INTEGER NOT NULL,
	worker_id TEXT,
	lease_until TEXT,
	last_failure TEXT,
	completed_at TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE(asset_id, policy_id, action, due_at)
);
CREATE INDEX IF NOT EXISTS idx_retention_jobs_claim
ON retention_jobs(status, due_at, lease_until, retention_job_id);
CREATE INDEX IF NOT EXISTS idx_retention_jobs_asset
ON retention_jobs(asset_id, created_at DESC, retention_job_id);
CREATE TABLE IF NOT EXISTS asset_delete_previews (
	asset_delete_preview_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	asset_id TEXT NOT NULL REFERENCES assets(asset_id),
	asset_status TEXT NOT NULL,
	asset_checksum TEXT NOT NULL,
	active_run_impacts_json TEXT NOT NULL,
	artifact_impacts_json TEXT NOT NULL,
	existing_artifacts_preserved INTEGER NOT NULL,
	snapshot_hash TEXT NOT NULL,
	status TEXT NOT NULL,
	created_at TEXT NOT NULL,
	resolved_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_asset_delete_previews_asset
ON asset_delete_previews(asset_id, status, created_at DESC, asset_delete_preview_id);
CREATE TABLE IF NOT EXISTS project_delete_previews (
	project_delete_preview_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	project_version INTEGER NOT NULL,
	impact_json TEXT NOT NULL,
	snapshot_hash TEXT NOT NULL,
	status TEXT NOT NULL,
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	resolved_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_project_delete_previews_project
ON project_delete_previews(project_id, status, created_at DESC, project_delete_preview_id);
CREATE TABLE IF NOT EXISTS runs (
	run_id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL DEFAULT '',
	skill_version_id TEXT REFERENCES skill_versions(skill_version_id),
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
	capability_id TEXT NOT NULL,
	capability_version TEXT NOT NULL,
	run_kind TEXT NOT NULL,
	write_intent INTEGER NOT NULL,
	status TEXT NOT NULL,
	current_step_run_id TEXT,
	current_input_snapshot_version_id TEXT NOT NULL,
	input_snapshot_status TEXT NOT NULL,
	config_snapshot_json TEXT NOT NULL,
	run_event_seq INTEGER NOT NULL DEFAULT 0,
	started_at TEXT,
	ended_at TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS one_active_write_run_per_project
ON runs(project_id)
WHERE write_intent = 1
  AND status IN ('pending', 'running', 'waiting_approval', 'pausing', 'paused', 'failed');
CREATE INDEX IF NOT EXISTS idx_runs_project_created ON runs(project_id, created_at DESC, run_id);
CREATE TABLE IF NOT EXISTS run_input_snapshot_versions (
	run_input_snapshot_version_id TEXT PRIMARY KEY,
	run_id TEXT NOT NULL REFERENCES runs(run_id),
	version INTEGER NOT NULL,
	status TEXT NOT NULL,
	payload_json TEXT NOT NULL,
	created_at TEXT NOT NULL,
	sealed_at TEXT,
	UNIQUE(run_id, version)
);
CREATE INDEX IF NOT EXISTS idx_run_input_snapshots_run ON run_input_snapshot_versions(run_id, version);
CREATE TABLE IF NOT EXISTS run_config_snapshots (
	config_snapshot_id TEXT PRIMARY KEY,
	run_id TEXT NOT NULL REFERENCES runs(run_id),
	config_ref TEXT NOT NULL,
	version INTEGER NOT NULL,
	status TEXT NOT NULL,
	payload_json TEXT NOT NULL,
	snapshot_hash TEXT NOT NULL,
	created_at TEXT NOT NULL,
	sealed_at TEXT NOT NULL,
	UNIQUE(run_id, config_ref, version)
);
CREATE INDEX IF NOT EXISTS idx_run_config_snapshots_run_ref
ON run_config_snapshots(run_id, config_ref, version DESC);
CREATE TABLE IF NOT EXISTS run_decision_snapshots (
	decision_snapshot_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	run_id TEXT NOT NULL REFERENCES runs(run_id),
	step_run_id TEXT NOT NULL REFERENCES step_runs(step_run_id),
	decision_type TEXT NOT NULL,
	source_kind TEXT NOT NULL,
	source_ref_id TEXT NOT NULL,
	version INTEGER NOT NULL,
	status TEXT NOT NULL,
	payload_json TEXT NOT NULL,
	snapshot_hash TEXT NOT NULL,
	created_at TEXT NOT NULL,
	UNIQUE(run_id, decision_type, version)
);
CREATE INDEX IF NOT EXISTS idx_run_decisions_run
ON run_decision_snapshots(run_id, created_at, decision_snapshot_id);
CREATE TABLE IF NOT EXISTS step_runs (
	step_run_id TEXT PRIMARY KEY,
	run_id TEXT NOT NULL REFERENCES runs(run_id),
	step_id TEXT NOT NULL,
	status TEXT NOT NULL,
	attempt_count INTEGER NOT NULL,
	approval_policy TEXT NOT NULL,
	input_version_snapshot_json TEXT NOT NULL,
	task_cursor_json TEXT NOT NULL,
	started_at TEXT,
	ended_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_step_runs_run ON step_runs(run_id, step_run_id);
CREATE TABLE IF NOT EXISTS task_items (
	task_item_id TEXT PRIMARY KEY,
	step_run_id TEXT NOT NULL REFERENCES step_runs(step_run_id),
	run_id TEXT NOT NULL REFERENCES runs(run_id),
	item_key TEXT NOT NULL,
	item_order INTEGER NOT NULL,
	status TEXT NOT NULL,
	attempt_count INTEGER NOT NULL,
	input_snapshot_json TEXT NOT NULL,
	cursor_json TEXT NOT NULL,
	current_attempt_id TEXT,
	output_artifact_version_id TEXT,
	started_at TEXT,
	ended_at TEXT,
	failure TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE(step_run_id, item_key)
);
CREATE INDEX IF NOT EXISTS idx_task_items_claim
ON task_items(status, item_order, created_at, task_item_id);
CREATE INDEX IF NOT EXISTS idx_task_items_step
ON task_items(step_run_id, item_order, task_item_id);
CREATE TABLE IF NOT EXISTS execution_attempts (
	attempt_id TEXT PRIMARY KEY,
	run_id TEXT NOT NULL REFERENCES runs(run_id),
	step_run_id TEXT NOT NULL REFERENCES step_runs(step_run_id),
	task_item_id TEXT NOT NULL REFERENCES task_items(task_item_id),
	attempt_no INTEGER NOT NULL,
	executor_id TEXT NOT NULL,
	provider_id TEXT NOT NULL,
	worker_id TEXT NOT NULL,
	request_fingerprint TEXT NOT NULL,
	input_snapshot_hash TEXT NOT NULL,
	status TEXT NOT NULL,
	token_hash TEXT NOT NULL,
	lease_until TEXT NOT NULL,
	response_hash TEXT,
	response_payload_json TEXT,
	usage_json TEXT NOT NULL,
	trace_ref TEXT,
	started_at TEXT NOT NULL,
	ended_at TEXT,
	error_code TEXT,
	UNIQUE(task_item_id, attempt_no)
);
CREATE INDEX IF NOT EXISTS idx_execution_attempts_lease
ON execution_attempts(status, lease_until, attempt_id);
CREATE TABLE IF NOT EXISTS execution_tool_calls (
	agent_tool_call_id TEXT PRIMARY KEY REFERENCES agent_tool_calls(agent_tool_call_id) ON DELETE CASCADE,
	execution_attempt_id TEXT NOT NULL REFERENCES execution_attempts(attempt_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_execution_tool_calls_attempt
ON execution_tool_calls(execution_attempt_id, agent_tool_call_id);
CREATE TABLE IF NOT EXISTS execution_run_states (
	attempt_id TEXT PRIMARY KEY REFERENCES execution_attempts(attempt_id) ON DELETE CASCADE,
	schema_version TEXT NOT NULL,
	state_json TEXT NOT NULL,
	state_hash TEXT NOT NULL,
	pending_sdk_tool_call_ids_json TEXT NOT NULL,
	worker_state_json TEXT NOT NULL,
	worker_state_hash TEXT NOT NULL,
	checkpoint_version INTEGER NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS execution_result_rejections (
	attempt_id TEXT NOT NULL REFERENCES execution_attempts(attempt_id) ON DELETE CASCADE,
	rejection_no INTEGER NOT NULL CHECK (rejection_no BETWEEN 1 AND 2),
	response_hash TEXT NOT NULL,
	response_payload_json TEXT NOT NULL,
	usage_json TEXT NOT NULL,
	usage_hash TEXT NOT NULL,
	trace_ref TEXT NOT NULL,
	error_code TEXT NOT NULL,
	error_detail TEXT NOT NULL,
	status TEXT NOT NULL CHECK (status IN ('queued','claimed','closed','terminal')),
	claim_count INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (attempt_id, rejection_no)
);
CREATE INDEX IF NOT EXISTS idx_execution_result_rejections_queue
ON execution_result_rejections(status, updated_at, attempt_id);
CREATE TABLE IF NOT EXISTS execution_failure_details (
	attempt_id TEXT PRIMARY KEY REFERENCES execution_attempts(attempt_id),
	task_item_id TEXT NOT NULL REFERENCES task_items(task_item_id),
	error_code TEXT NOT NULL,
	stage TEXT NOT NULL,
	summary TEXT NOT NULL,
	technical_detail TEXT,
	provider_output TEXT,
	provider_status_code INTEGER,
	provider_request_id TEXT,
	transport_category TEXT,
	retryable INTEGER NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_execution_failure_details_task
ON execution_failure_details(task_item_id, created_at, attempt_id);
CREATE TABLE IF NOT EXISTS context_packs (
	context_pack_id TEXT PRIMARY KEY,
	attempt_id TEXT NOT NULL UNIQUE REFERENCES execution_attempts(attempt_id),
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	run_id TEXT NOT NULL REFERENCES runs(run_id),
	step_run_id TEXT NOT NULL REFERENCES step_runs(step_run_id),
	task_item_id TEXT NOT NULL REFERENCES task_items(task_item_id),
	pack_type TEXT NOT NULL,
	pack_version TEXT NOT NULL,
	context_hash TEXT NOT NULL,
	payload_json TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_context_packs_task
ON context_packs(task_item_id, created_at, context_pack_id);
CREATE TABLE IF NOT EXISTS task_result_checkpoints (
	task_result_checkpoint_id TEXT PRIMARY KEY,
	task_item_id TEXT NOT NULL UNIQUE REFERENCES task_items(task_item_id),
	attempt_id TEXT NOT NULL UNIQUE REFERENCES execution_attempts(attempt_id),
	step_run_id TEXT NOT NULL REFERENCES step_runs(step_run_id),
	run_id TEXT NOT NULL REFERENCES runs(run_id),
	item_key TEXT NOT NULL,
	item_order INTEGER NOT NULL,
	artifact_type TEXT NOT NULL,
	payload_json TEXT NOT NULL,
	payload_hash TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_task_result_checkpoints_step
ON task_result_checkpoints(step_run_id, item_order, task_result_checkpoint_id);
CREATE TABLE IF NOT EXISTS quality_reviews (
	quality_review_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	run_id TEXT NOT NULL REFERENCES runs(run_id),
	step_run_id TEXT NOT NULL REFERENCES step_runs(step_run_id),
	input_snapshot_hash TEXT NOT NULL,
	status TEXT NOT NULL,
	scope TEXT NOT NULL,
	review_version INTEGER NOT NULL,
	issue_counts_json TEXT NOT NULL,
	recommended_route TEXT,
	affected_episode_nos_json TEXT NOT NULL,
	result_json TEXT NOT NULL,
	created_at TEXT NOT NULL,
	finished_at TEXT,
	UNIQUE(run_id, input_snapshot_hash)
);
CREATE INDEX IF NOT EXISTS idx_quality_reviews_run_current
ON quality_reviews(run_id, created_at DESC, quality_review_id);
CREATE TABLE IF NOT EXISTS quality_overrides (
	quality_override_id TEXT PRIMARY KEY,
	quality_review_id TEXT NOT NULL UNIQUE REFERENCES quality_reviews(quality_review_id),
	input_snapshot_hash TEXT NOT NULL,
	ignored_issue_ids_json TEXT NOT NULL,
	actor_ref TEXT NOT NULL,
	confirmed_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS artifacts (
	artifact_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	run_id TEXT REFERENCES runs(run_id),
	step_run_id TEXT REFERENCES step_runs(step_run_id),
	origin_type TEXT NOT NULL DEFAULT 'run',
	origin_id TEXT NOT NULL DEFAULT '',
	capability_id TEXT NOT NULL,
	artifact_type TEXT NOT NULL,
	scope_key TEXT NOT NULL,
	current_version_id TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	CHECK (
		(run_id IS NOT NULL AND step_run_id IS NOT NULL AND origin_type = 'run') OR
		(run_id IS NULL AND step_run_id IS NULL AND origin_type IN ('agent_turn','invocation','agent_task') AND origin_id <> '')
	),
	UNIQUE(run_id, artifact_type, scope_key)
);
CREATE INDEX IF NOT EXISTS idx_artifacts_project ON artifacts(project_id, updated_at DESC, artifact_id);
CREATE TABLE IF NOT EXISTS artifact_versions (
	artifact_version_id TEXT PRIMARY KEY,
	artifact_id TEXT NOT NULL REFERENCES artifacts(artifact_id),
	version INTEGER NOT NULL,
	status TEXT NOT NULL,
	payload_json TEXT NOT NULL,
	schema_id TEXT NOT NULL,
	schema_version TEXT NOT NULL,
	created_by_kind TEXT NOT NULL,
	actor_ref TEXT NOT NULL,
	creation_reason TEXT NOT NULL,
	base_version_id TEXT,
	created_at TEXT NOT NULL,
	confirmed_at TEXT,
	UNIQUE(artifact_id, version)
);
CREATE INDEX IF NOT EXISTS idx_artifact_versions_artifact ON artifact_versions(artifact_id, version);
CREATE TABLE IF NOT EXISTS artifact_dependencies (
	dependency_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	run_id TEXT REFERENCES runs(run_id),
	origin_type TEXT NOT NULL DEFAULT 'run',
	origin_id TEXT NOT NULL DEFAULT '',
	downstream_artifact_version_id TEXT NOT NULL REFERENCES artifact_versions(artifact_version_id),
	upstream_kind TEXT NOT NULL,
	upstream_ref_id TEXT NOT NULL,
	relation TEXT NOT NULL,
	upstream_scope_json TEXT NOT NULL,
	upstream_scope_hash TEXT NOT NULL,
	downstream_scope_json TEXT NOT NULL,
	downstream_scope_hash TEXT NOT NULL,
	impact_policy_id TEXT NOT NULL,
	created_at TEXT NOT NULL,
	UNIQUE(
		downstream_artifact_version_id,
		upstream_kind,
		upstream_ref_id,
		relation,
		upstream_scope_hash,
		downstream_scope_hash
	)
);
CREATE INDEX IF NOT EXISTS idx_artifact_dependencies_upstream
ON artifact_dependencies(upstream_kind, upstream_ref_id);
CREATE INDEX IF NOT EXISTS idx_artifact_dependencies_downstream
ON artifact_dependencies(downstream_artifact_version_id);
CREATE INDEX IF NOT EXISTS idx_artifact_dependencies_project_relation
ON artifact_dependencies(project_id, relation);
CREATE TABLE IF NOT EXISTS artifact_version_change_sets (
	change_set_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	artifact_version_id TEXT NOT NULL UNIQUE REFERENCES artifact_versions(artifact_version_id),
	base_version_id TEXT REFERENCES artifact_versions(artifact_version_id),
	change_mode TEXT NOT NULL,
	changes_json TEXT NOT NULL,
	derived_signals_json TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_artifact_change_sets_base
ON artifact_version_change_sets(base_version_id);
CREATE TABLE IF NOT EXISTS impact_reviews (
	impact_review_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	run_id TEXT NOT NULL REFERENCES runs(run_id),
	source_artifact_id TEXT NOT NULL REFERENCES artifacts(artifact_id),
	old_version_id TEXT NOT NULL REFERENCES artifact_versions(artifact_version_id),
	new_version_id TEXT NOT NULL UNIQUE REFERENCES artifact_versions(artifact_version_id),
	change_set_id TEXT NOT NULL REFERENCES artifact_version_change_sets(change_set_id),
	status TEXT NOT NULL,
	unaffected_summary_json TEXT NOT NULL,
	recommended_regeneration_start_json TEXT NOT NULL,
	snapshot_hash TEXT NOT NULL,
	created_at TEXT NOT NULL,
	resolved_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_impact_reviews_project_status
ON impact_reviews(project_id, status, created_at, impact_review_id);
CREATE INDEX IF NOT EXISTS idx_impact_reviews_source_status
ON impact_reviews(source_artifact_id, status, created_at, impact_review_id);
CREATE TABLE IF NOT EXISTS impact_review_items (
	impact_review_item_id TEXT PRIMARY KEY,
	impact_review_id TEXT NOT NULL REFERENCES impact_reviews(impact_review_id),
	artifact_id TEXT NOT NULL REFERENCES artifacts(artifact_id),
	artifact_version_id TEXT NOT NULL REFERENCES artifact_versions(artifact_version_id),
	artifact_type TEXT NOT NULL,
	scope_key TEXT NOT NULL,
	impact_path_json TEXT NOT NULL,
	reason_code TEXT NOT NULL,
	regenerate_from_step_id TEXT NOT NULL,
	regenerate_task_keys_json TEXT NOT NULL,
	item_order INTEGER NOT NULL,
	UNIQUE(impact_review_id, artifact_version_id)
);
CREATE INDEX IF NOT EXISTS idx_impact_review_items_review
ON impact_review_items(impact_review_id, item_order, impact_review_item_id);
CREATE TABLE IF NOT EXISTS dependency_decisions (
	dependency_decision_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	run_id TEXT NOT NULL REFERENCES runs(run_id),
	impact_review_id TEXT NOT NULL UNIQUE REFERENCES impact_reviews(impact_review_id),
	action TEXT NOT NULL,
	old_upstream_version_id TEXT NOT NULL REFERENCES artifact_versions(artifact_version_id),
	new_upstream_version_id TEXT NOT NULL REFERENCES artifact_versions(artifact_version_id),
	preserved_downstream_version_ids_json TEXT NOT NULL,
	stale_downstream_version_ids_json TEXT NOT NULL,
	actor_ref TEXT NOT NULL,
	resolved_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_dependency_decisions_project
ON dependency_decisions(project_id, resolved_at, dependency_decision_id);
CREATE TABLE IF NOT EXISTS regeneration_plans (
	regeneration_plan_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	run_id TEXT NOT NULL REFERENCES runs(run_id),
	impact_review_id TEXT NOT NULL UNIQUE REFERENCES impact_reviews(impact_review_id),
	source_new_version_id TEXT NOT NULL REFERENCES artifact_versions(artifact_version_id),
	status TEXT NOT NULL,
	current_group_order INTEGER NOT NULL,
	created_at TEXT NOT NULL,
	completed_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_regeneration_plans_run_status
ON regeneration_plans(run_id, status, created_at, regeneration_plan_id);
CREATE TABLE IF NOT EXISTS regeneration_plan_groups (
	regeneration_plan_group_id TEXT PRIMARY KEY,
	regeneration_plan_id TEXT NOT NULL REFERENCES regeneration_plans(regeneration_plan_id),
	group_order INTEGER NOT NULL,
	step_id TEXT NOT NULL,
	task_keys_json TEXT NOT NULL,
	stale_version_ids_json TEXT NOT NULL,
	preserved_version_ids_json TEXT NOT NULL,
	step_run_id TEXT REFERENCES step_runs(step_run_id),
	status TEXT NOT NULL,
	UNIQUE(regeneration_plan_id, group_order),
	UNIQUE(regeneration_plan_id, step_id)
);
CREATE INDEX IF NOT EXISTS idx_regeneration_plan_groups_plan
ON regeneration_plan_groups(regeneration_plan_id, group_order);
CREATE TABLE IF NOT EXISTS approvals (
	approval_request_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	run_id TEXT NOT NULL REFERENCES runs(run_id),
	step_run_id TEXT NOT NULL REFERENCES step_runs(step_run_id),
	scope TEXT NOT NULL,
	status TEXT NOT NULL,
	version INTEGER NOT NULL,
	title TEXT NOT NULL,
	reason TEXT NOT NULL,
	options_json TEXT NOT NULL,
	subject_kind TEXT NOT NULL,
	subject_ref_id TEXT NOT NULL,
	subject_version INTEGER NOT NULL,
	subject_snapshot_hash TEXT NOT NULL,
	requested_at TEXT NOT NULL,
	resolved_at TEXT,
	resolution_json TEXT,
	actor_ref TEXT
);
CREATE INDEX IF NOT EXISTS idx_approvals_project_status ON approvals(project_id, status, requested_at, approval_request_id);
CREATE TABLE IF NOT EXISTS approval_subject_versions (
	approval_request_id TEXT NOT NULL REFERENCES approvals(approval_request_id),
	artifact_version_id TEXT NOT NULL REFERENCES artifact_versions(artifact_version_id),
	item_order INTEGER NOT NULL,
	scope_key TEXT NOT NULL,
	PRIMARY KEY(approval_request_id, artifact_version_id),
	UNIQUE(approval_request_id, item_order)
);
CREATE INDEX IF NOT EXISTS idx_approval_subject_versions_version
ON approval_subject_versions(artifact_version_id, approval_request_id);
CREATE TABLE IF NOT EXISTS script_candidates (
` + scriptCandidateColumns + `
);
CREATE INDEX IF NOT EXISTS idx_script_candidates_project
ON script_candidates(project_id, created_at DESC, candidate_id ASC);
CREATE INDEX IF NOT EXISTS idx_script_candidates_run
ON script_candidates(source_run_id, created_at DESC, candidate_id);
CREATE UNIQUE INDEX IF NOT EXISTS one_final_candidate_per_project
ON script_candidates(project_id)
WHERE status = 'final';
CREATE TABLE IF NOT EXISTS final_selections (
	final_selection_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	candidate_id TEXT NOT NULL REFERENCES script_candidates(candidate_id),
	approval_request_id TEXT NOT NULL REFERENCES approvals(approval_request_id),
	selection_no INTEGER NOT NULL,
	status TEXT NOT NULL,
	selected_at TEXT NOT NULL,
	replaced_selection_id TEXT REFERENCES final_selections(final_selection_id),
	UNIQUE(project_id, selection_no),
	UNIQUE(approval_request_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS one_active_final_selection_per_project
ON final_selections(project_id)
WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_final_selections_project
ON final_selections(project_id, selection_no DESC, final_selection_id);
CREATE TABLE IF NOT EXISTS final_selection_previews (
	final_selection_preview_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	candidate_id TEXT NOT NULL REFERENCES script_candidates(candidate_id),
	approval_request_id TEXT NOT NULL REFERENCES approvals(approval_request_id),
	current_selection_id TEXT REFERENCES final_selections(final_selection_id),
	expected_current_selection_id TEXT REFERENCES final_selections(final_selection_id),
	proposed_selection_no INTEGER NOT NULL,
	preview_hash TEXT NOT NULL,
	status TEXT NOT NULL,
	created_at TEXT NOT NULL,
	resolved_at TEXT,
	UNIQUE(approval_request_id)
);
CREATE INDEX IF NOT EXISTS idx_final_selection_previews_project
ON final_selection_previews(project_id, status, created_at DESC, final_selection_preview_id);
CREATE TABLE IF NOT EXISTS exports (
	export_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	candidate_id TEXT NOT NULL REFERENCES script_candidates(candidate_id),
	artifact_version_id TEXT NOT NULL REFERENCES artifact_versions(artifact_version_id),
	format TEXT NOT NULL,
	status TEXT NOT NULL,
	content_type TEXT NOT NULL,
	filename TEXT NOT NULL,
	storage_ref TEXT NOT NULL,
	checksum TEXT NOT NULL,
	size_bytes INTEGER NOT NULL,
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_exports_candidate ON exports(candidate_id, created_at DESC, export_id);
CREATE TABLE IF NOT EXISTS migration_history (
	migration_id TEXT PRIMARY KEY,
	from_version INTEGER NOT NULL,
	to_version INTEGER NOT NULL,
	backup_ref TEXT NOT NULL,
	status TEXT NOT NULL,
	started_at TEXT NOT NULL,
	completed_at TEXT
);
CREATE TABLE IF NOT EXISTS events (
	event_id TEXT PRIMARY KEY,
	event_type TEXT NOT NULL,
	schema_version INTEGER NOT NULL,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	run_id TEXT,
	step_run_id TEXT,
	project_event_seq INTEGER NOT NULL,
	run_event_seq INTEGER,
	actor_kind TEXT NOT NULL,
	actor_ref TEXT NOT NULL,
	subject_type TEXT NOT NULL,
	subject_id TEXT NOT NULL,
	payload_json TEXT NOT NULL,
	occurred_at TEXT NOT NULL,
	UNIQUE(project_id, project_event_seq),
	UNIQUE(run_id, run_event_seq)
);
CREATE INDEX IF NOT EXISTS idx_events_project_seq ON events(project_id, project_event_seq);
CREATE INDEX IF NOT EXISTS idx_events_run_seq ON events(run_id, run_event_seq);
CREATE INDEX IF NOT EXISTS idx_events_subject ON events(subject_type, subject_id, occurred_at);
CREATE TABLE IF NOT EXISTS event_outbox (
	event_id TEXT PRIMARY KEY REFERENCES events(event_id),
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	run_id TEXT,
	status TEXT NOT NULL,
	delivery_attempts INTEGER NOT NULL DEFAULT 0,
	available_at TEXT NOT NULL,
	created_at TEXT NOT NULL,
	published_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_event_outbox_pending
ON event_outbox(status, available_at, event_id);
INSERT OR IGNORE INTO event_outbox(
	event_id, project_id, run_id, status, delivery_attempts,
	available_at, created_at, published_at
)
SELECT
	event_id, project_id, run_id, 'published', 0,
	occurred_at, occurred_at, occurred_at
FROM events;
CREATE TRIGGER IF NOT EXISTS events_reject_update
BEFORE UPDATE ON events
BEGIN
	SELECT RAISE(ABORT, 'events are append-only');
END;
CREATE TRIGGER IF NOT EXISTS events_reject_delete
BEFORE DELETE ON events
BEGIN
	SELECT RAISE(ABORT, 'events are append-only');
END;
CREATE TABLE IF NOT EXISTS idempotency_records (
	idempotency_record_id TEXT PRIMARY KEY,
	scope TEXT NOT NULL,
	command_type TEXT NOT NULL,
	idempotency_key TEXT NOT NULL,
	request_hash TEXT NOT NULL,
	status TEXT NOT NULL,
	response_json TEXT,
	error_code TEXT,
	error_message TEXT,
	created_at TEXT NOT NULL,
	completed_at TEXT,
	expires_at TEXT NOT NULL,
	UNIQUE(scope, command_type, idempotency_key)
);
CREATE INDEX IF NOT EXISTS idx_idempotency_expires ON idempotency_records(expires_at);
`

type Store struct {
	db                  *sql.DB
	registry            *capability.Registry
	contracts           *artifactcontract.Registry
	dataRoot            string
	now                 func() time.Time
	newID               func(string) string
	documentParser      documentparser.Parser
	skillDataRoot       string
	skillMu             sync.Mutex
	registryMu          sync.RWMutex
	workspaceRegistries map[string]*capability.Registry
	agentTools          *agenttool.Registry
	scriptSandbox       scriptsandbox.Sandbox
	mcpCredentialCipher cipher.AEAD
}

func Open(path string, registry *capability.Registry) (*Store, error) {
	if registry == nil {
		registry = capability.NewEmptyRegistry()
	}
	contracts, err := artifactcontract.Load(registry.ProjectRoot())
	if err != nil {
		return nil, fmt.Errorf("load artifact contracts: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create runtime directory: %w", err)
	}
	dataRoot := filepath.Dir(path)
	skillDataRoot := filepath.Join(dataRoot, "skills")
	for _, directory := range []string{
		filepath.Join(dataRoot, "assets"),
		filepath.Join(dataRoot, "staging"),
		filepath.Join(skillDataRoot, "quarantine"),
		filepath.Join(skillDataRoot, "packages"),
		filepath.Join(skillDataRoot, "activation"),
		filepath.Join(skillDataRoot, "active", SharedWorkspaceID),
	} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return nil, fmt.Errorf("create asset directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open runtime database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	var currentVersion int
	if err := db.QueryRow("PRAGMA user_version").Scan(&currentVersion); err != nil {
		db.Close()
		return nil, fmt.Errorf("read schema version: %w", err)
	}
	if currentVersion > schemaVersion {
		db.Close()
		return nil, fmt.Errorf("runtime schema version %d is newer than supported version %d", currentVersion, schemaVersion)
	}
	migrationID := ""
	backupRef := ""
	if currentVersion > 0 && currentVersion < schemaVersion {
		migrationID = fmt.Sprintf("mig_%d_to_%d_%d", currentVersion, schemaVersion, time.Now().UTC().UnixNano())
		backupDir := filepath.Join(dataRoot, "backups", migrationID)
		if err := os.MkdirAll(backupDir, 0o755); err != nil {
			db.Close()
			return nil, fmt.Errorf("create migration backup directory: %w", err)
		}
		backupRef = filepath.Join(backupDir, filepath.Base(path))
		quotedBackup := "'" + strings.ReplaceAll(filepath.ToSlash(backupRef), "'", "''") + "'"
		if _, err := db.Exec("VACUUM INTO " + quotedBackup); err != nil {
			db.Close()
			return nil, fmt.Errorf("create migration backup: %w", err)
		}
	}
	statement := "PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA synchronous=FULL; PRAGMA foreign_keys=ON;" + schema + projectFileSchema + projectSkillDraftSchema + agentSubtaskSchema + agentToolReconciliationSchema + nativeWorkspaceSchema + nativeWorkspaceEnvironmentSchema + nativeWorkspaceRestoreSchema + nativeWorkspaceCommandSchema + nativeWorkspaceFileSchema + nativeWorkspaceManifestSchema + nativeWorkspacePTYSchema + agentProgramCallSchema
	if _, err := db.Exec(statement); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate runtime database: %w", err)
	}
	if err := migrateIdentityBoundaries(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate identity boundaries: %w", err)
	}
	if err := migrateProjectFileBinaryVersions(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate binary project files: %w", err)
	}
	if err := migrateExecutionOwners(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate execution owners: %w", err)
	}
	if err := migrateSkillInstallAttemptScopes(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate Skill attempt scopes: %w", err)
	}
	if err := migrateAgentTurnObservability(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate Agent turn observability: %w", err)
	}
	if err := migrateAgentTurnApprovalCheckpoints(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate Agent turn approval checkpoints: %w", err)
	}
	if err := migrateAgentTaskInputs(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate Agent task inputs: %w", err)
	}
	if err := migrateSkillExecutionSnapshots(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate Skill execution snapshots: %w", err)
	}
	if err := migrateAgentTurnPauseStates(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate Agent turn pause states: %w", err)
	}
	if err := migrateScriptCandidateVersions(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate script candidate versions: %w", err)
	}
	if err := migrateAgentTurnInputs(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate Agent turn inputs: %w", err)
	}
	if err := migrateExecutionInputs(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate execution inputs: %w", err)
	}
	if err := migrateRunSkillCatalogs(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate Run Skill catalogs: %w", err)
	}
	if err := migrateScriptExecutionSnapshots(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate script execution snapshots: %w", err)
	}
	if err := migrateWorkspaceAgentTools(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate workspace Agent tools: %w", err)
	}
	if err := migrateMCPConnections(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate MCP connections: %w", err)
	}
	if err := migrateAgentInstructions(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate Agent instructions: %w", err)
	}
	if err := migrateAgentMemory(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate Agent memory: %w", err)
	}
	if err := migrateAgentMemoryPreferences(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateAgentMemorySnapshots(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate Agent memory snapshots: %w", err)
	}
	if err := migrateAgentMemoryPublications(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate Agent memory publications: %w", err)
	}
	if err := migrateAgentMemoryRollouts(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateAgentMemoryGenerations(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateAgentMemoryTools(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateExecutionInputChanges(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate execution input changes: %w", err)
	}
	if err := migrateExecutionInputAttachments(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate execution input attachments: %w", err)
	}
	if err := ensureAgentToolApprovalConsumedColumn(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate Agent tool approval consumption: %w", err)
	}
	if err := ensureSkillInvocationExecutionBindings(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate Skill Invocation execution bindings: %w", err)
	}
	if err := ensureProposedActionTaskColumn(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate proposed action Task binding: %w", err)
	}
	if err := migrateStandaloneArtifactOrigins(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate standalone Artifact origins: %w", err)
	}
	revisionRecoveryTime := formatTime(time.Now())
	revisionRecoveryCutoff := formatTime(time.Now().Add(-10 * time.Minute))
	if _, err := db.Exec(`UPDATE revision_attempts
		SET status = 'failed', failure_code = 'RUNTIME_RESTART_INTERRUPTED', finished_at = ?
		WHERE status = 'running' AND created_at <= ?`,
		revisionRecoveryTime, revisionRecoveryCutoff); err != nil {
		db.Close()
		return nil, fmt.Errorf("recover interrupted revision attempts: %w", err)
	}
	if _, err := db.Exec(`UPDATE skill_script_executions
		SET status = 'failed', error_code = 'RUNTIME_RESTART_INTERRUPTED',
			error_message = '运行时重启中断了脚本沙箱执行。', completed_at = ?, updated_at = ?
		WHERE status = 'running'`, revisionRecoveryTime, revisionRecoveryTime); err != nil {
		db.Close()
		return nil, fmt.Errorf("recover interrupted Skill script executions: %w", err)
	}
	if _, err := db.Exec(`UPDATE revision_requests
		SET status = 'failed', failure_code = 'RUNTIME_RESTART_INTERRUPTED',
			version = version + 1, updated_at = ?, finished_at = ?
		WHERE status = 'running' AND updated_at <= ?`,
		revisionRecoveryTime, revisionRecoveryTime, revisionRecoveryCutoff); err != nil {
		db.Close()
		return nil, fmt.Errorf("recover interrupted revision requests: %w", err)
	}
	if _, err := db.Exec(`UPDATE proposed_actions
		SET status = 'superseded', updated_at = CURRENT_TIMESTAMP
		WHERE status = 'pending' AND project_id IN (
			SELECT project_id FROM projects WHERE active_write_run_id IS NOT NULL
		)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("expire conflicting proposed actions: %w", err)
	}
	if _, err := db.Exec(`
		UPDATE regeneration_plan_groups SET status = 'failed'
		WHERE status IN ('ready','running','waiting_approval')
			AND regeneration_plan_id IN (
				SELECT rp.regeneration_plan_id FROM regeneration_plans rp
				JOIN runs r ON r.run_id = rp.run_id WHERE r.status = 'failed'
			);
		UPDATE regeneration_plans SET status = 'failed'
		WHERE status IN ('pending','running','waiting_approval')
			AND run_id IN (SELECT run_id FROM runs WHERE status = 'failed');
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("reconcile failed regeneration plans: %w", err)
	}
	reconciliationTime := formatTime(time.Now())
	if _, err := db.Exec(`
		UPDATE artifacts
		SET updated_at = substr(updated_at, 1, 10) || 'T' || substr(updated_at, 12) || 'Z'
		WHERE length(updated_at) = 19 AND substr(updated_at, 11, 1) = ' ';
		UPDATE regeneration_plans
		SET completed_at = substr(completed_at, 1, 10) || 'T' || substr(completed_at, 12) || 'Z'
		WHERE completed_at IS NOT NULL AND length(completed_at) = 19
			AND substr(completed_at, 11, 1) = ' ';
		UPDATE artifact_versions SET status = 'confirmed'
		WHERE status = 'superseded'
			AND artifact_version_id IN (
				SELECT current_av.base_version_id
				FROM artifacts a
				JOIN artifact_versions current_av ON current_av.artifact_version_id = a.current_version_id
				WHERE current_av.status = 'stale'
					AND current_av.creation_reason IN ('regenerate_artifact', 'regenerate_step')
					AND current_av.base_version_id IS NOT NULL
					AND current_av.artifact_version_id IN (
						SELECT json_each.value
						FROM regeneration_plan_groups rpg
						JOIN regeneration_plans rp ON rp.regeneration_plan_id = rpg.regeneration_plan_id
						JOIN runs r ON r.run_id = rp.run_id,
						json_each(rpg.stale_version_ids_json)
						WHERE r.status = 'cancelled'
							AND rp.status IN ('pending', 'running', 'waiting_approval', 'failed')
					)
			);
		UPDATE artifacts
		SET current_version_id = (
			SELECT base_version_id FROM artifact_versions
			WHERE artifact_version_id = artifacts.current_version_id
		), updated_at = ?
		WHERE current_version_id IN (
			SELECT current_av.artifact_version_id
			FROM artifact_versions current_av
			WHERE current_av.status = 'stale'
				AND current_av.creation_reason IN ('regenerate_artifact', 'regenerate_step')
				AND current_av.base_version_id IS NOT NULL
				AND current_av.artifact_version_id IN (
					SELECT json_each.value
					FROM regeneration_plan_groups rpg
					JOIN regeneration_plans rp ON rp.regeneration_plan_id = rpg.regeneration_plan_id
					JOIN runs r ON r.run_id = rp.run_id,
					json_each(rpg.stale_version_ids_json)
					WHERE r.status = 'cancelled'
						AND rp.status IN ('pending', 'running', 'waiting_approval', 'failed')
				)
		);
		UPDATE artifact_versions SET status = 'invalidated'
		WHERE status = 'stale'
			AND creation_reason IN ('regenerate_artifact', 'regenerate_step')
			AND artifact_version_id IN (
				SELECT json_each.value
				FROM regeneration_plan_groups rpg
				JOIN regeneration_plans rp ON rp.regeneration_plan_id = rpg.regeneration_plan_id
				JOIN runs r ON r.run_id = rp.run_id,
				json_each(rpg.stale_version_ids_json)
				WHERE r.status = 'cancelled'
					AND rp.status IN ('pending', 'running', 'waiting_approval', 'failed')
			);
		UPDATE artifact_versions SET status = 'confirmed'
		WHERE status = 'stale'
			AND artifact_version_id IN (SELECT current_version_id FROM artifacts)
			AND artifact_version_id IN (
				SELECT json_each.value
				FROM regeneration_plan_groups rpg
				JOIN regeneration_plans rp ON rp.regeneration_plan_id = rpg.regeneration_plan_id
				JOIN runs r ON r.run_id = rp.run_id,
				json_each(rpg.stale_version_ids_json)
				WHERE r.status = 'cancelled'
					AND rp.status IN ('pending', 'running', 'waiting_approval', 'failed')
			);
		UPDATE regeneration_plan_groups SET status = 'cancelled'
		WHERE status IN ('blocked', 'ready', 'running', 'waiting_approval', 'failed')
			AND regeneration_plan_id IN (
				SELECT rp.regeneration_plan_id FROM regeneration_plans rp
				JOIN runs r ON r.run_id = rp.run_id WHERE r.status = 'cancelled'
			);
		UPDATE regeneration_plans
		SET status = 'cancelled', completed_at = COALESCE(completed_at, ?)
		WHERE status IN ('pending', 'running', 'waiting_approval', 'failed')
			AND run_id IN (SELECT run_id FROM runs WHERE status = 'cancelled');
	`, reconciliationTime, reconciliationTime); err != nil {
		db.Close()
		return nil, fmt.Errorf("reconcile cancelled regeneration plans: %w", err)
	}
	if _, err := db.Exec(`
		UPDATE regeneration_plan_groups
		SET status = 'completed'
		WHERE status = 'waiting_approval'
			AND step_run_id IN (SELECT step_run_id FROM step_runs WHERE status = 'completed')
			AND regeneration_plan_id IN (
				SELECT regeneration_plan_id FROM regeneration_plan_groups
				GROUP BY regeneration_plan_id HAVING COUNT(*) = 1
			);
		UPDATE regeneration_plans
		SET status = 'completed', completed_at = COALESCE(completed_at, CURRENT_TIMESTAMP)
		WHERE status = 'waiting_approval'
			AND NOT EXISTS (
				SELECT 1 FROM regeneration_plan_groups rpg
				WHERE rpg.regeneration_plan_id = regeneration_plans.regeneration_plan_id
					AND rpg.status != 'completed'
			);
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("reconcile completed regeneration plans: %w", err)
	}
	if err := migrateRunConfigSnapshots(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("backfill runtime config snapshots: %w", err)
	}
	if err := ensureMessageRoutingInvocationColumn(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate message invocation routing: %w", err)
	}
	if err := ensureExecutionFailureProviderOutputColumn(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate execution failure provider output: %w", err)
	}
	if err := backfillMessageRoutingContexts(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("backfill message routing contexts: %w", err)
	}
	if err := backfillSkillInvocations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("backfill skill invocations: %w", err)
	}
	if err := completeRuntimeMigration(db, dataRoot, migrationID, backupRef, currentVersion); err != nil {
		db.Close()
		return nil, err
	}
	store := &Store{
		db:                  db,
		registry:            registry,
		contracts:           contracts,
		dataRoot:            dataRoot,
		now:                 func() time.Time { return time.Now().UTC() },
		newID:               randomID,
		documentParser:      documentparser.NewDefaultFromEnv(),
		skillDataRoot:       skillDataRoot,
		workspaceRegistries: make(map[string]*capability.Registry),
		scriptSandbox:       scriptsandbox.Disabled{},
	}
	if err := store.initializeSkillInstallations(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize Skill installations: %w", err)
	}
	return store, nil
}

func (s *Store) SetAgentToolRegistry(registry *agenttool.Registry) {
	s.agentTools = registry
}

func (s *Store) SetScriptSandbox(sandbox scriptsandbox.Sandbox) {
	if sandbox == nil {
		s.scriptSandbox = scriptsandbox.Disabled{}
		return
	}
	s.scriptSandbox = sandbox
}

func ensureAgentToolApprovalConsumedColumn(db *sql.DB) error {
	present, err := tableHasColumn(db, "agent_tool_calls", "approval_consumed_at")
	if err != nil || present {
		return err
	}
	_, err = db.Exec(`ALTER TABLE agent_tool_calls ADD COLUMN approval_consumed_at TEXT`)
	return err
}

func backfillSkillInvocations(db *sql.DB) error {
	_, err := db.Exec(`
		INSERT OR IGNORE INTO skill_invocations(
			skill_invocation_id, project_id, conversation_id, user_message_id, agent_message_id,
			capability_id, capability_version, execution_mode, status, proposed_action_id, run_id,
			created_at, updated_at
		)
		SELECT 'inv_' || lower(hex(randomblob(16))), pa.project_id, pa.conversation_id,
			ad.user_message_id, ad.agent_message_id, pa.capability_id, pa.capability_version,
			'stateful_workflow', CASE WHEN pa.consumed_run_id IS NULL THEN 'awaiting_confirmation' ELSE 'delegated_to_run' END,
			pa.proposed_action_id, pa.consumed_run_id, pa.created_at, pa.updated_at
		FROM proposed_actions pa
		JOIN agent_decisions ad ON ad.agent_decision_id = pa.agent_decision_id
		WHERE pa.capability_id IS NOT NULL AND pa.capability_version IS NOT NULL;
		UPDATE message_routing_contexts
		SET invocation_id = (
			SELECT si.skill_invocation_id FROM skill_invocations si
			WHERE si.user_message_id = message_routing_contexts.message_id OR si.agent_message_id = message_routing_contexts.message_id
			LIMIT 1
		)
		WHERE invocation_id IS NULL AND EXISTS (
			SELECT 1 FROM skill_invocations si
			WHERE si.user_message_id = message_routing_contexts.message_id OR si.agent_message_id = message_routing_contexts.message_id
		);
		UPDATE message_routing_contexts
		SET scope = CASE WHEN run_id IS NULL THEN 'invocation' ELSE 'run' END
		WHERE invocation_id IS NOT NULL;
	`)
	return err
}

func ensureSkillInvocationExecutionBindings(db *sql.DB) error {
	present, err := tableHasColumn(db, "skill_invocations", "agent_task_id")
	if err != nil {
		return err
	}
	if !present {
		if _, err := db.Exec(`ALTER TABLE skill_invocations ADD COLUMN agent_task_id TEXT`); err != nil {
			return err
		}
	}
	_, err = db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_skill_invocations_task
		ON skill_invocations(agent_task_id) WHERE agent_task_id IS NOT NULL;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_skill_invocations_run_unique
		ON skill_invocations(run_id) WHERE run_id IS NOT NULL;
		CREATE TRIGGER IF NOT EXISTS trg_skill_invocations_single_execution_insert
		BEFORE INSERT ON skill_invocations
		WHEN NEW.agent_task_id IS NOT NULL AND NEW.run_id IS NOT NULL
		BEGIN
			SELECT RAISE(ABORT, 'Skill Invocation cannot bind both Agent Task and Run');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_skill_invocations_single_execution_update
		BEFORE UPDATE OF agent_task_id, run_id ON skill_invocations
		WHEN NEW.agent_task_id IS NOT NULL AND NEW.run_id IS NOT NULL
		BEGIN
			SELECT RAISE(ABORT, 'Skill Invocation cannot bind both Agent Task and Run');
		END;`)
	return err
}

func ensureProposedActionTaskColumn(db *sql.DB) error {
	present, err := tableHasColumn(db, "proposed_actions", "consumed_task_id")
	if err != nil {
		return err
	}
	if present {
		return nil
	}
	_, err = db.Exec(`ALTER TABLE proposed_actions ADD COLUMN consumed_task_id TEXT`)
	return err
}

func tableHasColumn(db *sql.DB, tableName, columnName string) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + tableName + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if name == columnName {
			return true, nil
		}
	}
	return false, rows.Err()
}

func migrateIdentityBoundaries(db *sql.DB) error {
	for _, column := range []struct {
		table      string
		name       string
		definition string
	}{
		{table: "projects", name: "owner_user_id", definition: "TEXT"},
		{table: "agent_turns", name: "user_id", definition: "TEXT"},
	} {
		present, err := tableHasColumn(db, column.table, column.name)
		if err != nil {
			return err
		}
		if !present {
			if _, err := db.Exec(fmt.Sprintf(
				"ALTER TABLE %s ADD COLUMN %s %s", column.table, column.name, column.definition,
			)); err != nil {
				return err
			}
		}
	}
	now := formatTime(time.Now().UTC())
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT OR IGNORE INTO users(user_id, display_name, status, created_at, updated_at)
		  VALUES(?, ?, 'active', ?, ?)`, []any{identity.DefaultUserID, identity.DefaultUserName, now, now}},
		{`INSERT OR IGNORE INTO workspaces(workspace_id, name, status, created_at, updated_at)
		  VALUES(?, ?, 'active', ?, ?)`, []any{identity.DefaultWorkspaceID, identity.DefaultWorkspaceName, now, now}},
		{`INSERT OR IGNORE INTO workspaces(workspace_id, name, status, created_at, updated_at)
		  SELECT DISTINCT workspace_id, workspace_id, 'active', ?, ? FROM projects
		  WHERE workspace_id IS NOT NULL AND workspace_id != ''`, []any{now, now}},
		{`INSERT OR IGNORE INTO workspaces(workspace_id, name, status, created_at, updated_at)
		  SELECT DISTINCT workspace_id, workspace_id, 'active', ?, ? FROM skill_installations
		  WHERE workspace_id IS NOT NULL AND workspace_id != ''`, []any{now, now}},
		{`INSERT OR IGNORE INTO workspace_memberships(
			workspace_id, user_id, role, status, created_at, updated_at)
		  SELECT workspace_id, ?, 'owner', 'active', ?, ? FROM workspaces
		  WHERE status = 'active'`, []any{identity.DefaultUserID, now, now}},
		{`INSERT OR IGNORE INTO workspace_quotas(workspace_id, updated_at)
		  SELECT workspace_id, ? FROM workspaces`, []any{now}},
		{`UPDATE projects SET owner_user_id = ?
		  WHERE owner_user_id IS NULL OR owner_user_id = ''`, []any{identity.DefaultUserID}},
		{`UPDATE agent_turns SET user_id = ?
		  WHERE user_id IS NULL OR user_id = ''`, []any{identity.DefaultUserID}},
		{`UPDATE skill_installations SET created_by = ?
		  WHERE created_by = 'shared_internal_user' OR created_by = ''`, []any{identity.DefaultUserID}},
		{`UPDATE skill_versions SET installed_by = ?
		  WHERE installed_by = 'shared_internal_user' OR installed_by = ''`, []any{identity.DefaultUserID}},
		{`UPDATE skill_install_attempts SET created_by = ?
		  WHERE created_by = 'shared_internal_user' OR created_by = ''`, []any{identity.DefaultUserID}},
		{`UPDATE skill_installation_events SET actor_ref = ?
		  WHERE actor_ref = 'shared_internal_user' OR actor_ref = ''`, []any{identity.DefaultUserID}},
		{`CREATE INDEX IF NOT EXISTS idx_projects_workspace
		  ON projects(workspace_id, deleted_at, updated_at DESC, project_id)`, nil},
		{`CREATE INDEX IF NOT EXISTS idx_projects_owner
		  ON projects(owner_user_id, deleted_at, updated_at DESC, project_id)`, nil},
		{`CREATE INDEX IF NOT EXISTS idx_agent_turns_workspace_user
		  ON agent_turns(workspace_id, user_id, created_at DESC, agent_turn_id)`, nil},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement.query, statement.args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func migrateAgentTurnObservability(db *sql.DB) error {
	columns := []struct {
		name       string
		definition string
	}{
		{name: "provider_id", definition: "TEXT"},
		{name: "model_id", definition: "TEXT"},
		{name: "release_id", definition: "TEXT"},
		{name: "trace_refs_json", definition: "TEXT NOT NULL DEFAULT '[]'"},
		{name: "response_ids_json", definition: "TEXT NOT NULL DEFAULT '[]'"},
		{name: "request_ids_json", definition: "TEXT NOT NULL DEFAULT '[]'"},
		{name: "usage_json", definition: "TEXT NOT NULL DEFAULT '{}'"},
		{name: "latency_json", definition: "TEXT NOT NULL DEFAULT '{}'"},
		{name: "failure_stage", definition: "TEXT"},
		{name: "cancel_reason", definition: "TEXT"},
		{name: "skill_invocation_id", definition: "TEXT"},
		{name: "agent_task_id", definition: "TEXT"},
		{name: "run_id", definition: "TEXT"},
		{name: "agent_tool_call_ids_json", definition: "TEXT NOT NULL DEFAULT '[]'"},
	}
	for _, column := range columns {
		present, err := tableHasColumn(db, "agent_turns", column.name)
		if err != nil {
			return err
		}
		if present {
			continue
		}
		if _, err := db.Exec(fmt.Sprintf(
			"ALTER TABLE agent_turns ADD COLUMN %s %s", column.name, column.definition,
		)); err != nil {
			return err
		}
	}
	return nil
}

func migrateAgentTurnApprovalCheckpoints(db *sql.DB) error {
	var tableSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'agent_turns'`).Scan(&tableSQL); err != nil {
		return err
	}
	if !strings.Contains(tableSQL, "'waiting_approval'") {
		if err := rebuildAgentTurnsForApprovalCheckpoints(db); err != nil {
			return err
		}
	}
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_turn_run_states (
			agent_turn_id TEXT PRIMARY KEY REFERENCES agent_turns(agent_turn_id) ON DELETE CASCADE,
			schema_version TEXT NOT NULL,
			state_json TEXT NOT NULL,
			state_hash TEXT NOT NULL,
			pending_sdk_tool_call_ids_json TEXT NOT NULL,
			checkpoint_version INTEGER NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`)
	return err
}

func rebuildAgentTurnsForApprovalCheckpoints(db *sql.DB) error {
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
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE agent_turns_v32 (
			agent_turn_id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			user_id TEXT NOT NULL DEFAULT 'user_local_default',
			project_id TEXT NOT NULL REFERENCES projects(project_id),
			conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
			idempotency_key TEXT NOT NULL,
			request_hash TEXT NOT NULL,
			request_json TEXT NOT NULL,
			status TEXT NOT NULL CHECK(status IN (
				'accepted','running','waiting_approval','cancel_requested','committing',
				'committed','failed','cancelled'
			)),
			error_code TEXT,
			error_message TEXT,
			exchange_json TEXT,
			user_message_id TEXT REFERENCES messages(message_id),
			agent_message_id TEXT REFERENCES messages(message_id),
			terminal_event_id TEXT UNIQUE,
			created_at TEXT NOT NULL,
			started_at TEXT,
			completed_at TEXT,
			provider_id TEXT,
			model_id TEXT,
			release_id TEXT,
			trace_refs_json TEXT NOT NULL DEFAULT '[]',
			response_ids_json TEXT NOT NULL DEFAULT '[]',
			request_ids_json TEXT NOT NULL DEFAULT '[]',
			usage_json TEXT NOT NULL DEFAULT '{}',
			latency_json TEXT NOT NULL DEFAULT '{}',
			failure_stage TEXT,
			cancel_reason TEXT,
			skill_invocation_id TEXT,
			agent_task_id TEXT,
			run_id TEXT,
			agent_tool_call_ids_json TEXT NOT NULL DEFAULT '[]',
			updated_at TEXT NOT NULL,
			UNIQUE(project_id, idempotency_key)
		);
		INSERT INTO agent_turns_v32 SELECT * FROM agent_turns;
		DROP TABLE agent_turns;
		ALTER TABLE agent_turns_v32 RENAME TO agent_turns;
		CREATE INDEX idx_agent_turns_project
		ON agent_turns(project_id, created_at DESC, agent_turn_id);
		CREATE INDEX idx_agent_turns_conversation_status
		ON agent_turns(conversation_id, status, created_at, agent_turn_id);
		CREATE INDEX idx_agent_turns_workspace_user
		ON agent_turns(workspace_id, user_id, created_at DESC, agent_turn_id);
	`); err != nil {
		return err
	}
	return tx.Commit()
}

func migrateStandaloneArtifactOrigins(db *sql.DB) error {
	present, err := tableHasColumn(db, "artifacts", "origin_type")
	if err != nil {
		return err
	}
	if present {
		_, err = db.Exec(`
			CREATE UNIQUE INDEX IF NOT EXISTS idx_artifacts_standalone_origin
			ON artifacts(project_id, origin_type, origin_id, artifact_type, scope_key)
			WHERE run_id IS NULL`)
		return err
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
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE artifacts_v26 (
			artifact_id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(project_id),
			run_id TEXT REFERENCES runs(run_id),
			step_run_id TEXT REFERENCES step_runs(step_run_id),
			origin_type TEXT NOT NULL DEFAULT 'run',
			origin_id TEXT NOT NULL DEFAULT '',
			capability_id TEXT NOT NULL,
			artifact_type TEXT NOT NULL,
			scope_key TEXT NOT NULL,
			current_version_id TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			CHECK (
				(run_id IS NOT NULL AND step_run_id IS NOT NULL AND origin_type = 'run') OR
				(run_id IS NULL AND step_run_id IS NULL AND origin_type IN ('agent_turn','invocation','agent_task') AND origin_id <> '')
			),
			UNIQUE(run_id, artifact_type, scope_key)
		);
		INSERT INTO artifacts_v26(
			artifact_id, project_id, run_id, step_run_id, origin_type, origin_id,
			capability_id, artifact_type, scope_key, current_version_id, created_at, updated_at
		)
		SELECT artifact_id, project_id,
			CASE WHEN capability_id = 'agent_shell' THEN NULL ELSE run_id END,
			CASE WHEN capability_id = 'agent_shell' THEN NULL ELSE step_run_id END,
			CASE WHEN capability_id = 'agent_shell' THEN 'agent_turn' ELSE 'run' END,
			CASE WHEN capability_id = 'agent_shell' THEN 'legacy:' || artifact_id ELSE run_id END,
			capability_id, artifact_type, scope_key, current_version_id, created_at, updated_at
		FROM artifacts;
		DROP TABLE artifacts;
		ALTER TABLE artifacts_v26 RENAME TO artifacts;
		CREATE INDEX idx_artifacts_project ON artifacts(project_id, updated_at DESC, artifact_id);
		CREATE UNIQUE INDEX idx_artifacts_standalone_origin
		ON artifacts(project_id, origin_type, origin_id, artifact_type, scope_key)
		WHERE run_id IS NULL;
	`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE artifact_dependencies_v26 (
			dependency_id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(project_id),
			run_id TEXT REFERENCES runs(run_id),
			origin_type TEXT NOT NULL DEFAULT 'run',
			origin_id TEXT NOT NULL DEFAULT '',
			downstream_artifact_version_id TEXT NOT NULL REFERENCES artifact_versions(artifact_version_id),
			upstream_kind TEXT NOT NULL,
			upstream_ref_id TEXT NOT NULL,
			relation TEXT NOT NULL,
			upstream_scope_json TEXT NOT NULL,
			upstream_scope_hash TEXT NOT NULL,
			downstream_scope_json TEXT NOT NULL,
			downstream_scope_hash TEXT NOT NULL,
			impact_policy_id TEXT NOT NULL,
			created_at TEXT NOT NULL,
			UNIQUE(
				downstream_artifact_version_id, upstream_kind, upstream_ref_id, relation,
				upstream_scope_hash, downstream_scope_hash
			)
		);
		INSERT INTO artifact_dependencies_v26(
			dependency_id, project_id, run_id, origin_type, origin_id,
			downstream_artifact_version_id, upstream_kind, upstream_ref_id, relation,
			upstream_scope_json, upstream_scope_hash, downstream_scope_json,
			downstream_scope_hash, impact_policy_id, created_at
		)
		SELECT d.dependency_id, d.project_id, a.run_id, a.origin_type, a.origin_id,
			d.downstream_artifact_version_id, d.upstream_kind, d.upstream_ref_id, d.relation,
			d.upstream_scope_json, d.upstream_scope_hash, d.downstream_scope_json,
			d.downstream_scope_hash, d.impact_policy_id, d.created_at
		FROM artifact_dependencies d
		JOIN artifact_versions av ON av.artifact_version_id = d.downstream_artifact_version_id
		JOIN artifacts a ON a.artifact_id = av.artifact_id;
		DROP TABLE artifact_dependencies;
		ALTER TABLE artifact_dependencies_v26 RENAME TO artifact_dependencies;
		CREATE INDEX idx_artifact_dependencies_upstream
		ON artifact_dependencies(upstream_kind, upstream_ref_id);
		CREATE INDEX idx_artifact_dependencies_downstream
		ON artifact_dependencies(downstream_artifact_version_id);
		CREATE INDEX idx_artifact_dependencies_project_relation
		ON artifact_dependencies(project_id, relation);
	`); err != nil {
		return err
	}
	return tx.Commit()
}

func ensureMessageRoutingInvocationColumn(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(message_routing_contexts)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if name == "invocation_id" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(`ALTER TABLE message_routing_contexts ADD COLUMN invocation_id TEXT`)
	return err
}

func ensureExecutionFailureProviderOutputColumn(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(execution_failure_details)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if name == "provider_output" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(`ALTER TABLE execution_failure_details ADD COLUMN provider_output TEXT`)
	return err
}

func backfillMessageRoutingContexts(db *sql.DB) error {
	_, err := db.Exec(`
		INSERT OR IGNORE INTO message_routing_contexts(message_id, scope, run_id, capability_id, artifact_id)
		SELECT ad.user_message_id, 'run', pa.consumed_run_id, pa.capability_id, NULL
		FROM proposed_actions pa
		JOIN agent_decisions ad ON ad.agent_decision_id = pa.agent_decision_id
		WHERE pa.consumed_run_id IS NOT NULL;
		INSERT OR IGNORE INTO message_routing_contexts(message_id, scope, run_id, capability_id, artifact_id)
		SELECT ad.agent_message_id, 'run', pa.consumed_run_id, pa.capability_id, NULL
		FROM proposed_actions pa
		JOIN agent_decisions ad ON ad.agent_decision_id = pa.agent_decision_id
		WHERE pa.consumed_run_id IS NOT NULL;
		INSERT OR IGNORE INTO message_routing_contexts(message_id, scope, run_id, capability_id, artifact_id)
		SELECT mc.message_id, 'artifact', a.run_id, a.capability_id, a.artifact_id
		FROM message_contexts mc
		JOIN artifacts a ON a.artifact_id = json_extract(mc.client_context_json, '$.current_artifact_id')
		LEFT JOIN agent_decisions ad ON ad.user_message_id = mc.message_id
		WHERE json_extract(mc.client_context_json, '$.current_artifact_id') IS NOT NULL
			AND ((mc.selection_snapshot_json IS NOT NULL AND mc.selection_snapshot_json <> 'null')
				OR ad.intent IN ('inspect', 'revise'));
		INSERT OR IGNORE INTO message_routing_contexts(message_id, scope, run_id, capability_id, artifact_id)
		SELECT mc.message_id, 'run',
			json_extract(mc.client_context_json, '$.current_run_id'),
			json_extract(mc.client_context_json, '$.current_capability_id'), NULL
		FROM message_contexts mc
		LEFT JOIN agent_decisions ad ON ad.user_message_id = mc.message_id
		WHERE json_extract(mc.client_context_json, '$.current_run_id') IS NOT NULL
			AND ad.intent IN ('inspect', 'revise');
		INSERT OR IGNORE INTO message_routing_contexts(message_id, scope, run_id, capability_id, artifact_id)
		SELECT ad.agent_message_id, user_route.scope, user_route.run_id,
			user_route.capability_id, user_route.artifact_id
		FROM agent_decisions ad
		JOIN message_routing_contexts user_route ON user_route.message_id = ad.user_message_id;
		INSERT OR IGNORE INTO message_routing_contexts(message_id, scope, run_id, capability_id, artifact_id)
		SELECT m.message_id, 'project', NULL, NULL, NULL FROM messages m;
	`)
	return err
}

func migrateRunConfigSnapshots(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`
		SELECT r.run_id, r.capability_id, r.run_kind, r.config_snapshot_json, r.created_at
		FROM runs r
		WHERE NOT EXISTS (
			SELECT 1 FROM run_config_snapshots rcs WHERE rcs.run_id = r.run_id
		)
		ORDER BY r.created_at, r.run_id`)
	if err != nil {
		return err
	}
	type legacyConfig struct {
		runID        string
		capabilityID string
		runKind      string
		payload      json.RawMessage
		createdAt    string
	}
	var pending []legacyConfig
	for rows.Next() {
		var item legacyConfig
		var payload string
		if err := rows.Scan(&item.runID, &item.capabilityID, &item.runKind, &payload, &item.createdAt); err != nil {
			rows.Close()
			return err
		}
		item.payload = json.RawMessage(payload)
		pending = append(pending, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, item := range pending {
		configRef, err := configReference(item.payload)
		if err != nil && item.capabilityID == "agent_shell" && item.runKind == "artifact_session" {
			item.payload = json.RawMessage(`{"config_ref":"agent_shell","payload":{}}`)
			configRef, err = "agent_shell", nil
			if _, updateErr := tx.Exec(`UPDATE runs SET config_snapshot_json = ? WHERE run_id = ?`, string(item.payload), item.runID); updateErr != nil {
				return updateErr
			}
		}
		if err != nil {
			return fmt.Errorf("run %s has invalid legacy config: %w", item.runID, err)
		}
		if _, err := tx.Exec(`
			INSERT INTO run_config_snapshots(
				config_snapshot_id, run_id, config_ref, version, status,
				payload_json, snapshot_hash, created_at, sealed_at
			) VALUES(?, ?, ?, 1, 'sealed', ?, ?, ?, ?)`,
			"legacy_cfg_"+item.runID,
			item.runID,
			configRef,
			string(item.payload),
			sha256Hex(item.payload),
			item.createdAt,
			item.createdAt,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func completeRuntimeMigration(db *sql.DB, dataRoot, migrationID, backupRef string, previousVersion int) (err error) {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	reportPath := ""
	defer func() {
		if err != nil && reportPath != "" {
			if cleanupErr := os.Remove(reportPath); cleanupErr != nil && !errors.Is(cleanupErr, os.ErrNotExist) {
				err = errors.Join(err, fmt.Errorf("remove uncommitted migration report: %w", cleanupErr))
			}
		}
	}()
	if migrationID != "" {
		now := formatTime(time.Now().UTC())
		if _, err := tx.Exec(`
			INSERT INTO migration_history(
				migration_id, from_version, to_version, backup_ref, status, started_at, completed_at
			) VALUES(?, ?, ?, ?, 'completed', ?, ?)`,
			migrationID, previousVersion, schemaVersion, backupRef, now, now); err != nil {
			return fmt.Errorf("record migration history: %w", err)
		}
		reportDir := filepath.Join(dataRoot, "migration-reports")
		if err := os.MkdirAll(reportDir, 0o755); err != nil {
			return fmt.Errorf("create migration report directory: %w", err)
		}
		report, err := json.MarshalIndent(map[string]any{
			"migration_id": migrationID, "from_version": previousVersion,
			"to_version": schemaVersion, "backup_ref": backupRef,
			"status": "completed", "completed_at": now,
		}, "", "  ")
		if err != nil {
			return err
		}
		reportPath = filepath.Join(reportDir, migrationID+".json")
		if err := os.WriteFile(reportPath, report, 0o600); err != nil {
			return fmt.Errorf("write migration report: %w", err)
		}
	}
	// A component backfill must not advertise that every migration succeeded.
	// Keep the schema marker and its success receipt in the same final commit.
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version=%d", schemaVersion)); err != nil {
		return fmt.Errorf("record schema version: %w", err)
	}
	return tx.Commit()
}

func (s *Store) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.closeContext(ctx)
}

func (s *Store) closeContext(ctx context.Context) error {
	err := s.db.Close()
	// database/sql may return while a cancelled transaction is still rolling
	// back. Wait for its driver connection to close before releasing DB files.
	if s.db.Stats().OpenConnections == 0 {
		return err
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for s.db.Stats().OpenConnections != 0 {
		select {
		case <-ctx.Done():
			return errors.Join(err, fmt.Errorf("wait for database connections to close: %w", ctx.Err()))
		case <-ticker.C:
		}
	}
	return err
}

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func randomID(prefix string) string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(fmt.Sprintf("generate id: %v", err))
	}
	return prefix + "_" + hex.EncodeToString(bytes[:])
}
