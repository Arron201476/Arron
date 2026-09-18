export type Principal = {
  kind: "user" | "service" | "system";
  user_id: string;
  display_name: string;
  workspace_id: string;
  workspace_name: string;
  role: "viewer" | "editor" | "admin" | "owner";
  auth_method: string;
};

export type AgentMemoryDocument = {
  project_id: string; user_id: string; version: number; files: Record<string, string>;
  content_hash: string; enabled: boolean; forgotten: boolean; updated_at?: string; request_id?: string;
};
export type AgentMemoryUpdate = {
  project_id: string; expected_version: number; files: Record<string, string>;
  enabled: boolean; forget: boolean; request_id: string;
};

export type Project = {
  project_id: string;
  workspace_id: string;
  owner_user_id: string;
  title: string;
  version: number;
  status: string;
  primary_conversation_id: string;
  active_write_run_id: string | null;
  current_capability_id: string | null;
  latest_capability_id: string | null;
  latest_run_status: string | null;
  current_focus_artifact_version_id: string | null;
  updated_at: string;
};

export type ProjectSnapshot = {
  project: Project;
  goal?: unknown | null;
  messages: Message[];
  artifacts: Artifact[];
  approvals: Approval[];
  capabilities: Capability[];
  artifact_presentations: ArtifactPresentation[];
  script_candidates: ScriptCandidate[];
  final_selection: FinalSelection | null;
  active_run: RunSnapshot | null;
  latest_run?: RunSnapshot | null;
  pending_proposed_actions: ProposedAction[];
  proposed_actions?: ProposedAction[];
  activities: ProjectActivity[];
  revision_requests: RevisionRequest[];
  target_resolutions: TargetResolution[];
  agent_turns: AgentTurn[];
  agent_tasks: AgentTask[];
  agent_tool_calls: AgentToolCall[];
};

export type ArtifactPresentation = {
  artifact_type: string;
  label: string;
  description: string;
  renderer: "document" | "episode_plan" | "script" | "script_collection" | "video_script" | "table" | "media" | "form";
  editable: boolean;
  preferred_fields: string[];
  navigation: {
    order: number;
    group_mode: "single" | "episode_directory";
    visibility: "user" | "internal";
  };
  available_actions: string[];
  collection_member_type?: string;
};

export type ProjectActivity = {
  event_id: string;
  event_type: string;
  run_id: string | null;
  capability_id: string | null;
  step_id: string | null;
  occurred_at: string;
};

export type Message = {
  message_id: string;
  role: "user" | "assistant" | string;
  content: string;
  created_at: string;
  message_context?: MessageContext | null;
  submission_id?: string;
  delivery_status?: "sending" | "unconfirmed" | "queued" | "running" | "waiting_approval" | "pausing" | "paused" | "committing" | "cancelling" | "failed" | "cancelled";
  delivery_error?: string;
  agent_turn_id?: string;
};

export type AgentTurnStatus = "accepted" | "running" | "waiting_approval" | "pausing" | "paused" | "cancel_requested" | "committing" | "committed" | "failed" | "cancelled";

export type AgentTurn = {
  agent_turn_id: string;
  submission_id?: string;
  user_id?: string;
  workspace_id: string;
  project_id: string;
  conversation_id: string;
  status: AgentTurnStatus;
  additional_inputs?: AgentTurnInput[];
  request: {
    content: string;
    capability_ref?: MessageContext["capability_ref"];
    attachment_refs?: AttachmentRef[];
    selection_snapshot?: MessageContext["selection_snapshot"];
    client_context?: MessageContext["client_context"];
  };
  error_code?: string;
  error_message?: string;
  user_message_id?: string;
  agent_message_id?: string;
  created_at: string;
  started_at?: string;
  completed_at?: string;
  updated_at: string;
  observation?: AgentTurnObservation;
};

export type AgentTurnCancelResult = {
  turn: AgentTurn;
  accepted: boolean;
};

