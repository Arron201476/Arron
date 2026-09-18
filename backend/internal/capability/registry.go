package capability

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
)

type LoadOptions struct {
	ProjectRoot   string
	CapabilityDir string
	SkillRoots    []SkillRoot
}

type Registry struct {
	mu                   sync.RWMutex
	entries              map[string]Entry
	order                []string
	legacyEntries        map[string]Entry
	legacyOrder          []string
	projectRoot          string
	presentations        []ArtifactPresentation
	skillRoots           []SkillRoot
	snapshotSkills       []*SkillPackage
	skillDiagnostics     []SkillDiagnostic
	disabledSkills       map[string]bool
	pinnedSkillVersions  map[string]string
	pinnedSkillHashes    map[string]string
	dependencyResolver   SkillDependencyResolver
	availabilityResolver CapabilityAvailabilityResolver
}

type SkillDependencyResolution struct {
	Dependencies []SkillDependency
	Available    bool
	ReasonCode   string
	Message      string
}

type SkillDependencyResolver interface {
	ResolveSkillDependencies([]SkillDependency) SkillDependencyResolution
}

type CapabilityAvailabilityResolution struct {
	Available  bool
	ReasonCode string
	Message    string
}

type CapabilityAvailabilityResolver interface {
	ResolveCapabilityAvailability(Entry) CapabilityAvailabilityResolution
}

type artifactPresentationRegistry struct {
	ContractVersion  string                 `json:"contract_version"`
	DefinitionStatus string                 `json:"definition_status"`
	Presentations    []ArtifactPresentation `json:"presentations"`
}

type adapterRegistry struct {
	ContractVersion  string            `json:"contract_version"`
	DefinitionStatus string            `json:"definition_status"`
	Adapters         []responseAdapter `json:"adapters"`
}

type responseAdapter struct {
	ID              string `json:"id"`
	TargetSchemaRef string `json:"target_schema_ref"`
	Behavior        string `json:"behavior"`
}

func NewEmptyRegistry() *Registry {
	return &Registry{
		entries:             make(map[string]Entry),
		legacyEntries:       make(map[string]Entry),
		disabledSkills:      make(map[string]bool),
		pinnedSkillVersions: make(map[string]string),
		pinnedSkillHashes:   make(map[string]string),
	}
}

func LoadRegistry(options LoadOptions) (*Registry, error) {
	projectRoot, err := filepath.Abs(options.ProjectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve project root: %w", err)
	}
	capabilityDir := options.CapabilityDir
	if capabilityDir == "" {
		capabilityDir = filepath.Join(projectRoot, "capabilities", "v1")
	} else if !filepath.IsAbs(capabilityDir) {
		capabilityDir = filepath.Join(projectRoot, capabilityDir)
	}
	capabilityDir, err = filepath.Abs(capabilityDir)
	if err != nil {
		return nil, fmt.Errorf("resolve capability directory: %w", err)
	}

	registry := NewEmptyRegistry()
	registry.projectRoot = projectRoot
	registry.skillRoots, err = normalizeSkillRoots(projectRoot, options.SkillRoots)
	if err != nil {
		return nil, err
	}
	presentations, err := loadArtifactPresentations(projectRoot)
	if err != nil {
		return nil, err
	}
	registry.presentations = presentations
	files, err := filepath.Glob(filepath.Join(capabilityDir, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("list capability manifests: %w", err)
	}
	if len(files) == 0 {
		if _, statErr := os.Stat(capabilityDir); errors.Is(statErr, os.ErrNotExist) {
			return registry, nil
		}
	}

	adapterIDs, adapterErr := loadAdapterIDs(projectRoot, filepath.Join(capabilityDir, "response-adapters.json"))
	for _, file := range files {
		base := filepath.Base(file)
		if base == "manifest.schema.json" || base == "response-adapters.json" {
			continue
		}
		manifest, decodeErr := decodeManifest(file)
		if decodeErr != nil {
			registry.addUnavailable(strings.TrimSuffix(base, filepath.Ext(base)), file, "MANIFEST_INVALID", decodeErr)
			continue
		}
		if manifest.Schema != "./manifest.schema.json" {
			continue
		}

		if _, duplicate := registry.entries[manifest.ID]; duplicate {
			registry.addUnavailable(manifest.ID, file, "CAPABILITY_ID_DUPLICATED", fmt.Errorf("capability id %q is duplicated", manifest.ID))
			continue
		}
		if adapterErr != nil {
			registry.addUnavailable(manifest.ID, file, "ADAPTER_REGISTRY_INVALID", adapterErr)
			continue
		}
		if err := validateManifestReferences(projectRoot, file, manifest, adapterIDs); err != nil {
			registry.addUnavailable(manifest.ID, file, "MANIFEST_REFERENCE_INVALID", err)
			continue
		}
		definition, err := compileManifest(manifest)
		if err != nil {
			registry.addUnavailable(manifest.ID, file, "MANIFEST_SEMANTIC_INVALID", err)
			continue
		}
		registry.entries[definition.ID] = Entry{
			CapabilityID: definition.ID,
			Definition:   definition,
			Status:       Available,
			ContentRoot:  projectRoot,
			SourceFile:   file,
		}
		registry.order = append(registry.order, definition.ID)
	}
	registry.legacyEntries = cloneEntries(registry.entries)
	registry.legacyOrder = slices.Clone(registry.order)
	if err := registry.RefreshSkills(); err != nil {
		return nil, err
	}
	return registry, nil
}

func (r *Registry) ProjectRoot() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.projectRoot
}

