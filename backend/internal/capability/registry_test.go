package capability

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestLoadRegistryCompilesCanonicalManifests(t *testing.T) {
	registry, err := LoadRegistry(LoadOptions{ProjectRoot: testProjectRoot(t)})
	if err != nil {
		t.Fatalf("LoadRegistry() error = %v", err)
	}

	entries := registry.Entries()
	if len(entries) != 5 {
		t.Fatalf("entry count = %d, want 5", len(entries))
	}
	gotIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Status != Available {
			t.Errorf("entry %q status = %s, reason = %s, message = %s", entry.SourceFile, entry.Status, entry.ReasonCode, entry.Message)
			continue
		}
		gotIDs = append(gotIDs, entry.Definition.ID)
	}
	wantIDs := []string{"novel_to_script", "non_novel_to_script", "video_reference_creation", "script_continuation", "outline_critic"}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("capability order = %v, want %v", gotIDs, wantIDs)
	}

	novel, ok := registry.Get("novel_to_script")
	if !ok || novel.Definition == nil {
		t.Fatal("novel_to_script definition is missing")
	}
	if novel.Definition.ExecutionMode != "stateful_workflow" {
		t.Fatalf("novel execution mode = %q", novel.Definition.ExecutionMode)
	}
	if novel.Definition.Kind != "domain_workflow" {
		t.Fatalf("novel kind = %q", novel.Definition.Kind)
	}
	publicItems := registry.PublicList()
	publicIndex := slices.IndexFunc(publicItems, func(item PublicCapability) bool { return item.CapabilityID == "novel_to_script" })
	if publicIndex < 0 || publicItems[publicIndex].Kind != "domain_workflow" ||
		publicItems[publicIndex].ExecutionMode != "stateful_workflow" || !publicItems[publicIndex].CreatesRun {
		t.Fatalf("public capability execution contract = %+v", publicItems)
	}
	storyBible := findStep(t, novel.Definition, "build_story_bible")
	sourceManifest := findStep(t, novel.Definition, "build_source_manifest")
	if novel.Definition.Version != "1.4.0" ||
		sourceManifest.Kind != "system" ||
		sourceManifest.ExecutorRef != "runtime.build_source_manifest" ||
		len(sourceManifest.OutputRefs) != 1 ||
		sourceManifest.OutputRefs[0].ArtifactType != "source_manifest" ||
		sourceManifest.OutputRefs[0].InitialStatus != "confirmed" ||
		sourceManifest.Approval.Type != "none" {
		t.Fatalf("source manifest step = %+v, capability version = %s", sourceManifest, novel.Definition.Version)
	}
	if storyBible.ConfigRef == nil || *storyBible.ConfigRef != "creation" {
		t.Fatalf("build_story_bible config = %v, want creation", storyBible.ConfigRef)
	}
	if storyBible.Kind != "batch" ||
		storyBible.Batch == nil ||
		storyBible.Batch.Preparation == nil ||
		storyBible.Batch.Preparation.ID != "source_analysis" ||
		storyBible.Batch.Preparation.ResultMode != "checkpoint" ||
		storyBible.Batch.TaskStage == nil ||
		storyBible.Batch.TaskStage.ID != "story_bible_aggregate" ||
		storyBible.Batch.TaskStage.ResultMode != "artifact" {
		t.Fatalf("build_story_bible batch stages = %+v", storyBible.Batch)
	}
	if storyBible.Approval.Scope != "artifact" {
		t.Fatalf("build_story_bible approval scope = %q, want artifact", storyBible.Approval.Scope)
	}
	if !slices.Contains(storyBible.Approval.AllowedActions, "edit_artifact") {
		t.Fatalf("build_story_bible actions = %v, want edit_artifact", storyBible.Approval.AllowedActions)
	}
	for _, entry := range entries {
		for _, step := range entry.Definition.Steps {
			for _, action := range []string{"pause", "resume", "cancel", "retry_failed"} {
				if slices.Contains(step.Approval.AllowedActions, action) {
					t.Fatalf(
						"capability %q step %q exposes run control %q as approval action",
						entry.Definition.ID,
						step.ID,
						action,
					)
				}
			}
		}
	}
	splitEpisodes := findStep(t, novel.Definition, "split_episodes")
	if splitEpisodes.Batch == nil ||
		splitEpisodes.Batch.Preparation == nil ||
		splitEpisodes.Batch.Preparation.ID != "global_plan" ||
		splitEpisodes.Batch.TaskStage == nil ||
		splitEpisodes.Batch.TaskStage.ID != "boundary_batch" {
		t.Fatalf("split_episodes batch stages = %+v", splitEpisodes.Batch)
	}
	if len(splitEpisodes.InputRefs) != 3 ||
		splitEpisodes.InputRefs[0].ArtifactType != "source_input" ||
		splitEpisodes.InputRefs[1].ArtifactType != "story_bible" ||
		splitEpisodes.InputRefs[2].ArtifactType != "source_manifest" {
		t.Fatalf("split_episodes inputs = %+v", splitEpisodes.InputRefs)
	}
	scriptGeneration := findStep(t, novel.Definition, "generate_script_units")
	if len(scriptGeneration.Next) != 1 || scriptGeneration.Next[0].To != "review_script_set" {
		t.Fatalf("generate_script_units next = %+v", scriptGeneration.Next)
	}
	qualityReview := findStep(t, novel.Definition, "review_script_set")
	if qualityReview.Kind != "review" ||
		qualityReview.ExecutorRef != "workflow.shared_script_quality_review" ||
		qualityReview.Visibility != "internal" ||
		qualityReview.Batch == nil || qualityReview.Batch.Execution != "parallel" ||
		qualityReview.Batch.MaxItemsPerTask != 5 ||
		qualityReview.ResultSchemaRef == nil ||
		*qualityReview.ResultSchemaRef != "../../schemas/v1/quality-review.schema.json" ||
		qualityReview.GatePolicyRef == nil || *qualityReview.GatePolicyRef != "quality.script.v1" ||
		len(qualityReview.Next) != 1 || qualityReview.Next[0].To != "aggregate_scripts" {
		t.Fatalf("quality review step = %+v", qualityReview)
	}
	publicDefinition, ok := registry.PublicDefinition("novel_to_script")
	if !ok {
		t.Fatal("novel_to_script public definition is missing")
	}
	for _, step := range publicDefinition.Steps {
		if step.ID == "review_script_set" {
			t.Fatal("public definition still exposes automatic quality review")
		}
	}

	nonNovel, ok := registry.Get("non_novel_to_script")
	if !ok || nonNovel.Definition == nil {
		t.Fatal("non_novel_to_script definition is missing")
	}
	nonNovelManifest := findStep(t, nonNovel.Definition, "build_source_manifest")
	materialBank := findStep(t, nonNovel.Definition, "build_material_bank")
	if nonNovel.Definition.Version != "1.3.0" ||
		nonNovelManifest.Kind != "system" ||
		nonNovelManifest.ExecutorRef != "runtime.build_source_manifest" ||
		nonNovelManifest.Approval.Type != "none" ||
		len(materialBank.InputRefs) != 2 ||
		materialBank.InputRefs[1].ArtifactType != "source_manifest" ||
		materialBank.InputRefs[1].VersionPolicy != "exact" ||
		materialBank.ProviderResultSchemaRef == nil ||
		!strings.Contains(*materialBank.ProviderResultSchemaRef, "materialBankProviderResult") {
		t.Fatalf(
			"non-novel source manifest = %+v, material bank = %+v, capability version = %s",
			nonNovelManifest,
			materialBank,
			nonNovel.Definition.Version,
		)
	}

	video, ok := registry.Get("video_reference_creation")
	if !ok || video.Definition == nil {
		t.Fatal("video_reference_creation definition is missing")
	}
	brief := findStep(t, video.Definition, "build_adaptation_brief")
	if brief.ConfigRef == nil || *brief.ConfigRef != "creation" {
		t.Fatalf("build_adaptation_brief config = %v, want creation", brief.ConfigRef)
	}
	if !slices.Contains(brief.RequiredContext, "decision_snapshot") {
		t.Fatalf("build_adaptation_brief context = %v, want decision_snapshot", brief.RequiredContext)
	}
	extract := findStep(t, video.Definition, "extract_video_scripts")
	for _, action := range []string{"approve", "edit_artifact", "request_ai_revision", "regenerate_artifact"} {
		if !slices.Contains(extract.Approval.AllowedActions, action) {
			t.Fatalf("extract_video_scripts approval actions = %v, missing %s", extract.Approval.AllowedActions, action)
		}
	}
	continuation, ok := registry.Get("script_continuation")
	if !ok || continuation.Definition == nil {
		t.Fatal("script_continuation definition is missing")
	}
	options := findStep(t, continuation.Definition, "generate_continuation_options")
	script := findStep(t, continuation.Definition, "generate_continuation_script")
	if continuation.Definition.Version != "1.0.0" || len(options.OutputRefs) != 1 ||
		options.OutputRefs[0].ArtifactType != "continuation_options" ||
		!slices.Contains(options.Approval.AllowedActions, "select_single_option") ||
		!slices.Contains(script.RequiredContext, "decision_snapshot") ||
		len(script.InputRefs) != 2 || script.InputRefs[1].ArtifactType != "continuation_options" {
		t.Fatalf("script continuation contract = options %+v, script %+v", options, script)
	}

	critic, ok := registry.Get("outline_critic")
	if !ok || critic.Definition == nil || critic.Skill == nil {
		t.Fatal("outline_critic standard Skill is missing")
	}
	if critic.Definition.Kind != "agent_skill" || critic.Definition.ExecutionMode != "inline" ||
		critic.Definition.EntryPolicy.RequiresUserConfirmation || critic.Skill.Instructions == "" {
		t.Fatalf("outline_critic contract = %+v, skill = %+v", critic.Definition, critic.Skill)
	}
	criticIndex := slices.IndexFunc(publicItems, func(item PublicCapability) bool {
		return item.CapabilityID == "outline_critic"
	})
	if criticIndex < 0 || publicItems[criticIndex].CreatesRun || publicItems[criticIndex].Skill == nil ||
		publicItems[criticIndex].Skill.Instructions != "" || len(publicItems[criticIndex].Routing.IntentExamples) == 0 {
		t.Fatalf("outline_critic public summary = %+v", publicItems)
	}
	criticDetail, ok := registry.PublicDefinition("outline_critic")
	if !ok || criticDetail.Skill == nil || criticDetail.Skill.Instructions == "" {
		t.Fatalf("outline_critic progressive detail = %+v", criticDetail)
	}

	novelDetail, ok, err := registry.PublicDefinitionWithSchemas("novel_to_script")
	if err != nil || !ok {
		t.Fatalf("PublicDefinitionWithSchemas() ok = %v, error = %v", ok, err)
	}
	if len(novelDetail.InputSchema) == 0 || len(novelDetail.ConfigSchemas["creation"]) == 0 {
		t.Fatalf("novel schema projection = %+v", novelDetail)
	}
	if bytes.Contains(novelDetail.InputSchema, []byte(`"$ref"`)) ||
		bytes.Contains(novelDetail.ConfigSchemas["creation"], []byte(`"$ref"`)) {
		t.Fatal("public schema projection still contains unresolved references")
	}
	var creationSchema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(novelDetail.ConfigSchemas["creation"], &creationSchema); err != nil {
		t.Fatalf("decode creation schema: %v", err)
	}
	var episodeCountSchema struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(creationSchema.Properties["target_episode_count"], &episodeCountSchema); err != nil ||
		episodeCountSchema.Type != "integer" {
		t.Fatalf("expanded target_episode_count schema = %+v, error = %v", episodeCountSchema, err)
	}
}