export type AdditionalInputStatus = "received" | "included" | "withdrawn" | "superseded";
export type InputChangeScope = { projectID: string; mode: "conversation" | "background_task" | "stateful_workflow" };
export type InputChangeReceipt = { mode: InputChangeScope["mode"]; execution_id: string; input_id: string; status: "withdrawn" | "superseded"; replacement_input_id?: string };
export type InputAttachment = { asset_id: string; asset_snapshot_id: string; kind: string; name: string; mime_type: string; checksum: string; size_bytes: number; text_hash?: string };
export type AgentTurnInput = { input_id: string; agent_turn_id: string; user_id: string; sequence: number; content: string; status: AdditionalInputStatus; created_at: string; included_at?: string; can_modify?: boolean; replacement_input_id?: string; attachments?: InputAttachment[] };

export type AgentTurnObservation = {
  model_id?: string;
  provider_id?: string;
  response_ids?: string[];
  usage?: Record<string, number>;
  failure_stage?: string;
};

export type AgentToolDescriptor = {
  id: string;
  kind: string;
  name: string;
  description: string;
  enabled: boolean;
  access: string;
  approval: string;
  transport?: string;
  server_id?: string;
};

export type AgentToolApproval = {
  agent_tool_approval_id: string;
  agent_tool_call_id: string;
  workspace_id: string;
  project_id: string;
  conversation_id: string;
  status: "pending" | "approved" | "rejected" | "cancelled" | string;
  version: number;
  title: string;
  reason: string;
  options: string[];
  subject_snapshot_hash: string;
  requested_at: string;
  resolved_at?: string | null;
  resolution?: unknown;
  actor_ref?: string | null;
};

export type AgentToolExecution = {
  mode: "conversation" | "background_task" | "stateful_workflow";
  agent_turn_id?: string;
  agent_task_id?: string;
  attempt_id?: string;
  attempt_no?: number;
  run_id?: string;
  step_run_id?: string;
  step_id?: string;
  task_item_id?: string;
  item_key?: string;
  capability_id?: string;
};

export type AgentToolCall = {
  parent_tool_call_id?: string;
  program_call_id?: string;
  agent_tool_call_id: string;
  workspace_id: string;
  project_id: string;
  conversation_id: string;
  skill_invocation_id?: string | null;
  agent_turn_id?: string | null;
  execution?: AgentToolExecution | null;
  sdk_tool_call_id: string;
  tool_id: string;
  tool_kind: string;
  server_id?: string | null;
  tool_name: string;
  access_mode: "read" | "write" | "sensitive" | string;
  approval_policy: string;
  approval_status: string;
  status: string;
  arguments_summary: unknown;
  arguments_hash: string;
  result_summary?: unknown;
  citations?: Array<{ type: "url_citation" | "file_citation"; title?: string; url?: string; file_id?: string; filename?: string }>;
  omitted_citations?: number;
  error_code?: string | null;
  error_message?: string | null;
  requested_at: string;
  started_at?: string | null;
  completed_at?: string | null;
  updated_at: string;
  approval?: AgentToolApproval | null;
};

export type AgentSubtaskResult = {
  schema_version: "agent_subtask.v1";
  agent_tool_call_id: string;
  project_id: string;
  text: string;
  read_call_ids: string[];
  inspected_artifacts: Array<{ artifact_id: string; artifact_version_id: string }>;
};

export type AgentToolOutcomeReview = {
  agent_tool_call_id: string;
  project_id: string;
  sdk_tool_call_id: string;
  tool_id: string;
  arguments_hash: string;
  configuration_hash: string;
  user_id: string;
  execution_mode: string;
  execution_id: string;
  execution_status: string;
  subject_snapshot_hash: string;
  can_resolve: boolean;
  resolution?: { request_id: string; outcome: "applied" | "not_applied"; evidence: string; actor_user_id: string; created_at: string };
};

export type AgentToolOutcomeCommand = {
  subject_snapshot_hash: string;
  request_id: string;
  outcome: "applied" | "not_applied";
  evidence: string;
};

export type AgentTargetEntity = {
  episode_id?: string;
  scene_id?: string;
  line_id?: string;
  block_type?: string;
  entity_id?: string;
};

export type AgentTextRange = {
  start: number;
  end: number;
  selected_text: string;
  before_context: string;
  after_context: string;
};

