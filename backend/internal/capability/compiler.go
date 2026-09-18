package capability

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var (
	capabilityIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	versionPattern      = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
)

// Contract names are closed; availability is checked separately below.
var knownTransitionConditions = []string{
	"completed", "approved", "user_continue", "user_stop",
	"incomplete_material_confirmed", "all_batch_items_succeeded", "failure",
	"volume_fit_sufficient", "expansion_strategy_confirmed",
	"adaptation_selection_and_creation_config_confirmed",
}

func compileManifest(manifest Manifest) (*CompiledDefinition, error) {
	if err := validateManifest(manifest); err != nil {
		return nil, err
	}

	definition := &CompiledDefinition{
		ID:                 manifest.ID,
		Version:            manifest.Version,
		Label:              manifest.Label,
		Description:        manifest.Description,
		Kind:               manifest.Kind,
		ExecutionMode:      manifest.ExecutionMode,
		InputBinding:       manifest.InputBinding,
		AcceptedAssetKinds: slices.Clone(manifest.AcceptedAssetKinds),
		RequiredProviders:  slices.Clone(manifest.RequiredProviders),
		InputSchemaRef:     manifest.InputSchemaRef,
		ConfigSchemaRefs:   cloneStringMap(manifest.ConfigSchemaRefs),
		EntryPolicy:        manifest.EntryPolicy,
		Routing:            manifest.Routing,
		Commands:           slices.Clone(manifest.Commands),
		UI:                 manifest.UI,
		Completion:         manifest.Completion,
		StateContract:      cloneStateContract(manifest.StateContract),
		Steps:              make([]CompiledStep, 0, len(manifest.Steps)),
	}

	for _, step := range manifest.Steps {
		configRef := cloneStringPointer(step.ConfigRef)
		if !step.ConfigRefSet {
			configRef = cloneStringPointer(manifest.DefaultConfigRef)
		}
		retry := manifest.DefaultRetry
		if step.Retry != nil {
			retry = *step.Retry
		}
		contextPolicyRef := step.ContextPolicyRef
		if contextPolicyRef == "" {
			contextPolicyRef = manifest.ContextPolicyRef
		}
		visibility := step.Visibility
		if visibility == "" {
			visibility = "user"
		}

		definition.Steps = append(definition.Steps, CompiledStep{
			ID:                      step.ID,
			Kind:                    step.Kind,
			ExecutorRef:             step.ExecutorRef,
			ConfigRef:               configRef,
			InputRefs:               slices.Clone(step.InputRefs),
			OutputRefs:              slices.Clone(step.OutputRefs),
			PromptRef:               cloneStringPointer(step.PromptRef),
			RuleRefs:                slices.Clone(step.RuleRefs),
			ResponseAdapterRef:      cloneStringPointer(step.ResponseAdapterRef),
			ProviderResultSchemaRef: cloneStringPointer(step.ProviderResultSchemaRef),
			ResultSchemaRef:         cloneStringPointer(step.ResultSchemaRef),
			GatePolicyRef:           cloneStringPointer(step.GatePolicyRef),
			QualityReviewRoutes:     slices.Clone(step.QualityReviewRoutes),
			RequiredContext:         slices.Clone(step.RequiredContext),
			ContextPolicyRef:        contextPolicyRef,
			Visibility:              visibility,
			Retry:                   retry,
			Approval:                compileApproval(step.Approval),
			Batch:                   cloneBatchPolicy(step.Batch),
			StateTransition:         cloneStateTransition(step.StateTransition),
			Next:                    slices.Clone(step.Next),
		})
	}

	return definition, nil
}

