package capability

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type Manifest struct {
	Schema             string            `json:"$schema"`
	ID                 string            `json:"id"`
	Version            string            `json:"version"`
	DefinitionStatus   string            `json:"definition_status"`
	Label              string            `json:"label"`
	Description        string            `json:"description"`
	Kind               string            `json:"kind"`
	ExecutionMode      string            `json:"execution_mode"`
	InputBinding       InputBinding      `json:"input_binding,omitempty"`
	EntryPolicy        EntryPolicy       `json:"entry_policy"`
	Routing            Routing           `json:"routing"`
	AcceptedAssetKinds []string          `json:"accepted_asset_kinds"`
	RequiredProviders  []string          `json:"required_providers"`
	InputSchemaRef     string            `json:"input_schema_ref"`
	ConfigSchemaRefs   map[string]string `json:"config_schema_refs"`
	DefaultConfigRef   *string           `json:"default_config_ref"`
	DefaultRetry       RetryPolicy       `json:"default_retry"`
	ContextPolicyRef   string            `json:"context_policy_ref"`
	StateContract      *StateContract    `json:"state_contract,omitempty"`
	Commands           []string          `json:"commands"`
	UI                 UISeed            `json:"ui"`
	Completion         Completion        `json:"completion"`
	Steps              []Step            `json:"steps"`
}

type EntryPolicy struct {
	ExplicitInvocation       bool     `json:"explicit_invocation"`
	AutoRoute                bool     `json:"auto_route"`
	RequiresUserConfirmation bool     `json:"requires_user_confirmation"`
	InputCollectionModes     []string `json:"input_collection_modes"`
}

type Routing struct {
	ExplicitAliases []string `json:"explicit_aliases"`
	IntentExamples  []string `json:"intent_examples"`
	AmbiguityPolicy string   `json:"ambiguity_policy"`
}

type RetryPolicy struct {
	AutomaticAttempts int      `json:"automatic_attempts"`
	UserRetryAllowed  bool     `json:"user_retry_allowed"`
	ResumeFromCursor  bool     `json:"resume_from_cursor"`
	RetryableErrors   []string `json:"retryable_errors"`
}

// InputBinding tells a generic host how uploaded material maps into a
// capability input contract. The capability owns this data so hosts do not
// branch on capability IDs.
type InputBinding struct {
	SourceType      string `json:"source_type,omitempty"`
	AssetRole       string `json:"asset_role,omitempty"`
	AssetSetPurpose string `json:"asset_set_purpose,omitempty"`
}

type UISeed struct {
	IconKey       string `json:"icon_key"`
	SortOrder     int    `json:"sort_order"`
	EntryViewKey  string `json:"entry_view_key"`
	ConfigViewKey string `json:"config_view_key,omitempty"`
	DefaultPrompt string `json:"default_prompt,omitempty"`
}

// ArtifactPresentation describes how the Agent shell presents an Artifact.
// Capabilities produce semantic Artifact types; they never ship frontend code.
type ArtifactPresentation struct {
	ArtifactType         string             `json:"artifact_type"`
	Label                string             `json:"label"`
	Description          string             `json:"description"`
	Renderer             string             `json:"renderer"`
	Editable             bool               `json:"editable"`
	PreferredFields      []string           `json:"preferred_fields"`
	Navigation           ArtifactNavigation `json:"navigation"`
	AvailableActions     []string           `json:"available_actions"`
	CollectionMemberType string             `json:"collection_member_type,omitempty"`
}

type ArtifactNavigation struct {
	Order      int    `json:"order"`
	GroupMode  string `json:"group_mode"`
	Visibility string `json:"visibility"`
}

type Completion struct {
	TerminalStepIDs          []string             `json:"terminal_step_ids"`
	RequiredArtifacts        []CompletionArtifact `json:"required_artifacts"`
	RequiresTerminalApproval bool                 `json:"requires_terminal_approval"`
}

type CompletionArtifact struct {
	ArtifactType   string `json:"artifact_type"`
	RequiredStatus string `json:"required_status"`
	Coverage       string `json:"coverage"`
}