export type AgentTargetSelection = {
  schema_version: "1.0.0";
  artifact_id: string;
  artifact_type: string;
  target_scope: "artifact" | "field" | "entity" | "selection";
  scope_key?: string;
  field_path?: string;
  entity?: AgentTargetEntity;
  text_range?: AgentTextRange;
  display: {
    artifact_label: string;
    location_label?: string;
    selected_text_summary?: string;
  };
};

export type AgentViewContext = {
  project_id: string;
  run_id: string | null;
  capability_id: string | null;
  artifact_id: string | null;
  artifact_version_id: string | null;
  artifact_type: string | null;
  scope_key: string | null;
  artifact_label: string | null;
  asset_set_version_id?: string | null;
};

export type AgentComposerContext = {
  view: AgentViewContext;
  selection: AgentTargetSelection | null;
};

export type MessageContext = {
  capability_ref?: { capability_id: string; version: string; selection_mode?: string } | null;
  attachment_refs: AttachmentRef[];
  selection_snapshot?: {
    artifact_version_id: string;
    snapshot_hash: string;
    selection: AgentTargetSelection;
  } | null;
  client_context?: {
    current_artifact_id?: string;
    current_artifact_version_id?: string;
    current_run_id?: string;
    current_capability_id?: string;
    viewed_run_id?: string;
    viewed_capability_id?: string;
    current_scope_key?: string;
    current_asset_set_version_id?: string;
  };
  routing_context?: {
    scope: "project" | "capability" | "run" | "artifact" | string;
    invocation_id?: string;
    run_id?: string;
    capability_id?: string;
    artifact_id?: string;
  };
};

export type Artifact = {
  artifact_id: string;
  project_id?: string;
  run_id?: string;
  step_run_id?: string;
  artifact_type: string;
  scope_key?: string;
  title?: string;
  status?: string;
  current_version_id: string | null;
  updated_at: string;
};

export type ArtifactDownload = { format: string; filename: string; content_type: string; download_url: string };
export type ArtifactDelivery = { project_id: string; artifact_id: string; artifact_version_id: string; version: number; status: string; title: string; downloads: ArtifactDownload[]; warnings: string[] };

export type ArtifactVersion = {
  artifact_version_id: string;
  artifact_id: string;
  version: number;
  status: string;
  payload: unknown;
  creation_reason: string;
  created_at: string;
};

export type VersionResult = {
  artifact_version: ArtifactVersion;
  approval: Approval;
	impact_review?: unknown;
	propagation?: {
		impact_review: unknown;
		regeneration_plan?: unknown;
		run_snapshot: RunSnapshot;
	};
  handoff_refresh_required?: boolean;
  pending_refresh_scopes?: string[];
  pending_refresh_version_ids?: string[];
};

export type ScriptCandidate = {
  candidate_id: string;
  project_id: string;
  source_run_id: string;
  source_capability_id: string;
  scripts_artifact_version_id: string;
  status: string;
  label: string;
  updated_at: string;
};

export type FinalSelection = {
  final_selection_id: string;
  project_id?: string;
  approval_request_id?: string;
  replaced_selection_id?: string | null;
  candidate_id: string;
  selection_no: number;
  status: string;
};

export type ScriptExport = {
  export_id: string;
  project_id: string;
  candidate_id: string;
  artifact_version_id: string;
  format: "txt" | "docx";
  status: string;
  filename: string;
  size_bytes: number;
  checksum: string;
  content_type: string;
  expires_at: string;
};

export type FinalSelectionPreviewResult = {
  preview: { final_selection_preview_id: string; project_id: string; candidate_id: string; approval_request_id: string; current_selection_id: string | null; expected_current_selection_id: string | null; proposed_selection_no: number; preview_hash: string; status: string };
  approval: Approval;
};

export type FinalSelectionResult = { final_selection: FinalSelection; candidate: ScriptCandidate; approval: Approval };

export type UploadItem = {
  upload_item_id: string;
  client_item_key: string;
  original_filename: string;
  status: string;
};

export type UploadSession = { upload_session_id: string; items: UploadItem[] };

export type AgentToolConfigurationOption = {
  id: string;
  kind: "mcp" | "hosted";
  name: string;
  description: string;
  transport?: string;
  enabled: boolean;
  configurable: boolean;
  user_message?: string;
};

