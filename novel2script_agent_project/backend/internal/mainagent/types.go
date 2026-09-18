package mainagent

import (
	"context"
	"time"

	"novel2script-agent/backend/internal/agent"
)

// Decider is the stable Main Agent boundary used by the HTTP service. Native
// and Eino orchestration must return the same guarded Decision contract.
type Decider interface {
	Decide(context.Context, Context) Decision
}

type Intent string

const (
	IntentChatIdle             Intent = "chat_idle"
	IntentGenerateFromNovel    Intent = "generate_from_novel"
	IntentGenerateFromMaterial Intent = "generate_from_material"
	IntentApproveCheckpoint    Intent = "approve_checkpoint"
	IntentPauseRun             Intent = "pause_run"
	IntentResumeRun            Intent = "resume_run"
	IntentContinueWithNote     Intent = "continue_with_note"
	IntentReviseCheckpoint     Intent = "revise_checkpoint"
	IntentInspectArtifact      Intent = "inspect_artifact"
	IntentInspectSource        Intent = "inspect_source"
	IntentRerunStep            Intent = "rerun_step"
	IntentExplainState         Intent = "explain_current_state"
	IntentUnsupported          Intent = "unsupported"
)

type NextAction string

const (
	ActionReply            NextAction = "reply"
	ActionStartRun         NextAction = "start_run"
	ActionApproveRun       NextAction = "approve_run"
	ActionPauseRun         NextAction = "pause_run"
	ActionResumeRun        NextAction = "resume_run"
	ActionReviseCheckpoint NextAction = "revise_checkpoint"
	ActionInspectArtifact  NextAction = "inspect_artifact"
	ActionInspectSource    NextAction = "inspect_source"
	ActionRerunStep        NextAction = "rerun_step"
	ActionUnsupported      NextAction = "unsupported"
)

type RevisionIntent string

const (
	RevisionIntentUnknown                 RevisionIntent = ""
	RevisionIntentExplainOrLocate         RevisionIntent = "explain_or_locate"
	RevisionIntentClarifyRevisionTarget   RevisionIntent = "clarify_revision_target"
	RevisionIntentAddRequirement          RevisionIntent = "add_requirement_to_checkpoint"
	RevisionIntentPatchArtifactField      RevisionIntent = "patch_artifact_field"
	RevisionIntentPatchArtifactEntity     RevisionIntent = "patch_artifact_entity"
	RevisionIntentPatchArtifactCollection RevisionIntent = "patch_artifact_collection"
	RevisionIntentPatchArtifactSection    RevisionIntent = "patch_artifact_section"
	RevisionIntentRegenerateArtifact      RevisionIntent = "regenerate_artifact"
	RevisionIntentPatchScriptSpan         RevisionIntent = "patch_script_span"
	RevisionIntentRegenerateScriptScene   RevisionIntent = "regenerate_script_scene"
	RevisionIntentRegenerateScriptEpisode RevisionIntent = "regenerate_script_episode"
	RevisionIntentRegenerateScriptRange   RevisionIntent = "regenerate_script_range"
	RevisionIntentRerunFailedTask         RevisionIntent = "rerun_failed_task"
	RevisionIntentResumeAfterRevision     RevisionIntent = "resume_after_revision"
	RevisionIntentReplaceSourceInput      RevisionIntent = "replace_source_input"
)

type RevisionTarget struct {
	ArtifactID   string   `json:"artifact_id,omitempty"`
	ArtifactType string   `json:"artifact_type,omitempty"`
	FieldPath    string   `json:"field_path,omitempty"`
	EpisodeID    string   `json:"episode_id,omitempty"`
	SceneID      string   `json:"scene_id,omitempty"`
	NodeID       string   `json:"node_id,omitempty"`
	StartSceneID string   `json:"start_scene_id,omitempty"`
	EndSceneID   string   `json:"end_scene_id,omitempty"`
	StartLineID  string   `json:"start_line_id,omitempty"`
	EndLineID    string   `json:"end_line_id,omitempty"`
	LineIDs      []string `json:"line_ids,omitempty"`
	Scope        string   `json:"scope,omitempty"`
}

type ArtifactSnapshot struct {
	ArtifactID     string               `json:"artifact_id,omitempty"`
	ArtifactType   string               `json:"artifact_type,omitempty"`
	Status         agent.ArtifactStatus `json:"status,omitempty"`
	Version        int                  `json:"version,omitempty"`
	Payload        map[string]any       `json:"payload,omitempty"`
	PayloadSummary string               `json:"payload_summary,omitempty"`
}

