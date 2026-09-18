package agent

import "time"

type SourceMode string

const (
	SourceModeAuto     SourceMode = "auto"
	SourceModeNovel    SourceMode = "novel"
	SourceModeNonNovel SourceMode = "non_novel"
	SourceModeUnknown  SourceMode = "unknown"
)

type GenerationConfig struct {
	TargetEpisodeCount             int     `json:"target_episode_count,omitempty"`
	EpisodeDurationMinutes         float64 `json:"episode_duration_minutes,omitempty"`
	TargetScriptChars              int     `json:"target_script_chars,omitempty"`
	TargetSourceCharsPerEpisode    int     `json:"target_source_chars_per_episode,omitempty"`
	BoundaryDetectionWindowChars   int     `json:"boundary_detection_window_chars,omitempty"`
	PreserveExistingEpisodeMarks   bool    `json:"preserve_existing_episode_marks,omitempty"`
	ExistingEpisodeMarkersDetected bool    `json:"existing_episode_markers_detected,omitempty"`
	DetectedEpisodeCount           int     `json:"detected_episode_count,omitempty"`
}

func (c GenerationConfig) Empty() bool {
	return c.TargetEpisodeCount == 0 &&
		c.EpisodeDurationMinutes == 0 &&
		c.TargetScriptChars == 0 &&
		c.TargetSourceCharsPerEpisode == 0 &&
		c.BoundaryDetectionWindowChars == 0 &&
		!c.PreserveExistingEpisodeMarks &&
		!c.ExistingEpisodeMarkersDetected &&
		c.DetectedEpisodeCount == 0
}

type RunStatus string

const (
	RunPending         RunStatus = "pending"
	RunRunning         RunStatus = "running"
	RunWaitingApproval RunStatus = "waiting_approval"
	RunPaused          RunStatus = "paused"
	RunCompleted       RunStatus = "completed"
	RunFailed          RunStatus = "failed"
	RunCancelled       RunStatus = "cancelled"
)

type StepStatus string

const (
	StepPending         StepStatus = "pending"
	StepRunning         StepStatus = "running"
	StepWaitingApproval StepStatus = "waiting_approval"
	StepCompleted       StepStatus = "completed"
	StepFailed          StepStatus = "failed"
	StepSkipped         StepStatus = "skipped"
)

type ArtifactStatus string

const (
	ArtifactDraft           ArtifactStatus = "draft"
	ArtifactPendingApproval ArtifactStatus = "pending_approval"
	ArtifactConfirmed       ArtifactStatus = "confirmed"
	ArtifactStale           ArtifactStatus = "stale"
	ArtifactSuperseded      ArtifactStatus = "superseded"
	ArtifactInvalidated     ArtifactStatus = "invalidated"
)

type EventType string

const (
	EventRunStarted          EventType = "run_started"
	EventStepStarted         EventType = "step_started"
	EventArtifactCreated     EventType = "artifact_created"
	EventArtifactUpdated     EventType = "artifact_updated"
	EventScriptBatchInserted EventType = "script_batch_inserted"
	EventApprovalRequested   EventType = "approval_requested"
	EventApprovalResolved    EventType = "approval_resolved"
	EventProgressUpdated     EventType = "progress_updated"
	EventStepCompleted       EventType = "step_completed"
	EventRunCompleted        EventType = "run_completed"
	EventStepFailed          EventType = "step_failed"
	EventRunPaused           EventType = "run_paused"
	EventRunResumed          EventType = "run_resumed"
)

type Run struct {
	RunID             string                 `json:"run_id"`
	ProjectID         string                 `json:"project_id"`
	Intent            string                 `json:"intent"`
	SourceMode        SourceMode             `json:"source_mode"`
	Status            RunStatus              `json:"status"`
	CurrentStepID     string                 `json:"current_step_id"`
	NextAction        map[string]any         `json:"next_action"`
	CreatedArtifacts  []string               `json:"created_artifacts"`
	UpdatedArtifacts  []string               `json:"updated_artifacts"`
	Invalidated       []string               `json:"invalidated_artifacts"`
	ApprovalRequestID string                 `json:"approval_request_id,omitempty"`
	StartedAt         time.Time              `json:"started_at"`
	EndedAt           *time.Time             `json:"ended_at,omitempty"`
	Metadata          map[string]interface{} `json:"metadata,omitempty"`
}

type Artifact struct {
	ArtifactID   string         `json:"artifact_id"`
	ArtifactType string         `json:"artifact_type"`
	ProjectID    string         `json:"project_id"`
	RunID        string         `json:"run_id"`
	Version      int            `json:"version"`
	Status       ArtifactStatus `json:"status"`
	SourceMode   SourceMode     `json:"source_mode"`
	DerivedFrom  []string       `json:"derived_from"`
	Payload      map[string]any `json:"payload"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

type RunEvent struct {
	EventID       string         `json:"event_id"`
	RunID         string         `json:"run_id"`
	StepID        string         `json:"step_id,omitempty"`
	Type          EventType      `json:"type"`
	Message       string         `json:"message"`
	ArtifactRefs  []string       `json:"artifact_refs,omitempty"`
	FocusArtifact map[string]any `json:"focus_artifact,omitempty"`
	Payload       map[string]any `json:"payload,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
}

type ApprovalRequest struct {
	ApprovalRequestID string         `json:"approval_request_id"`
	RunID             string         `json:"run_id"`
	StepID            string         `json:"step_id"`
	Title             string         `json:"title"`
	Reason            string         `json:"reason"`
	ProposedAction    map[string]any `json:"proposed_action"`
	AffectedArtifacts []string       `json:"affected_artifacts"`
	RiskNotes         []string       `json:"risk_notes"`
	Options           []string       `json:"options"`
	Status            string         `json:"status"`
	UserResponse      string         `json:"user_response,omitempty"`
	CreatedAt         time.Time      `json:"created_at"`
	ResolvedAt        *time.Time     `json:"resolved_at,omitempty"`
}

type StartRunRequest struct {
	ProjectID        string            `json:"project_id"`
	UserMessage      string            `json:"user_message"`
	SourceText       string            `json:"source_text,omitempty"`
	SourceFiles      []SourceFile      `json:"source_files,omitempty"`
	SourceMode       SourceMode        `json:"source_mode,omitempty"`
	GenerationConfig *GenerationConfig `json:"generation_config,omitempty"`
}

type SourceFile struct {
	FileID   string `json:"file_id,omitempty"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

type ContinueRunRequest struct {
	Decision string `json:"decision"`
	Note     string `json:"note,omitempty"`
}

type RerunStepRequest struct {
	Reason string `json:"reason,omitempty"`
}

type StartRunResponse struct {
	Run             Run              `json:"run"`
	Events          []RunEvent       `json:"events"`
	Artifacts       []Artifact       `json:"artifacts"`
	ApprovalRequest *ApprovalRequest `json:"approval_request,omitempty"`
}

type ContinueRunResponse struct {
	Run       Run        `json:"run"`
	Events    []RunEvent `json:"events"`
	Artifacts []Artifact `json:"artifacts"`
}

type RerunStepResponse struct {
	Run       Run        `json:"run"`
	Events    []RunEvent `json:"events"`
	Artifacts []Artifact `json:"artifacts"`
}