export type AgentToolConfiguration = {
  workspace_id: string;
  version: number;
  options: AgentToolConfigurationOption[];
};

export type MCPConnectionScope = "user" | "workspace";
export type AgentInstructionScope = "workspace" | "user" | "project";
export type AgentInstructionDocument = {
  scope: AgentInstructionScope; scope_ref: string; version: number; content: string; content_hash: string;
  enabled: boolean; updated_by?: string; updated_at?: string; can_edit: boolean;
};
export type AgentInstructionView = {
  workspace_id: string; project_id?: string; documents: AgentInstructionDocument[]; applies_to: "new_executions";
};
export type AgentInstructionUpdate = {
  project_id?: string; scope: AgentInstructionScope; expected_version: number; content: string; enabled: boolean; request_id: string;
};
export type AgentInstructionProposal = {
  agent_tool_call_id: string; project_id: string; user_id: string; arguments_hash: string;
  arguments: Omit<AgentInstructionUpdate, "project_id" | "request_id">;
  current: AgentInstructionDocument; can_approve: boolean; can_reject: boolean;
};
export type AgentMemoryProposal = {
  agent_tool_call_id: string; project_id: string; user_id: string; arguments_hash: string;
  approval_id: string; approval_version: number; subject_snapshot_hash: string;
  expected_version: number; current: AgentMemoryDocument; files: Record<string, string>;
  available: boolean; can_approve: boolean; can_reject: boolean;
};
export type AgentMemoryToolProposal = {
  agent_tool_call_id: string; generation_id: string; project_id: string; user_id: string;
  arguments_hash: string; arguments?: Record<string, unknown>;
  approval_id: string; approval_version: number; subject_snapshot_hash: string;
  can_approve: boolean; can_reject: boolean;
};
export type MCPConnectionState = { scope: MCPConnectionScope; version: number; status: "missing" | "active" | "deleted"; updated_at?: string };
export type MCPConnectionOption = {
  server_id: string; display_name: string; fields: string[]; enabled: boolean;
  effective_scope: MCPConnectionScope | "none"; personal: MCPConnectionState; workspace: MCPConnectionState;
};
export type MCPConnectionInventory = {
  storage_available: boolean; can_manage_personal: boolean; can_manage_workspace: boolean; items: MCPConnectionOption[];
};
export type MCPConnectionUpdate = {
  server_id: string; scope: MCPConnectionScope; expected_version: number; request_id: string;
  values?: Record<string, string>; delete?: boolean;
};
export type Asset = {
  project_id?: string;
  source_type?: string;
  metadata?: { agent_tool_call_id?: string; sdk_tool_call_id?: string };
  size_bytes?: number;
  expires_at?: string | null;
  deleted_at?: string | null;
  asset_id: string;
  current_snapshot_id: string;
  kind: string;
  display_name: string;
  original_filename: string;
  status: string;
  parse_status: string;
};
export type AssetResult = {
  asset: { asset_id: string; display_name: string; kind: string };
  asset_snapshot: { asset_snapshot_id: string };
  extracted_assets?: AssetResult[];
  ignored_entries?: string[];
};
export type AttachmentRef = {
  asset_id: string;
  asset_snapshot_id: string;
  display_name: string;
  kind?: string;
  hidden?: boolean;
  container_asset_id?: string;
  ignored_entries?: string[];
};