// Fork returns an independent registry overlay. Legacy capabilities and
// configured global Skill roots are shared as immutable inputs, while Skill
// enablement and version pins remain isolated in the returned registry.
func (r *Registry) Fork(additionalRoots ...SkillRoot) (*Registry, error) {
	return r.fork(nil, nil, additionalRoots)
}

// ForkForSelection composes visible directory roots with immutable managed
// packages. Ownership is resolved by the Runtime, not by package manifests.
func (r *Registry) ForkForSelection(include func(SkillRoot) bool, snapshots []*SkillPackage) (*Registry, error) {
	return r.fork(include, snapshots, nil)
}

// ForkWithSnapshots retains built-in capabilities but never rescans live Skill
// directories. The Runtime has already authorized these execution snapshots.
func (r *Registry) ForkWithSnapshots(snapshots []*SkillPackage) (*Registry, error) {
	fork, err := r.fork(func(SkillRoot) bool { return false }, nil, nil)
	if err != nil {
		return nil, err
	}
	for _, skill := range snapshots {
		if skill != nil {
			copy := *skill
			fork.snapshotSkills = append(fork.snapshotSkills, &copy)
		}
	}
	return fork, fork.RefreshSkills()
}

func (r *Registry) fork(include func(SkillRoot) bool, snapshots []*SkillPackage, additionalRoots []SkillRoot) (*Registry, error) {
	r.mu.RLock()
	fork := NewEmptyRegistry()
	fork.projectRoot = r.projectRoot
	fork.legacyEntries = cloneEntries(r.legacyEntries)
	fork.legacyOrder = slices.Clone(r.legacyOrder)
	fork.entries = cloneEntries(r.legacyEntries)
	fork.order = slices.Clone(r.legacyOrder)
	fork.presentations = slices.Clone(r.presentations)
	for _, root := range r.skillRoots {
		if include == nil || include(root) {
			fork.skillRoots = append(fork.skillRoots, root)
		}
	}
	for _, skill := range append(slices.Clone(r.snapshotSkills), snapshots...) {
		if skill != nil && (include == nil || include(SkillRoot{Scope: skill.Scope, WorkspaceID: skill.WorkspaceID, ScopeRef: skill.ScopeRef, Path: filepath.Dir(skill.Directory), Priority: skill.Priority})) {
			copy := *skill
			fork.snapshotSkills = append(fork.snapshotSkills, &copy)
		}
	}
	fork.dependencyResolver = r.dependencyResolver
	fork.availabilityResolver = r.availabilityResolver
	r.mu.RUnlock()

	roots, err := normalizeSkillRoots(fork.projectRoot, append(fork.skillRoots, additionalRoots...))
	if err != nil {
		return nil, err
	}
	fork.skillRoots = deduplicateSkillRoots(roots)
	if err := fork.RefreshSkills(); err != nil {
		return nil, err
	}
	return fork, nil
}

func (r *Registry) AddSkillRoot(root SkillRoot) error {
	r.mu.RLock()
	projectRoot := r.projectRoot
	existing := slices.Clone(r.skillRoots)
	r.mu.RUnlock()

	normalized, err := normalizeSkillRoots(projectRoot, append(existing, root))
	if err != nil {
		return err
	}
	deduplicated := deduplicateSkillRoots(normalized)
	r.mu.Lock()
	r.skillRoots = deduplicated
	r.mu.Unlock()
	return r.RefreshSkills()
}

func deduplicateSkillRoots(roots []SkillRoot) []SkillRoot {
	deduplicated := make([]SkillRoot, 0, len(roots))
	for _, candidate := range roots {
		duplicate := false
		for _, current := range deduplicated {
			if current.Scope == candidate.Scope && current.WorkspaceID == candidate.WorkspaceID && current.ScopeRef == candidate.ScopeRef && strings.EqualFold(current.Path, candidate.Path) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			deduplicated = append(deduplicated, candidate)
		}
	}
	return deduplicated
}

func (r *Registry) SetSkillDependencyResolver(resolver SkillDependencyResolver) error {
	r.mu.Lock()
	r.dependencyResolver = resolver
	r.mu.Unlock()
	return r.RefreshSkills()
}

func (r *Registry) SetCapabilityAvailabilityResolver(
	resolver CapabilityAvailabilityResolver,
) error {
	r.mu.Lock()
	r.availabilityResolver = resolver
	r.mu.Unlock()
	return r.RefreshSkills()
}

