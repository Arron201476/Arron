package runtime

import (
	"encoding/json"
	"os"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/scriptsandbox"
)

const SharedWorkspaceID = "workspace_internal_shared"

type Project struct {
	ProjectID                     string     `json:"project_id"`
	WorkspaceID                   string     `json:"workspace_id"`
	OwnerUserID                   string     `json:"owner_user_id"`
	Title                         string     `json:"title"`
	Version                       int        `json:"version"`
	Status                        string     `json:"status"`
	PrimaryConversationID         string     `json:"primary_conversation_id"`
	ActiveWriteRunID              *string    `json:"active_write_run_id"`
	CurrentCapabilityID           *string    `json:"current_capability_id"`
	LatestCapabilityID            *string    `json:"latest_capability_id"`
	LatestRunStatus               *string    `json:"latest_run_status"`
	CurrentFocusArtifactVersionID *string    `json:"current_focus_artifact_version_id"`
	CurrentFinalSelectionID       *string    `json:"current_final_selection_id"`
	CreatedAt                     time.Time  `json:"created_at"`
	UpdatedAt                     time.Time  `json:"updated_at"`
	DeletedAt                     *time.Time `json:"deleted_at"`
}

type ProjectGoal struct {
	GoalID          string     `json:"goal_id"`
	ProjectID       string     `json:"project_id"`
	ConversationID  string     `json:"conversation_id"`
	Title           string     `json:"title"`
	SuccessCriteria []string   `json:"success_criteria"`
	Status          string     `json:"status"`
	Version         int        `json:"version"`
	SourceMessageID string     `json:"source_message_id"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	ResolvedAt      *time.Time `json:"resolved_at,omitempty"`
}

type ProjectDeleteRunImpact struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

type ProjectDeleteImpact struct {
	NativeWorkspaceCount        int  `json:"native_workspace_count"`
	WorkspaceSnapshotCount      int  `json:"workspace_snapshot_count"`
	WorkspaceCommandCount       int  `json:"workspace_command_count"`
	WorkspaceFileOperationCount int  `json:"workspace_file_operation_count"`
	SubtaskResultCount          int  `json:"subtask_result_count"`
	ToolReconciliationCount     int  `json:"tool_reconciliation_count"`
	InstructionVersionCount     int  `json:"instruction_version_count"`
	InstructionSnapshotCount    int  `json:"instruction_snapshot_count"`
	InstructionProposalCount    int  `json:"instruction_proposal_count"`
	WorkingFileVersionCount     int  `json:"working_file_version_count"`
	ConversationCount           int  `json:"conversation_count"`
	MessageCount                int  `json:"message_count"`
	AssetCount                  int  `json:"asset_count"`
	ArtifactCount               int  `json:"artifact_count"`
	CandidateCount              int  `json:"candidate_count"`
	HasFinalSelection           bool `json:"has_final_selection"`
}

type ProjectDeletePreview struct {
	ProjectDeletePreviewID string                   `json:"project_delete_preview_id"`
	ProjectID              string                   `json:"project_id"`
	ProjectVersion         int                      `json:"project_version"`
	ActiveRunImpacts       []ProjectDeleteRunImpact `json:"active_run_impacts"`
	Impact                 ProjectDeleteImpact      `json:"impact"`
	SourceFilesWillDelete  bool                     `json:"source_files_will_be_deleted"`
	DerivedRecordsRetained bool                     `json:"derived_records_retained_for_audit"`
	SnapshotHash           string                   `json:"snapshot_hash"`
	Status                 string                   `json:"status"`
	CreatedAt              time.Time                `json:"created_at"`
	ExpiresAt              time.Time                `json:"expires_at"`
	ResolvedAt             *time.Time               `json:"resolved_at"`
}

type CreateProjectDeletePreviewCommand struct {
	CommandMeta
	ProjectID string `json:"project_id"`
}

type ConfirmProjectDeleteCommand struct {
	CommandMeta
	ProjectID   string `json:"project_id"`
	PreviewHash string `json:"preview_hash"`
	Confirmed   bool   `json:"confirmed"`
	ActorRef    string `json:"actor_ref"`
}

type ProjectDeleteResult struct {
	ProjectID              string    `json:"project_id"`
	DeletedAt              time.Time `json:"deleted_at"`
	SourceDeletionJobIDs   []string  `json:"source_deletion_job_ids"`
	DerivedRecordsRetained bool      `json:"derived_records_retained_for_audit"`
}

type Conversation struct {
	ConversationID string    `json:"conversation_id"`
	ProjectID      string    `json:"project_id"`
	IsPrimary      bool      `json:"is_primary"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type Message struct {
	MessageID      string          `json:"message_id"`
	ConversationID string          `json:"conversation_id"`
	ProjectID      string          `json:"project_id"`
	Role           string          `json:"role"`
	Content        string          `json:"content"`
	CreatedAt      time.Time       `json:"created_at"`
	Context        *MessageContext `json:"message_context,omitempty"`
}

// AgentTurn is the durable lifecycle record for one SDK-owned conversation
// write. The request is persisted before execution so browser refreshes and
// runtime restarts never lose an accepted user instruction.
type AgentTurn struct {
	AgentTurnID         string                       `json:"agent_turn_id"`
	SubmissionID        string                       `json:"submission_id,omitempty"`
	WorkspaceID         string                       `json:"workspace_id"`
	UserID              string                       `json:"user_id"`
	ProjectID           string                       `json:"project_id"`
	ConversationID      string                       `json:"conversation_id"`
	Status              string                       `json:"status"`
	Request             agentcontract.MessageRequest `json:"request"`
	ErrorCode           *string                      `json:"error_code,omitempty"`
	ErrorMessage        *string                      `json:"error_message,omitempty"`
	UserMessageID       *string                      `json:"user_message_id,omitempty"`
	AgentMessageID      *string                      `json:"agent_message_id,omitempty"`
	CreatedAt           time.Time                    `json:"created_at"`
	StartedAt           *time.Time                   `json:"started_at,omitempty"`
	CompletedAt         *time.Time                   `json:"completed_at,omitempty"`
	UpdatedAt           time.Time                    `json:"updated_at"`
	Observation         AgentTurnObservation         `json:"observation"`
	Correlation         AgentTurnCorrelation         `json:"correlation"`
	IdempotencyKey      string                       `json:"-"`
	DispatchGeneration  int64                        `json:"-"`
	InputPauseRequested bool                         `json:"-"`
	AdditionalInputs    []AgentTurnInput             `json:"additional_inputs,omitempty"`
}

// AgentTurnObservation is the privacy-filtered SDK execution telemetry kept
// with the durable turn. It contains identifiers and counters only, never
// prompts, model output, or raw tool arguments/results.
type AgentTurnObservation struct {
	SchemaVersion string           `json:"schema_version"`
	ProviderID    string           `json:"provider_id,omitempty"`
	ModelID       string           `json:"model_id,omitempty"`
	ReleaseID     string           `json:"release_id,omitempty"`
	TraceRefs     []string         `json:"trace_refs"`
	ResponseIDs   []string         `json:"response_ids"`
	RequestIDs    []string         `json:"request_ids"`
	Usage         map[string]int64 `json:"usage"`
	LatencyMS     map[string]int64 `json:"latency_ms"`
	FailureStage  string           `json:"failure_stage,omitempty"`
	CancelReason  string           `json:"cancel_reason,omitempty"`
}

type AgentTurnCorrelation struct {
	SkillInvocationID *string  `json:"skill_invocation_id,omitempty"`
	AgentTaskID       *string  `json:"agent_task_id,omitempty"`
	RunID             *string  `json:"run_id,omitempty"`
	AgentToolCallIDs  []string `json:"agent_tool_call_ids"`
}

type AgentTurnCancelResult struct {
	Turn     AgentTurn `json:"turn"`
	Accepted bool      `json:"accepted"`
}

type AgentTurnResumeContext struct {
	RunState          json.RawMessage                           `json:"run_state"`
	ApprovalDecisions []agentcontract.AgentToolApprovalDecision `json:"approval_decisions"`
}

type PauseAgentTurnForApprovalCommand struct {
	RecoveryReason              string          `json:"recovery_reason,omitempty"`
	SchemaVersion               string          `json:"schema_version"`
	RunState                    json.RawMessage `json:"run_state"`
	PendingSDKToolCallIDs       []string        `json:"pending_sdk_tool_call_ids"`
	AgentTurnDispatchGeneration int64           `json:"-"`
	IncludedAgentTurnInputIDs   []string        `json:"-"`
}

type MessageContext struct {
	CapabilityRef     *agentcontract.CapabilityRef     `json:"capability_ref"`
	AttachmentRefs    []agentcontract.AttachmentRef    `json:"attachment_refs"`
	SelectionSnapshot *agentcontract.SelectionSnapshot `json:"selection_snapshot"`
	ClientContext     agentcontract.ClientContext      `json:"client_context"`
	RoutingContext    MessageRoutingContext            `json:"routing_context"`
}

type MessageRoutingContext struct {
	Scope        string  `json:"scope"`
	InvocationID *string `json:"invocation_id,omitempty"`
	RunID        *string `json:"run_id,omitempty"`
	CapabilityID *string `json:"capability_id,omitempty"`
	ArtifactID   *string `json:"artifact_id,omitempty"`
}

type SkillInvocation struct {
	SkillInvocationID string    `json:"skill_invocation_id"`
	ProjectID         string    `json:"project_id"`
	ConversationID    string    `json:"conversation_id"`
	UserMessageID     string    `json:"user_message_id"`
	AgentMessageID    string    `json:"agent_message_id"`
	CapabilityID      string    `json:"capability_id"`
	CapabilityVersion string    `json:"capability_version"`
	ExecutionMode     string    `json:"execution_mode"`
	Status            string    `json:"status"`
	ProposedActionID  *string   `json:"proposed_action_id,omitempty"`
	AgentTaskID       *string   `json:"agent_task_id,omitempty"`
	RunID             *string   `json:"run_id,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type AgentToolCall struct {
	ProgramCallID      string              `json:"program_call_id,omitempty"`
	ParentToolCallID   string              `json:"parent_tool_call_id,omitempty"`
	AgentToolCallID    string              `json:"agent_tool_call_id"`
	WorkspaceID        string              `json:"workspace_id"`
	ProjectID          string              `json:"project_id"`
	ConversationID     string              `json:"conversation_id"`
	SkillInvocationID  *string             `json:"skill_invocation_id,omitempty"`
	AgentTurnID        *string             `json:"agent_turn_id,omitempty"`
	Execution          *AgentToolExecution `json:"execution,omitempty"`
	SDKToolCallID      string              `json:"sdk_tool_call_id"`
	ToolID             string              `json:"tool_id"`
	ToolKind           string              `json:"tool_kind"`
	ServerID           *string             `json:"server_id,omitempty"`
	ToolName           string              `json:"tool_name"`
	AccessMode         string              `json:"access_mode"`
	ApprovalPolicy     string              `json:"approval_policy"`
	MaxResultBytes     int                 `json:"max_result_bytes"`
	ApprovalStatus     string              `json:"approval_status"`
	Status             string              `json:"status"`
	ArgumentsSummary   json.RawMessage     `json:"arguments_summary"`
	ArgumentsHash      string              `json:"arguments_hash"`
	ResultSummary      json.RawMessage     `json:"result_summary,omitempty"`
	Citations          []AgentToolCitation `json:"citations,omitempty"`
	OmittedCitations   int                 `json:"omitted_citations,omitempty"`
	ResultHash         *string             `json:"result_hash,omitempty"`
	ResultSizeBytes    *int                `json:"result_size_bytes,omitempty"`
	TraceRef           *string             `json:"trace_ref,omitempty"`
	ErrorCode          *string             `json:"error_code,omitempty"`
	ErrorMessage       *string             `json:"error_message,omitempty"`
	RequestedAt        time.Time           `json:"requested_at"`
	StartedAt          *time.Time          `json:"started_at,omitempty"`
	CompletedAt        *time.Time          `json:"completed_at,omitempty"`
	ApprovalConsumedAt *time.Time          `json:"approval_consumed_at,omitempty"`
	UpdatedAt          time.Time           `json:"updated_at"`
	Approval           *AgentToolApproval  `json:"approval,omitempty"`
}

type AgentToolExecution struct {
	Mode         string `json:"mode"`
	AgentTurnID  string `json:"agent_turn_id,omitempty"`
	AgentTaskID  string `json:"agent_task_id,omitempty"`
	AttemptID    string `json:"attempt_id,omitempty"`
	AttemptNo    int    `json:"attempt_no,omitempty"`
	RunID        string `json:"run_id,omitempty"`
	StepRunID    string `json:"step_run_id,omitempty"`
	StepID       string `json:"step_id,omitempty"`
	TaskItemID   string `json:"task_item_id,omitempty"`
	ItemKey      string `json:"item_key,omitempty"`
	CapabilityID string `json:"capability_id,omitempty"`
}

type AgentToolApproval struct {
	AgentToolApprovalID string          `json:"agent_tool_approval_id"`
	AgentToolCallID     string          `json:"agent_tool_call_id"`
	WorkspaceID         string          `json:"workspace_id"`
	ProjectID           string          `json:"project_id"`
	ConversationID      string          `json:"conversation_id"`
	Status              string          `json:"status"`
	Version             int             `json:"version"`
	Title               string          `json:"title"`
	Reason              string          `json:"reason"`
	Options             []string        `json:"options"`
	SubjectSnapshotHash string          `json:"subject_snapshot_hash"`
	RequestedAt         time.Time       `json:"requested_at"`
	ResolvedAt          *time.Time      `json:"resolved_at,omitempty"`
	Resolution          json.RawMessage `json:"resolution,omitempty"`
	ActorRef            *string         `json:"actor_ref,omitempty"`
}

type ScriptSandboxPolicy struct {
	WorkspaceID          string               `json:"workspace_id"`
	Enabled              bool                 `json:"enabled"`
	Version              int                  `json:"version"`
	Limits               scriptsandbox.Limits `json:"limits"`
	EnvironmentAllowlist []string             `json:"environment_allowlist"`
	Sandbox              scriptsandbox.Status `json:"sandbox"`
	UpdatedBy            string               `json:"updated_by,omitempty"`
	UpdatedAt            *time.Time           `json:"updated_at,omitempty"`
}

type UpdateScriptSandboxPolicyCommand struct {
	CommandMeta
	ExpectedVersion int                   `json:"expected_version"`
	Enabled         bool                  `json:"enabled"`
	Limits          *scriptsandbox.Limits `json:"limits,omitempty"`
	ActorRef        string                `json:"actor_ref,omitempty"`
}

type SkillScriptExecution struct {
	SkillScriptExecutionID string                   `json:"skill_script_execution_id"`
	SkillSnapshotID        string                   `json:"skill_snapshot_id,omitempty"`
	WorkspaceID            string                   `json:"workspace_id"`
	ProjectID              string                   `json:"project_id"`
	ConversationID         string                   `json:"conversation_id"`
	AgentToolCallID        string                   `json:"agent_tool_call_id"`
	SkillInstallationID    string                   `json:"skill_installation_id"`
	SkillVersionID         string                   `json:"skill_version_id"`
	ScriptID               string                   `json:"script_id"`
	ScriptPath             string                   `json:"script_path"`
	Runtime                string                   `json:"runtime"`
	Adapter                string                   `json:"adapter"`
	Engine                 string                   `json:"engine"`
	Image                  string                   `json:"image"`
	Status                 string                   `json:"status"`
	InputHash              string                   `json:"input_hash"`
	Limits                 scriptsandbox.Limits     `json:"limits"`
	EnvironmentNames       []string                 `json:"environment_names"`
	StdoutSummary          string                   `json:"stdout_summary"`
	StderrSummary          string                   `json:"stderr_summary"`
	ExitCode               *int                     `json:"exit_code,omitempty"`
	Artifacts              []scriptsandbox.Artifact `json:"artifacts"`
	ErrorCode              *string                  `json:"error_code,omitempty"`
	ErrorMessage           *string                  `json:"error_message,omitempty"`
	RequestedBy            string                   `json:"requested_by"`
	StartedAt              time.Time                `json:"started_at"`
	CompletedAt            *time.Time               `json:"completed_at,omitempty"`
	UpdatedAt              time.Time                `json:"updated_at"`
	outputStorageRef       string
}

type ExecuteSkillScriptCommand struct {
	AgentToolCallID       string          `json:"agent_tool_call_id"`
	ExpectedSDKToolCallID string          `json:"expected_sdk_tool_call_id"`
	Arguments             json.RawMessage `json:"arguments"`
}

type SkillInstallation struct {
	SkillInstallationID string                   `json:"skill_installation_id"`
	WorkspaceID         string                   `json:"workspace_id"`
	Scope               string                   `json:"scope"`
	ScopeRef            string                   `json:"scope_ref"`
	SkillName           string                   `json:"skill_name"`
	CapabilityID        string                   `json:"capability_id"`
	Status              string                   `json:"status"`
	Enabled             bool                     `json:"enabled"`
	ActiveVersionID     *string                  `json:"active_version_id,omitempty"`
	CreatedBy           string                   `json:"created_by"`
	CreatedAt           time.Time                `json:"created_at"`
	UpdatedAt           time.Time                `json:"updated_at"`
	UninstalledAt       *time.Time               `json:"uninstalled_at,omitempty"`
	Versions            []SkillVersion           `json:"versions"`
	Events              []SkillInstallationEvent `json:"events"`
	RegistryStatus      string                   `json:"registry_status"`
	RegistryReasonCode  string                   `json:"registry_reason_code,omitempty"`
}

type SkillVersion struct {
	SkillVersionID      string          `json:"skill_version_id"`
	SkillInstallationID string          `json:"skill_installation_id"`
	Version             string          `json:"version"`
	ContentHash         string          `json:"content_hash"`
	ExecutionMode       string          `json:"execution_mode"`
	SourceType          string          `json:"source_type"`
	SourceName          string          `json:"source_name"`
	Manifest            json.RawMessage `json:"manifest"`
	Status              string          `json:"status"`
	InstalledBy         string          `json:"installed_by"`
	CreatedAt           time.Time       `json:"created_at"`
	packageRef          string
}

type SkillInstallDiagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

type SkillInstallAttempt struct {
	Scope                 string                   `json:"scope"`
	ScopeRef              string                   `json:"scope_ref"`
	SkillInstallAttemptID string                   `json:"skill_install_attempt_id"`
	WorkspaceID           string                   `json:"workspace_id"`
	SourceType            string                   `json:"source_type"`
	SourceName            string                   `json:"source_name"`
	Status                string                   `json:"status"`
	FailureCode           string                   `json:"failure_code,omitempty"`
	Diagnostics           []SkillInstallDiagnostic `json:"diagnostics"`
	CreatedBy             string                   `json:"created_by"`
	CreatedAt             time.Time                `json:"created_at"`
	CompletedAt           *time.Time               `json:"completed_at,omitempty"`
}

type SkillInstallationEvent struct {
	SkillInstallationEventID string          `json:"skill_installation_event_id"`
	SkillInstallationID      string          `json:"skill_installation_id"`
	SkillVersionID           *string         `json:"skill_version_id,omitempty"`
	EventType                string          `json:"event_type"`
	ActorRef                 string          `json:"actor_ref"`
	Payload                  json.RawMessage `json:"payload"`
	CreatedAt                time.Time       `json:"created_at"`
}

type AgentDecisionRecord struct {
	AgentDecisionID string                      `json:"agent_decision_id"`
	ProjectID       string                      `json:"project_id"`
	ConversationID  string                      `json:"conversation_id"`
	UserMessageID   string                      `json:"user_message_id"`
	AgentMessageID  string                      `json:"agent_message_id"`
	Decision        agentcontract.AgentDecision `json:"decision"`
	CreatedAt       time.Time                   `json:"created_at"`
}

type ProposedAction struct {
	ProposedActionID      string                       `json:"proposed_action_id"`
	AgentDecisionID       string                       `json:"agent_decision_id"`
	ProjectID             string                       `json:"project_id"`
	ConversationID        string                       `json:"conversation_id"`
	ConfirmationMessageID string                       `json:"confirmation_message_id"`
	ActionType            string                       `json:"action_type"`
	Version               int                          `json:"version"`
	Status                string                       `json:"status"`
	CapabilityRef         *agentcontract.CapabilityRef `json:"capability_ref"`
	Input                 json.RawMessage              `json:"input"`
	Config                json.RawMessage              `json:"config"`
	SnapshotHash          string                       `json:"snapshot_hash"`
	RequiresConfirmation  bool                         `json:"requires_confirmation"`
	ConsumedRunID         *string                      `json:"consumed_run_id"`
	ConsumedTaskID        *string                      `json:"consumed_task_id"`
	CreatedAt             time.Time                    `json:"created_at"`
	UpdatedAt             time.Time                    `json:"updated_at"`
}

type MessageExchange struct {
	UserMessage      Message                     `json:"user_message"`
	AgentMessage     Message                     `json:"agent_message"`
	Context          MessageContext              `json:"message_context"`
	Decision         AgentDecisionRecord         `json:"agent_decision"`
	Action           *ProposedAction             `json:"proposed_action"`
	Invocation       *SkillInvocation            `json:"skill_invocation,omitempty"`
	TaskRef          *AgentTask                  `json:"task_ref,omitempty"`
	RunRef           *Run                        `json:"run_ref"`
	Resolution       *TargetResolution           `json:"target_resolution,omitempty"`
	Revision         *RevisionRequest            `json:"revision_request,omitempty"`
	Regeneration     *ApprovalRegenerationResult `json:"regeneration,omitempty"`
	GenericArtifact  *Artifact                   `json:"generic_artifact,omitempty"`
	GenericArtifacts []Artifact                  `json:"generic_artifacts,omitempty"`
	Goal             *ProjectGoal                `json:"goal,omitempty"`
}

type CreateGenericArtifactCommand struct {
	CommandMeta
	ProjectID                string
	ConversationID           string
	Draft                    agentcontract.ArtifactDraft
	SourceArtifactVersionIDs []string
	OriginType               string
	OriginID                 string
	CapabilityID             string
	ActorRef                 string
}

type TargetCandidate struct {
	CandidateID       string          `json:"candidate_id"`
	ArtifactID        string          `json:"artifact_id"`
	ArtifactVersionID string          `json:"artifact_version_id"`
	ArtifactType      string          `json:"artifact_type"`
	ScopeKey          string          `json:"scope_key"`
	FieldPath         string          `json:"field_path,omitempty"`
	Entity            json.RawMessage `json:"entity"`
	Display           json.RawMessage `json:"display"`
	Score             float64         `json:"score"`
}

type TargetResolution struct {
	TargetResolutionID string            `json:"target_resolution_id"`
	ProjectID          string            `json:"project_id"`
	ConversationID     string            `json:"conversation_id"`
	RequestMessageID   string            `json:"request_message_id"`
	Status             string            `json:"status"`
	Source             string            `json:"source"`
	ArtifactID         *string           `json:"artifact_id,omitempty"`
	ArtifactVersionID  *string           `json:"artifact_version_id,omitempty"`
	ArtifactType       *string           `json:"artifact_type,omitempty"`
	ScopeKey           *string           `json:"scope_key,omitempty"`
	FieldPath          *string           `json:"field_path,omitempty"`
	Entity             json.RawMessage   `json:"entity"`
	TextRange          json.RawMessage   `json:"text_range,omitempty"`
	Display            json.RawMessage   `json:"display"`
	Candidates         []TargetCandidate `json:"candidates"`
	TargetHash         *string           `json:"target_hash,omitempty"`
	CreatedAt          time.Time         `json:"created_at"`
	ResolvedAt         *time.Time        `json:"resolved_at,omitempty"`
}

type RevisionRequest struct {
	SourceApprovalRequestID *string         `json:"source_approval_request_id,omitempty"`
	RevisionRequestID       string          `json:"revision_request_id"`
	ProjectID               string          `json:"project_id"`
	ConversationID          string          `json:"conversation_id"`
	RequestMessageID        string          `json:"request_message_id"`
	TargetResolutionID      string          `json:"target_resolution_id"`
	ArtifactID              *string         `json:"artifact_id,omitempty"`
	BaseVersionID           *string         `json:"base_artifact_version_id,omitempty"`
	Instruction             string          `json:"instruction"`
	Operation               string          `json:"operation"`
	Status                  string          `json:"status"`
	ExecutionPolicy         string          `json:"execution_policy"`
	Version                 int             `json:"version"`
	ProposalPayload         json.RawMessage `json:"proposal_payload,omitempty"`
	ProposalSummary         *string         `json:"proposal_summary,omitempty"`
	ProposalHash            *string         `json:"proposal_hash,omitempty"`
	FailureCode             *string         `json:"failure_code,omitempty"`
	CreatedAt               time.Time       `json:"created_at"`
	UpdatedAt               time.Time       `json:"updated_at"`
	FinishedAt              *time.Time      `json:"finished_at,omitempty"`
}

type RevisionAttempt struct {
	RevisionAttemptID string          `json:"revision_attempt_id"`
	RevisionRequestID string          `json:"revision_request_id"`
	AttemptNo         int             `json:"attempt_no"`
	Status            string          `json:"status"`
	ContextHash       string          `json:"context_hash"`
	ContextPayload    json.RawMessage `json:"context_payload"`
	AdapterID         string          `json:"adapter_id"`
	AdapterVersion    string          `json:"adapter_version"`
	ProviderID        *string         `json:"provider_id,omitempty"`
	TraceRef          *string         `json:"trace_ref,omitempty"`
	ResponseHash      *string         `json:"response_hash,omitempty"`
	FailureCode       *string         `json:"failure_code,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	FinishedAt        *time.Time      `json:"finished_at,omitempty"`
}

type UploadSession struct {
	UploadSessionID    string       `json:"upload_session_id"`
	ProjectID          string       `json:"project_id"`
	Status             string       `json:"status"`
	DeclaredItemCount  int          `json:"declared_item_count"`
	CompletedItemCount int          `json:"completed_item_count"`
	FailedItemCount    int          `json:"failed_item_count"`
	CreatedAt          time.Time    `json:"created_at"`
	ClosedAt           *time.Time   `json:"closed_at"`
	Items              []UploadItem `json:"items"`
}

type UploadItem struct {
	UploadItemID      string     `json:"upload_item_id"`
	UploadSessionID   string     `json:"upload_session_id"`
	ClientItemKey     string     `json:"client_item_key"`
	Kind              string     `json:"kind"`
	OriginalFilename  string     `json:"original_filename"`
	DeclaredMIMEType  string     `json:"declared_mime_type"`
	DeclaredSizeBytes int64      `json:"declared_size_bytes"`
	ReceivedSizeBytes int64      `json:"received_size_bytes"`
	Status            string     `json:"status"`
	AssetID           *string    `json:"asset_id"`
	Failure           *string    `json:"failure"`
	CreatedAt         time.Time  `json:"created_at"`
	CompletedAt       *time.Time `json:"completed_at"`
}

type Asset struct {
	AssetID           string          `json:"asset_id"`
	ProjectID         string          `json:"project_id"`
	CurrentSnapshotID string          `json:"current_snapshot_id"`
	Kind              string          `json:"kind"`
	SourceType        string          `json:"source_type"`
	DisplayName       string          `json:"display_name"`
	OriginalFilename  string          `json:"original_filename"`
	Extension         string          `json:"extension"`
	DeclaredMIMEType  string          `json:"declared_mime_type"`
	DetectedMIMEType  string          `json:"detected_mime_type"`
	SizeBytes         int64           `json:"size_bytes"`
	ChecksumAlgorithm string          `json:"checksum_algorithm"`
	Checksum          string          `json:"checksum"`
	Status            string          `json:"status"`
	ParseStatus       string          `json:"parse_status"`
	OriginalBlobID    string          `json:"original_blob_id"`
	Metadata          json.RawMessage `json:"metadata"`
	RetentionPolicyID *string         `json:"retention_policy_id"`
	UploadedAt        time.Time       `json:"uploaded_at"`
	ExpiresAt         *time.Time      `json:"expires_at"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	DeletedAt         *time.Time      `json:"deleted_at"`
	DeleteReason      *string         `json:"delete_reason"`
}

type AssetSnapshot struct {
	AssetSnapshotID string          `json:"asset_snapshot_id"`
	AssetID         string          `json:"asset_id"`
	ProjectID       string          `json:"project_id"`
	SnapshotVersion int             `json:"snapshot_version"`
	Status          string          `json:"status"`
	Payload         json.RawMessage `json:"payload"`
	CreatedAt       time.Time       `json:"created_at"`
}

type UploadItemSpec struct {
	ClientItemKey     string `json:"client_item_key"`
	Kind              string `json:"kind"`
	OriginalFilename  string `json:"original_filename"`
	DeclaredMIMEType  string `json:"declared_mime_type"`
	DeclaredSizeBytes int64  `json:"declared_size_bytes"`
}

type AssetResult struct {
	Asset           Asset         `json:"asset"`
	Snapshot        AssetSnapshot `json:"asset_snapshot"`
	ExtractedAssets []AssetResult `json:"extracted_assets,omitempty"`
	IgnoredEntries  []string      `json:"ignored_entries,omitempty"`
}

type AssetParseResult struct {
	AssetParseResultID string    `json:"asset_parse_result_id"`
	AssetID            string    `json:"asset_id"`
	AssetSnapshotID    string    `json:"asset_snapshot_id"`
	Status             string    `json:"status"`
	ParserID           string    `json:"parser_id"`
	ParserVersion      string    `json:"parser_version"`
	ContentHash        string    `json:"content_hash"`
	ErrorCode          *string   `json:"error_code"`
	CreatedAt          time.Time `json:"created_at"`
}

type RetryAssetParseCommand struct {
	CommandMeta
	AssetID string `json:"asset_id"`
}

type RetentionJob struct {
	RetentionJobID string     `json:"retention_job_id"`
	ProjectID      string     `json:"project_id"`
	AssetID        string     `json:"asset_id"`
	PolicyID       string     `json:"policy_id"`
	Action         string     `json:"action"`
	DueAt          time.Time  `json:"due_at"`
	Status         string     `json:"status"`
	AttemptCount   int        `json:"attempt_count"`
	WorkerID       *string    `json:"worker_id"`
	LeaseUntil     *time.Time `json:"lease_until"`
	LastFailure    *string    `json:"last_failure"`
	CompletedAt    *time.Time `json:"completed_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type AssetSet struct {
	AssetSetID         string     `json:"asset_set_id"`
	ProjectID          string     `json:"project_id"`
	Purpose            string     `json:"purpose"`
	DisplayName        string     `json:"display_name"`
	Status             string     `json:"status"`
	CurrentVersionID   string     `json:"current_version_id"`
	CurrentVersion     int        `json:"current_version"`
	CreatedByMessageID *string    `json:"created_by_message_id"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	SealedAt           *time.Time `json:"sealed_at"`
}

type EpisodeFilenameCandidate struct {
	EpisodeNo  *int   `json:"episode_no"`
	Confidence string `json:"confidence"`
	Pattern    string `json:"pattern"`
	Raw        string `json:"raw"`
}

type AssetSetCompleteness struct {
	RecognizedEpisodeCount  int      `json:"recognized_episode_count"`
	UnrecognizedCount       int      `json:"unrecognized_count"`
	DuplicateEpisodeNumbers []int    `json:"duplicate_episode_numbers"`
	MissingEpisodeNumbers   []int    `json:"missing_episode_numbers"`
	FailedAssetIDs          []string `json:"failed_asset_ids"`
	OrderConfirmed          bool     `json:"order_confirmed"`
}

type AssetSetVersion struct {
	AssetSetVersionID  string               `json:"asset_set_version_id"`
	AssetSetID         string               `json:"asset_set_id"`
	Version            int                  `json:"version"`
	Status             string               `json:"status"`
	MemberCount        int                  `json:"member_count"`
	Completeness       AssetSetCompleteness `json:"completeness"`
	ContinuationPolicy json.RawMessage      `json:"continuation_policy"`
	CreatedAt          time.Time            `json:"created_at"`
}

type AssetSetMember struct {
	AssetSetMemberID  string                   `json:"asset_set_member_id"`
	AssetSetVersionID string                   `json:"asset_set_version_id"`
	AssetID           string                   `json:"asset_id"`
	EpisodeOrder      int                      `json:"episode_order"`
	EpisodeNo         *int                     `json:"episode_no"`
	EpisodeLabel      *string                  `json:"episode_label"`
	EpisodeSource     string                   `json:"episode_source"`
	FilenameCandidate EpisodeFilenameCandidate `json:"filename_candidate"`
	Included          bool                     `json:"included"`
	ExclusionReason   *string                  `json:"exclusion_reason"`
}

type AssetSetSnapshot struct {
	AssetSet AssetSet         `json:"asset_set"`
	Version  AssetSetVersion  `json:"version"`
	Members  []AssetSetMember `json:"members"`
}

type CreateAssetSetCommand struct {
	CommandMeta
	ProjectID          string  `json:"project_id"`
	Purpose            string  `json:"purpose"`
	DisplayName        string  `json:"display_name"`
	CreatedByMessageID *string `json:"created_by_message_id"`
}

type AssetSetChange struct {
	Operation       string  `json:"operation"`
	AssetID         string  `json:"asset_id"`
	EpisodeOrder    *int    `json:"episode_order,omitempty"`
	EpisodeNo       *int    `json:"episode_no,omitempty"`
	ClearEpisodeNo  bool    `json:"clear_episode_no,omitempty"`
	ExclusionReason *string `json:"exclusion_reason,omitempty"`
}

type CreateAssetSetVersionCommand struct {
	CommandMeta
	AssetSetID             string           `json:"asset_set_id"`
	ExpectedCurrentVersion int              `json:"expected_asset_set_version"`
	Changes                []AssetSetChange `json:"changes"`
}

type SealAssetSetCommand struct {
	CommandMeta
	AssetSetID                  string          `json:"asset_set_id"`
	ExpectedCurrentVersion      int             `json:"expected_asset_set_version"`
	ContinuationPolicy          json.RawMessage `json:"continuation_policy"`
	UserConfirmedUploadComplete bool            `json:"user_confirmed_upload_complete"`
}

type ReopenAssetSetCommand struct {
	CommandMeta
	AssetSetID             string `json:"asset_set_id"`
	ExpectedCurrentVersion int    `json:"expected_asset_set_version"`
}

type AssetDeleteRunImpact struct {
	RunID        string `json:"run_id"`
	Status       string `json:"status"`
	BlocksDelete bool   `json:"blocks_delete"`
}

type AssetDeleteArtifactImpact struct {
	ArtifactVersionID string `json:"artifact_version_id"`
	ArtifactType      string `json:"artifact_type"`
	ScopeKey          string `json:"scope_key"`
	Effect            string `json:"effect"`
}

type AssetDeletePreview struct {
	AssetDeletePreviewID       string                      `json:"asset_delete_preview_id"`
	ProjectID                  string                      `json:"project_id"`
	AssetID                    string                      `json:"asset_id"`
	AssetStatus                string                      `json:"asset_status"`
	ActiveRunImpacts           []AssetDeleteRunImpact      `json:"active_run_impacts"`
	ArtifactImpacts            []AssetDeleteArtifactImpact `json:"artifact_impacts"`
	ExistingArtifactsPreserved bool                        `json:"existing_artifacts_will_be_preserved"`
	SnapshotHash               string                      `json:"snapshot_hash"`
	Status                     string                      `json:"status"`
	CreatedAt                  time.Time                   `json:"created_at"`
	ResolvedAt                 *time.Time                  `json:"resolved_at"`
}

type CreateAssetDeletePreviewCommand struct {
	CommandMeta
	AssetID string `json:"asset_id"`
}

type ConfirmAssetDeleteCommand struct {
	CommandMeta
	AssetID     string `json:"asset_id"`
	PreviewHash string `json:"preview_hash"`
	Confirmed   bool   `json:"confirmed"`
	ActorRef    string `json:"actor_ref"`
}

type AssetDeleteResult struct {
	Asset Asset        `json:"asset"`
	Job   RetentionJob `json:"retention_job"`
}

type RetentionSweepResult struct {
	Claimed   int `json:"claimed"`
	Completed int `json:"completed"`
	Retried   int `json:"retried"`
	Failed    int `json:"failed"`
}

type Run struct {
	RunID                         string          `json:"run_id"`
	ProjectID                     string          `json:"project_id"`
	ConversationID                string          `json:"conversation_id"`
	CapabilityID                  string          `json:"capability_id"`
	CapabilityVersion             string          `json:"capability_version"`
	RunKind                       string          `json:"run_kind"`
	Status                        string          `json:"status"`
	CurrentStepRunID              *string         `json:"current_step_run_id"`
	CurrentInputSnapshotVersionID string          `json:"current_input_snapshot_version_id"`
	InputSnapshotStatus           string          `json:"input_snapshot_status"`
	ConfigSnapshot                json.RawMessage `json:"config_snapshot"`
	EpisodeExecutionMode          string          `json:"episode_execution_mode,omitempty"`
	StartedAt                     *time.Time      `json:"started_at"`
	EndedAt                       *time.Time      `json:"ended_at"`
	CreatedAt                     time.Time       `json:"created_at"`
	UpdatedAt                     time.Time       `json:"updated_at"`
}

type RunInputSnapshotVersion struct {
	RunInputSnapshotVersionID string          `json:"run_input_snapshot_version_id"`
	RunID                     string          `json:"run_id"`
	Version                   int             `json:"version"`
	Status                    string          `json:"status"`
	Payload                   json.RawMessage `json:"payload"`
	CreatedAt                 time.Time       `json:"created_at"`
	SealedAt                  *time.Time      `json:"sealed_at"`
}

type RunConfigSnapshot struct {
	ConfigSnapshotID string          `json:"config_snapshot_id"`
	RunID            string          `json:"run_id"`
	ConfigRef        string          `json:"config_ref"`
	Version          int             `json:"version"`
	Status           string          `json:"status"`
	Payload          json.RawMessage `json:"payload"`
	SnapshotHash     string          `json:"snapshot_hash"`
	CreatedAt        time.Time       `json:"created_at"`
	SealedAt         time.Time       `json:"sealed_at"`
}

type ContextDecisionSnapshot struct {
	DecisionSnapshotID string          `json:"decision_snapshot_id"`
	DecisionType       string          `json:"decision_type"`
	SourceKind         string          `json:"source_kind"`
	SourceRefID        string          `json:"source_ref_id"`
	Version            int             `json:"version"`
	Payload            json.RawMessage `json:"payload"`
	SnapshotHash       string          `json:"snapshot_hash"`
}

type StepRun struct {
	StepRunID            string          `json:"step_run_id"`
	RunID                string          `json:"run_id"`
	StepID               string          `json:"step_id"`
	Status               string          `json:"status"`
	AttemptCount         int             `json:"attempt_count"`
	ApprovalPolicy       string          `json:"approval_policy"`
	InputVersionSnapshot json.RawMessage `json:"input_version_snapshot"`
	TaskCursor           json.RawMessage `json:"task_cursor"`
	StartedAt            *time.Time      `json:"started_at"`
	EndedAt              *time.Time      `json:"ended_at"`
}

type TaskItem struct {
	ProgramStatus           string                  `json:"program_status,omitempty"`
	TaskItemID              string                  `json:"task_item_id"`
	StepRunID               string                  `json:"step_run_id"`
	RunID                   string                  `json:"run_id"`
	ItemKey                 string                  `json:"item_key"`
	ItemOrder               int                     `json:"item_order"`
	Status                  string                  `json:"status"`
	AttemptCount            int                     `json:"attempt_count"`
	InputSnapshot           json.RawMessage         `json:"input_snapshot"`
	Cursor                  json.RawMessage         `json:"cursor"`
	CurrentAttemptID        *string                 `json:"current_attempt_id"`
	OutputArtifactVersionID *string                 `json:"output_artifact_version_id"`
	StartedAt               *time.Time              `json:"started_at"`
	EndedAt                 *time.Time              `json:"ended_at"`
	Failure                 *string                 `json:"failure"`
	FailureDetail           *ExecutionFailureDetail `json:"failure_detail,omitempty"`
	OutputRepair            *TaskOutputRepair       `json:"output_repair,omitempty"`
	CreatedAt               time.Time               `json:"created_at"`
	UpdatedAt               time.Time               `json:"updated_at"`
}

type TaskOutputRepair struct {
	Status      string `json:"status"`
	RejectionNo int    `json:"rejection_no"`
	ErrorCode   string `json:"error_code"`
}

type AgentTask struct {
	UserID                  string           `json:"user_id"`
	InputPauseRequested     bool             `json:"input_pause_requested"`
	AdditionalInputs        []AgentTaskInput `json:"additional_inputs,omitempty"`
	RetryBlockedReason      string           `json:"retry_blocked_reason,omitempty"`
	AgentTaskID             string           `json:"agent_task_id"`
	WorkspaceID             string           `json:"workspace_id"`
	ProjectID               string           `json:"project_id"`
	ConversationID          string           `json:"conversation_id"`
	SkillInvocationID       string           `json:"skill_invocation_id"`
	ProposedActionID        *string          `json:"proposed_action_id,omitempty"`
	CapabilityID            string           `json:"capability_id"`
	CapabilityVersion       string           `json:"capability_version"`
	Status                  string           `json:"status"`
	ProgressCurrent         int              `json:"progress_current"`
	ProgressTotal           int              `json:"progress_total"`
	ProgressMessage         string           `json:"progress_message"`
	Input                   json.RawMessage  `json:"input"`
	Config                  json.RawMessage  `json:"config"`
	ResultArtifactID        *string          `json:"result_artifact_id,omitempty"`
	ResultArtifactVersionID *string          `json:"result_artifact_version_id,omitempty"`
	Result                  json.RawMessage  `json:"result,omitempty"`
	FailureCode             *string          `json:"failure_code,omitempty"`
	FailureMessage          *string          `json:"failure_message,omitempty"`
	AttemptCount            int              `json:"attempt_count"`
	MaxAttempts             int              `json:"max_attempts"`
	CancelRequested         bool             `json:"cancel_requested"`
	CreatedAt               time.Time        `json:"created_at"`
	QueuedAt                time.Time        `json:"queued_at"`
	StartedAt               *time.Time       `json:"started_at,omitempty"`
	CompletedAt             *time.Time       `json:"completed_at,omitempty"`
	UpdatedAt               time.Time        `json:"updated_at"`
}

type AgentTaskAttempt struct {
	AgentTaskAttemptID string          `json:"agent_task_attempt_id"`
	AgentTaskID        string          `json:"agent_task_id"`
	AttemptNo          int             `json:"attempt_no"`
	WorkerID           string          `json:"worker_id"`
	ProviderID         string          `json:"provider_id"`
	ModelID            string          `json:"model_id"`
	InputSnapshotHash  string          `json:"input_snapshot_hash"`
	Status             string          `json:"status"`
	LeaseUntil         time.Time       `json:"lease_until"`
	Result             json.RawMessage `json:"result,omitempty"`
	Usage              json.RawMessage `json:"usage"`
	TraceRef           *string         `json:"trace_ref,omitempty"`
	ErrorCode          *string         `json:"error_code,omitempty"`
	ErrorMessage       *string         `json:"error_message,omitempty"`
	StartedAt          time.Time       `json:"started_at"`
	EndedAt            *time.Time      `json:"ended_at,omitempty"`
}

type AgentTaskClaim struct {
	AdditionalInputs []AgentTaskInput        `json:"additional_inputs,omitempty"`
	Task             AgentTask               `json:"task"`
	Attempt          AgentTaskAttempt        `json:"attempt"`
	AttemptToken     string                  `json:"attempt_token"`
	SkillName        string                  `json:"skill_name"`
	Description      string                  `json:"description"`
	Instructions     string                  `json:"instructions"`
	RequestContent   string                  `json:"request_content"`
	Resume           *AgentTurnResumeContext `json:"resume,omitempty"`
}

type ExecutionFailureDetail struct {
	ErrorCode          string  `json:"error_code"`
	Stage              string  `json:"stage"`
	Summary            string  `json:"summary"`
	TechnicalDetail    *string `json:"technical_detail,omitempty"`
	ProviderOutput     *string `json:"provider_output,omitempty"`
	ProviderStatusCode *int    `json:"provider_status_code,omitempty"`
	ProviderRequestID  *string `json:"provider_request_id,omitempty"`
	TransportCategory  *string `json:"transport_category,omitempty"`
	Retryable          bool    `json:"retryable"`
}

type TaskResultCheckpoint struct {
	TaskResultCheckpointID string          `json:"task_result_checkpoint_id"`
	TaskItemID             string          `json:"task_item_id"`
	AttemptID              string          `json:"attempt_id"`
	StepRunID              string          `json:"step_run_id"`
	RunID                  string          `json:"run_id"`
	ItemKey                string          `json:"item_key"`
	ItemOrder              int             `json:"item_order"`
	ArtifactType           string          `json:"artifact_type"`
	Payload                json.RawMessage `json:"payload"`
	PayloadHash            string          `json:"payload_hash"`
	CreatedAt              time.Time       `json:"created_at"`
}

type QualityIssueCounts struct {
	Blocker int `json:"blocker"`
	High    int `json:"high"`
	Medium  int `json:"medium"`
	Low     int `json:"low"`
}

type QualityReview struct {
	QualityReviewID    string             `json:"quality_review_id"`
	ProjectID          string             `json:"project_id"`
	RunID              string             `json:"run_id"`
	StepRunID          string             `json:"step_run_id"`
	InputSnapshotHash  string             `json:"input_snapshot_hash"`
	Status             string             `json:"status"`
	Scope              string             `json:"scope"`
	ReviewVersion      int                `json:"review_version"`
	IssueCounts        QualityIssueCounts `json:"issue_counts"`
	RecommendedRoute   *string            `json:"recommended_route"`
	AffectedEpisodeNos []int              `json:"affected_episode_nos"`
	Result             json.RawMessage    `json:"result"`
	CreatedAt          time.Time          `json:"created_at"`
	FinishedAt         *time.Time         `json:"finished_at"`
}

type QualityOverride struct {
	QualityOverrideID string    `json:"quality_override_id"`
	QualityReviewID   string    `json:"quality_review_id"`
	InputSnapshotHash string    `json:"input_snapshot_hash"`
	IgnoredIssueIDs   []string  `json:"ignored_issue_ids"`
	ActorRef          string    `json:"actor_ref"`
	ConfirmedAt       time.Time `json:"confirmed_at"`
}

type ExecutionAttempt struct {
	ProgramStatus      string          `json:"program_status,omitempty"`
	PauseRequested     bool            `json:"pause_requested,omitempty"`
	AttemptID          string          `json:"attempt_id"`
	RunID              string          `json:"run_id"`
	StepRunID          string          `json:"step_run_id"`
	TaskItemID         string          `json:"task_item_id"`
	AttemptNo          int             `json:"attempt_no"`
	ExecutorID         string          `json:"executor_id"`
	ProviderID         string          `json:"provider_id"`
	WorkerID           string          `json:"worker_id"`
	RequestFingerprint string          `json:"request_fingerprint"`
	InputSnapshotHash  string          `json:"input_snapshot_hash"`
	Status             string          `json:"status"`
	LeaseUntil         time.Time       `json:"lease_until"`
	ResponseHash       *string         `json:"response_hash"`
	Usage              json.RawMessage `json:"usage"`
	TraceRef           *string         `json:"trace_ref"`
	StartedAt          time.Time       `json:"started_at"`
	EndedAt            *time.Time      `json:"ended_at"`
	ErrorCode          *string         `json:"error_code"`
}

type TaskClaim struct {
	Task              TaskItem                 `json:"task"`
	Attempt           ExecutionAttempt         `json:"attempt"`
	AttemptToken      string                   `json:"attempt_token"`
	CapabilityID      string                   `json:"capability_id"`
	CapabilityVersion string                   `json:"capability_version"`
	ExecutorID        string                   `json:"executor_id"`
	ConfigSnapshot    json.RawMessage          `json:"config_snapshot"`
	ContextPack       StepExecutionContextPack `json:"context_pack"`
	Resume            *ExecutionResumeContext  `json:"resume,omitempty"`
	Repair            *ExecutionResultRepair   `json:"repair,omitempty"`
	AdditionalInputs  []ExecutionInput         `json:"additional_inputs,omitempty"`
}

type ExecutionResultRepair struct {
	SchemaVersion     string          `json:"schema_version"`
	RejectionNo       int             `json:"rejection_no"`
	InputSnapshotHash string          `json:"input_snapshot_hash"`
	ResponseHash      string          `json:"response_hash"`
	Candidate         string          `json:"candidate"`
	Usage             json.RawMessage `json:"usage"`
	TraceRef          string          `json:"trace_ref"`
	ErrorCode         string          `json:"error_code"`
	ErrorDetail       string          `json:"error_detail"`
}

type ExecutionResumeContext struct {
	AgentTurnResumeContext
	SchemaVersion     string          `json:"schema_version"`
	WorkerState       json.RawMessage `json:"worker_state"`
	CheckpointVersion int             `json:"checkpoint_version"`
}

type PauseExecutionForApprovalCommand struct {
	UserPause         bool            `json:"-"`
	AttemptID         string          `json:"-"`
	AttemptToken      string          `json:"-"`
	InputSnapshotHash string          `json:"input_snapshot_hash"`
	WorkerState       json.RawMessage `json:"worker_state"`
	PauseAgentTurnForApprovalCommand
}

type StepExecutionContextPack struct {
	ContextPackID          string                     `json:"context_pack_id"`
	ContextPackVersion     string                     `json:"context_pack_version"`
	PackType               string                     `json:"pack_type"`
	ProjectID              string                     `json:"project_id"`
	ConversationID         string                     `json:"conversation_id"`
	RequestMessageID       *string                    `json:"request_message_id"`
	Capability             ContextCapabilityRef       `json:"capability"`
	Run                    ContextRunRef              `json:"run"`
	Step                   ContextStepRef             `json:"step"`
	Intent                 ContextIntent              `json:"intent"`
	Target                 ContextTarget              `json:"target"`
	RunInputSnapshot       json.RawMessage            `json:"run_input_snapshot,omitempty"`
	InputVersionSnapshot   json.RawMessage            `json:"input_version_snapshot"`
	TaskCursor             json.RawMessage            `json:"task_cursor"`
	AssetContext           []ContextAsset             `json:"asset_context"`
	UpstreamContext        []ContextUpstreamArtifact  `json:"upstream_context"`
	StructuredRunState     *ContextStructuredRunState `json:"structured_run_state,omitempty"`
	ConfigSnapshotRef      *RunConfigSnapshot         `json:"config_snapshot_ref,omitempty"`
	ConfigSnapshot         json.RawMessage            `json:"config_snapshot"`
	DecisionSnapshots      []ContextDecisionSnapshot  `json:"decision_snapshots"`
	SkillInstructions      *ContextDocument           `json:"skill_instructions,omitempty"`
	Prompt                 ContextDocument            `json:"prompt"`
	Rules                  []ContextDocument          `json:"rules"`
	OutputContract         ContextOutputContract      `json:"output_contract"`
	OutputContracts        []ContextOutputContract    `json:"output_contracts,omitempty"`
	ProviderResultContract *ContextOutputContract     `json:"provider_result_contract,omitempty"`
	Budget                 ContextBudget              `json:"budget"`
	Provenance             []ContextProvenance        `json:"provenance"`
	ContextHash            string                     `json:"context_hash"`
	CreatedAt              time.Time                  `json:"created_at"`
}

type ContextStructuredRunState struct {
	ArtifactID        string          `json:"artifact_id"`
	ArtifactVersionID string          `json:"artifact_version_id"`
	Version           int             `json:"version"`
	SchemaRef         string          `json:"schema_ref"`
	Content           json.RawMessage `json:"content"`
	ContentHash       string          `json:"content_hash"`
}

type ReviewExecutionContextPack struct {
	ContextPackID      string                    `json:"context_pack_id"`
	ContextPackVersion string                    `json:"context_pack_version"`
	PackType           string                    `json:"pack_type"`
	QualityReviewID    string                    `json:"quality_review_id"`
	ProjectID          string                    `json:"project_id"`
	Run                ContextRunRef             `json:"run"`
	Step               ContextStepRef            `json:"step"`
	InputSnapshotHash  string                    `json:"input_snapshot_hash"`
	EpisodeScope       []int                     `json:"episode_scope"`
	ScriptUnits        []ContextUpstreamArtifact `json:"script_units"`
	ScriptHandoffs     []ContextUpstreamArtifact `json:"script_handoffs"`
	ScriptContexts     []ContextUpstreamArtifact `json:"script_contexts"`
	UpstreamFacts      []ContextUpstreamArtifact `json:"upstream_facts"`
	ConfigSnapshot     json.RawMessage           `json:"config_snapshot"`
	DecisionSnapshots  []json.RawMessage         `json:"decision_snapshots"`
	Prompt             ContextDocument           `json:"prompt"`
	Rules              []ContextDocument         `json:"rules"`
	ResultContract     ContextOutputContract     `json:"result_contract"`
	Provenance         []ContextProvenance       `json:"provenance"`
	ContextHash        string                    `json:"context_hash"`
	CreatedAt          time.Time                 `json:"created_at"`
}

type ContextCapabilityRef struct {
	CapabilityID      string `json:"capability_id"`
	CapabilityVersion string `json:"capability_version"`
}

type ContextRunRef struct {
	RunID   string `json:"run_id"`
	RunKind string `json:"run_kind"`
	Status  string `json:"status"`
}

type ContextStepRef struct {
	StepRunID  string `json:"step_run_id"`
	StepID     string `json:"step_id"`
	TaskItemID string `json:"task_item_id"`
}

type ContextIntent struct {
	Operation    string `json:"operation"`
	ArtifactType string `json:"artifact_type"`
}

type ContextTarget struct {
	ScopeKey string `json:"scope_key"`
}

type ContextAsset struct {
	AssetID         string `json:"asset_id"`
	AssetSnapshotID string `json:"asset_snapshot_id"`
	Kind            string `json:"kind"`
	Filename        string `json:"filename"`
	Checksum        string `json:"checksum"`
	Role            string `json:"role"`
	Order           int    `json:"order"`
	Content         string `json:"content"`
	ContentHash     string `json:"content_hash"`
}

type ContextUpstreamArtifact struct {
	ArtifactID        string          `json:"artifact_id"`
	ArtifactVersionID string          `json:"artifact_version_id"`
	ArtifactType      string          `json:"artifact_type"`
	ScopeKey          string          `json:"scope_key"`
	Status            string          `json:"status"`
	SelectionPolicy   string          `json:"selection_policy"`
	Content           json.RawMessage `json:"content"`
	ContentHash       string          `json:"content_hash"`
}

type ContextDocument struct {
	ID          string `json:"id"`
	Ref         string `json:"ref"`
	Content     string `json:"content"`
	ContentHash string `json:"content_hash"`
}

type ContextOutputContract struct {
	ArtifactType  string          `json:"artifact_type"`
	SchemaRef     string          `json:"schema_ref"`
	SchemaVersion string          `json:"schema_version"`
	Schema        json.RawMessage `json:"schema"`
}

type ContextBudget struct {
	ProviderID         string  `json:"provider_id"`
	ModelID            *string `json:"model_id"`
	MaxInputCharacters int     `json:"max_input_characters"`
	RequiredCharacters int     `json:"required_characters"`
	Estimator          string  `json:"estimator"`
	TruncationApplied  bool    `json:"truncation_applied"`
}

type ContextProvenance struct {
	Kind        string `json:"kind"`
	Ref         string `json:"ref"`
	ContentHash string `json:"content_hash"`
}

type Artifact struct {
	ArtifactID       string    `json:"artifact_id"`
	ProjectID        string    `json:"project_id"`
	RunID            string    `json:"run_id,omitempty"`
	StepRunID        string    `json:"step_run_id,omitempty"`
	OriginType       string    `json:"origin_type"`
	OriginID         string    `json:"origin_id"`
	CapabilityID     string    `json:"capability_id"`
	ArtifactType     string    `json:"artifact_type"`
	ScopeKey         string    `json:"scope_key"`
	Title            string    `json:"title,omitempty"`
	CurrentVersionID string    `json:"current_version_id"`
	Status           string    `json:"status,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type ArtifactVersion struct {
	ArtifactVersionID string          `json:"artifact_version_id"`
	ArtifactID        string          `json:"artifact_id"`
	Version           int             `json:"version"`
	Status            string          `json:"status"`
	Payload           json.RawMessage `json:"payload"`
	SchemaID          string          `json:"schema_id"`
	SchemaVersion     string          `json:"schema_version"`
	CreatedByKind     string          `json:"created_by_kind"`
	ActorRef          string          `json:"actor_ref"`
	CreationReason    string          `json:"creation_reason"`
	BaseVersionID     *string         `json:"base_version_id"`
	CreatedAt         time.Time       `json:"created_at"`
	ConfirmedAt       *time.Time      `json:"confirmed_at"`
}

type ArtifactDependency struct {
	DependencyID                string          `json:"dependency_id"`
	ProjectID                   string          `json:"project_id"`
	RunID                       string          `json:"run_id,omitempty"`
	OriginType                  string          `json:"origin_type"`
	OriginID                    string          `json:"origin_id"`
	DownstreamArtifactVersionID string          `json:"downstream_artifact_version_id"`
	UpstreamKind                string          `json:"upstream_kind"`
	UpstreamRefID               string          `json:"upstream_ref_id"`
	Relation                    string          `json:"relation"`
	UpstreamScope               json.RawMessage `json:"upstream_scope"`
	DownstreamScope             json.RawMessage `json:"downstream_scope"`
	ImpactPolicyID              string          `json:"impact_policy_id"`
	CreatedAt                   time.Time       `json:"created_at"`
}

type ArtifactLineageEdge struct {
	ArtifactDependency
	Depth int `json:"depth"`
}

type ArtifactVersionLineage struct {
	ArtifactVersionID string                `json:"artifact_version_id"`
	Upstream          []ArtifactLineageEdge `json:"upstream"`
}

type ArtifactVersionChangeSet struct {
	ChangeSetID       string          `json:"change_set_id"`
	ProjectID         string          `json:"project_id"`
	ArtifactVersionID string          `json:"artifact_version_id"`
	BaseVersionID     *string         `json:"base_version_id"`
	ChangeMode        string          `json:"change_mode"`
	Changes           json.RawMessage `json:"changes"`
	DerivedSignals    json.RawMessage `json:"derived_signals"`
	CreatedAt         time.Time       `json:"created_at"`
}

type ImpactReviewItem struct {
	ImpactReviewItemID   string   `json:"impact_review_item_id"`
	ImpactReviewID       string   `json:"impact_review_id"`
	ArtifactID           string   `json:"artifact_id"`
	ArtifactVersionID    string   `json:"artifact_version_id"`
	ArtifactType         string   `json:"artifact_type"`
	ScopeKey             string   `json:"scope_key"`
	ImpactPath           []string `json:"impact_path"`
	ReasonCode           string   `json:"reason_code"`
	RegenerateFromStepID string   `json:"regenerate_from_step_id"`
	RegenerateTaskKeys   []string `json:"regenerate_task_keys"`
	ItemOrder            int      `json:"item_order"`
}

type ImpactReview struct {
	ImpactReviewID               string             `json:"impact_review_id"`
	ProjectID                    string             `json:"project_id"`
	RunID                        string             `json:"run_id"`
	SourceArtifactID             string             `json:"source_artifact_id"`
	OldVersionID                 string             `json:"old_version_id"`
	NewVersionID                 string             `json:"new_version_id"`
	ChangeSetID                  string             `json:"change_set_id"`
	Status                       string             `json:"status"`
	AffectedItems                []ImpactReviewItem `json:"affected_items"`
	UnaffectedSummary            json.RawMessage    `json:"unaffected_summary"`
	RecommendedRegenerationStart []string           `json:"recommended_regeneration_start"`
	SnapshotHash                 string             `json:"snapshot_hash"`
	CreatedAt                    time.Time          `json:"created_at"`
	ResolvedAt                   *time.Time         `json:"resolved_at"`
}

type DependencyDecision struct {
	DependencyDecisionID          string    `json:"dependency_decision_id"`
	ProjectID                     string    `json:"project_id"`
	RunID                         string    `json:"run_id"`
	ImpactReviewID                string    `json:"impact_review_id"`
	Action                        string    `json:"action"`
	OldUpstreamVersionID          string    `json:"old_upstream_version_id"`
	NewUpstreamVersionID          string    `json:"new_upstream_version_id"`
	PreservedDownstreamVersionIDs []string  `json:"preserved_downstream_version_ids"`
	StaleDownstreamVersionIDs     []string  `json:"stale_downstream_version_ids"`
	ActorRef                      string    `json:"actor_ref"`
	ResolvedAt                    time.Time `json:"resolved_at"`
}

type RegenerationPlanGroup struct {
	RegenerationPlanGroupID string   `json:"regeneration_plan_group_id"`
	RegenerationPlanID      string   `json:"regeneration_plan_id"`
	GroupOrder              int      `json:"group_order"`
	StepID                  string   `json:"step_id"`
	TaskKeys                []string `json:"task_keys"`
	StaleVersionIDs         []string `json:"stale_version_ids"`
	PreservedVersionIDs     []string `json:"preserved_version_ids"`
	StepRunID               *string  `json:"step_run_id"`
	Status                  string   `json:"status"`
}

type RegenerationPlan struct {
	RegenerationPlanID string                  `json:"regeneration_plan_id"`
	ProjectID          string                  `json:"project_id"`
	RunID              string                  `json:"run_id"`
	ImpactReviewID     string                  `json:"impact_review_id"`
	SourceNewVersionID string                  `json:"source_new_version_id"`
	Status             string                  `json:"status"`
	CurrentGroupOrder  int                     `json:"current_group_order"`
	Groups             []RegenerationPlanGroup `json:"groups"`
	CreatedAt          time.Time               `json:"created_at"`
	CompletedAt        *time.Time              `json:"completed_at"`
}

type Approval struct {
	ApprovalRequestID   string          `json:"approval_request_id"`
	ProjectID           string          `json:"project_id"`
	RunID               string          `json:"run_id"`
	StepRunID           string          `json:"step_run_id"`
	Scope               string          `json:"scope"`
	Status              string          `json:"status"`
	Version             int             `json:"version"`
	Title               string          `json:"title"`
	Reason              string          `json:"reason"`
	Options             []string        `json:"options"`
	SubjectKind         string          `json:"subject_kind"`
	SubjectRefID        string          `json:"subject_ref_id"`
	SubjectVersion      int             `json:"subject_version"`
	SubjectSnapshotHash string          `json:"subject_snapshot_hash"`
	RequestedAt         time.Time       `json:"requested_at"`
	ResolvedAt          *time.Time      `json:"resolved_at"`
	Resolution          json.RawMessage `json:"resolution"`
	ActorRef            *string         `json:"actor_ref"`
}

type ScriptCandidate struct {
	CandidateID              string    `json:"candidate_id"`
	ProjectID                string    `json:"project_id"`
	SourceRunID              string    `json:"source_run_id"`
	SourceCapabilityID       string    `json:"source_capability_id"`
	ScriptsArtifactVersionID string    `json:"scripts_artifact_version_id"`
	Status                   string    `json:"status"`
	Label                    string    `json:"label"`
	SupersedesCandidateID    *string   `json:"supersedes_candidate_id"`
	CreatedAt                time.Time `json:"created_at"`
	UpdatedAt                time.Time `json:"updated_at"`
}

type FinalSelectionPreview struct {
	FinalSelectionPreviewID    string     `json:"final_selection_preview_id"`
	ProjectID                  string     `json:"project_id"`
	CandidateID                string     `json:"candidate_id"`
	ApprovalRequestID          string     `json:"approval_request_id"`
	CurrentSelectionID         *string    `json:"current_selection_id"`
	ExpectedCurrentSelectionID *string    `json:"expected_current_selection_id"`
	ProposedSelectionNo        int        `json:"proposed_selection_no"`
	PreviewHash                string     `json:"preview_hash"`
	Status                     string     `json:"status"`
	CreatedAt                  time.Time  `json:"created_at"`
	ResolvedAt                 *time.Time `json:"resolved_at"`
}

type FinalSelection struct {
	FinalSelectionID    string    `json:"final_selection_id"`
	ProjectID           string    `json:"project_id"`
	CandidateID         string    `json:"candidate_id"`
	ApprovalRequestID   string    `json:"approval_request_id"`
	SelectionNo         int       `json:"selection_no"`
	Status              string    `json:"status"`
	SelectedAt          time.Time `json:"selected_at"`
	ReplacedSelectionID *string   `json:"replaced_selection_id"`
}

type ScriptExport struct {
	ExportID          string    `json:"export_id"`
	ProjectID         string    `json:"project_id"`
	CandidateID       string    `json:"candidate_id"`
	ArtifactVersionID string    `json:"artifact_version_id"`
	Format            string    `json:"format"`
	Status            string    `json:"status"`
	ContentType       string    `json:"content_type"`
	Filename          string    `json:"filename"`
	Checksum          string    `json:"checksum"`
	SizeBytes         int64     `json:"size_bytes"`
	CreatedAt         time.Time `json:"created_at"`
	ExpiresAt         time.Time `json:"expires_at"`
}

type CreateScriptExportCommand struct {
	CommandMeta
	CandidateID       string `json:"candidate_id"`
	ArtifactVersionID string `json:"artifact_version_id"`
	Format            string `json:"format"`
}

type ScriptExportContent struct {
	Export ScriptExport
	File   *os.File
}

type Event struct {
	EventID         string          `json:"event_id"`
	EventType       string          `json:"event_type"`
	SchemaVersion   int             `json:"schema_version"`
	ProjectID       string          `json:"project_id"`
	RunID           *string         `json:"run_id"`
	StepRunID       *string         `json:"step_run_id"`
	ProjectEventSeq int64           `json:"project_event_seq"`
	RunEventSeq     *int64          `json:"run_event_seq"`
	ActorKind       string          `json:"actor_kind"`
	ActorRef        string          `json:"actor_ref"`
	SubjectType     string          `json:"subject_type"`
	SubjectID       string          `json:"subject_id"`
	Payload         json.RawMessage `json:"payload"`
	OccurredAt      time.Time       `json:"occurred_at"`
}

type RunSnapshot struct {
	Run               Run                `json:"run"`
	Steps             []StepRun          `json:"steps"`
	CurrentApproval   *Approval          `json:"current_approval"`
	Artifacts         []Artifact         `json:"artifact_summary"`
	AvailableActions  []AvailableAction  `json:"available_actions"`
	EventCursor       EventCursor        `json:"event_cursor"`
	PendingScriptEdit *PendingScriptEdit `json:"pending_script_edit,omitempty"`
}

type PendingScriptEdit struct {
	ProjectID                string   `json:"project_id"`
	RunID                    string   `json:"run_id"`
	StepRunID                string   `json:"step_run_id"`
	ExpectedScriptVersionIDs []string `json:"expected_script_version_ids"`
	PendingScopes            []string `json:"pending_scopes"`
	CanComplete              bool     `json:"can_complete"`
	DisabledReason           string   `json:"disabled_reason,omitempty"`
}

type AvailableAction struct {
	ActionID       string  `json:"action_id"`
	TargetType     string  `json:"target_type"`
	TargetID       string  `json:"target_id"`
	Enabled        bool    `json:"enabled"`
	DisabledReason *string `json:"disabled_reason"`
}

type EventCursor struct {
	ProjectEventSeq int64 `json:"project_event_seq"`
	RunEventSeq     int64 `json:"run_event_seq"`
}

type EventBatch struct {
	Items       []Event `json:"items"`
	AfterSeq    int64   `json:"after_seq"`
	NextSeq     int64   `json:"next_seq"`
	CurrentSeq  int64   `json:"current_seq"`
	HasMore     bool    `json:"has_more"`
	StreamScope string  `json:"stream_scope"`
}

// ProjectActivity is a compact, read-only projection of durable runtime events.
// It intentionally excludes event payloads so the UI cannot depend on internal
// execution data or accidentally expose model/runtime metadata.
type ProjectActivity struct {
	EventID      string    `json:"event_id"`
	EventType    string    `json:"event_type"`
	RunID        *string   `json:"run_id"`
	CapabilityID *string   `json:"capability_id"`
	StepID       *string   `json:"step_id"`
	OccurredAt   time.Time `json:"occurred_at"`
}

type CommandMeta struct {
	Scope          string
	CommandType    string
	IdempotencyKey string
	RequestHash    string
}

type CreateMessageExchangeCommand struct {
	CommandMeta
	ConversationID string
	Request        agentcontract.MessageRequest
	Decision       agentcontract.AgentDecision
}

type ResolveTargetCandidateCommand struct {
	CommandMeta
	TargetResolutionID string
	CandidateID        string
	ExpectedVersion    int
}

type RequestRevisionExecutionCommand struct {
	CommandMeta
	RevisionRequestID string
	ExpectedVersion   int
}

type BeginRevisionAttemptCommand struct {
	RevisionRequestID string
	ExpectedVersion   int
	ContextPayload    json.RawMessage
	ContextHash       string
	AdapterID         string
	AdapterVersion    string
}

type CompleteRevisionAttemptCommand struct {
	RevisionRequestID string
	RevisionAttemptID string
	ProposalPayload   json.RawMessage
	ProposalSummary   string
	ProviderID        string
	TraceRef          string
}

type CompleteRevisionNoChangeCommand struct {
	RevisionRequestID string
	RevisionAttemptID string
	Summary           string
	ProviderID        string
	TraceRef          string
}

type FailRevisionAttemptCommand struct {
	RevisionRequestID string
	RevisionAttemptID string
	FailureCode       string
}

type AcceptRevisionCommand struct {
	CommandMeta
	RevisionRequestID string
	ExpectedVersion   int
	ActorRef          string
}

type RejectRevisionCommand struct {
	CommandMeta
	RevisionRequestID string
	ExpectedVersion   int
}

type CancelRevisionCommand struct {
	CommandMeta
	RevisionRequestID string
	ExpectedVersion   int
}

type RevisionAcceptResult struct {
	Revision RevisionRequest `json:"revision_request"`
	Version  VersionResult   `json:"version_result"`
}

type ConfigureProposedActionCommand struct {
	CommandMeta
	ProposedActionID string
	ExpectedVersion  int
	Input            json.RawMessage
	Config           json.RawMessage
}

type BindProposedActionInputCommand struct {
	CommandMeta
	ProposedActionID string
	ExpectedVersion  int
	Input            json.RawMessage
}

type StartRunCommand struct {
	CommandMeta
	ProjectID                string
	ConversationID           string
	CapabilityID             string
	CapabilityVersion        string
	RunKind                  string
	Input                    json.RawMessage
	Config                   json.RawMessage
	Confirmed                bool
	ProposedActionID         string
	ProposedActionVersion    int
	ConfirmationMessageID    string
	ConfirmationSnapshotHash string
}

type StartAgentTaskCommand struct {
	CommandMeta
	ProjectID                string
	ConversationID           string
	CapabilityID             string
	CapabilityVersion        string
	Input                    json.RawMessage
	Config                   json.RawMessage
	Confirmed                bool
	ProposedActionID         string
	ProposedActionVersion    int
	ConfirmationMessageID    string
	ConfirmationSnapshotHash string
}

type BeginAgentToolCallCommand struct {
	ProgramCallID           string                  `json:"program_call_id,omitempty"`
	MemoryGenerationID      string                  `json:"memory_generation_id,omitempty"`
	MemoryGenerationAttempt int                     `json:"memory_generation_attempt,omitempty"`
	ConfigurationHash       string                  `json:"configuration_hash,omitempty"`
	ProjectID               string                  `json:"project_id"`
	ConversationID          string                  `json:"conversation_id"`
	SkillInvocationID       string                  `json:"skill_invocation_id,omitempty"`
	AgentTurnID             string                  `json:"agent_turn_id,omitempty"`
	SDKToolCallID           string                  `json:"sdk_tool_call_id"`
	ToolID                  string                  `json:"tool_id"`
	Arguments               json.RawMessage         `json:"arguments"`
	AgentTaskAttemptID      string                  `json:"agent_task_attempt_id,omitempty"`
	ExecutionAttemptID      string                  `json:"execution_attempt_id,omitempty"`
	AttemptToken            string                  `json:"attempt_token,omitempty"`
	SkillSnapshot           *AgentToolSkillSnapshot `json:"skill_snapshot,omitempty"`
}

type AgentToolSkillSnapshot struct {
	CapabilityID string `json:"capability_id"`
	Version      string `json:"version"`
	ContentHash  string `json:"content_hash"`
}

type PauseAgentTaskForApprovalCommand struct {
	IncludedInputIDs   []string
	AgentTaskAttemptID string
	AttemptToken       string
	PauseAgentTurnForApprovalCommand
}

type StartAgentToolCallCommand struct {
	ConfigurationHash     string `json:"configuration_hash,omitempty"`
	AgentToolCallID       string `json:"agent_tool_call_id"`
	ExpectedSDKToolCallID string `json:"expected_sdk_tool_call_id"`
}

type CompleteAgentToolCallCommand struct {
	AgentToolCallID string          `json:"agent_tool_call_id"`
	Result          json.RawMessage `json:"result"`
	ResultSizeBytes int             `json:"result_size_bytes"`
	TraceRef        string          `json:"trace_ref,omitempty"`
}

type FailAgentToolCallCommand struct {
	AgentToolCallID string `json:"agent_tool_call_id"`
	ErrorCode       string `json:"error_code"`
	ErrorMessage    string `json:"error_message"`
	TraceRef        string `json:"trace_ref,omitempty"`
}

type CancelAgentToolCallCommand struct {
	AgentToolCallID string `json:"agent_tool_call_id"`
	Reason          string `json:"reason,omitempty"`
}

type ResolveAgentToolApprovalCommand struct {
	CommandMeta
	AgentToolApprovalID string `json:"agent_tool_approval_id"`
	ExpectedVersion     int    `json:"expected_version"`
	SubjectSnapshotHash string `json:"subject_snapshot_hash"`
	Action              string `json:"action"`
	ActorRef            string `json:"actor_ref"`
}

type ClaimAgentTaskCommand struct {
	WorkerID     string
	ProviderID   string
	ModelID      string
	LeaseSeconds int
}

type UpdateAgentTaskProgressCommand struct {
	AgentTaskAttemptID string
	AttemptToken       string
	Current            int
	Total              int
	Message            string
	LeaseSeconds       int
}

type CompleteAgentTaskCommand struct {
	IncludedInputIDs   []string
	AgentTaskAttemptID string
	AttemptToken       string
	Result             json.RawMessage
	ArtifactDraft      *agentcontract.ArtifactDraft
	Usage              json.RawMessage
	TraceRef           string
}

type FailAgentTaskCommand struct {
	AgentTaskAttemptID string
	AttemptToken       string
	ErrorCode          string
	ErrorMessage       string
	Retryable          bool
	IncludedInputIDs   []string
}

type CancelAgentTaskCommand struct {
	CommandMeta
	AgentTaskID string
	ActorRef    string
}

type RetryAgentTaskCommand struct {
	CommandMeta
	AgentTaskID string
	ActorRef    string
}

type PauseRunCommand struct {
	CommandMeta
	RunID string
}

type CompleteRunPauseCommand struct {
	CommandMeta
	RunID      string
	TaskCursor json.RawMessage
}

type ResumeRunCommand struct {
	CommandMeta
	RunID string
}

type SetEpisodeExecutionModeCommand struct {
	CommandMeta
	RunID string
	Mode  string
}

type RetryFailedStepCommand struct {
	CommandMeta
	StepRunID string
}

type ContinueWithPartialResultsCommand struct {
	CommandMeta
	StepRunID string
	Confirmed bool
	ActorRef  string
}

type CancelRunCommand struct {
	CommandMeta
	RunID     string
	Confirmed bool
	ActorRef  string
}

type ClaimExecutionTaskCommand struct {
	WorkerID     string
	ExecutorIDs  []string
	ProviderID   string
	ModelID      string
	LeaseSeconds int
}

type SubmitExecutionResultCommand struct {
	AttemptID         string
	AttemptToken      string
	InputSnapshotHash string
	ResponsePayload   json.RawMessage
	Usage             json.RawMessage
	TraceRef          string
}

type HeartbeatExecutionAttemptCommand struct {
	ProgramStatus     string
	AttemptID         string
	AttemptToken      string
	InputSnapshotHash string
	LeaseSeconds      int
}

type FailExecutionAttemptCommand struct {
	AttemptID         string
	AttemptToken      string
	InputSnapshotHash string
	ErrorCode         string
	FailureDetail     *ExecutionFailureDetail
}

type AttemptFailureResult struct {
	Attempt   ExecutionAttempt `json:"attempt"`
	Task      TaskItem         `json:"task"`
	Retryable bool             `json:"retryable"`
}

type CommitExecutionResultCommand struct {
	AttemptID            string
	ExpectedResponseHash string
	AttemptToken         string
	RepairOutput         bool
}

type CreateVersionCommand struct {
	CommandMeta
	ArtifactID    string
	BaseVersionID string
	BaseVersion   int
	ChangeMode    string
	NewPayload    json.RawMessage
	ActorRef      string
}

type CompleteScriptEditCommand struct {
	CommandMeta
	RunID                    string
	ExpectedScriptVersionIDs []string
}

type ResolveApprovalCommand struct {
	CommandMeta
	ApprovalRequestID       string
	Action                  string
	ExpectedApprovalVersion int
	SubjectSnapshotHash     string
	ResolutionPayload       json.RawMessage
	ActorRef                string
}

type StartQualityReviewCommand struct {
	RunID             string
	StepRunID         string
	InputSnapshotHash string
	Scope             string
}

type CompleteQualityReviewCommand struct {
	QualityReviewID   string
	InputSnapshotHash string
	Result            json.RawMessage
}

type ResolveQualityReviewActionCommand struct {
	CommandMeta
	QualityReviewID      string
	ExpectedReviewStatus string
	InputSnapshotHash    string
	Action               string
	Instruction          string
	ActorRef             string
	IgnoredIssueIDs      []string
}

type RequestApprovalRegenerationCommand struct {
	CommandMeta
	ApprovalRequestID       string
	ExpectedApprovalVersion int
	SubjectSnapshotHash     string
	TargetArtifactVersionID string
	Action                  string
	Instruction             string
	ActorRef                string
}

type RequestTargetedRegenerationCommand struct {
	CommandMeta
	ProjectID         string
	ArtifactID        string
	RegenerationScope string
	Instruction       string
	ActorRef          string
}

type ApprovalRegenerationResult struct {
	ImpactReview     ImpactReview     `json:"impact_review"`
	RegenerationPlan RegenerationPlan `json:"regeneration_plan"`
	RunSnapshot      RunSnapshot      `json:"run_snapshot"`
}

type QualityReviewActionResult struct {
	Review           QualityReview     `json:"quality_review"`
	Override         *QualityOverride  `json:"quality_override,omitempty"`
	ImpactReview     *ImpactReview     `json:"impact_review,omitempty"`
	RegenerationPlan *RegenerationPlan `json:"regeneration_plan,omitempty"`
	RunSnapshot      *RunSnapshot      `json:"run_snapshot,omitempty"`
}

type ResolveImpactReviewCommand struct {
	CommandMeta
	ImpactReviewID string
	Action         string
	SnapshotHash   string
	ActorRef       string
}

type CreateFinalSelectionPreviewCommand struct {
	CommandMeta
	ProjectID                  string
	CandidateID                string
	ExpectedArtifactVersionID  string
	ExpectedCurrentSelectionID *string
	ActorRef                   string
}

type ConfirmFinalSelectionCommand struct {
	CommandMeta
	ProjectID                  string
	CandidateID                string
	PreviewHash                string
	ExpectedCurrentSelectionID *string
	Confirmed                  bool
	ActorRef                   string
}

type FinalSelectionPreviewResult struct {
	Preview  FinalSelectionPreview `json:"preview"`
	Approval Approval              `json:"approval"`
}

type FinalSelectionResult struct {
	Selection FinalSelection  `json:"final_selection"`
	Candidate ScriptCandidate `json:"candidate"`
	Approval  Approval        `json:"approval"`
}

type ImpactReviewResolutionResult struct {
	ImpactReview     ImpactReview       `json:"impact_review"`
	Decision         DependencyDecision `json:"dependency_decision"`
	RegenerationPlan *RegenerationPlan  `json:"regeneration_plan"`
	RunSnapshot      RunSnapshot        `json:"run_snapshot"`
}

type VersionResult struct {
	ArtifactVersion          ArtifactVersion               `json:"artifact_version"`
	Approval                 Approval                      `json:"approval"`
	ImpactReview             *ImpactReview                 `json:"impact_review"`
	Propagation              *ImpactReviewResolutionResult `json:"propagation,omitempty"`
	HandoffRefreshRequired   bool                          `json:"handoff_refresh_required,omitempty"`
	PendingRefreshScopes     []string                      `json:"pending_refresh_scopes,omitempty"`
	PendingRefreshVersionIDs []string                      `json:"pending_refresh_version_ids,omitempty"`
}

type ScriptEditCompletionResult struct {
	RunSnapshot    RunSnapshot `json:"run_snapshot"`
	RefreshTaskIDs []string    `json:"refresh_task_ids"`
	PendingScopes  []string    `json:"pending_scopes"`
}

type ArtifactCommitResult struct {
	CommitStatus    string                 `json:"commit_status"`
	TaskCheckpoint  *TaskResultCheckpoint  `json:"task_checkpoint,omitempty"`
	Artifact        Artifact               `json:"artifact"`
	ArtifactVersion ArtifactVersion        `json:"artifact_version"`
	Outputs         []ArtifactCommitOutput `json:"outputs,omitempty"`
	Approval        Approval               `json:"approval"`
	RunSnapshot     RunSnapshot            `json:"run_snapshot"`
}

type ArtifactCommitOutput struct {
	Artifact        Artifact        `json:"artifact"`
	ArtifactVersion ArtifactVersion `json:"artifact_version"`
}