export type AvailableAction = { action_id: string; target_type: string; target_id: string; enabled: boolean; disabled_reason: string | null };
export type StepRun = { step_run_id: string; run_id: string; step_id: string; status: string; attempt_count: number; started_at?: string | null; ended_at?: string | null };
export type ExecutionFailureDetail = {
  error_code: string;
  stage: string;
  summary: string;
  technical_detail?: string | null;
  provider_output?: string | null;
  provider_status_code?: number | null;
  provider_request_id?: string | null;
  transport_category?: string | null;
  retryable: boolean;
};
export type TaskItem = { task_item_id: string; step_run_id: string; item_key: string; item_order: number; status: string; program_status?: string; attempt_count: number; current_attempt_id?: string | null; input_snapshot?: Record<string, unknown>; failure: string | null; failure_detail?: ExecutionFailureDetail | null; output_repair?: { status: "queued" | "claimed" | "closed" | "terminal"; rejection_no: number; error_code: string } };
export type RunSnapshot = {
  run: {
    run_id: string;
    project_id?: string;
    conversation_id?: string;
    capability_version?: string;
    capability_id: string;
    status: string;
    current_step_run_id: string | null;
		episode_execution_mode?: "continuous" | "review_each";
    started_at?: string | null;
    config_snapshot?: {
      config_ref?: string;
      payload?: {
        target_episode_count?: number;
        episode_duration_minutes?: number;
        [key: string]: unknown;
      };
    };
  };
  steps?: StepRun[];
  task_items?: TaskItem[];
  available_actions: AvailableAction[];
  current_approval: Approval | null;
  pending_script_edit?: PendingScriptEdit;
};

export type PendingScriptEdit = {
  project_id: string;
  run_id: string;
  step_run_id: string;
  expected_script_version_ids: string[];
  pending_scopes: string[];
  can_complete: boolean;
  disabled_reason?: string;
};

export type ExecutionInput = { input_id: string; attempt_id: string; user_id: string; sequence: number; content: string; content_hash: string; status: AdditionalInputStatus; created_at: string; included_at?: string; can_modify?: boolean; replacement_input_id?: string; attachments?: InputAttachment[] };
export type ExecutionInputsView = { attempt_id: string; run_id: string; task_item_id: string; status: string; can_append: boolean; inputs: ExecutionInput[] };
export type ExecutionInputAttempt = { attempt_id: string; task_item_id: string; item_key: string; attempt_no: number; status: string; input_count: number; included_count: number; current: boolean };
export type ExecutionInputAttemptPage = { items: ExecutionInputAttempt[]; next_cursor: string };

export type AssetSetSnapshot = {
  asset_set: { asset_set_id: string; project_id: string; current_version_id: string; current_version: number; status: string };
  version: { asset_set_version_id: string; asset_set_id: string; version: number; member_count: number; status: string; completeness: { recognized_episode_count: number; unrecognized_count: number; duplicate_episode_numbers: number[]; missing_episode_numbers: number[]; failed_asset_ids: string[]; order_confirmed: boolean } };
  members: Array<{ asset_id: string; episode_order: number; episode_no: number | null; episode_label: string | null; filename_candidate: { raw: string }; included: boolean }>;
};

export type Approval = {
	project_id?: string;
	run_id?: string;
  approval_request_id: string;
  scope: string;
  status: string;
  version: number;
  title: string;
  reason: string;
  options: string[];
  subject_kind: string;
  subject_ref_id: string;
  subject_version: number;
  subject_snapshot_hash: string;
  requested_at: string;
};

export type ApprovalRevisionTarget = {
  artifact_id: string;
  artifact_version_id: string;
  artifact_type: string;
  scope_key: string;
  version: number;
  label: string;
};

export type ApprovalRevisionTargets = { approval: Approval; targets: ApprovalRevisionTarget[] };

export type AdaptationOption = {
	option_id: string;
	title: string;
	one_sentence_strategy: string;
	patterns_to_keep: string[];
	content_to_replace: string[];
	new_element_suggestions: string[];
	risks: string[];
	suitable_when: string;
};

export type AdaptationOptions = {
	options: AdaptationOption[];
	selection_instructions: string;
};

export type ContinuationOption = {
	option_id: string;
	title: string;
	genre_tag: string;
	summary: string;
	outline: string;
};

export type ContinuationOptions = {
	core_settings: Record<string, unknown>;
	options: ContinuationOption[];
	selection_instructions: string;
};

export type AdaptationResolutionPayload = {
	adaptation_options_artifact_version_id: string;
	selection: {
		selected_option_ids: string[];
		combined_methods: string[];
		custom_changes: string[];
	};
	creation_config: {
		target_episode_count: number;
		episode_duration_minutes: number;
		user_requirements: string[];
	};
};