// EntryForSkillPackage compiles an already-inspected immutable Skill package
// with the registry's current dependency policy. It lets callers resume work
// pinned to an archived version without changing the active registry.
func (r *Registry) EntryForSkillPackage(skill *SkillPackage) (Entry, bool) {
	if skill == nil {
		return Entry{}, false
	}
	r.mu.RLock()
	resolver := r.dependencyResolver
	availabilityResolver := r.availabilityResolver
	r.mu.RUnlock()

	resolved := *skill
	resolved.Dependencies = slices.Clone(skill.Dependencies)
	status := Available
	reasonCode := ""
	message := ""
	if skill.Disabled {
		status, reasonCode, message = Unavailable, "SKILL_DISABLED", "该 Skill 已禁用或卸载。"
		if skill.UnavailableReasonCode != "" {
			reasonCode, message = skill.UnavailableReasonCode, skill.UnavailableMessage
		}
	}
	if len(resolved.Dependencies) > 0 {
		resolution := SkillDependencyResolution{
			Dependencies: slices.Clone(resolved.Dependencies),
			Available:    false,
			ReasonCode:   "SKILL_TOOL_REGISTRY_UNAVAILABLE",
			Message:      "该 Skill 依赖工具，但平台尚未配置可信工具目录。",
		}
		if resolver != nil {
			resolution = resolver.ResolveSkillDependencies(resolved.Dependencies)
		}
		resolved.Dependencies = slices.Clone(resolution.Dependencies)
		if status == Available && !resolution.Available {
			status = Unavailable
			reasonCode = resolution.ReasonCode
			message = resolution.Message
		}
	}
	sourceFile := resolved.SkillFile
	if resolved.WorkflowFile != "" {
		sourceFile = resolved.WorkflowFile
	}
	entry := Entry{
		CapabilityID: resolved.CapabilityID,
		Definition:   compileSkillPackage(&resolved),
		Skill:        &resolved,
		Status:       status,
		ReasonCode:   reasonCode,
		Message:      message,
		ContentRoot:  resolved.Directory,
		SourceFile:   sourceFile,
	}
	if entry.Status == Available && availabilityResolver != nil {
		resolution := availabilityResolver.ResolveCapabilityAvailability(entry)
		if !resolution.Available {
			entry.Status = Unavailable
			entry.ReasonCode = resolution.ReasonCode
			entry.Message = resolution.Message
		}
	}
	return entry, true
}

func (r *Registry) ClearSkillState(capabilityID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.disabledSkills, capabilityID)
	delete(r.pinnedSkillVersions, capabilityID)
	delete(r.pinnedSkillHashes, capabilityID)
}

func (r *Registry) PublicArtifactPresentations() []ArtifactPresentation {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]ArtifactPresentation, len(r.presentations))
	copy(result, r.presentations)
	for index := range result {
		result[index].PreferredFields = slices.Clone(result[index].PreferredFields)
		result[index].AvailableActions = slices.Clone(result[index].AvailableActions)
	}
	return result
}

func loadArtifactPresentations(projectRoot string) ([]ArtifactPresentation, error) {
	file := filepath.Join(projectRoot, "capabilities", "v1", "registries", "artifact-presentations.json")
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read artifact presentation registry: %w", err)
	}
	var source artifactPresentationRegistry
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&source); err != nil {
		return nil, fmt.Errorf("decode artifact presentation registry: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, fmt.Errorf("decode artifact presentation registry: %w", err)
	}
	if source.ContractVersion != "1.0.0" || source.DefinitionStatus != "design_contract" {
		return nil, errors.New("artifact presentation registry version or status is unsupported")
	}
	seen := make(map[string]struct{}, len(source.Presentations))
	allowedRenderers := map[string]struct{}{"document": {}, "episode_plan": {}, "script": {}, "script_collection": {}, "video_script": {}, "table": {}, "media": {}, "form": {}}
	allowedGroups := map[string]struct{}{"single": {}, "episode_directory": {}}
	allowedVisibility := map[string]struct{}{"user": {}, "internal": {}}
	for index, item := range source.Presentations {
		if item.ArtifactType == "" || item.Label == "" || item.Description == "" {
			return nil, fmt.Errorf("artifact presentation %d is missing identity fields", index)
		}
		if _, duplicate := seen[item.ArtifactType]; duplicate {
			return nil, fmt.Errorf("artifact presentation %q is duplicated", item.ArtifactType)
		}
		if _, ok := allowedRenderers[item.Renderer]; !ok {
			return nil, fmt.Errorf("artifact presentation %q uses unsupported renderer %q", item.ArtifactType, item.Renderer)
		}
		if _, ok := allowedGroups[item.Navigation.GroupMode]; !ok {
			return nil, fmt.Errorf("artifact presentation %q uses unsupported group mode %q", item.ArtifactType, item.Navigation.GroupMode)
		}
		if _, ok := allowedVisibility[item.Navigation.Visibility]; !ok {
			return nil, fmt.Errorf("artifact presentation %q uses unsupported visibility %q", item.ArtifactType, item.Navigation.Visibility)
		}
		seen[item.ArtifactType] = struct{}{}
	}
	sort.SliceStable(source.Presentations, func(left, right int) bool {
		return source.Presentations[left].Navigation.Order < source.Presentations[right].Navigation.Order
	})
	return source.Presentations, nil
}