type Step struct {
	ID                      string           `json:"id"`
	Kind                    string           `json:"kind"`
	ExecutorRef             string           `json:"executor_ref"`
	ConfigRef               *string          `json:"config_ref,omitempty"`
	ConfigRefSet            bool             `json:"-"`
	InputRefs               []ArtifactRef    `json:"input_refs"`
	OutputRefs              []ArtifactOutput `json:"output_refs"`
	PromptRef               *string          `json:"prompt_ref"`
	RuleRefs                []string         `json:"rule_refs"`
	ResponseAdapterRef      *string          `json:"response_adapter_ref,omitempty"`
	ProviderResultSchemaRef *string          `json:"provider_result_schema_ref,omitempty"`
	ResultSchemaRef         *string          `json:"result_schema_ref,omitempty"`
	GatePolicyRef           *string          `json:"gate_policy_ref,omitempty"`
	QualityReviewRoutes     []string         `json:"quality_review_routes,omitempty"`
	RequiredContext         []string         `json:"required_context,omitempty"`
	ContextPolicyRef        string           `json:"context_policy_ref,omitempty"`
	Visibility              string           `json:"visibility,omitempty"`
	Retry                   *RetryPolicy     `json:"retry,omitempty"`
	Approval                Approval         `json:"approval"`
	Batch                   *BatchPolicy     `json:"batch,omitempty"`
	StateTransition         *StateTransition `json:"state_transition,omitempty"`
	Next                    []TransitionRef  `json:"next"`
}

type StateContract struct {
	Scope                 string `json:"scope"`
	SnapshotSchemaRef     string `json:"snapshot_schema_ref"`
	ReducerRef            string `json:"reducer_ref"`
	MaxSnapshotCharacters int    `json:"max_snapshot_characters"`
}

type StateTransition struct {
	Mode                 string              `json:"mode"`
	DeltaArtifactType    string              `json:"delta_artifact_type"`
	DeltaPointer         string              `json:"delta_pointer"`
	PreviousOutputPolicy string              `json:"previous_output_policy"`
	FieldMappings        []StateFieldMapping `json:"field_mappings"`
}

type StateFieldMapping struct {
	DeltaField string `json:"delta_field"`
	StateField string `json:"state_field"`
	Operation  string `json:"operation"`
}