export type QualityReview = {
  quality_review_id: string;
  project_id: string;
  run_id: string;
  step_run_id: string;
  input_snapshot_hash: string;
  status: string;
  scope: string;
  review_version: number;
  issue_counts: { blocker: number; high: number; medium: number; low: number };
  recommended_route: string | null;
  affected_episode_nos: number[];
  result: unknown;
};

export type Capability = {
  capability_id: string;
  label: string;
  version: string;
  description?: string;
  status?: string;
  kind?: "agent_skill" | "domain_workflow" | string;
  execution_mode?: "inline" | "background_task" | "stateful_workflow" | string;
  creates_run?: boolean;
  reason_code?: string;
  user_message?: string;
  accepted_asset_kinds?: string[];
  default_config_ref?: string;
  config_options?: string[];
  input_binding?: InputBinding;
  ui_entry?: {
    icon_key: string;
    menu_order: number;
    entry_view_key: string;
    config_view_key?: string;
    default_prompt?: string;
  };
  entry_policy?: {
    explicit_invocation: boolean;
    auto_route: boolean;
    requires_user_confirmation: boolean;
    input_collection_modes: string[];
  };
  skill?: PublicSkill | null;
};

export type JSONSchema = {
  $schema?: string;
  $id?: string;
  title?: string;
  description?: string;
  type?: string | string[];
  default?: unknown;
  const?: unknown;
  enum?: unknown[];
  properties?: Record<string, JSONSchema>;
  required?: string[];
  items?: JSONSchema;
  additionalProperties?: boolean | JSONSchema;
  minimum?: number;
  maximum?: number;
  multipleOf?: number;
  minLength?: number;
  maxLength?: number;
  minItems?: number;
  maxItems?: number;
  pattern?: string;
  format?: string;
  allOf?: JSONSchema[];
  anyOf?: JSONSchema[];
  oneOf?: JSONSchema[];
  not?: JSONSchema;
  if?: JSONSchema;
  then?: JSONSchema;
  else?: JSONSchema;
};

export type CapabilityDefinition = Capability & {
  input_schema?: JSONSchema;
  config_schemas?: Record<string, JSONSchema>;
  commands?: string[];
  steps?: Array<{
    step_id: string;
    kind: string;
    artifact_types: string[];
    approval_type?: string;
    approval_scope?: string;
    result_kind?: string;
    visibility?: string;
  }>;
  completion?: Record<string, unknown>;
};

export type InputBinding = {
  source_type?: string;
  asset_role?: string;
  asset_set_purpose?: string;
};

export type SkillDependency = {
  type: string;
  value: string;
  description?: string;
  transport?: string;
  status?: string;
  reason_code?: string;
  user_message?: string;
};

export type PublicSkill = {
  name: string;
  scope: string;
  path: string;
  content_hash: string;
  allow_implicit_invocation: boolean;
  interface: {
    display_name?: string;
    short_description?: string;
    icon_small?: string;
    icon_large?: string;
    brand_color?: string;
    default_prompt?: string;
  };
  dependencies: SkillDependency[];
  scripts?: Array<{ id: string; path: string; runtime: string; description: string }>;
};

export type ScriptSandboxPolicy = {
  workspace_id: string;
  enabled: boolean;
  version: number;
  limits: {
    timeout_seconds: number;
    cpu_count: number;
    memory_bytes: number;
    process_count: number;
    disk_bytes: number;
    temp_bytes: number;
  };
  environment_allowlist: string[];
  sandbox: {
    available: boolean;
    adapter: string;
    engine?: string;
    runtimes: string[];
    reason_code?: string;
    user_message?: string;
  };
  updated_by?: string;
  updated_at?: string;
};

export type ComposerRegistryEntry = {
  capability_id: string;
  version: string;
  label: string;
  description: string;
  status: string;
  reason_code?: string;
  user_message?: string;
  execution_mode: string;
  creates_run: boolean;
  view_key: string;
  icon_key: string;
  menu_order: number;
  default_prompt?: string;
  accepted_asset_kinds: string[];
  input_binding?: InputBinding;
  entry_policy: {
    explicit_invocation: boolean;
    auto_route: boolean;
    requires_user_confirmation: boolean;
    input_collection_modes: string[];
  };
  config: {
    view_key: string;
    default_ref?: string;
    options: string[];
  };
  skill?: PublicSkill | null;
};

