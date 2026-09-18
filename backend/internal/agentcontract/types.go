package agentcontract

import "encoding/json"

type CapabilityRef struct {
	CapabilityID  string `json:"capability_id"`
	Version       string `json:"version"`
	SelectionMode string `json:"selection_mode,omitempty"`
}

type AttachmentRef struct {
	AssetID          string  `json:"asset_id"`
	AssetSnapshotID  string  `json:"asset_snapshot_id"`
	DisplayName      string  `json:"display_name,omitempty"`
	Hidden           bool    `json:"hidden,omitempty"`
	ContainerAssetID *string `json:"container_asset_id,omitempty"`
}

type SelectionSnapshot struct {
	ArtifactVersionID string          `json:"artifact_version_id"`
	SnapshotHash      string          `json:"snapshot_hash"`
	Selection         json.RawMessage `json:"selection"`
}

type ClientContext struct {
	Locale                   string  `json:"locale,omitempty"`
	Timezone                 string  `json:"timezone,omitempty"`
	Surface                  string  `json:"surface,omitempty"`
	CurrentView              string  `json:"current_view,omitempty"`
	CurrentArtifactID        *string `json:"current_artifact_id,omitempty"`
	CurrentArtifactVersionID *string `json:"current_artifact_version_id,omitempty"`
	CurrentRunID             *string `json:"current_run_id,omitempty"`
	CurrentCapabilityID      *string `json:"current_capability_id,omitempty"`
	ViewedRunID              *string `json:"viewed_run_id,omitempty"`
	ViewedCapabilityID       *string `json:"viewed_capability_id,omitempty"`
	CurrentScopeKey          *string `json:"current_scope_key,omitempty"`
	CurrentAssetSetVersionID *string `json:"current_asset_set_version_id,omitempty"`
}

type MessageRequest struct {
	Content           string             `json:"content"`
	DisplayContent    string             `json:"display_content,omitempty"`
	CapabilityRef     *CapabilityRef     `json:"capability_ref"`
	AttachmentRefs    []AttachmentRef    `json:"attachment_refs"`
	SelectionSnapshot *SelectionSnapshot `json:"selection_snapshot"`
	ClientContext     ClientContext      `json:"client_context"`
}

type ConversationMessage struct {
	MessageID    string  `json:"message_id,omitempty"`
	Role         string  `json:"role"`
	Content      string  `json:"content"`
	Scope        string  `json:"scope"`
	InvocationID *string `json:"invocation_id,omitempty"`
	RunID        *string `json:"run_id,omitempty"`
	CapabilityID *string `json:"capability_id,omitempty"`
	ArtifactID   *string `json:"artifact_id,omitempty"`
}

// MemoryContext contains rebuildable, non-authoritative project memory. It may
// help the Agent find older facts, but Runtime and current Artifact versions
// remain the source of truth whenever they disagree.
type MemoryContext struct {
	ConversationSummary *ConversationSummaryContext `json:"conversation_summary,omitempty"`
	RetrievedMessages   []ConversationMessage       `json:"retrieved_messages"`
	Entries             []MemoryEntryContext        `json:"entries"`
}

type ConversationSummaryContext struct {
	Scope                   string `json:"scope"`
	CoveredThroughMessageID string `json:"covered_through_message_id,omitempty"`
	CoveredMessageCount     int    `json:"covered_message_count"`
	Summary                 string `json:"summary"`
}