func validateManifest(manifest Manifest) error {
	var problems []string

	if manifest.Schema != "./manifest.schema.json" {
		problems = append(problems, `$schema must be "./manifest.schema.json"`)
	}
	if !capabilityIDPattern.MatchString(manifest.ID) {
		problems = append(problems, "id must use snake_case")
	}
	if !versionPattern.MatchString(manifest.Version) {
		problems = append(problems, "version must use semantic version format")
	}
	if manifest.DefinitionStatus != "design_contract" {
		problems = append(problems, "definition_status must be design_contract")
	}
	if !slices.Contains([]string{"agent_skill", "domain_workflow"}, manifest.Kind) {
		problems = append(problems, "kind must be agent_skill or domain_workflow")
	}
	if !slices.Contains([]string{"inline", "background_task", "stateful_workflow"}, manifest.ExecutionMode) {
		problems = append(problems, "execution_mode must be inline, background_task, or stateful_workflow")
	}
	if manifest.Kind == "domain_workflow" && manifest.ExecutionMode != "stateful_workflow" {
		problems = append(problems, "domain_workflow capabilities must use stateful_workflow execution_mode")
	}
	if manifest.Kind == "agent_skill" && manifest.ExecutionMode == "stateful_workflow" {
		problems = append(problems, "agent_skill capabilities cannot use stateful_workflow execution_mode")
	}
	if manifest.ExecutionMode == "stateful_workflow" &&
		(strings.TrimSpace(manifest.InputBinding.SourceType) == "" ||
			strings.TrimSpace(manifest.InputBinding.AssetRole) == "") {
		problems = append(problems, "stateful_workflow requires input_binding.source_type and input_binding.asset_role")
	}
	if manifest.InputBinding.SourceType != "" && !capabilityIDPattern.MatchString(manifest.InputBinding.SourceType) {
		problems = append(problems, "input_binding.source_type must use snake_case")
	}
	if manifest.InputBinding.AssetRole != "" && !capabilityIDPattern.MatchString(manifest.InputBinding.AssetRole) {
		problems = append(problems, "input_binding.asset_role must use snake_case")
	}
	if manifest.InputBinding.AssetSetPurpose != "" && !capabilityIDPattern.MatchString(manifest.InputBinding.AssetSetPurpose) {
		problems = append(problems, "input_binding.asset_set_purpose must use snake_case")
	}
	if strings.TrimSpace(manifest.Label) == "" || strings.TrimSpace(manifest.Description) == "" {
		problems = append(problems, "label and description are required")
	}
	if len(manifest.AcceptedAssetKinds) == 0 {
		problems = append(problems, "accepted_asset_kinds cannot be empty")
	}
	if len(manifest.RequiredProviders) == 0 {
		problems = append(problems, "required_providers cannot be empty")
	}
	if len(manifest.Commands) == 0 {
		problems = append(problems, "commands cannot be empty")
	}
	if len(manifest.ConfigSchemaRefs) == 0 {
		problems = append(problems, "config_schema_refs cannot be empty")
	}
	if manifest.DefaultConfigRef != nil {
		if _, ok := manifest.ConfigSchemaRefs[*manifest.DefaultConfigRef]; !ok {
			problems = append(problems, "default_config_ref is not registered")
		}
	}
	if manifest.ContextPolicyRef == "" {
		problems = append(problems, "context_policy_ref is required")
	}
	if manifest.StateContract != nil {
		contract := manifest.StateContract
		if contract.Scope != "run" || contract.SnapshotSchemaRef == "" ||
			contract.ReducerRef != "structured_state_reducer.v1" ||
			contract.MaxSnapshotCharacters < 1000 {
			problems = append(problems, "state_contract is incomplete")
		}
	}
	if len(manifest.EntryPolicy.InputCollectionModes) == 0 {
		problems = append(problems, "entry_policy.input_collection_modes cannot be empty")
	}
	if len(manifest.Routing.ExplicitAliases) == 0 || len(manifest.Routing.IntentExamples) == 0 {
		problems = append(problems, "routing aliases and intent examples cannot be empty")
	}
	if manifest.Routing.AmbiguityPolicy != "ask_user" {
		problems = append(problems, "routing.ambiguity_policy must be ask_user")
	}
	if manifest.UI.IconKey == "" || manifest.UI.EntryViewKey == "" {
		problems = append(problems, "ui icon_key and entry_view_key are required")
	}
	if len(manifest.Steps) == 0 {
		problems = append(problems, "steps cannot be empty")
	}

	stepIDs := make(map[string]struct{}, len(manifest.Steps))
	producedArtifacts := make(map[string]struct{})
	qualityReviewRouteOwners := make(map[string]string)
	hasQualityReview := false
	qualityReviewRoutes := []string{
		"source_analysis", "story_bible", "episode_plan", "script_generation", "user",
	}
	runControlActions := map[string]struct{}{
		"pause": {}, "resume": {}, "cancel": {}, "retry_failed": {},
	}
	for _, step := range manifest.Steps {
		if !capabilityIDPattern.MatchString(step.ID) {
			problems = append(problems, fmt.Sprintf("step %q has invalid id", step.ID))
		}
		if _, duplicate := stepIDs[step.ID]; duplicate {
			problems = append(problems, fmt.Sprintf("step %q is duplicated", step.ID))
		}
		stepIDs[step.ID] = struct{}{}
		if step.ExecutorRef == "" {
			problems = append(problems, fmt.Sprintf("step %q executor_ref is required", step.ID))
		}
		if step.ConfigRef != nil {
			if _, ok := manifest.ConfigSchemaRefs[*step.ConfigRef]; !ok {
				problems = append(problems, fmt.Sprintf("step %q uses unknown config_ref %q", step.ID, *step.ConfigRef))
			}
		}
		if step.Approval.Type == "" {
			problems = append(problems, fmt.Sprintf("step %q approval.type is required", step.ID))
		}
		if step.Approval.Condition != nil && !step.Approval.Required {
			problems = append(problems, fmt.Sprintf("step %q conditional approval must be required", step.ID))
		}
		for _, action := range step.Approval.AllowedActions {
			if _, isRunControl := runControlActions[action]; isRunControl {
				problems = append(problems, fmt.Sprintf(
					"step %q approval action %q must be declared as run/task control",
					step.ID,
					action,
				))
			}
		}
		if step.Kind == "review" {
			hasQualityReview = true
			if step.PromptRef == nil || strings.TrimSpace(*step.PromptRef) == "" {
				problems = append(problems, fmt.Sprintf("review step %q requires prompt_ref", step.ID))
			}
			if step.ResultSchemaRef == nil || strings.TrimSpace(*step.ResultSchemaRef) == "" {
				problems = append(problems, fmt.Sprintf("review step %q requires result_schema_ref", step.ID))
			}
			if step.GatePolicyRef == nil || strings.TrimSpace(*step.GatePolicyRef) == "" {
				problems = append(problems, fmt.Sprintf("review step %q requires gate_policy_ref", step.ID))
			}
			if len(step.OutputRefs) != 0 {
				problems = append(problems, fmt.Sprintf("review step %q cannot create artifact outputs", step.ID))
			}
			if step.Approval.Type != "conditional_review" ||
				step.Approval.Scope != "quality_review" ||
				!step.Approval.Required ||
				step.Approval.RequiredWhen == nil ||
				*step.Approval.RequiredWhen != "action_required" {
				problems = append(problems, fmt.Sprintf("review step %q requires action_required quality review approval", step.ID))
			}
			if step.Batch == nil || step.Batch.Execution != "parallel" ||
				step.Batch.MaxItemsPerTask <= 0 || step.Batch.MaxItemsPerTask > 5 {
				problems = append(problems, fmt.Sprintf("review step %q requires a bounded parallel batch policy", step.ID))
			}
		} else if step.ResultSchemaRef != nil || step.GatePolicyRef != nil {
			problems = append(problems, fmt.Sprintf("non-review step %q cannot declare review result or gate policy", step.ID))
		}
		for _, route := range step.QualityReviewRoutes {
			if !slices.Contains(qualityReviewRoutes, route) {
				problems = append(problems, fmt.Sprintf("step %q declares unknown quality review route %q", step.ID, route))
				continue
			}
			if step.Kind == "review" {
				problems = append(problems, fmt.Sprintf("review step %q cannot be its own regeneration target", step.ID))
			}
			if owner, duplicate := qualityReviewRouteOwners[route]; duplicate {
				problems = append(problems, fmt.Sprintf(
					"quality review route %q is assigned to both %q and %q", route, owner, step.ID,
				))
				continue
			}
			qualityReviewRouteOwners[route] = step.ID
		}
		if step.ProviderResultSchemaRef != nil && strings.TrimSpace(*step.ProviderResultSchemaRef) == "" {
			problems = append(problems, fmt.Sprintf("step %q provider_result_schema_ref cannot be empty", step.ID))
		}
		if step.ExecutorRef == "runtime.build_script_contexts" &&
			(step.ResponseAdapterRef == nil || strings.TrimSpace(*step.ResponseAdapterRef) == "") {
			problems = append(problems, fmt.Sprintf("step %q requires response_adapter_ref", step.ID))
		}
		if step.Kind == "batch" && step.Batch == nil {
			problems = append(problems, fmt.Sprintf("batch step %q requires batch policy", step.ID))
		}
		if step.Batch != nil {
			if step.Batch.Preparation != nil && step.Batch.TaskStage == nil {
				problems = append(
					problems,
					fmt.Sprintf("step %q batch preparation requires task_stage", step.ID),
				)
			}
			for label, stage := range map[string]*BatchInternalTaskStage{
				"preparation": step.Batch.Preparation,
				"task_stage":  step.Batch.TaskStage,
			} {
				if stage == nil {
					continue
				}
				if !capabilityIDPattern.MatchString(stage.ID) ||
					stage.PromptRef == "" || stage.SchemaRef == "" {
					problems = append(
						problems,
						fmt.Sprintf("step %q batch.%s is incomplete", step.ID, label),
					)
				}
				if stage.ResultMode != "" &&
					stage.ResultMode != "checkpoint" &&
					stage.ResultMode != "artifact" {
					problems = append(
						problems,
						fmt.Sprintf("step %q batch.%s has invalid result_mode", step.ID, label),
					)
				}
				if label == "preparation" && stage.ResultMode == "artifact" {
					problems = append(
						problems,
						fmt.Sprintf("step %q batch preparation cannot emit final artifact", step.ID),
					)
				}
			}
		}
		if step.StateTransition != nil {
			transition := step.StateTransition
			if manifest.StateContract == nil {
				problems = append(problems, fmt.Sprintf("step %q declares state_transition without state_contract", step.ID))
			}
			if transition.Mode != "read_write" || transition.DeltaArtifactType == "" ||
				!strings.HasPrefix(transition.DeltaPointer, "/") ||
				transition.PreviousOutputPolicy != "adjacent" || len(transition.FieldMappings) == 0 {
				problems = append(problems, fmt.Sprintf("step %q has incomplete state_transition", step.ID))
			}
			outputFound := false
			for _, output := range step.OutputRefs {
				outputFound = outputFound || output.ArtifactType == transition.DeltaArtifactType
			}
			if !outputFound {
				problems = append(problems, fmt.Sprintf("step %q state delta artifact is not an output", step.ID))
			}
			for _, mapping := range transition.FieldMappings {
				if mapping.DeltaField == "" || mapping.StateField == "" ||
					!slices.Contains([]string{"append_unique", "remove", "set_latest"}, mapping.Operation) {
					problems = append(problems, fmt.Sprintf("step %q has invalid state field mapping", step.ID))
				}
			}
		}
		for _, output := range step.OutputRefs {
			if output.ArtifactType == "" || output.SchemaRef == "" {
				problems = append(problems, fmt.Sprintf("step %q output artifact_type and schema_ref are required", step.ID))
			}
			producedArtifacts[output.ArtifactType] = struct{}{}
			if output.InitialStatus == "confirmed" &&
				!((step.Kind == "system" || step.Kind == "aggregate") && step.Approval.Type == "none") {
				problems = append(problems, fmt.Sprintf("step %q cannot create confirmed output directly", step.ID))
			}
		}
	}
	if hasQualityReview {
		for _, route := range qualityReviewRoutes {
			if _, ok := qualityReviewRouteOwners[route]; !ok {
				problems = append(problems, fmt.Sprintf("quality review route %q has no regeneration target", route))
			}
		}
	} else if len(qualityReviewRouteOwners) != 0 {
		problems = append(problems, "quality review routes require a review step")
	}

	for _, step := range manifest.Steps {
		targetsByCondition := make(map[string]string, len(step.Next))
		for _, transition := range step.Next {
			if transition.When == "" || transition.To == "" {
				problems = append(problems, fmt.Sprintf("step %q has incomplete transition", step.ID))
				continue
			}
			if !slices.Contains(knownTransitionConditions, transition.When) {
				problems = append(problems, fmt.Sprintf("step %q has unknown transition condition %q", step.ID, transition.When))
			}
			if transition.When == "user_stop" {
				problems = append(problems, fmt.Sprintf("step %q: user_stop is reserved and not executable; cancellation currently does not run successors", step.ID))
			}
			if previous, exists := targetsByCondition[transition.When]; exists && previous != transition.To {
				problems = append(problems, fmt.Sprintf("step %q condition %q has multiple targets %q and %q", step.ID, transition.When, previous, transition.To))
			}
			targetsByCondition[transition.When] = transition.To
			if transition.When == "failure" && transition.To == step.ID {
				problems = append(problems, fmt.Sprintf("step %q failure cannot target itself; use the retry policy for retrying the same step", step.ID))
			}
			if _, ok := stepIDs[transition.To]; !ok {
				problems = append(problems, fmt.Sprintf("step %q targets unknown step %q", step.ID, transition.To))
			}
		}
	}

	for _, terminalStepID := range manifest.Completion.TerminalStepIDs {
		if _, ok := stepIDs[terminalStepID]; !ok {
			problems = append(problems, fmt.Sprintf("completion targets unknown step %q", terminalStepID))
		}
	}
	for _, artifact := range manifest.Completion.RequiredArtifacts {
		if _, ok := producedArtifacts[artifact.ArtifactType]; !ok {
			problems = append(problems, fmt.Sprintf("completion requires unproduced artifact %q", artifact.ArtifactType))
		}
	}
	if len(manifest.Completion.TerminalStepIDs) == 0 || len(manifest.Completion.RequiredArtifacts) == 0 {
		problems = append(problems, "completion terminal steps and artifacts are required")
	}

	if len(manifest.Steps) > 0 {
		reachable := map[string]bool{manifest.Steps[0].ID: true}
		for changed := true; changed; {
			changed = false
			for _, step := range manifest.Steps {
				if !reachable[step.ID] {
					continue
				}
				for _, transition := range step.Next {
					if !reachable[transition.To] {
						reachable[transition.To] = true
						changed = true
					}
				}
			}
		}
		for _, step := range manifest.Steps {
			if !reachable[step.ID] {
				problems = append(problems, fmt.Sprintf("step %q is unreachable", step.ID))
			}
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

func cloneStateContract(source *StateContract) *StateContract {
	if source == nil {
		return nil
	}
	copy := *source
	return &copy
}

func cloneStateTransition(source *StateTransition) *StateTransition {
	if source == nil {
		return nil
	}
	copy := *source
	copy.FieldMappings = slices.Clone(source.FieldMappings)
	return &copy
}

func compileApproval(source Approval) CompiledApproval {
	scope := source.Scope
	actions := slices.Clone(source.AllowedActions)
	if scope == "" {
		switch source.Type {
		case "checkpoint":
			scope = "artifact"
		case "batch_checkpoint":
			scope = "batch"
		case "transition_checkpoint":
			scope = "transition"
		default:
			scope = "none"
		}
	}
	if actions == nil {
		switch source.Type {
		case "checkpoint", "batch_checkpoint":
			actions = []string{"approve", "edit_artifact", "request_ai_revision", "regenerate_artifact"}
		case "transition_checkpoint":
			actions = []string{"approve"}
		default:
			actions = []string{}
		}
	}
	if slices.Contains(actions, "edit_artifact") {
		for _, action := range []string{"request_ai_revision", "regenerate_artifact"} {
			if !slices.Contains(actions, action) {
				actions = append(actions, action)
			}
		}
	}
	return CompiledApproval{
		Type:                   source.Type,
		Scope:                  scope,
		Required:               source.Required,
		RequiredWhen:           cloneStringPointer(source.RequiredWhen),
		InvalidateOnNewVersion: source.InvalidateOnNewVersion,
		Condition:              cloneStringPointer(source.Condition),
		AllowedActions:         actions,
	}
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneStringMap(source map[string]string) map[string]string {
	target := make(map[string]string, len(source))
	for key, value := range source {
		target[key] = value
	}
	return target
}

func cloneBatchPolicy(source *BatchPolicy) *BatchPolicy {
	if source == nil {
		return nil
	}
	copy := *source
	if source.Preparation != nil {
		preparation := *source.Preparation
		copy.Preparation = &preparation
	}
	if source.TaskStage != nil {
		taskStage := *source.TaskStage
		copy.TaskStage = &taskStage
	}
	return &copy
}