type RevisionContextPack struct {
	RevisionIntent         RevisionIntent      `json:"revision_intent,omitempty"`
	UserRequest            string              `json:"user_request,omitempty"`
	SourceMode             agent.SourceMode    `json:"source_mode,omitempty"`
	Target                 RevisionTarget      `json:"target,omitempty"`
	TargetArtifact         *ArtifactSnapshot   `json:"target_artifact,omitempty"`
	RequiredUpstream       []ArtifactSnapshot  `json:"required_upstream,omitempty"`
	GenerationConfig       map[string]any      `json:"generation_config,omitempty"`
	CurrentStepContext     *CurrentStepContext `json:"current_step_context,omitempty"`
	FocusedContext         *FocusedContext     `json:"focused_context,omitempty"`
	RecentTurns            []ConversationTurn  `json:"recent_turns,omitempty"`
	RecentEvents           []EventDigest       `json:"recent_events,omitempty"`
	DownstreamRefreshScope []string            `json:"downstream_refresh_scope,omitempty"`
	ApplyPolicy            string              `json:"apply_policy,omitempty"`
}

type MessageRequest struct {
	ProjectID          string                  `json:"project_id,omitempty"`
	Message            string                  `json:"message"`
	SourceMode         agent.SourceMode        `json:"source_mode,omitempty"`
	RunID              string                  `json:"run_id,omitempty"`
	SelectedArtifactID string                  `json:"selected_artifact_id,omitempty"`
	SelectedText       string                  `json:"selected_text,omitempty"`
	SelectionContext   map[string]any          `json:"selection_context,omitempty"`
	Attachments        []FileAttachment        `json:"attachments,omitempty"`
	GenerationConfig   *agent.GenerationConfig `json:"generation_config,omitempty"`
}

type FileAttachment struct {
	FileID        string `json:"file_id,omitempty"`
	FileName      string `json:"file_name"`
	MimeType      string `json:"mime_type,omitempty"`
	Size          int64  `json:"size,omitempty"`
	TextContent   string `json:"text_content,omitempty"`
	ContentBase64 string `json:"content_base64,omitempty"`
}

type Decision struct {
	Intent                   Intent                  `json:"intent"`
	Confidence               float64                 `json:"confidence"`
	NextAction               NextAction              `json:"next_action"`
	SourceMode               agent.SourceMode        `json:"source_mode,omitempty"`
	AgentReply               string                  `json:"agent_reply"`
	RequiresApproval         bool                    `json:"requires_approval"`
	RequiresGenerationConfig bool                    `json:"requires_generation_config,omitempty"`
	Reason                   string                  `json:"reason,omitempty"`
	TargetArtifact           string                  `json:"target_artifact,omitempty"`
	TargetFileIDs            []string                `json:"target_file_ids,omitempty"`
	Warning                  string                  `json:"warning,omitempty"`
	Runtime                  string                  `json:"runtime"`
	Orchestrator             string                  `json:"orchestrator,omitempty"`
	GenerationConfig         *agent.GenerationConfig `json:"generation_config,omitempty"`
	RevisionIntent           RevisionIntent          `json:"revision_intent,omitempty"`
	RevisionTarget           *RevisionTarget         `json:"revision_target,omitempty"`
	RevisionContext          *RevisionContextPack    `json:"revision_context,omitempty"`
	Trace                    *DecisionTrace          `json:"trace,omitempty"`
}

type DecisionSnapshot struct {
	Intent         Intent           `json:"intent"`
	NextAction     NextAction       `json:"next_action"`
	SourceMode     agent.SourceMode `json:"source_mode,omitempty"`
	AgentReply     string           `json:"agent_reply,omitempty"`
	RevisionIntent RevisionIntent   `json:"revision_intent,omitempty"`
}

type DecisionTrace struct {
	ModelDecision   DecisionSnapshot `json:"model_decision"`
	GuardedDecision DecisionSnapshot `json:"guarded_decision"`
	ExecutedAction  NextAction       `json:"executed_action,omitempty"`
	GuardApplied    bool             `json:"guard_applied"`
	SourceModeRule  string           `json:"source_mode_rule,omitempty"`
}

type Context struct {
	Request            MessageRequest         `json:"request"`
	Project            *ProjectContext        `json:"project,omitempty"`
	Conversation       *ConversationContext   `json:"conversation,omitempty"`
	Run                *agent.Run             `json:"run,omitempty"`
	ApprovalRequest    *agent.ApprovalRequest `json:"approval_request,omitempty"`
	Events             []agent.RunEvent       `json:"events,omitempty"`
	EventDigest        []EventDigest          `json:"event_digest,omitempty"`
	Artifacts          []ArtifactDigest       `json:"artifacts,omitempty"`
	ArtifactIndex      map[string]string      `json:"artifact_index,omitempty"`
	CurrentStepContext *CurrentStepContext    `json:"current_step_context,omitempty"`
	FocusedContext     *FocusedContext        `json:"focused_context,omitempty"`
	UpstreamContext    *UpstreamContext       `json:"upstream_context,omitempty"`
}