type MemoryEntryContext struct {
	Kind       string          `json:"kind"`
	Scope      string          `json:"scope"`
	Content    string          `json:"content"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	SourceKind string          `json:"source_kind"`
	SourceRef  string          `json:"source_ref"`
	Confidence float64         `json:"confidence"`
}

// RuntimeContext is the server-owned execution state available to the Agent.
// ClientContext remains a view hint and must not be used as workflow authority.
type RuntimeContext struct {
	ActiveRun              *ActiveRunContext         `json:"active_run,omitempty"`
	ConfirmedAssetSet      *ConfirmedAssetSetContext `json:"confirmed_asset_set,omitempty"`
	CurrentRevision        *RuntimeRevisionContext   `json:"current_revision,omitempty"`
	ViewedRun              *ViewedRunContext         `json:"viewed_run,omitempty"`
	RunIndex               []RunIndexItem            `json:"run_index"`
	ArtifactSets           []ArtifactSetContext      `json:"artifact_sets"`
	FocusedArtifacts       []FocusedArtifactContext  `json:"focused_artifacts"`
	ArtifactFocus          string                    `json:"artifact_focus,omitempty"`
	ArtifactFocusTruncated bool                      `json:"artifact_focus_truncated,omitempty"`
	LatestCapabilityID     *string                   `json:"latest_capability_id,omitempty"`
	LatestRunStatus        *string                   `json:"latest_run_status,omitempty"`
}

// ConfirmedAssetSetContext is a server-verified input collection. The client
// may identify the viewed version, but only Runtime can establish that it
// belongs to this project and is ready for capability execution.
type ConfirmedAssetSetContext struct {
	AssetSetID        string `json:"asset_set_id"`
	AssetSetVersionID string `json:"asset_set_version_id"`
	Purpose           string `json:"purpose"`
	Status            string `json:"status"`
	MemberCount       int    `json:"member_count"`
}

// ArtifactSetContext is a compact inventory of the current Artifact versions.
// Execution state and Artifact availability are deliberately represented by
// separate fields so a stopped Run cannot make persisted output disappear.
type ArtifactSetContext struct {
	RunID         string   `json:"run_id"`
	CapabilityID  string   `json:"capability_id"`
	ArtifactType  string   `json:"artifact_type"`
	ArtifactLabel string   `json:"artifact_label"`
	Status        string   `json:"status"`
	Count         int      `json:"count"`
	ScopeKeys     []string `json:"scope_keys"`
	UpdatedAt     string   `json:"updated_at"`
}

// FocusedArtifactContext carries only the smallest current or explicitly
// referenced payload needed for this turn. Large payloads are represented by
// a bounded excerpt while the inventory remains complete.
type FocusedArtifactContext struct {
	ArtifactID        string          `json:"artifact_id"`
	ArtifactVersionID string          `json:"artifact_version_id"`
	RunID             string          `json:"run_id"`
	CapabilityID      string          `json:"capability_id"`
	ArtifactType      string          `json:"artifact_type"`
	ArtifactLabel     string          `json:"artifact_label"`
	ScopeKey          string          `json:"scope_key"`
	Status            string          `json:"status"`
	Version           int             `json:"version"`
	Payload           json.RawMessage `json:"payload,omitempty"`
	PayloadExcerpt    string          `json:"payload_excerpt,omitempty"`
	ContentTruncated  bool            `json:"content_truncated,omitempty"`
}

type RunIndexItem struct {
	RunID        string `json:"run_id"`
	CapabilityID string `json:"capability_id"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

type ViewedRunContext struct {
	RunID        string `json:"run_id"`
	CapabilityID string `json:"capability_id"`
	Status       string `json:"status"`
}

type ActiveRunContext struct {
	RunID             string              `json:"run_id"`
	CapabilityID      string              `json:"capability_id"`
	Status            string              `json:"status"`
	CurrentStepRunID  *string             `json:"current_step_run_id,omitempty"`
	CurrentStepID     *string             `json:"current_step_id,omitempty"`
	CurrentStepStatus *string             `json:"current_step_status,omitempty"`
	ExecutionReason   string              `json:"execution_reason,omitempty"`
	CurrentApproval   *RuntimeApproval    `json:"current_approval,omitempty"`
	AvailableActions  []RuntimeActionHint `json:"available_actions"`
	TaskItems         []RuntimeTaskItem   `json:"task_items,omitempty"`
}

type RuntimeRevisionContext struct {
	RevisionRequestID string  `json:"revision_request_id"`
	Status            string  `json:"status"`
	Instruction       string  `json:"instruction"`
	ArtifactID        *string `json:"artifact_id,omitempty"`
	BaseVersionID     *string `json:"base_artifact_version_id,omitempty"`
}

type RuntimeApproval struct {
	ApprovalRequestID string `json:"approval_request_id"`
	Title             string `json:"title"`
	Reason            string `json:"reason"`
	Status            string `json:"status"`
}

type RuntimeActionHint struct {
	ActionID string `json:"action_id"`
	Enabled  bool   `json:"enabled"`
}

type RuntimeTaskItem struct {
	ItemKey string  `json:"item_key"`
	Status  string  `json:"status"`
	Failure *string `json:"failure,omitempty"`
}

type AgentInput struct {
	ProjectID          string                      `json:"project_id"`
	ConversationID     string                      `json:"conversation_id"`
	Request            MessageRequest              `json:"request"`
	RecentMessages     []ConversationMessage       `json:"recent_messages"`
	MemoryContext      MemoryContext               `json:"memory_context"`
	RuntimeContext     RuntimeContext              `json:"runtime_context"`
	SDKRunState        json.RawMessage             `json:"run_state,omitempty"`
	DispatchGeneration int64                       `json:"dispatch_generation,omitempty"`
	ApprovalDecisions  []AgentToolApprovalDecision `json:"approval_decisions,omitempty"`
	AdditionalInputs   []AgentTurnAdditionalInput  `json:"additional_inputs,omitempty"`
}

type AgentTurnAdditionalInput struct {
	InputID     string                     `json:"input_id"`
	AgentTurnID string                     `json:"agent_turn_id"`
	Sequence    int                        `json:"sequence"`
	Content     string                     `json:"content"`
	Status      string                     `json:"status"`
	Attachments []ExecutionInputAttachment `json:"attachments,omitempty"`
}

type ExecutionInputAttachment struct {
	AssetID         string `json:"asset_id"`
	AssetSnapshotID string `json:"asset_snapshot_id"`
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	MIMEType        string `json:"mime_type"`
	Checksum        string `json:"checksum"`
	SizeBytes       int64  `json:"size_bytes"`
	TextHash        string `json:"text_hash,omitempty"`
}

type AgentToolApprovalDecision struct {
	SDKToolCallID string `json:"sdk_tool_call_id"`
	Action        string `json:"action"`
}

type TargetRef struct {
	TargetType        string `json:"target_type"`
	TargetID          string `json:"target_id"`
	Version           int    `json:"version,omitempty"`
	ArtifactVersionID string `json:"artifact_version_id,omitempty"`
	ArtifactType      string `json:"artifact_type,omitempty"`
	ScopeKey          string `json:"scope_key,omitempty"`
	FieldPath         string `json:"field_path,omitempty"`
}

type Clarification struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
}