export type WorkspaceRegistries = {
  artifacts: Array<{ artifact_type: string; view_key: string; label: string; description: string; editable: boolean; preferred_fields: string[]; available_actions: string[]; collection_member_type?: string }>;
  navigation: Array<{ artifact_type: string; order: number; group_mode: string; visibility: string }>;
  tasks: Array<{ status: string; view_key: string }>;
  approvals: Array<{ registry_key: string; view_key: string; match: { scope?: string; option?: string; subject_kind?: string } }>;
  interactions: Array<{ registry_key: string; view_key: string; commands: string[] }>;
  composer: ComposerRegistryEntry[];
};

export type WorkspaceProjection = {
  contract_version: string;
  revision: string;
  registries: WorkspaceRegistries;
};

export type ProjectWorkspaceProjection = {
  contract_version: string;
  registry_revision: string;
  snapshot: ProjectSnapshot;
  registries: WorkspaceRegistries;
};

export type SkillDirectoryUpdate = {
  skill_installation_id: string;
  current_version_id: string;
  capability_id: string;
  version?: string;
  content_hash?: string;
  source_path?: string;
  source_scope?: "system" | "workspace" | "project" | "user";
  status: "available" | "current" | "version_conflict" | "not_found";
  user_message?: string;
};

export type SkillVersion = {
  skill_version_id: string;
  skill_installation_id: string;
  version: string;
  content_hash: string;
  execution_mode: string;
  source_type: string;
  source_name: string;
  manifest: PublicSkill;
  status: string;
  installed_by: string;
  created_at: string;
};

export type SkillInstallTarget = { scope: "user" | "project" | "workspace"; project_id?: string };

export type SkillInstallation = {
  skill_installation_id: string;
  workspace_id: string;
  scope: string;
  scope_ref: string;
  skill_name: string;
  capability_id: string;
  status: string;
  enabled: boolean;
  active_version_id?: string;
  created_by: string;
  created_at: string;
  updated_at: string;
  versions: SkillVersion[];
  events: Array<{ skill_installation_event_id: string; skill_installation_id: string; skill_version_id?: string; event_type: string; actor_ref: string; payload: Record<string, unknown>; created_at: string }>;
  registry_status: string;
  registry_reason_code?: string;
};

export type SkillLifecycleAction = "enable" | "disable" | "activate" | "uninstall";
export type SkillPackageAction = "install_zip" | "upgrade_zip" | "adopt_directory" | "update_directory";
export type SkillPackageResult = {
  receipt: Omit<SkillLifecycleResult["receipt"], "action"> & { action: SkillPackageAction; workspace_id: string; scope: string; scope_ref: string; skill_name: string; capability_id: string; version: string; content_hash: string; source_hash: string };
  installation: SkillInstallation;
};
export type SkillLifecycleResult = {
  receipt: { request_id: string; skill_installation_id: string; skill_installation_event_id: string; skill_version_id: string; action: SkillLifecycleAction; actor_ref: string };
  installation: SkillInstallation;
};

export type SkillInstallAttempt = {
  scope?: string;
  scope_ref?: string;
  skill_install_attempt_id: string;
  source_type: string;
  source_name: string;
  status: string;
  failure_code?: string;
  diagnostics: Array<{ code: string; message?: string }>;
  created_at: string;
  completed_at?: string;
};

export type TargetCandidate = {
  candidate_id: string;
  artifact_id: string;
  artifact_version_id: string;
  artifact_type: string;
  scope_key: string;
  field_path?: string;
  entity: Record<string, unknown>;
  display: { artifact_label?: string; location_label?: string };
  score: number;
};

export type TargetResolution = {
  target_resolution_id: string;
  project_id: string;
  conversation_id: string;
  request_message_id: string;
  status: "resolved" | "ambiguous" | "not_found" | string;
  source: string;
  artifact_id?: string;
  artifact_version_id?: string;
  artifact_type?: string;
  scope_key?: string;
  field_path?: string;
  display: { artifact_label?: string; location_label?: string };
  candidates: TargetCandidate[];
  created_at: string;
};

