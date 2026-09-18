export type SourceMode = "auto" | "novel" | "non_novel" | "unknown";
export type ProjectStatus = "idle" | "running" | "waiting_approval" | "paused" | "failed" | "completed";
export type RunStatus = "pending" | "running" | "waiting_approval" | "paused" | "completed" | "failed" | "cancelled";
export type StepStatus = "pending" | "running" | "waiting_approval" | "completed" | "failed" | "skipped";
export type ArtifactStatus = "draft" | "pending_approval" | "confirmed" | "stale" | "superseded" | "invalidated" | "failed";
export type MessageRole = "agent" | "system" | "user";
export type IntentType =
  | "chat"
  | "generate_novel"
  | "generate_non_novel"
  | "generate_from_novel"
  | "generate_from_material"
  | "revise_artifact"
  | "revise_selection"
  | "revise_checkpoint"
  | "approve_checkpoint"
  | "pause_run"
  | "resume_run"
  | "continue_with_note"
  | "inspect_artifact"
  | "inspect_source"
  | "rerun_step"
  | "explain_current_state"
  | "chat_idle"
  | "unsupported"
  | "project_control"
  | "unknown";
export type ApprovalStatus = "pending" | "approved" | "revised" | "paused" | "rejected" | "expired";
export type ApprovalAction = "approve" | "keep_downstream" | "regenerate_downstream" | "revise_instruction" | "pause" | "reject" | "rerun_step" | "auto_continue";
export type FileStatus = "uploaded" | "parsed" | "failed" | "deleted";
export type EventLevel = "info" | "warning" | "error";
export type ScriptBlockType = "action" | "dialogue" | "transition" | "scene_note";
export type ScriptCommentStatus = "open" | "resolved" | "stale" | "deleted";
export type ScriptTextMarkType = "bold" | "italic" | "underline" | "strike";

export interface FileAttachment {
	file_id?: string;
  file_name: string;
  mime_type?: string;
  size?: number;
  text_content?: string;
  content_base64?: string;
}

export interface GenerationConfig {
  target_episode_count?: number;
  episode_duration_minutes?: number;
  target_script_chars?: number;
  target_source_chars_per_episode?: number;
  boundary_detection_window_chars?: number;
  preserve_existing_episode_marks?: boolean;
  existing_episode_markers_detected?: boolean;
  detected_episode_count?: number;
}

export interface Project {
  project_id: string;
  title: string;
  source_mode: SourceMode;
  status: ProjectStatus;
  active_run_id?: string;
  current_focus_artifact_id?: string;
  active_artifacts?: Partial<Record<ArtifactType, string>>;
  created_at?: string;
  updated_at?: string;
}

export interface Message {
  message_id: string;
  project_id: string;
  run_id?: string;
  role: MessageRole;
  content: string;
  attachments?: Array<FileRef | string>;
  selection_context?: SelectionContext | null;
  intent?: IntentType;
  decision_context?: {
    intent?: string;
    next_action?: string;
    source_mode?: SourceMode;
    requires_generation_config?: boolean;
    generation_config?: GenerationConfig;
    target_file_ids?: string[];
  };
  created_at?: string;
}

export interface Run {
  run_id: string;
  project_id: string;
  intent: string;
  source_mode: SourceMode;
  status: RunStatus;
  current_step_id?: string;
  approval_request_id?: string;
  next_action?: Record<string, unknown>;
  metadata?: Record<string, unknown>;
  started_at?: string;
  ended_at?: string;
}

export interface RunStep {
  step_id: string;
  run_id: string;
  project_id: string;
  status: StepStatus;
  name?: string;
  created_at?: string;
  started_at?: string;
  ended_at?: string;
}

export interface RunEvent {
  event_id: string;
  run_id: string;
  project_id?: string;
  step_id?: string;
  type: string;
  level?: EventLevel;
  message: string;
  artifact_refs?: Array<ArtifactRef | string>;
  approval_request_id?: string;
  payload?: Record<string, unknown>;
  created_at?: string;
}

export interface Artifact {
  artifact_id: string;
  artifact_type: ArtifactType;
  project_id: string;
  run_id: string;
  version: number;
  status: ArtifactStatus;
  source_mode: SourceMode;
  payload: Record<string, unknown>;
  created_at?: string;
  updated_at?: string;
}

export type ArtifactType =
  | "source_input"
  | "story_bible"
  | "episode_split"
  | "material_bank"
  | "story_seed"
  | "series_blueprint"
  | "episode_cards"
  | "script_context"
  | "script_unit"
  | "scripts";

export interface ApprovalRequest {
  approval_request_id: string;
  run_id: string;
  project_id?: string;
  step_id: string;
  status?: ApprovalStatus;
  title?: string;
  reason?: string;
  proposed_action?: Record<string, unknown>;
  affected_artifacts?: Array<ArtifactRef | string>;
  risk_notes?: string[];
  options?: Array<ApprovalOption | ApprovalAction>;
  created_at?: string;
  resolved_at?: string;
}

export interface ApprovalOption {
  type: ApprovalAction;
  label: string;
  danger?: boolean;
}

export interface FileRef {
  file_id: string;
  project_id: string;
  filename: string;
  mime_type: string;
  size_bytes: number;
  status: FileStatus;
  text_preview?: string;
  created_at?: string;
}

export interface ArtifactRef {
  artifact_id: string;
  artifact_type: ArtifactType;
  version?: number;
  node_id?: string;
}