func (s *Step) UnmarshalJSON(data []byte) error {
	type stepAlias Step
	var decoded stepAlias
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if err := ensureEOF(decoder); err != nil {
		return err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*s = Step(decoded)
	_, s.ConfigRefSet = fields["config_ref"]
	return nil
}

type ArtifactRef struct {
	ArtifactType   string `json:"artifact_type"`
	Cardinality    string `json:"cardinality"`
	RequiredStatus string `json:"required_status,omitempty"`
	VersionPolicy  string `json:"version_policy"`
}

type ArtifactOutput struct {
	ArtifactType  string `json:"artifact_type"`
	Cardinality   string `json:"cardinality"`
	InitialStatus string `json:"initial_status"`
	SchemaRef     string `json:"schema_ref"`
}

type Approval struct {
	Type                   string   `json:"type"`
	Scope                  string   `json:"scope,omitempty"`
	Required               bool     `json:"required"`
	RequiredWhen           *string  `json:"required_when,omitempty"`
	InvalidateOnNewVersion bool     `json:"invalidate_on_new_version"`
	Condition              *string  `json:"condition"`
	AllowedActions         []string `json:"allowed_actions,omitempty"`
}

type BatchPolicy struct {
	ItemKey           string                  `json:"item_key"`
	Execution         string                  `json:"execution"`
	FailurePolicy     string                  `json:"failure_policy"`
	Ordering          string                  `json:"ordering"`
	MaxItemsPerTask   int                     `json:"max_items_per_task,omitempty"`
	ConcurrencyPolicy string                  `json:"concurrency_policy,omitempty"`
	Preparation       *BatchInternalTaskStage `json:"preparation,omitempty"`
	TaskStage         *BatchInternalTaskStage `json:"task_stage,omitempty"`
}

type BatchInternalTaskStage struct {
	ID         string `json:"id"`
	PromptRef  string `json:"prompt_ref"`
	SchemaRef  string `json:"schema_ref"`
	ResultMode string `json:"result_mode,omitempty"`
}

type TransitionRef struct {
	When string
	To   string
}

func (t *TransitionRef) UnmarshalJSON(data []byte) error {
	var shorthand string
	if err := json.Unmarshal(data, &shorthand); err == nil {
		t.When = "completed"
		t.To = shorthand
		return nil
	}

	var explicit struct {
		When string `json:"when"`
		To   string `json:"to"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&explicit); err != nil {
		return fmt.Errorf("transition must be a step ID or condition object: %w", err)
	}
	t.When = explicit.When
	t.To = explicit.To
	return nil
}

type CompiledDefinition struct {
	ID                 string
	Version            string
	Label              string
	Description        string
	Kind               string
	ExecutionMode      string
	InputBinding       InputBinding
	AcceptedAssetKinds []string
	RequiredProviders  []string
	InputSchemaRef     string
	ConfigSchemaRefs   map[string]string
	EntryPolicy        EntryPolicy
	Routing            Routing
	Commands           []string
	UI                 UISeed
	Completion         Completion
	StateContract      *StateContract
	Steps              []CompiledStep
}

type CompiledStep struct {
	ID                      string
	Kind                    string
	ExecutorRef             string
	ConfigRef               *string
	InputRefs               []ArtifactRef
	OutputRefs              []ArtifactOutput
	PromptRef               *string
	RuleRefs                []string
	ResponseAdapterRef      *string
	ProviderResultSchemaRef *string
	ResultSchemaRef         *string
	GatePolicyRef           *string
	QualityReviewRoutes     []string
	RequiredContext         []string
	ContextPolicyRef        string
	Visibility              string
	Retry                   RetryPolicy
	Approval                CompiledApproval
	Batch                   *BatchPolicy
	StateTransition         *StateTransition
	Next                    []TransitionRef
}

type CompiledApproval struct {
	Type                   string
	Scope                  string
	Required               bool
	RequiredWhen           *string
	InvalidateOnNewVersion bool
	Condition              *string
	AllowedActions         []string
}

type Availability string

const (
	Available   Availability = "available"
	Unavailable Availability = "unavailable"
)

type Entry struct {
	CapabilityID string
	Definition   *CompiledDefinition
	Skill        *SkillPackage
	Status       Availability
	ReasonCode   string
	Message      string
	ContentRoot  string
	SourceFile   string
}

type PublicCapability struct {
	CapabilityID       string        `json:"capability_id"`
	Version            string        `json:"version"`
	Label              string        `json:"label"`
	Description        string        `json:"description"`
	Kind               string        `json:"kind"`
	ExecutionMode      string        `json:"execution_mode"`
	CreatesRun         bool          `json:"creates_run"`
	InputBinding       InputBinding  `json:"input_binding,omitempty"`
	Status             Availability  `json:"status"`
	ReasonCode         string        `json:"reason_code,omitempty"`
	UserMessage        string        `json:"user_message,omitempty"`
	AcceptedAssetKinds []string      `json:"accepted_asset_kinds"`
	DefaultConfigRef   string        `json:"default_config_ref,omitempty"`
	ConfigOptions      []string      `json:"config_options"`
	Commands           []string      `json:"commands"`
	UIEntry            PublicUIEntry `json:"ui_entry"`
	EntryPolicy        EntryPolicy   `json:"entry_policy"`
	Routing            PublicRouting `json:"routing"`
	Skill              *PublicSkill  `json:"skill,omitempty"`
}

type PublicUIEntry struct {
	IconKey       string `json:"icon_key"`
	MenuOrder     int    `json:"menu_order"`
	EntryViewKey  string `json:"entry_view_key"`
	ConfigViewKey string `json:"config_view_key,omitempty"`
	DefaultPrompt string `json:"default_prompt,omitempty"`
}

type PublicDefinition struct {
	PublicCapability
	InputSchema   json.RawMessage            `json:"input_schema,omitempty"`
	ConfigSchemas map[string]json.RawMessage `json:"config_schemas,omitempty"`
	Steps         []PublicStep               `json:"steps"`
	Completion    Completion                 `json:"completion"`
}

type PublicRouting struct {
	ExplicitAliases []string `json:"explicit_aliases"`
	IntentExamples  []string `json:"intent_examples"`
	AmbiguityPolicy string   `json:"ambiguity_policy"`
}

type PublicSkill struct {
	Name                    string            `json:"name"`
	Scope                   SkillScope        `json:"scope"`
	Path                    string            `json:"path"`
	ContentHash             string            `json:"content_hash"`
	AllowImplicitInvocation bool              `json:"allow_implicit_invocation"`
	Interface               SkillInterface    `json:"interface"`
	Dependencies            []SkillDependency `json:"dependencies"`
	Scripts                 []SkillScript     `json:"scripts"`
	Instructions            string            `json:"instructions,omitempty"`
}

type PublicStep struct {
	ID            string   `json:"step_id"`
	Kind          string   `json:"kind"`
	ArtifactTypes []string `json:"artifact_types"`
	ApprovalType  string   `json:"approval_type"`
	ApprovalScope string   `json:"approval_scope"`
	ResultKind    string   `json:"result_kind,omitempty"`
	Visibility    string   `json:"visibility"`
}