func TestPublicSchemaProjectionRejectsCyclesAndEscapes(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(source, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cycle := filepath.Join(root, "cycle.json")
	if err := os.WriteFile(cycle, []byte(`{"$ref":"cycle.json"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPublicJSONSchema(root, source, "cycle.json"); err == nil ||
		!strings.Contains(err.Error(), "cyclic schema reference") {
		t.Fatalf("cycle error = %v", err)
	}
	if _, err := loadPublicJSONSchema(root, source, "../outside.json"); err == nil ||
		!strings.Contains(err.Error(), "escapes project root") {
		t.Fatalf("escape error = %v", err)
	}
}

func TestCompilerAllowsCustomQualityReviewStepID(t *testing.T) {
	manifest := minimalManifest()
	resultSchema := "quality-review.json"
	gatePolicy := "quality.script.v1"
	requiredWhen := "action_required"
	promptRef := "quality-review.md"
	manifest.Completion.TerminalStepIDs = []string{"custom_review_gate"}
	manifest.Steps[0].QualityReviewRoutes = []string{
		"source_analysis", "story_bible", "episode_plan", "script_generation", "user",
	}
	manifest.Steps[0].Next = []TransitionRef{{When: "completed", To: "custom_review_gate"}}
	manifest.Steps = append(manifest.Steps, Step{
		ID:              "custom_review_gate",
		Kind:            "review",
		ExecutorRef:     "workflow.shared_script_quality_review",
		InputRefs:       []ArtifactRef{},
		OutputRefs:      []ArtifactOutput{},
		PromptRef:       &promptRef,
		ResultSchemaRef: &resultSchema,
		GatePolicyRef:   &gatePolicy,
		Approval: Approval{
			Type:                   "conditional_review",
			Scope:                  "quality_review",
			Required:               true,
			RequiredWhen:           &requiredWhen,
			InvalidateOnNewVersion: true,
		},
		Batch: &BatchPolicy{
			ItemKey:         "episode_range",
			Execution:       "parallel",
			FailurePolicy:   "preserve_success_retry_failed",
			Ordering:        "natural_episode_order",
			MaxItemsPerTask: 5,
		},
		Next: []TransitionRef{},
	})

	definition, err := compileManifest(manifest)
	if err != nil {
		t.Fatalf("compileManifest() error = %v", err)
	}
	if definition.Steps[1].ID != "custom_review_gate" ||
		definition.Steps[1].ExecutorRef != "workflow.shared_script_quality_review" {
		t.Fatalf("compiled custom review = %+v", definition.Steps[1])
	}
}

func TestCompilerRejectsStatefulWorkflowWithoutInputBinding(t *testing.T) {
	manifest := minimalManifest()
	manifest.InputBinding = InputBinding{}
	if _, err := compileManifest(manifest); err == nil ||
		!strings.Contains(err.Error(), "input_binding.source_type") {
		t.Fatalf("compileManifest() error = %v, want input binding requirement", err)
	}
}

func TestCompilerRejectsScriptContextExecutorWithoutAdapter(t *testing.T) {
	manifest := minimalManifest()
	manifest.Steps[0].ExecutorRef = "runtime.build_script_contexts"
	if _, err := compileManifest(manifest); err == nil ||
		!strings.Contains(err.Error(), "requires response_adapter_ref") {
		t.Fatalf("compileManifest() error = %v, want response adapter requirement", err)
	}
}

func TestRegistryAddsManagedSkillRootAndClearsState(t *testing.T) {
	root := t.TempDir()
	managedRoot := filepath.Join(root, "managed")
	writeTestSkill(t, managedRoot, "managed-skill", "Managed.", "1.0.0")
	registry := NewEmptyRegistry()
	registry.projectRoot = root
	if err := registry.AddSkillRoot(SkillRoot{
		Scope: SkillScopeWorkspace, Path: managedRoot, Priority: 250,
	}); err != nil {
		t.Fatalf("AddSkillRoot() error = %v", err)
	}
	if !registry.SetSkillEnabled("managed_skill", false) ||
		!registry.PinSkillVersion("managed_skill", "1.0.0") {
		t.Fatal("managed Skill state was not accepted")
	}
	registry.ClearSkillState("managed_skill")
	if err := registry.RefreshSkills(); err != nil {
		t.Fatal(err)
	}
	entry, ok := registry.Get("managed_skill")
	if !ok || entry.Status != Available {
		t.Fatalf("cleared managed Skill = %+v", entry)
	}
}

func TestRegistryMarksUnresolvedSkillToolDependencyUnavailable(t *testing.T) {
	root := t.TempDir()
	skillRoot := filepath.Join(root, "skills")
	directory := writeTestSkill(t, skillRoot, "tool-skill", "Uses one trusted tool.", "1.0.0")
	if err := os.MkdirAll(filepath.Join(directory, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := `dependencies:
  tools:
    - type: mcp
      value: story-data/lookup_fact
`
	if err := os.WriteFile(filepath.Join(directory, "agents", "openai.yaml"), []byte(metadata), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := NewEmptyRegistry()
	registry.projectRoot = root
	registry.skillRoots = []SkillRoot{{Scope: SkillScopeWorkspace, Path: skillRoot, Priority: 200}}
	if err := registry.RefreshSkills(); err != nil {
		t.Fatal(err)
	}
	entry, ok := registry.Get("tool_skill")
	if !ok || entry.Status != Unavailable || entry.ReasonCode != "SKILL_TOOL_REGISTRY_UNAVAILABLE" {
		t.Fatalf("entry without resolver = %+v", entry)
	}

	if err := registry.SetSkillDependencyResolver(availableDependencyResolver{}); err != nil {
		t.Fatal(err)
	}
	entry, ok = registry.Get("tool_skill")
	if !ok || entry.Status != Available || entry.Skill.Dependencies[0].Status != Available {
		t.Fatalf("entry with resolver = %+v", entry)
	}
}

type availableDependencyResolver struct{}

func (availableDependencyResolver) ResolveSkillDependencies(dependencies []SkillDependency) SkillDependencyResolution {
	resolved := slices.Clone(dependencies)
	for index := range resolved {
		resolved[index].Status = Available
	}
	return SkillDependencyResolution{Dependencies: resolved, Available: true}
}

func TestLegacyPlatformContractMatchesGolden(t *testing.T) {
	registry, err := LoadRegistry(LoadOptions{ProjectRoot: testProjectRoot(t)})
	if err != nil {
		t.Fatalf("LoadRegistry() error = %v", err)
	}
	definitions := make([]*CompiledDefinition, 0, 4)
	for _, entry := range registry.Entries() {
		if entry.Skill == nil && entry.Definition != nil {
			definitions = append(definitions, entry.Definition)
		}
	}
	actual, err := json.MarshalIndent(struct {
		Capabilities          []*CompiledDefinition  `json:"capabilities"`
		ArtifactPresentations []ArtifactPresentation `json:"artifact_presentations"`
	}{
		Capabilities:          definitions,
		ArtifactPresentations: registry.PublicArtifactPresentations(),
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal platform contract: %v", err)
	}
	actual = append(actual, '\n')

	goldenFile := filepath.Join("testdata", "platform-contract-golden.json")
	if os.Getenv("CONTENT_AGENT_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenFile), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenFile, actual, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.ReadFile(goldenFile)
	if err != nil {
		t.Fatalf("read platform contract golden: %v", err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatal("legacy platform contract changed; inspect the diff before updating CONTENT_AGENT_UPDATE_GOLDEN=1")
	}
}

func TestPublicProjectionDoesNotExposeExecutionInternals(t *testing.T) {
	registry, err := LoadRegistry(LoadOptions{ProjectRoot: testProjectRoot(t)})
	if err != nil {
		t.Fatalf("LoadRegistry() error = %v", err)
	}
	definition, ok := registry.PublicDefinition("novel_to_script")
	if !ok {
		t.Fatal("public definition is missing")
	}
	data, err := json.Marshal(definition)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	text := string(data)
	for _, forbidden := range []string{"prompt_ref", "rule_refs", "executor_ref", "schema_ref", "novel2script_agent_project"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("public projection leaks %q: %s", forbidden, text)
		}
	}
}

func TestValidateContentFileEnforcesLocalAssetRoots(t *testing.T) {
	projectRoot := t.TempDir()
	manifestFile := filepath.Join(projectRoot, "capabilities", "v1", "test.json")
	promptFile := filepath.Join(projectRoot, "design", "prompts", "test.v1.md")
	ruleFile := filepath.Join(projectRoot, "design", "rules", "test.v1.md")
	for _, file := range []string{manifestFile, promptFile, ruleFile} {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", file, err)
		}
		if err := os.WriteFile(file, []byte("# test\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", file, err)
		}
	}

	if err := validateContentFile(
		projectRoot,
		manifestFile,
		"../../design/prompts/test.v1.md",
		"design/prompts",
	); err != nil {
		t.Fatalf("local Prompt rejected: %v", err)
	}
	if err := validateContentFile(
		projectRoot,
		manifestFile,
		"../../design/rules/test.v1.md",
		"design/rules",
	); err != nil {
		t.Fatalf("local Rule rejected: %v", err)
	}
	if err := validateContentFile(
		projectRoot,
		manifestFile,
		"../../design/rules/test.v1.md",
		"design/prompts",
	); err == nil || !strings.Contains(err.Error(), "design/prompts") {
		t.Fatalf("cross-root Prompt error = %v", err)
	}
	if err := validateContentFile(
		projectRoot,
		manifestFile,
		"../../novel2script_agent_project/design/prompts/test.v1.md",
		"design/prompts",
	); err == nil || !strings.Contains(err.Error(), "legacy project") {
		t.Fatalf("legacy Prompt error = %v", err)
	}
}

func TestCompileManifestRejectsUnknownTransition(t *testing.T) {
	manifest := minimalManifest()
	manifest.Steps[0].Next = []TransitionRef{{When: "completed", To: "missing_step"}}
	if _, err := compileManifest(manifest); err == nil || !strings.Contains(err.Error(), "unknown step") {
		t.Fatalf("compileManifest() error = %v, want unknown step", err)
	}
}

func TestCompileManifestRejectsPreparationWithoutTaskStage(t *testing.T) {
	manifest := minimalManifest()
	manifest.Steps[0].Kind = "batch"
	manifest.Steps[0].Batch = &BatchPolicy{
		ItemKey:       "episode_no",
		Execution:     "sequential",
		FailurePolicy: "preserve_success_retry_failed",
		Ordering:      "natural_episode_order",
		Preparation: &BatchInternalTaskStage{
			ID:        "global_plan",
			PromptRef: "global.md",
			SchemaRef: "global.json",
		},
	}
	if _, err := compileManifest(manifest); err == nil ||
		!strings.Contains(err.Error(), "requires task_stage") {
		t.Fatalf("compileManifest() error = %v, want task_stage requirement", err)
	}
}

func TestStepExplicitNullConfigDoesNotInheritDefault(t *testing.T) {
	var step Step
	if err := json.Unmarshal([]byte(`{
		"id": "first_step",
		"kind": "aggregate",
		"executor_ref": "runtime.test",
		"config_ref": null,
		"input_refs": [],
		"output_refs": [{
			"artifact_type": "result",
			"cardinality": "one",
			"initial_status": "confirmed",
			"schema_ref": "result.json"
		}],
		"prompt_ref": null,
		"rule_refs": [],
		"approval": {
			"type": "none",
			"required": false,
			"invalidate_on_new_version": false,
			"condition": null
		},
		"next": []
	}`), &step); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if !step.ConfigRefSet {
		t.Fatal("explicit null config_ref was not recorded as present")
	}

	manifest := minimalManifest()
	manifest.Steps = []Step{step}
	definition, err := compileManifest(manifest)
	if err != nil {
		t.Fatalf("compileManifest() error = %v", err)
	}
	if definition.Steps[0].ConfigRef != nil {
		t.Fatalf("compiled config_ref = %v, want nil", definition.Steps[0].ConfigRef)
	}
}

func TestUnavailableEntryKeepsManifestCapabilityID(t *testing.T) {
	registry := NewEmptyRegistry()
	registry.addUnavailable("broken_capability", "different-file-name.json", "MANIFEST_INVALID", nilError{})
	got := registry.PublicList()
	if len(got) != 1 || got[0].CapabilityID != "broken_capability" {
		t.Fatalf("public capability = %+v, want broken_capability", got)
	}
}

func TestResolveProjectPathRejectsEscape(t *testing.T) {
	root := testProjectRoot(t)
	source := filepath.Join(root, "capabilities", "v1", "novel-to-script.json")
	if _, err := resolveProjectPath(root, source, "../../../outside.txt"); err == nil {
		t.Fatal("resolveProjectPath() accepted a path outside project root")
	}
}

type nilError struct{}

func (nilError) Error() string {
	return "invalid"
}

func findStep(t *testing.T, definition *CompiledDefinition, id string) CompiledStep {
	t.Helper()
	for _, step := range definition.Steps {
		if step.ID == id {
			return step
		}
	}
	t.Fatalf("step %q is missing", id)
	return CompiledStep{}
}

func findPublicStep(t *testing.T, definition PublicDefinition, id string) PublicStep {
	t.Helper()
	for _, step := range definition.Steps {
		if step.ID == id {
			return step
		}
	}
	t.Fatalf("public step %q is missing", id)
	return PublicStep{}
}

func testProjectRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func minimalManifest() Manifest {
	defaultConfig := "creation"
	return Manifest{
		Schema:           "./manifest.schema.json",
		ID:               "test_capability",
		Version:          "1.0.0",
		DefinitionStatus: "design_contract",
		Label:            "测试能力",
		Description:      "测试能力定义",
		Kind:             "domain_workflow",
		ExecutionMode:    "stateful_workflow",
		InputBinding: InputBinding{
			SourceType: "test_source",
			AssetRole:  "primary_source",
		},
		AcceptedAssetKinds: []string{"text"},
		RequiredProviders:  []string{"content_model_provider"},
		InputSchemaRef:     "input.json",
		ConfigSchemaRefs:   map[string]string{"creation": "config.json"},
		DefaultConfigRef:   &defaultConfig,
		DefaultRetry: RetryPolicy{
			AutomaticAttempts: 1,
			UserRetryAllowed:  true,
			ResumeFromCursor:  true,
		},
		ContextPolicyRef: "context.test.v1",
		Commands:         []string{"start"},
		EntryPolicy: EntryPolicy{
			ExplicitInvocation:       true,
			RequiresUserConfirmation: true,
			InputCollectionModes:     []string{"fixed"},
		},
		Routing: Routing{
			ExplicitAliases: []string{"测试能力"},
			IntentExamples:  []string{"执行测试能力"},
			AmbiguityPolicy: "ask_user",
		},
		UI: UISeed{
			IconKey:      "test",
			EntryViewKey: "test",
		},
		Completion: Completion{
			TerminalStepIDs: []string{"first_step"},
			RequiredArtifacts: []CompletionArtifact{{
				ArtifactType:   "result",
				RequiredStatus: "confirmed",
				Coverage:       "one",
			}},
		},
		Steps: []Step{{
			ID:          "first_step",
			Kind:        "aggregate",
			ExecutorRef: "runtime.test",
			InputRefs:   []ArtifactRef{},
			OutputRefs: []ArtifactOutput{{
				ArtifactType:  "result",
				Cardinality:   "one",
				InitialStatus: "confirmed",
				SchemaRef:     "result.json",
			}},
			RuleRefs: []string{},
			Approval: Approval{
				Type: "none",
			},
			Next: []TransitionRef{},
		}},
	}
}

func TestArtifactPresentationRegistryCoversSpecializedAndGenericArtifacts(t *testing.T) {
	registry, err := LoadRegistry(LoadOptions{ProjectRoot: testProjectRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	items := registry.PublicArtifactPresentations()
	byType := make(map[string]ArtifactPresentation, len(items))
	for _, item := range items {
		byType[item.ArtifactType] = item
	}
	if byType["script_unit"].Renderer != "script" || byType["script_unit"].Navigation.GroupMode != "episode_directory" {
		t.Fatalf("script presentation = %+v", byType["script_unit"])
	}
	if byType["video_script_unit"].Renderer != "video_script" {
		t.Fatalf("video script presentation = %+v", byType["video_script_unit"])
	}
	if byType["generic_document"].Renderer != "document" || byType["generic_table"].Renderer != "table" {
		t.Fatalf("generic presentations are missing: document=%+v table=%+v", byType["generic_document"], byType["generic_table"])
	}
	if byType["structured_run_state"].Navigation.Visibility != "internal" || byType["structured_run_state"].Editable {
		t.Fatalf("structured run state must remain internal and read-only: %+v", byType["structured_run_state"])
	}
}

func TestCompilerExpressesNonRunAgentSkill(t *testing.T) {
	manifest := minimalManifest()
	manifest.Kind = "agent_skill"
	manifest.ExecutionMode = "inline"
	definition, err := compileManifest(manifest)
	if err != nil {
		t.Fatalf("compile inline agent Skill: %v", err)
	}
	if definition.Kind != "agent_skill" || definition.ExecutionMode != "inline" {
		t.Fatalf("compiled agent Skill = kind %q, execution mode %q", definition.Kind, definition.ExecutionMode)
	}

	manifest.ExecutionMode = "stateful_workflow"
	if _, err := compileManifest(manifest); err == nil || !strings.Contains(err.Error(), "agent_skill capabilities cannot use stateful_workflow") {
		t.Fatalf("invalid agent Skill mode error = %v", err)
	}
}