export interface SelectionContext {
  artifact_id: string;
  artifact_type: ArtifactType;
  version: number;
  field_path?: string;
  node_id?: string;
  episode_id?: number | string;
  scene_id?: string;
  line_id?: string;
  start_scene_id?: string;
  end_scene_id?: string;
  start_line_id?: string;
  end_line_id?: string;
  line_ids?: string[];
  selection_scope?: "line" | "range";
  block_type?: ScriptBlockType;
  selection_start?: number;
  selection_end?: number;
  selected_text: string;
  before_context?: string;
  after_context?: string;
  selection_source?: string;
  selection_hash?: string;
}

export interface ScriptTextMark {
  type: ScriptTextMarkType;
  offset_range: [number, number];
}

export interface ScriptLine {
  line_id: string;
  block_type: ScriptBlockType;
  speaker?: string;
  text: string;
  marks?: ScriptTextMark[];
}

export interface ScriptScene {
  scene_id: string;
  scene_no?: number;
  title?: string;
  location?: string;
  time_of_day?: string;
  lines: ScriptLine[];
}

export interface ScriptEpisode {
  episode_id: number | string;
  episode_no?: number;
  title?: string;
  scenes: ScriptScene[];
}

export interface ScriptDocument {
  artifact_id: string;
  version: number;
  source_mode: SourceMode;
  episodes: ScriptEpisode[];
}

export interface ScriptSelectionTarget {
  episode_id?: number | string;
  scene_id?: string;
  line_id?: string;
  selection_start?: number;
  selection_end?: number;
  selection_hash?: string;
}

export interface ScriptComment {
  comment_id: string;
  artifact_id: string;
  version: number;
  target: ScriptSelectionTarget;
  author: string;
  body: string;
  status: ScriptCommentStatus;
  created_at?: string;
  updated_at?: string;
}

export interface ErrorResponse {
  error: {
    code: string;
    message: string;
    recoverable?: boolean;
    retryable?: boolean;
    details?: Record<string, unknown>;
  };
}

export interface AgentDecision {
  intent?: string;
  confidence?: number;
  next_action?: string;
  source_mode?: SourceMode;
  agent_reply?: string;
  requires_approval?: boolean;
  requires_generation_config?: boolean;
  reason?: string;
  target_artifact?: string;
  runtime?: string;
  warning?: string;
  generation_config?: GenerationConfig;
  revision_intent?: string;
  revision_target?: {
    artifact_id?: string;
    artifact_type?: string;
    field_path?: string;
    episode_id?: string;
    scene_id?: string;
    node_id?: string;
    scope?: string;
  };
  revision_context?: unknown;
}

export interface AgentResponse {
  decision?: AgentDecision;
  agent_message?: string;
  run?: Run;
  events?: RunEvent[];
  artifacts?: Artifact[];
  approval_request?: ApprovalRequest;
}

export interface ProjectMessageRequest {
  content: string;
  display_content?: string;
  source_mode_hint?: SourceMode;
  file_ids?: string[];
  generation_config?: GenerationConfig;
  selection_context?: SelectionContext | null;
  client_context?: {
    current_artifact_id?: string;
    current_view?: ViewMode | ArtifactType | string;
  };
}

export interface ProjectMessageResponse {
  user_message?: Message;
  agent_message?: Message;
  decision?: AgentDecision;
  project?: Project;
  run?: Run | null;
  approval_request?: ApprovalRequest | null;
  events?: RunEvent[];
  artifacts?: Artifact[];
}

export interface ArtifactUpdateRequest {
  base_version: number;
  payload: Record<string, unknown>;
}

export interface ArtifactUpdateResponse {
  artifact: Artifact;
  events?: RunEvent[];
  artifacts?: Artifact[];
}

export interface CreateProjectRequest {
  title?: string;
  source_mode?: SourceMode;
}

export interface CreateProjectResponse {
  project: Project;
}

export interface ApprovalResolveRequest {
  action: ApprovalAction;
  instruction?: string;
  client_context?: {
    current_artifact_id?: string;
  };
}

export interface ApprovalResolveResponse {
  approval_request?: ApprovalRequest;
  project?: Project;
  run?: Run;
  events?: RunEvent[];
  artifacts?: Artifact[];
  agent_message?: Message;
  user_message?: Message;
}

export interface RunSnapshotResponse {
  run: Run;
  approval_request?: ApprovalRequest | null;
  events?: RunEvent[];
  artifacts?: Artifact[];
}

export interface RunActionResponse extends RunSnapshotResponse {
  project?: Project;
}

export interface ProjectFileRequest {
  file_name: string;
  mime_type?: string;
  size?: number;
  text_content?: string;
  content_base64?: string;
}

export interface ProjectFileResponse {
  file: FileRef;
}

export interface ChatMessage {
  id: string;
  role: MessageRole;
  text: string;
  attachments?: Pick<FileAttachment, "file_name" | "mime_type" | "size">[];
  selectionContext?: SelectionContext | null;
  approval?: ApprovalRequest;
  generationConfigPrompt?: GenerationConfigPrompt;
  steps?: ExecutionStep[];
  activeStep?: string;
}

export interface GenerationConfigPrompt {
  source_mode: SourceMode;
  original_message: string;
  file_ids?: string[];
  title?: string;
  reason?: string;
  defaults?: GenerationConfig;
}

export interface ExecutionStep {
  key: string;
  label: string;
}

export type ViewMode = "script" | "artifacts" | "events";

export interface SendAgentMessageInput {
  message: string;
  sourceMode: SourceMode;
  runID?: string;
  attachments?: FileAttachment[];
}

export interface HealthResponse {
  ok: boolean;
  runtime: string;
  store?: "ok" | "memory" | "error";
}