type ProjectContext struct {
	ProjectID   string           `json:"project_id"`
	Title       string           `json:"title,omitempty"`
	Status      string           `json:"status,omitempty"`
	SourceMode  agent.SourceMode `json:"source_mode,omitempty"`
	ActiveRunID string           `json:"active_run_id,omitempty"`
	Files       []ProjectFile    `json:"files,omitempty"`
}

type ProjectFile struct {
	FileID      string `json:"file_id"`
	FileName    string `json:"file_name"`
	MimeType    string `json:"mime_type,omitempty"`
	Size        int64  `json:"size,omitempty"`
	TextPreview string `json:"text_preview,omitempty"`
}

type ConversationContext struct {
	RecentTurns []ConversationTurn `json:"recent_turns,omitempty"`
	Summary     string             `json:"summary,omitempty"`
}

type ConversationTurn struct {
	Role             string         `json:"role"`
	Content          string         `json:"content,omitempty"`
	RunID            string         `json:"run_id,omitempty"`
	Intent           string         `json:"intent,omitempty"`
	AttachmentIDs    []string       `json:"attachment_ids,omitempty"`
	SelectionContext map[string]any `json:"selection_context,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
}

type EventDigest struct {
	Type         agent.EventType `json:"type"`
	StepID       string          `json:"step_id,omitempty"`
	Message      string          `json:"message,omitempty"`
	ArtifactRefs []string        `json:"artifact_refs,omitempty"`
	Payload      map[string]any  `json:"payload,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
}

type CurrentStepContext struct {
	StepID         string               `json:"step_id,omitempty"`
	StepLabel      string               `json:"step_label,omitempty"`
	ArtifactID     string               `json:"artifact_id,omitempty"`
	ArtifactType   string               `json:"artifact_type,omitempty"`
	Status         agent.ArtifactStatus `json:"status,omitempty"`
	Version        int                  `json:"version,omitempty"`
	PayloadExcerpt map[string]any       `json:"payload_excerpt,omitempty"`
	PayloadSummary string               `json:"payload_summary,omitempty"`
	ReadPolicy     string               `json:"read_policy,omitempty"`
}

type FocusedContext struct {
	ArtifactID      string   `json:"artifact_id,omitempty"`
	ArtifactType    string   `json:"artifact_type,omitempty"`
	FieldPath       string   `json:"field_path,omitempty"`
	EpisodeID       string   `json:"episode_id,omitempty"`
	SceneID         string   `json:"scene_id,omitempty"`
	NodeID          string   `json:"node_id,omitempty"`
	StartSceneID    string   `json:"start_scene_id,omitempty"`
	EndSceneID      string   `json:"end_scene_id,omitempty"`
	StartLineID     string   `json:"start_line_id,omitempty"`
	EndLineID       string   `json:"end_line_id,omitempty"`
	LineIDs         []string `json:"line_ids,omitempty"`
	SelectionScope  string   `json:"selection_scope,omitempty"`
	SelectionStart  int      `json:"selection_start,omitempty"`
	SelectionEnd    int      `json:"selection_end,omitempty"`
	SelectedText    string   `json:"selected_text,omitempty"`
	BeforeText      string   `json:"before_text,omitempty"`
	AfterText       string   `json:"after_text,omitempty"`
	SelectionSource string   `json:"selection_source,omitempty"`
}

type UpstreamContext struct {
	RequiredArtifacts []string          `json:"required_artifacts,omitempty"`
	Summaries         map[string]string `json:"summaries,omitempty"`
}

type ArtifactDigest struct {
	ArtifactID   string               `json:"artifact_id"`
	ArtifactType string               `json:"artifact_type"`
	Status       agent.ArtifactStatus `json:"status"`
	Version      int                  `json:"version"`
}

type MessageResponse struct {
	Decision        Decision               `json:"decision"`
	AgentMessage    string                 `json:"agent_message"`
	Run             *agent.Run             `json:"run,omitempty"`
	Events          []agent.RunEvent       `json:"events,omitempty"`
	Artifacts       []agent.Artifact       `json:"artifacts,omitempty"`
	ApprovalRequest *agent.ApprovalRequest `json:"approval_request,omitempty"`
}