type ProposedActionDraft struct {
	ActionType           string          `json:"action_type"`
	CapabilityRef        *CapabilityRef  `json:"capability_ref"`
	Input                json.RawMessage `json:"input"`
	Config               json.RawMessage `json:"config"`
	RequiresConfirmation bool            `json:"requires_confirmation"`
}

// ArtifactDraft is a bounded, renderer-independent payload produced by the
// Agent Shell. It is deliberately limited to registered generic Artifact
// types; domain Skills continue to own their workflow Artifact schemas.
type ArtifactDraft struct {
	ArtifactType string          `json:"artifact_type"`
	Title        string          `json:"title"`
	Payload      json.RawMessage `json:"payload"`
}

// GoalUpdate is an optional authoritative project-goal mutation requested by
// the SDK Agent and committed atomically with its message exchange.
type GoalUpdate struct {
	Action          string   `json:"action"`
	Title           string   `json:"title,omitempty"`
	SuccessCriteria []string `json:"success_criteria,omitempty"`
}

type AgentDecision struct {
	Reply                    string               `json:"reply"`
	Intent                   string               `json:"intent"`
	Confidence               float64              `json:"confidence"`
	SourceArtifactVersionIDs []string             `json:"source_artifact_version_ids,omitempty"`
	CapabilityRef            *CapabilityRef       `json:"capability_ref"`
	TargetRef                *TargetRef           `json:"target_ref"`
	Clarification            *Clarification       `json:"clarification"`
	ProposedAction           *ProposedActionDraft `json:"proposed_action"`
	ArtifactDraft            *ArtifactDraft       `json:"artifact_draft,omitempty"`
	ArtifactDrafts           []ArtifactDraft      `json:"artifact_drafts,omitempty"`
	GoalUpdate               *GoalUpdate          `json:"goal_update,omitempty"`
}