export type RevisionRequest = {
  source_approval_request_id?: string;
  revision_request_id: string;
  project_id: string;
  conversation_id: string;
  request_message_id: string;
  target_resolution_id: string;
  artifact_id?: string;
  base_artifact_version_id?: string;
  instruction: string;
  operation: string;
  status: string;
  execution_policy: string;
  version: number;
  proposal_payload?: Record<string, unknown>;
  proposal_summary?: string;
  failure_code?: string;
  created_at: string;
  updated_at: string;
};

export type RevisionAcceptResult = {
  revision_request: RevisionRequest;
  version_result: VersionResult;
};

export type ProposedAction = {
  proposed_action_id: string;
  project_id?: string;
  conversation_id?: string;
  version: number;
  snapshot_hash: string;
  status: string;
  action_type: "collect_run_configuration" | "start_run" | "start_background_task" | string;
  capability_ref: { capability_id: string; version: string };
  input: Record<string, unknown>;
  config: Record<string, unknown>;
  confirmation_message_id: string;
  consumed_run_id?: string | null;
  consumed_task_id?: string | null;
  created_at: string;
  updated_at: string;
};

export type AgentTaskInput = { input_id: string; agent_task_id: string; user_id: string; sequence: number; content: string; status: AdditionalInputStatus; created_at: string; included_at?: string; can_modify?: boolean; replacement_input_id?: string; attachments?: InputAttachment[] };

export type AgentTask = {
  user_id?: string;
  input_pause_requested?: boolean;
  additional_inputs?: AgentTaskInput[];
  retry_blocked_reason?: string;
  agent_task_id: string;
  workspace_id: string;
  project_id: string;
  conversation_id: string;
  skill_invocation_id: string;
  proposed_action_id?: string | null;
  capability_id: string;
  capability_version: string;
  status: "queued" | "running" | "waiting_approval" | "completed" | "failed" | "cancelled" | string;
  progress_current: number;
  progress_total: number;
  progress_message: string;
  input: Record<string, unknown>;
  config: Record<string, unknown>;
  result_artifact_id?: string | null;
  result_artifact_version_id?: string | null;
  result?: unknown;
  failure_code?: string | null;
  failure_message?: string | null;
  attempt_count: number;
  max_attempts: number;
  cancel_requested: boolean;
  created_at: string;
  queued_at: string;
  started_at?: string | null;
  completed_at?: string | null;
  updated_at: string;
};

export type ProjectDeletePreview = {
  snapshot_hash: string;
  source_files_will_be_deleted: boolean;
  active_run_impacts: Array<{ run_id: string; status: string }>;
  impact: {
    working_file_version_count?: number;
    subtask_result_count?: number;
    tool_reconciliation_count?: number;
    instruction_version_count?: number;
    instruction_snapshot_count?: number;
    instruction_proposal_count?: number;
    message_count: number;
    asset_count: number;
    artifact_count: number;
    candidate_count: number;
    has_final_selection: boolean;
  };
};

export type ProjectFile = {
  project_id: string;
  path: string;
  version: number;
  content_hash: string;
  size_bytes: number;
  deleted: boolean;
  binary?: boolean;
  agent_tool_call_id: string;
  created_at: string;
};

export type ProjectFileContent = {
  file: ProjectFile;
  content: string;
  offset: number;
  next_offset: number;
  truncated: boolean;
};

export type ProjectSkillDraft = {
  project_id: string;
  root_path: string;
  snapshot_hash: string;
  files: ProjectFile[];
  status: "valid" | "invalid";
  capability_id?: string;
  version?: string;
  execution_mode?: string;
  manifest?: PublicSkill;
  diagnostics: Array<{ code: string; message?: string; path?: string }>;
  installations: Array<{ skill_installation_id: string; scope: string; scope_ref: string; active_version_id: string; version: string }>;
};

export type ProjectSkillInstallArguments = {
  root_path: string;
  snapshot_hash: string;
  scope: SkillInstallTarget["scope"];
  installation_id: string;
  expected_active_version_id: string;
};

export type ProjectSkillInstallResult = {
  receipt: { receipt_id: string; project_id: string; skill_installation_id: string; skill_version_id: string; created_at: string };
  installation: SkillInstallation;
};