func decodeManifest(file string) (Manifest, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return Manifest{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, err
	}
	if err := ensureEOF(decoder); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("multiple JSON documents are not allowed")
}

func loadAdapterIDs(projectRoot, file string) (map[string]struct{}, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read response adapter registry: %w", err)
	}
	var source adapterRegistry
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&source); err != nil {
		return nil, fmt.Errorf("decode response adapter registry: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, fmt.Errorf("decode response adapter registry: %w", err)
	}
	ids := make(map[string]struct{}, len(source.Adapters))
	for _, adapter := range source.Adapters {
		if adapter.ID == "" {
			return nil, errors.New("response adapter id is required")
		}
		if _, duplicate := ids[adapter.ID]; duplicate {
			return nil, fmt.Errorf("response adapter %q is duplicated", adapter.ID)
		}
		if err := validateJSONReference(projectRoot, file, adapter.TargetSchemaRef); err != nil {
			return nil, fmt.Errorf("adapter %s: %w", adapter.ID, err)
		}
		ids[adapter.ID] = struct{}{}
	}
	return ids, nil
}

func validateManifestReferences(projectRoot, manifestFile string, manifest Manifest, adapterIDs map[string]struct{}) error {
	var problems []string
	if err := validateJSONReference(projectRoot, manifestFile, manifest.InputSchemaRef); err != nil {
		problems = append(problems, fmt.Sprintf("input_schema_ref: %v", err))
	}
	for name, ref := range manifest.ConfigSchemaRefs {
		if err := validateJSONReference(projectRoot, manifestFile, ref); err != nil {
			problems = append(problems, fmt.Sprintf("config_schema_refs.%s: %v", name, err))
		}
	}
	for _, step := range manifest.Steps {
		for _, output := range step.OutputRefs {
			if err := validateJSONReference(projectRoot, manifestFile, output.SchemaRef); err != nil {
				problems = append(problems, fmt.Sprintf("%s output %s: %v", step.ID, output.ArtifactType, err))
			}
		}
		if step.ResultSchemaRef != nil {
			if err := validateJSONReference(projectRoot, manifestFile, *step.ResultSchemaRef); err != nil {
				problems = append(problems, fmt.Sprintf("%s result_schema_ref: %v", step.ID, err))
			}
		}
		if step.ProviderResultSchemaRef != nil {
			if err := validateJSONReference(projectRoot, manifestFile, *step.ProviderResultSchemaRef); err != nil {
				problems = append(problems, fmt.Sprintf("%s provider_result_schema_ref: %v", step.ID, err))
			}
		}
		if step.PromptRef != nil {
			if err := validateContentFile(projectRoot, manifestFile, *step.PromptRef, "design/prompts"); err != nil {
				problems = append(problems, fmt.Sprintf("%s prompt_ref: %v", step.ID, err))
			}
		}
		for _, ref := range step.RuleRefs {
			if err := validateContentFile(projectRoot, manifestFile, ref, "design/rules"); err != nil {
				problems = append(problems, fmt.Sprintf("%s rule_ref: %v", step.ID, err))
			}
		}
		if step.Batch != nil {
			for label, stage := range map[string]*BatchInternalTaskStage{
				"preparation": step.Batch.Preparation,
				"task_stage":  step.Batch.TaskStage,
			} {
				if stage == nil {
					continue
				}
				if err := validateContentFile(projectRoot, manifestFile, stage.PromptRef, "design/prompts"); err != nil {
					problems = append(problems, fmt.Sprintf("%s batch.%s.prompt_ref: %v", step.ID, label, err))
				}
				if err := validateJSONReference(projectRoot, manifestFile, stage.SchemaRef); err != nil {
					problems = append(problems, fmt.Sprintf("%s batch.%s.schema_ref: %v", step.ID, label, err))
				}
			}
		}
		if step.ResponseAdapterRef != nil {
			if _, ok := adapterIDs[*step.ResponseAdapterRef]; !ok {
				problems = append(problems, fmt.Sprintf("%s response adapter %q is not registered", step.ID, *step.ResponseAdapterRef))
			}
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func validateContentFile(projectRoot, sourceFile, ref, contentRoot string) error {
	if strings.Contains(filepath.ToSlash(ref), "novel2script_agent_project/") {
		return errors.New("legacy project content reference is forbidden")
	}
	target, err := resolveProjectPath(projectRoot, sourceFile, ref)
	if err != nil {
		return err
	}
	expectedRoot, err := filepath.Abs(filepath.Join(projectRoot, filepath.FromSlash(contentRoot)))
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(expectedRoot, target)
	if err != nil {
		return err
	}
	if relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(relative) {
		return fmt.Errorf("referenced content file must be under %s", contentRoot)
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if info.IsDir() || info.Size() == 0 {
		return errors.New("referenced content file must be non-empty")
	}
	return nil
}

func validateJSONReference(projectRoot, sourceFile, ref string) error {
	parts := strings.SplitN(ref, "#", 2)
	target, err := resolveProjectPath(projectRoot, sourceFile, parts[0])
	if err != nil {
		return err
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	var document any
	if err := json.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("parse referenced JSON: %w", err)
	}
	if len(parts) == 2 && parts[1] != "" {
		if _, ok := resolveJSONPointer(document, parts[1]); !ok {
			return fmt.Errorf("JSON pointer #%s does not exist", parts[1])
		}
	}
	return nil
}

func resolveProjectPath(projectRoot, sourceFile, ref string) (string, error) {
	target := ref
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(sourceFile), filepath.FromSlash(ref))
	}
	target, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(projectRoot, target)
	if err != nil {
		return "", err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("reference escapes project root: %s", ref)
	}
	return target, nil
}

func resolveJSONPointer(document any, pointer string) (any, bool) {
	if pointer == "" {
		return document, true
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	current := document
	for _, rawPart := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		part := strings.ReplaceAll(strings.ReplaceAll(rawPart, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func (r *Registry) addUnavailable(id, sourceFile, reasonCode string, err error) {
	if id == "" {
		id = "invalid_capability"
	}
	r.entries[id] = Entry{
		CapabilityID: id,
		Status:       Unavailable,
		ReasonCode:   reasonCode,
		Message:      err.Error(),
		ContentRoot:  r.projectRoot,
		SourceFile:   sourceFile,
	}
	if !slices.Contains(r.order, id) {
		r.order = append(r.order, id)
	}
}

func (r *Registry) sort() {
	sort.SliceStable(r.order, func(left, right int) bool {
		leftEntry := r.entries[r.order[left]]
		rightEntry := r.entries[r.order[right]]
		leftOrder := int(^uint(0) >> 1)
		rightOrder := leftOrder
		if leftEntry.Definition != nil {
			leftOrder = leftEntry.Definition.UI.SortOrder
		}
		if rightEntry.Definition != nil {
			rightOrder = rightEntry.Definition.UI.SortOrder
		}
		if leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		return r.order[left] < r.order[right]
	})
}

func (r *Registry) Entries() []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Entry, 0, len(r.order))
	for _, id := range r.order {
		result = append(result, r.entries[id])
	}
	return result
}

func (r *Registry) Get(id string) (Entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.entries[id]
	return entry, ok
}

func (r *Registry) MarkUnavailable(id, reasonCode, message string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[id]
	if !ok {
		return false
	}
	entry.Status = Unavailable
	entry.ReasonCode = reasonCode
	entry.Message = message
	r.entries[id] = entry
	return true
}

func (r *Registry) CountByStatus(status Availability) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	count := 0
	for _, entry := range r.entries {
		if entry.Status == status {
			count++
		}
	}
	return count
}

func (r *Registry) RefreshSkills() error {
	r.mu.RLock()
	projectRoot := r.projectRoot
	roots := slices.Clone(r.skillRoots)
	snapshots := slices.Clone(r.snapshotSkills)
	dependencyResolver := r.dependencyResolver
	availabilityResolver := r.availabilityResolver
	r.mu.RUnlock()

	packages, diagnostics := scanSkillRoots(projectRoot, roots)
	for _, snapshot := range snapshots {
		copy := *snapshot
		copy.Dependencies = slices.Clone(snapshot.Dependencies)
		packages = append(packages, &copy)
	}
	candidates := make(map[string][]*SkillPackage)
	for _, skill := range packages {
		candidates[skill.CapabilityID] = append(candidates[skill.CapabilityID], skill)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = cloneEntries(r.legacyEntries)
	r.order = slices.Clone(r.legacyOrder)

	ids := make([]string, 0, len(candidates))
	for id := range candidates {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		items := candidates[id]
		sort.SliceStable(items, func(left, right int) bool {
			if items[left].Priority != items[right].Priority {
				return items[left].Priority > items[right].Priority
			}
			return items[left].SkillFile < items[right].SkillFile
		})
		selected := items[0]
		pinMissing := false
		if pinned := r.pinnedSkillVersions[id]; pinned != "" {
			match := slices.IndexFunc(items, func(skill *SkillPackage) bool {
				_, matches := skill.MatchSnapshot(pinned, r.pinnedSkillHashes[id])
				return matches
			})
			if match >= 0 {
				selected, _ = items[match].MatchSnapshot(pinned, r.pinnedSkillHashes[id])
			} else {
				pinMissing = true
				diagnostics = append(diagnostics, SkillDiagnostic{
					Code:    "SKILL_PIN_UNAVAILABLE",
					Message: fmt.Sprintf("pinned version %s is not available", pinned),
					Scope:   selected.Scope, Path: selected.DisplayPath,
					SkillName: selected.Name, CapabilityID: id,
				})
			}
		}
		for _, shadowed := range items {
			if shadowed.SkillFile == selected.SkillFile {
				continue
			}
			diagnostics = append(diagnostics, SkillDiagnostic{
				Code:    "SKILL_SHADOWED",
				Message: fmt.Sprintf("shadowed by %s", selected.DisplayPath),
				Scope:   shadowed.Scope, Path: shadowed.DisplayPath,
				SkillName: shadowed.Name, CapabilityID: id,
			})
		}
		if legacy, conflict := r.legacyEntries[id]; conflict {
			diagnostics = append(diagnostics, SkillDiagnostic{
				Code:    "SKILL_CAPABILITY_ID_CONFLICT",
				Message: fmt.Sprintf("capability id conflicts with %s", legacy.SourceFile),
				Scope:   selected.Scope, Path: selected.DisplayPath,
				SkillName: selected.Name, CapabilityID: id,
			})
			continue
		}
		status := Available
		reasonCode := ""
		message := ""
		if r.disabledSkills[id] || selected.Disabled {
			status = Unavailable
			reasonCode = "SKILL_DISABLED"
			message = "该 Skill 已禁用。"
			if selected.UnavailableReasonCode != "" {
				reasonCode, message = selected.UnavailableReasonCode, selected.UnavailableMessage
			}
		} else if pinMissing {
			status = Unavailable
			reasonCode = "SKILL_PIN_UNAVAILABLE"
			message = "固定的 Skill 版本当前不可用。"
		}
		if len(selected.Dependencies) > 0 {
			resolution := SkillDependencyResolution{
				Dependencies: slices.Clone(selected.Dependencies),
				Available:    false,
				ReasonCode:   "SKILL_TOOL_REGISTRY_UNAVAILABLE",
				Message:      "该 Skill 依赖工具，但平台尚未配置可信工具目录。",
			}
			if dependencyResolver != nil {
				resolution = dependencyResolver.ResolveSkillDependencies(selected.Dependencies)
			}
			selected.Dependencies = slices.Clone(resolution.Dependencies)
			if status == Available && !resolution.Available {
				status = Unavailable
				reasonCode = resolution.ReasonCode
				message = resolution.Message
			}
			if !resolution.Available {
				diagnostics = append(diagnostics, SkillDiagnostic{
					Code:         resolution.ReasonCode,
					Message:      resolution.Message,
					Scope:        selected.Scope,
					Path:         selected.DisplayPath,
					SkillName:    selected.Name,
					CapabilityID: id,
				})
			}
		}
		sourceFile := selected.SkillFile
		if selected.WorkflowFile != "" {
			sourceFile = selected.WorkflowFile
		}
		r.entries[id] = Entry{
			CapabilityID: id,
			Definition:   compileSkillPackage(selected),
			Skill:        selected,
			Status:       status,
			ReasonCode:   reasonCode,
			Message:      message,
			ContentRoot:  selected.Directory,
			SourceFile:   sourceFile,
		}
		r.order = append(r.order, id)
	}
	if availabilityResolver != nil {
		for _, id := range r.order {
			entry := r.entries[id]
			if entry.Status != Available || entry.Definition == nil {
				continue
			}
			resolution := availabilityResolver.ResolveCapabilityAvailability(entry)
			if !resolution.Available {
				entry.Status = Unavailable
				entry.ReasonCode = resolution.ReasonCode
				entry.Message = resolution.Message
				r.entries[id] = entry
			}
		}
	}
	r.skillDiagnostics = diagnostics
	r.sort()
	return nil
}

func (r *Registry) SetSkillEnabled(id string, enabled bool) bool {
	r.mu.Lock()
	entry, ok := r.entries[id]
	if !ok || entry.Skill == nil {
		r.mu.Unlock()
		return false
	}
	if enabled {
		delete(r.disabledSkills, id)
	} else {
		r.disabledSkills[id] = true
	}
	r.mu.Unlock()
	_ = r.RefreshSkills()
	return true
}

func (r *Registry) PinSkillVersion(id, version string) bool {
	return r.PinSkillSnapshot(id, version, "")
}

func (r *Registry) PinSkillSnapshot(id, version, contentHash string) bool {
	r.mu.Lock()
	entry, ok := r.entries[id]
	if !ok || entry.Skill == nil {
		r.mu.Unlock()
		return false
	}
	version = strings.TrimSpace(version)
	delete(r.pinnedSkillHashes, id)
	if version == "" {
		delete(r.pinnedSkillVersions, id)
	} else {
		r.pinnedSkillVersions[id] = version
		if contentHash != "" {
			r.pinnedSkillHashes[id] = contentHash
		}
	}
	r.mu.Unlock()
	_ = r.RefreshSkills()
	return true
}

func (r *Registry) SkillDiagnostics() []SkillDiagnostic {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Clone(r.skillDiagnostics)
}

func (r *Registry) PublicList() []PublicCapability {
	entries := r.Entries()
	result := make([]PublicCapability, 0, len(entries))
	for _, entry := range entries {
		result = append(result, publicCapability(entry))
	}
	return result
}

func (r *Registry) PublicDefinition(id string) (PublicDefinition, bool) {
	entry, ok := r.Get(id)
	if !ok {
		return PublicDefinition{}, false
	}
	return publicDefinition(entry), true
}

func publicDefinition(entry Entry) PublicDefinition {
	base := publicCapability(entry)
	if entry.Skill != nil {
		base.Skill = publicSkill(entry.Skill, true)
	}
	if entry.Definition == nil {
		return PublicDefinition{PublicCapability: base}
	}
	steps := make([]PublicStep, 0, len(entry.Definition.Steps))
	for _, step := range entry.Definition.Steps {
		if step.Visibility != "user" {
			continue
		}
		artifactTypes := make([]string, 0, len(step.OutputRefs))
		for _, output := range step.OutputRefs {
			artifactTypes = append(artifactTypes, output.ArtifactType)
		}
		resultKind := ""
		if step.Kind == "review" {
			resultKind = "quality_review"
		}
		steps = append(steps, PublicStep{
			ID:            step.ID,
			Kind:          step.Kind,
			ArtifactTypes: artifactTypes,
			ApprovalType:  step.Approval.Type,
			ApprovalScope: step.Approval.Scope,
			ResultKind:    resultKind,
			Visibility:    step.Visibility,
		})
	}
	return PublicDefinition{
		PublicCapability: base,
		Steps:            steps,
		Completion:       entry.Definition.Completion,
	}
}

func (r *Registry) PublicDefinitionWithSchemas(
	id string,
) (PublicDefinition, bool, error) {
	entry, ok := r.Get(id)
	if !ok {
		return PublicDefinition{}, false, nil
	}
	result, err := r.PublicDefinitionForEntryWithSchemas(entry)
	return result, true, err
}

// PublicDefinitionForEntryWithSchemas projects a specific resolved entry,
// including immutable archived Skill versions selected by Runtime.
func (r *Registry) PublicDefinitionForEntryWithSchemas(
	entry Entry,
) (PublicDefinition, error) {
	result := publicDefinition(entry)
	if entry.Definition == nil {
		return result, nil
	}
	if entry.Definition.InputSchemaRef != "" {
		schema, err := loadPublicJSONSchema(
			entry.ContentRoot, entry.SourceFile, entry.Definition.InputSchemaRef,
		)
		if err != nil {
			return PublicDefinition{}, fmt.Errorf("load input schema: %w", err)
		}
		result.InputSchema = schema
	}
	result.ConfigSchemas = make(map[string]json.RawMessage, len(entry.Definition.ConfigSchemaRefs))
	for name, ref := range entry.Definition.ConfigSchemaRefs {
		schema, err := loadPublicJSONSchema(entry.ContentRoot, entry.SourceFile, ref)
		if err != nil {
			return PublicDefinition{}, fmt.Errorf("load config schema %s: %w", name, err)
		}
		result.ConfigSchemas[name] = schema
	}
	return result, nil
}

func loadPublicJSONSchema(
	contentRoot, sourceFile, ref string,
) (json.RawMessage, error) {
	parts := strings.SplitN(ref, "#", 2)
	file, err := resolveProjectPath(contentRoot, sourceFile, parts[0])
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	if len(data) > maxSkillMarkdownBytes {
		return nil, errors.New("schema exceeds public projection size limit")
	}
	var document any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	if len(parts) == 2 && parts[1] != "" {
		var ok bool
		document, ok = resolveJSONPointer(document, parts[1])
		if !ok {
			return nil, errors.New("schema fragment does not exist")
		}
	}
	document, err = expandPublicJSONSchemaReferences(
		contentRoot, file, document, map[string]bool{},
	)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxSkillMarkdownBytes {
		return nil, errors.New("expanded schema exceeds public projection size limit")
	}
	return json.RawMessage(encoded), nil
}

func expandPublicJSONSchemaReferences(
	contentRoot, currentFile string,
	value any,
	stack map[string]bool,
) (any, error) {
	switch typed := value.(type) {
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			expanded, err := expandPublicJSONSchemaReferences(contentRoot, currentFile, item, stack)
			if err != nil {
				return nil, err
			}
			result[index] = expanded
		}
		return result, nil
	case map[string]any:
		if reference, ok := typed["$ref"].(string); ok {
			parts := strings.SplitN(reference, "#", 2)
			targetFile := currentFile
			if parts[0] != "" {
				var err error
				targetFile, err = resolveProjectPath(contentRoot, currentFile, parts[0])
				if err != nil {
					return nil, err
				}
			}
			fragment := ""
			if len(parts) == 2 {
				fragment = parts[1]
			}
			key := filepath.Clean(targetFile) + "#" + fragment
			if stack[key] {
				return nil, fmt.Errorf("cyclic schema reference %s", reference)
			}
			stack[key] = true
			data, err := os.ReadFile(targetFile)
			if err != nil {
				delete(stack, key)
				return nil, err
			}
			if len(data) > maxSkillMarkdownBytes {
				delete(stack, key)
				return nil, errors.New("schema exceeds public projection size limit")
			}
			var referenced any
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.UseNumber()
			if err := decoder.Decode(&referenced); err != nil {
				delete(stack, key)
				return nil, err
			}
			if fragment != "" {
				var found bool
				referenced, found = resolveJSONPointer(referenced, fragment)
				if !found {
					delete(stack, key)
					return nil, errors.New("schema reference fragment does not exist")
				}
			}
			expanded, err := expandPublicJSONSchemaReferences(contentRoot, targetFile, referenced, stack)
			delete(stack, key)
			if err != nil {
				return nil, err
			}
			if len(typed) == 1 {
				return expanded, nil
			}
			siblings := make(map[string]any, len(typed)-1)
			for key, item := range typed {
				if key != "$ref" {
					siblings[key] = item
				}
			}
			expandedSiblings, err := expandPublicJSONSchemaReferences(
				contentRoot, currentFile, siblings, stack,
			)
			if err != nil {
				return nil, err
			}
			return map[string]any{"allOf": []any{expanded, expandedSiblings}}, nil
		}
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			expanded, err := expandPublicJSONSchemaReferences(contentRoot, currentFile, item, stack)
			if err != nil {
				return nil, err
			}
			result[key] = expanded
		}
		return result, nil
	default:
		return value, nil
	}
}

func publicCapability(entry Entry) PublicCapability {
	if entry.Definition == nil {
		return PublicCapability{
			CapabilityID: entry.CapabilityID,
			Status:       entry.Status,
			ReasonCode:   entry.ReasonCode,
			UserMessage:  "该能力定义无效，当前不可用。",
			Commands:     []string{},
		}
	}
	definition := entry.Definition
	commands := slices.Clone(definition.Commands)
	defaultConfigRef := ""
	for _, step := range definition.Steps {
		if step.ConfigRef != nil && *step.ConfigRef != "" {
			defaultConfigRef = *step.ConfigRef
			break
		}
	}
	configOptions := make([]string, 0, len(definition.ConfigSchemaRefs))
	for name := range definition.ConfigSchemaRefs {
		configOptions = append(configOptions, name)
	}
	sort.Strings(configOptions)
	result := PublicCapability{
		CapabilityID:       definition.ID,
		Version:            definition.Version,
		Label:              definition.Label,
		Description:        definition.Description,
		Kind:               definition.Kind,
		ExecutionMode:      definition.ExecutionMode,
		CreatesRun:         definition.ExecutionMode == "stateful_workflow",
		InputBinding:       definition.InputBinding,
		Status:             entry.Status,
		ReasonCode:         entry.ReasonCode,
		UserMessage:        entry.Message,
		AcceptedAssetKinds: slices.Clone(definition.AcceptedAssetKinds),
		DefaultConfigRef:   defaultConfigRef,
		ConfigOptions:      configOptions,
		Commands:           commands,
		UIEntry: PublicUIEntry{
			IconKey:       definition.UI.IconKey,
			MenuOrder:     definition.UI.SortOrder,
			EntryViewKey:  definition.UI.EntryViewKey,
			ConfigViewKey: definition.UI.ConfigViewKey,
			DefaultPrompt: definition.UI.DefaultPrompt,
		},
		EntryPolicy: definition.EntryPolicy,
		Routing: PublicRouting{
			ExplicitAliases: slices.Clone(definition.Routing.ExplicitAliases),
			IntentExamples:  slices.Clone(definition.Routing.IntentExamples),
			AmbiguityPolicy: definition.Routing.AmbiguityPolicy,
		},
	}
	if entry.Skill != nil {
		result.Skill = publicSkill(entry.Skill, false)
	}
	return result
}

func publicSkill(skill *SkillPackage, includeInstructions bool) *PublicSkill {
	if skill == nil {
		return nil
	}
	result := &PublicSkill{
		Name:                    skill.Name,
		Scope:                   skill.Scope,
		Path:                    skill.DisplayPath,
		ContentHash:             skill.ContentHash,
		AllowImplicitInvocation: skill.AllowImplicitInvocation,
		Interface:               skill.Interface,
		Dependencies:            slices.Clone(skill.Dependencies),
		Scripts:                 slices.Clone(skill.Scripts),
	}
	if includeInstructions {
		result.Instructions = skill.Instructions
	}
	return result
}

func cloneEntries(source map[string]Entry) map[string]Entry {
	result := make(map[string]Entry, len(source))
	for id, entry := range source {
		result[id] = entry
	}
	return result
}
