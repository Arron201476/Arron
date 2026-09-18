package capability

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxSkillMarkdownBytes = 256 * 1024

var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type SkillScope string

const (
	SkillScopeSystem    SkillScope = "system"
	SkillScopeWorkspace SkillScope = "workspace"
	SkillScopeProject   SkillScope = "project"
	SkillScopeUser      SkillScope = "user"
)

type SkillRoot struct {
	Scope       SkillScope
	Path        string
	Priority    int
	WorkspaceID string
	ScopeRef    string
}

type SkillInterface struct {
	DisplayName      string `json:"display_name,omitempty" yaml:"display_name"`
	ShortDescription string `json:"short_description,omitempty" yaml:"short_description"`
	IconSmall        string `json:"icon_small,omitempty" yaml:"icon_small"`
	IconLarge        string `json:"icon_large,omitempty" yaml:"icon_large"`
	BrandColor       string `json:"brand_color,omitempty" yaml:"brand_color"`
	DefaultPrompt    string `json:"default_prompt,omitempty" yaml:"default_prompt"`
}

type SkillDependency struct {
	Type        string       `json:"type" yaml:"type"`
	Value       string       `json:"value" yaml:"value"`
	Description string       `json:"description,omitempty" yaml:"description"`
	Transport   string       `json:"transport,omitempty" yaml:"transport"`
	URL         string       `json:"url,omitempty" yaml:"url"`
	Status      Availability `json:"status,omitempty" yaml:"-"`
	ReasonCode  string       `json:"reason_code,omitempty" yaml:"-"`
	UserMessage string       `json:"user_message,omitempty" yaml:"-"`
}

type SkillScript struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	Runtime     string `json:"runtime"`
	Description string `json:"description"`
}

type SkillPackage struct {
	Name                    string
	Description             string
	Instructions            string
	CapabilityID            string
	Version                 string
	Scope                   SkillScope
	Priority                int
	WorkspaceID             string
	ScopeRef                string
	Disabled                bool
	UnavailableReasonCode   string
	UnavailableMessage      string
	Directory               string
	SkillFile               string
	DisplayPath             string
	ContentHash             string
	ExecutionSnapshotID     string `json:"-"`
	ManagedVersionID        string `json:"-"`
	autoVersion             bool
	AllowImplicitInvocation bool
	Interface               SkillInterface
	Dependencies            []SkillDependency
	Scripts                 []SkillScript
	Resources               []SkillResource
	ExecutionMode           string
	InputBinding            InputBinding
	AcceptedAssetKinds      []string
	RequiredProviders       []string
	Commands                []string
	UI                      UISeed
	RequiresConfirmation    bool
	ExplicitAliases         []string
	IntentExamples          []string
	WorkflowRef             string
	WorkflowFile            string
	WorkflowDefinition      *CompiledDefinition
}

type SkillDiagnostic struct {
	Code         string     `json:"code"`
	Message      string     `json:"message"`
	Scope        SkillScope `json:"scope,omitempty"`
	Path         string     `json:"path,omitempty"`
	SkillName    string     `json:"skill_name,omitempty"`
	CapabilityID string     `json:"capability_id,omitempty"`
}

type SkillDocument struct {
	Name         string
	Description  string
	Instructions string
}

type skillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type openAISkillMetadata struct {
	Interface    SkillInterface `yaml:"interface"`
	Policy       openAIPolicy   `yaml:"policy"`
	Dependencies struct {
		Tools []SkillDependency `yaml:"tools"`
	} `yaml:"dependencies"`
}

type openAIPolicy struct {
	AllowImplicitInvocation *bool `yaml:"allow_implicit_invocation"`
}

type contentAgentSkillManifest struct {
	SchemaVersion            string        `json:"schema_version"`
	ID                       string        `json:"id"`
	Version                  string        `json:"version"`
	ExecutionMode            string        `json:"execution_mode"`
	InputBinding             InputBinding  `json:"input_binding,omitempty"`
	AcceptedAssetKinds       []string      `json:"accepted_asset_kinds"`
	RequiredProviders        []string      `json:"required_providers"`
	Commands                 []string      `json:"commands"`
	RequiresUserConfirmation *bool         `json:"requires_user_confirmation,omitempty"`
	ExplicitAliases          []string      `json:"explicit_aliases,omitempty"`
	IntentExamples           []string      `json:"intent_examples,omitempty"`
	Scripts                  []SkillScript `json:"scripts,omitempty"`
	WorkflowRef              string        `json:"workflow_ref,omitempty"`
	UI                       UISeed        `json:"ui"`
}

func defaultSkillRoots(projectRoot string) []SkillRoot {
	return []SkillRoot{
		{Scope: SkillScopeSystem, Path: filepath.Join(projectRoot, "skills", "system")},
		{Scope: SkillScopeWorkspace, Path: filepath.Join(projectRoot, ".agents", "skills")},
		{Scope: SkillScopeProject, Path: filepath.Join(projectRoot, "skills", "project")},
		{Scope: SkillScopeUser, Path: filepath.Join(projectRoot, "skills", "user")},
	}
}

func normalizeSkillRoots(projectRoot string, roots []SkillRoot) ([]SkillRoot, error) {
	if roots == nil {
		roots = defaultSkillRoots(projectRoot)
	}
	result := make([]SkillRoot, 0, len(roots))
	for index, root := range roots {
		if !isSkillScope(root.Scope) {
			return nil, fmt.Errorf("skill root %d uses unsupported scope %q", index, root.Scope)
		}
		path := strings.TrimSpace(root.Path)
		if path == "" {
			return nil, fmt.Errorf("skill root %d path is required", index)
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(projectRoot, path)
		}
		absolute, err := filepath.Abs(filepath.Clean(path))
		if err != nil {
			return nil, fmt.Errorf("resolve skill root %d: %w", index, err)
		}
		priority := root.Priority
		if priority == 0 {
			priority = skillScopePriority(root.Scope)
		}
		root.Path, root.Priority = absolute, priority
		result = append(result, root)
	}
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].Priority != result[right].Priority {
			return result[left].Priority < result[right].Priority
		}
		if result[left].Scope != result[right].Scope {
			return result[left].Scope < result[right].Scope
		}
		return result[left].Path < result[right].Path
	})
	return result, nil
}

func isSkillScope(scope SkillScope) bool {
	return slices.Contains(
		[]SkillScope{SkillScopeSystem, SkillScopeWorkspace, SkillScopeProject, SkillScopeUser},
		scope,
	)
}

func skillScopePriority(scope SkillScope) int {
	switch scope {
	case SkillScopeSystem:
		return 100
	case SkillScopeWorkspace:
		return 200
	case SkillScopeProject:
		return 300
	case SkillScopeUser:
		return 400
	default:
		return 0
	}
}

func scanSkillRoots(projectRoot string, roots []SkillRoot) ([]*SkillPackage, []SkillDiagnostic) {
	packages := make([]*SkillPackage, 0)
	diagnostics := make([]SkillDiagnostic, 0)
	for _, root := range roots {
		items, err := os.ReadDir(root.Path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			diagnostics = append(diagnostics, SkillDiagnostic{
				Code: "SKILL_ROOT_UNREADABLE", Message: err.Error(), Scope: root.Scope,
				Path: displayPath(projectRoot, root.Path, root.Scope),
			})
			continue
		}
		for _, item := range items {
			if !item.IsDir() || item.Type()&os.ModeSymlink != 0 {
				continue
			}
			directory := filepath.Join(root.Path, item.Name())
			skill, parseErr := loadSkillPackage(projectRoot, root, directory)
			if parseErr != nil {
				diagnostics = append(diagnostics, SkillDiagnostic{
					Code: "SKILL_PACKAGE_INVALID", Message: parseErr.Error(), Scope: root.Scope,
					Path: displayPath(projectRoot, filepath.Join(directory, "SKILL.md"), root.Scope),
				})
				continue
			}
			packages = append(packages, skill)
		}
	}
	return packages, diagnostics
}

func loadSkillPackage(projectRoot string, root SkillRoot, directory string) (*SkillPackage, error) {
	skillFile := filepath.Join(directory, "SKILL.md")
	data, err := os.ReadFile(skillFile)
	if err != nil {
		return nil, fmt.Errorf("read SKILL.md: %w", err)
	}
	if len(data) > maxSkillMarkdownBytes {
		return nil, fmt.Errorf("SKILL.md exceeds %d bytes", maxSkillMarkdownBytes)
	}
	metadata, instructions, err := parseSkillMarkdown(data)
	if err != nil {
		return nil, err
	}
	if !skillNamePattern.MatchString(metadata.Name) || len(metadata.Name) > 64 {
		return nil, errors.New("skill name must be lowercase kebab-case and at most 64 characters")
	}
	if !strings.EqualFold(filepath.Base(directory), metadata.Name) {
		return nil, errors.New("skill directory name must match frontmatter name")
	}
	if len(metadata.Description) > 1024 {
		return nil, errors.New("skill description exceeds 1024 characters")
	}

	openAI, err := loadOpenAISkillMetadata(directory)
	if err != nil {
		return nil, err
	}
	platform, err := loadContentAgentSkillManifest(directory)
	if err != nil {
		return nil, err
	}
	if err := validateSkillAssetPath(directory, openAI.Interface.IconSmall); err != nil {
		return nil, fmt.Errorf("interface.icon_small: %w", err)
	}
	if err := validateSkillAssetPath(directory, openAI.Interface.IconLarge); err != nil {
		return nil, fmt.Errorf("interface.icon_large: %w", err)
	}
	hash, err := hashSkillDirectory(directory)
	if err != nil {
		return nil, err
	}
	resources, err := snapshotSkillResources(directory)
	if err != nil {
		return nil, err
	}

	allowImplicit := true
	if openAI.Policy.AllowImplicitInvocation != nil {
		allowImplicit = *openAI.Policy.AllowImplicitInvocation
	}
	capabilityID := canonicalSkillID(metadata.Name)
	if platform.ID != "" {
		if !capabilityIDPattern.MatchString(platform.ID) {
			return nil, errors.New("content-agent manifest id must use snake_case")
		}
		capabilityID = platform.ID
	}
	version := platform.Version
	if version == "" {
		version = "0.0.0"
		if platform.WorkflowRef == "" {
			version += "+" + strings.TrimPrefix(hash, "sha256:")
		}
	}
	if !versionPattern.MatchString(version) {
		return nil, errors.New("content-agent manifest version must use semantic version format")
	}
	executionMode := platform.ExecutionMode
	if executionMode == "" {
		executionMode = "inline"
	}
	if !slices.Contains([]string{"inline", "background_task", "stateful_workflow"}, executionMode) {
		return nil, fmt.Errorf("unsupported execution_mode %q", executionMode)
	}
	if executionMode == "stateful_workflow" && strings.TrimSpace(platform.WorkflowRef) == "" {
		return nil, errors.New("stateful_workflow skill requires workflow_ref")
	}
	if executionMode != "stateful_workflow" && strings.TrimSpace(platform.WorkflowRef) != "" {
		return nil, errors.New("workflow_ref is only valid for stateful_workflow skills")
	}
	if platform.WorkflowRef != "" {
		if err := validateSkillAssetPath(directory, platform.WorkflowRef); err != nil {
			return nil, fmt.Errorf("workflow_ref: %w", err)
		}
	}
	if platform.Scripts == nil {
		platform.Scripts = discoverSkillScripts(resources)
	}
	if err := validateSkillScripts(directory, platform.Scripts); err != nil {
		return nil, err
	}
	workflowFile := ""
	var workflowDefinition *CompiledDefinition
	if platform.WorkflowRef != "" {
		workflowFile = filepath.Join(directory, filepath.FromSlash(platform.WorkflowRef))
		workflow, err := decodeManifest(workflowFile)
		if err != nil {
			return nil, fmt.Errorf("decode workflow_ref: %w", err)
		}
		if workflow.ID != capabilityID || workflow.Version != version {
			return nil, fmt.Errorf(
				"workflow identity %s@%s must match Skill capability %s@%s",
				workflow.ID,
				workflow.Version,
				capabilityID,
				version,
			)
		}
		if workflow.Kind != "domain_workflow" || workflow.ExecutionMode != "stateful_workflow" {
			return nil, errors.New("workflow_ref must define a stateful domain_workflow")
		}
		if err := validateSkillWorkflowReferences(directory, workflowFile, workflow); err != nil {
			return nil, fmt.Errorf("validate workflow_ref: %w", err)
		}
		workflowDefinition, err = compileManifest(workflow)
		if err != nil {
			return nil, fmt.Errorf("compile workflow_ref: %w", err)
		}
	}

	acceptedKinds := slices.Clone(platform.AcceptedAssetKinds)
	if len(acceptedKinds) == 0 {
		acceptedKinds = []string{"text", "document"}
	}
	providers := slices.Clone(platform.RequiredProviders)
	if len(providers) == 0 {
		providers = []string{"content_model_provider"}
	}
	commands := slices.Clone(platform.Commands)
	if len(commands) == 0 {
		commands = []string{"inspect", "invoke"}
	}
	requiresConfirmation := executionMode != "inline"
	if platform.RequiresUserConfirmation != nil {
		requiresConfirmation = *platform.RequiresUserConfirmation
	}
	aliases := append([]string{metadata.Name}, platform.ExplicitAliases...)
	if displayName := strings.TrimSpace(openAI.Interface.DisplayName); displayName != "" {
		aliases = append(aliases, displayName)
	}
	aliases = uniqueNonEmpty(aliases)
	intentExamples := uniqueNonEmpty(platform.IntentExamples)
	if len(intentExamples) == 0 {
		intentExamples = []string{metadata.Description}
	}
	ui := platform.UI
	if ui.IconKey == "" {
		ui.IconKey = "sparkles"
	}
	if ui.SortOrder == 0 {
		ui.SortOrder = 1000
	}
	if ui.EntryViewKey == "" {
		ui.EntryViewKey = "source_materials"
	}
	if ui.ConfigViewKey == "" {
		if executionMode == "stateful_workflow" {
			ui.ConfigViewKey = "json_schema"
		} else {
			ui.ConfigViewKey = "none"
		}
	}
	if ui.DefaultPrompt == "" {
		ui.DefaultPrompt = strings.TrimSpace(openAI.Interface.DefaultPrompt)
	}
	inputBinding := platform.InputBinding
	if inputBinding.SourceType == "" && workflowDefinition != nil {
		inputBinding = workflowDefinition.InputBinding
	}

	return &SkillPackage{
		Name:                    metadata.Name,
		Description:             metadata.Description,
		Instructions:            instructions,
		CapabilityID:            capabilityID,
		Version:                 version,
		Scope:                   root.Scope,
		Priority:                root.Priority,
		WorkspaceID:             root.WorkspaceID,
		ScopeRef:                root.ScopeRef,
		Directory:               directory,
		SkillFile:               skillFile,
		DisplayPath:             displayPath(projectRoot, skillFile, root.Scope),
		ContentHash:             hash,
		autoVersion:             platform.Version == "" && platform.WorkflowRef == "",
		Resources:               resources,
		AllowImplicitInvocation: allowImplicit,
		Interface:               openAI.Interface,
		Dependencies:            slices.Clone(openAI.Dependencies.Tools),
		Scripts:                 slices.Clone(platform.Scripts),
		ExecutionMode:           executionMode,
		InputBinding:            inputBinding,
		AcceptedAssetKinds:      acceptedKinds,
		RequiredProviders:       providers,
		Commands:                commands,
		UI:                      ui,
		RequiresConfirmation:    requiresConfirmation,
		ExplicitAliases:         aliases,
		IntentExamples:          intentExamples,
		WorkflowRef:             platform.WorkflowRef,
		WorkflowFile:            workflowFile,
		WorkflowDefinition:      workflowDefinition,
	}, nil
}

func discoverSkillScripts(resources []SkillResource) []SkillScript {
	var scripts []SkillScript
	for _, resource := range resources {
		if !strings.HasPrefix(resource.Path, "scripts/") || strings.ToLower(path.Ext(resource.Path)) != ".py" || path.Base(resource.Path) == "__init__.py" {
			continue
		}
		// Hash the relative path, not the contents: nested entrypoints with the
		// same basename stay distinct, and edits do not rename a tool entrypoint.
		digest := sha256.Sum256([]byte(resource.Path))
		scripts = append(scripts, SkillScript{
			ID: "script-" + hex.EncodeToString(digest[:12]), Path: resource.Path, Runtime: "python",
			Description: "Run " + resource.Path + " in the approved Python sandbox.",
		})
	}
	return scripts
}

func validateSkillScripts(directory string, scripts []SkillScript) error {
	seenIDs := make(map[string]struct{}, len(scripts))
	seenPaths := make(map[string]struct{}, len(scripts))
	for index := range scripts {
		script := &scripts[index]
		script.ID = strings.TrimSpace(script.ID)
		script.Path = strings.TrimSpace(script.Path)
		script.Runtime = strings.TrimSpace(script.Runtime)
		script.Description = strings.TrimSpace(script.Description)
		if !skillNamePattern.MatchString(script.ID) || len(script.ID) > 64 {
			return fmt.Errorf("scripts[%d].id must be lowercase kebab-case and at most 64 characters", index)
		}
		if _, duplicate := seenIDs[script.ID]; duplicate {
			return fmt.Errorf("scripts[%d].id is duplicated", index)
		}
		seenIDs[script.ID] = struct{}{}
		if script.Path == "" || strings.Contains(script.Path, "\\") || path.IsAbs(script.Path) ||
			path.Clean(script.Path) != script.Path || !strings.HasPrefix(script.Path, "scripts/") {
			return fmt.Errorf("scripts[%d].path must be a normalized path under scripts/", index)
		}
		pathKey := strings.ToLower(script.Path)
		if _, duplicate := seenPaths[pathKey]; duplicate {
			return fmt.Errorf("scripts[%d].path is duplicated", index)
		}
		seenPaths[pathKey] = struct{}{}
		if script.Runtime != "python" || strings.ToLower(path.Ext(script.Path)) != ".py" {
			return fmt.Errorf("scripts[%d] must use the python runtime with a .py entrypoint", index)
		}
		if script.Description == "" || len(script.Description) > 512 {
			return fmt.Errorf("scripts[%d].description is required and must not exceed 512 bytes", index)
		}
		if err := validateSkillAssetPath(directory, script.Path); err != nil {
			return fmt.Errorf("scripts[%d].path: %w", index, err)
		}
	}
	return nil
}

func ParseSkillDocument(data []byte) (SkillDocument, error) {
	metadata, instructions, err := parseSkillMarkdown(data)
	if err != nil {
		return SkillDocument{}, err
	}
	return SkillDocument{
		Name:         metadata.Name,
		Description:  metadata.Description,
		Instructions: instructions,
	}, nil
}

func InspectSkillPackage(
	projectRoot string,
	root SkillRoot,
	directory string,
) (*SkillPackage, error) {
	normalizedRoots, err := normalizeSkillRoots(projectRoot, []SkillRoot{root})
	if err != nil {
		return nil, err
	}
	if len(normalizedRoots) != 1 {
		return nil, errors.New("exactly one Skill root is required")
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("resolve Skill directory: %w", err)
	}
	relative, err := filepath.Rel(normalizedRoots[0].Path, directory)
	if err != nil {
		return nil, fmt.Errorf("resolve Skill root boundary: %w", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return nil, errors.New("Skill directory must be inside its declared root")
	}
	return loadSkillPackage(projectRoot, normalizedRoots[0], directory)
}

func (s *SkillPackage) Public(includeInstructions bool) *PublicSkill {
	return publicSkill(s, includeInstructions)
}

func parseSkillMarkdown(data []byte) (skillFrontmatter, string, error) {
	text := strings.TrimPrefix(strings.ReplaceAll(string(data), "\r\n", "\n"), "\ufeff")
	lines := strings.Split(text, "\n")
	if len(lines) < 4 || strings.TrimSpace(lines[0]) != "---" {
		return skillFrontmatter{}, "", errors.New("SKILL.md must start with YAML frontmatter")
	}
	closing := -1
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == "---" {
			closing = index
			break
		}
	}
	if closing < 0 {
		return skillFrontmatter{}, "", errors.New("SKILL.md YAML frontmatter is not closed")
	}
	var metadata skillFrontmatter
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:closing], "\n")), &metadata); err != nil {
		return skillFrontmatter{}, "", fmt.Errorf("parse SKILL.md frontmatter: %w", err)
	}
	metadata.Name = strings.TrimSpace(metadata.Name)
	metadata.Description = strings.TrimSpace(metadata.Description)
	if metadata.Name == "" || metadata.Description == "" {
		return skillFrontmatter{}, "", errors.New("SKILL.md frontmatter requires name and description")
	}
	instructions := strings.TrimSpace(strings.Join(lines[closing+1:], "\n"))
	if instructions == "" {
		return skillFrontmatter{}, "", errors.New("SKILL.md instructions cannot be empty")
	}
	return metadata, instructions, nil
}

func loadOpenAISkillMetadata(directory string) (openAISkillMetadata, error) {
	file := filepath.Join(directory, "agents", "openai.yaml")
	data, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return openAISkillMetadata{}, nil
	}
	if err != nil {
		return openAISkillMetadata{}, fmt.Errorf("read agents/openai.yaml: %w", err)
	}
	var metadata openAISkillMetadata
	if err := yaml.Unmarshal(data, &metadata); err != nil {
		return openAISkillMetadata{}, fmt.Errorf("parse agents/openai.yaml: %w", err)
	}
	for index := range metadata.Dependencies.Tools {
		dependency := &metadata.Dependencies.Tools[index]
		dependency.Type = strings.TrimSpace(dependency.Type)
		dependency.Value = strings.TrimSpace(dependency.Value)
		dependency.Transport = strings.TrimSpace(dependency.Transport)
		dependency.URL = strings.TrimSpace(dependency.URL)
		if dependency.Type == "" || dependency.Value == "" {
			return openAISkillMetadata{}, fmt.Errorf("dependency %d requires type and value", index)
		}
		if dependency.Type != "mcp" {
			return openAISkillMetadata{}, fmt.Errorf("dependency %d uses unsupported type %q", index, dependency.Type)
		}
		if dependency.URL != "" {
			return openAISkillMetadata{}, fmt.Errorf(
				"dependency %d cannot declare url; reference a trusted server/tool entry instead",
				index,
			)
		}
	}
	return metadata, nil
}

func loadContentAgentSkillManifest(directory string) (contentAgentSkillManifest, error) {
	file := filepath.Join(directory, "content-agent", "manifest.json")
	data, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return contentAgentSkillManifest{}, nil
	}
	if err != nil {
		return contentAgentSkillManifest{}, fmt.Errorf("read content-agent/manifest.json: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest contentAgentSkillManifest
	if err := decoder.Decode(&manifest); err != nil {
		return contentAgentSkillManifest{}, fmt.Errorf("parse content-agent/manifest.json: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return contentAgentSkillManifest{}, fmt.Errorf("parse content-agent/manifest.json: %w", err)
	}
	if manifest.SchemaVersion != "1.0.0" {
		return contentAgentSkillManifest{}, errors.New("content-agent manifest schema_version must be 1.0.0")
	}
	return manifest, nil
}

func validateSkillAssetPath(directory, reference string) error {
	if strings.TrimSpace(reference) == "" {
		return nil
	}
	if filepath.IsAbs(reference) {
		return errors.New("path must be relative to the skill directory")
	}
	target, err := filepath.Abs(filepath.Join(directory, filepath.FromSlash(reference)))
	if err != nil {
		return err
	}
	root, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("path escapes the skill directory")
	}
	info, err := os.Lstat(target)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("path must reference a regular file")
	}
	return nil
}

func hashSkillDirectory(directory string) (string, error) {
	files := make([]string, 0)
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not allowed in skill packages: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular skill package entry: %s", path)
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("inspect skill package: %w", err)
	}
	sort.Strings(files)
	hash := sha256.New()
	for _, file := range files {
		relative, err := filepath.Rel(directory, file)
		if err != nil {
			return "", err
		}
		_, _ = io.WriteString(hash, filepath.ToSlash(relative))
		_, _ = hash.Write([]byte{0})
		handle, err := os.Open(file)
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(hash, handle)
		closeErr := handle.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func compileSkillPackage(skill *SkillPackage) *CompiledDefinition {
	label := strings.TrimSpace(skill.Interface.DisplayName)
	if label == "" {
		label = skill.Name
	}
	description := skill.Description
	if short := strings.TrimSpace(skill.Interface.ShortDescription); short != "" {
		description = short
	}
	if skill.WorkflowDefinition != nil {
		definition := *skill.WorkflowDefinition
		definition.Label = label
		definition.Description = description
		definition.InputBinding = skill.InputBinding
		definition.AcceptedAssetKinds = slices.Clone(skill.AcceptedAssetKinds)
		definition.RequiredProviders = slices.Clone(skill.RequiredProviders)
		definition.EntryPolicy = EntryPolicy{
			ExplicitInvocation:       true,
			AutoRoute:                skill.AllowImplicitInvocation,
			RequiresUserConfirmation: skill.RequiresConfirmation,
			InputCollectionModes:     slices.Clone(skill.WorkflowDefinition.EntryPolicy.InputCollectionModes),
		}
		definition.Routing = Routing{
			ExplicitAliases: slices.Clone(skill.ExplicitAliases),
			IntentExamples:  slices.Clone(skill.IntentExamples),
			AmbiguityPolicy: "ask_user",
		}
		definition.Commands = slices.Clone(skill.Commands)
		definition.UI = skill.UI
		return &definition
	}
	return &CompiledDefinition{
		ID:                 skill.CapabilityID,
		Version:            skill.Version,
		Label:              label,
		Description:        description,
		Kind:               "agent_skill",
		ExecutionMode:      skill.ExecutionMode,
		InputBinding:       skill.InputBinding,
		AcceptedAssetKinds: slices.Clone(skill.AcceptedAssetKinds),
		RequiredProviders:  slices.Clone(skill.RequiredProviders),
		ConfigSchemaRefs:   map[string]string{},
		EntryPolicy: EntryPolicy{
			ExplicitInvocation:       true,
			AutoRoute:                skill.AllowImplicitInvocation,
			RequiresUserConfirmation: skill.RequiresConfirmation,
			InputCollectionModes:     []string{"fixed"},
		},
		Routing: Routing{
			ExplicitAliases: slices.Clone(skill.ExplicitAliases),
			IntentExamples:  slices.Clone(skill.IntentExamples),
			AmbiguityPolicy: "ask_user",
		},
		Commands: slices.Clone(skill.Commands),
		UI:       skill.UI,
		Completion: Completion{
			TerminalStepIDs:   []string{},
			RequiredArtifacts: []CompletionArtifact{},
		},
		Steps: []CompiledStep{},
	}
}

func validateSkillWorkflowReferences(skillRoot, workflowFile string, manifest Manifest) error {
	var problems []string
	validateSchema := func(label, ref string) {
		if err := validateSkillJSONReference(skillRoot, workflowFile, ref); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", label, err))
		}
	}
	validateSchema("input_schema_ref", manifest.InputSchemaRef)
	for name, ref := range manifest.ConfigSchemaRefs {
		validateSchema("config_schema_refs."+name, ref)
	}
	if manifest.StateContract != nil {
		validateSchema("state_contract.snapshot_schema_ref", manifest.StateContract.SnapshotSchemaRef)
	}
	for _, step := range manifest.Steps {
		if (step.Kind != "model" && step.Kind != "batch") || step.ExecutorRef != "worker.structured_content" {
			problems = append(problems, fmt.Sprintf(
				"step %s may only use the model or batch kind with worker.structured_content",
				step.ID,
			))
		}
		outputTypes := make(map[string]bool, len(step.OutputRefs))
		for _, output := range step.OutputRefs {
			validateSchema(step.ID+" output "+output.ArtifactType, output.SchemaRef)
			if outputTypes[output.ArtifactType] {
				problems = append(problems, fmt.Sprintf("step %s repeats output artifact_type %s", step.ID, output.ArtifactType))
			}
			outputTypes[output.ArtifactType] = true
			if output.Cardinality == "many" && step.Batch == nil {
				problems = append(problems, fmt.Sprintf("step %s many-cardinality output requires a batch task scope", step.ID))
			}
		}
		if step.ResultSchemaRef != nil {
			validateSchema(step.ID+" result_schema_ref", *step.ResultSchemaRef)
		}
		if step.ProviderResultSchemaRef != nil {
			validateSchema(step.ID+" provider_result_schema_ref", *step.ProviderResultSchemaRef)
		}
		if step.PromptRef != nil {
			if err := validateSkillContentReference(skillRoot, workflowFile, *step.PromptRef, "prompts"); err != nil {
				problems = append(problems, fmt.Sprintf("%s prompt_ref: %v", step.ID, err))
			}
		}
		for _, ref := range step.RuleRefs {
			if err := validateSkillContentReference(skillRoot, workflowFile, ref, "references"); err != nil {
				problems = append(problems, fmt.Sprintf("%s rule_ref: %v", step.ID, err))
			}
		}
		approval := compileApproval(step.Approval)
		if !approval.Required || approval.Type == "none" {
			problems = append(problems, fmt.Sprintf("step %s model outputs require user approval; none is reserved for platform internal or aggregate steps", step.ID))
		}
		if step.Batch != nil && !supportsManagedSkillBatch(step.Batch, step.OutputRefs, approval.Required, approval.Type, approval.Scope) {
			problems = append(problems, fmt.Sprintf("step %s requires a sequential or parallel episode_no batch with one many-cardinality pending_approval output per task, batch approval, and no platform internal stages", step.ID))
		}
		if step.ResponseAdapterRef != nil {
			problems = append(problems, fmt.Sprintf("step %s cannot reference a platform response adapter", step.ID))
		}
	}
	if len(problems) != 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// Direct output stays in the package's schema, without a platform adapter or merger.
func SupportsDirectSkillOutput(step CompiledStep) bool {
	if step.ResponseAdapterRef != nil || len(step.OutputRefs) == 0 {
		return false
	}
	if step.Batch != nil {
		return supportsManagedSkillBatch(step.Batch, step.OutputRefs, step.Approval.Required, step.Approval.Type, step.Approval.Scope)
	}
	if len(step.OutputRefs) == 1 {
		return true
	}
	for _, output := range step.OutputRefs {
		if output.Cardinality != "one" || output.InitialStatus != "pending_approval" {
			return false
		}
	}
	return true
}

func supportsManagedSkillBatch(batch *BatchPolicy, outputs []ArtifactOutput, approvalRequired bool, approvalType, approvalScope string) bool {
	return batch != nil && batch.ItemKey == "episode_no" &&
		(batch.Execution == "sequential" || batch.Execution == "parallel") &&
		batch.Ordering == "natural_episode_order" && batch.MaxItemsPerTask == 1 &&
		batch.FailurePolicy == "preserve_success_retry_failed" &&
		batch.Preparation == nil && batch.TaskStage == nil &&
		len(outputs) == 1 && outputs[0].Cardinality == "many" &&
		outputs[0].InitialStatus == "pending_approval" && approvalRequired &&
		approvalType == "batch_checkpoint" && approvalScope == "batch"
}

func validateSkillJSONReference(skillRoot, sourceFile, ref string) error {
	parts := strings.SplitN(ref, "#", 2)
	if err := validateSkillContentReference(skillRoot, sourceFile, parts[0], "schemas"); err != nil {
		return err
	}
	return validateJSONReference(skillRoot, sourceFile, ref)
}

func validateSkillContentReference(skillRoot, sourceFile, ref, contentRoot string) error {
	if strings.TrimSpace(ref) == "" || strings.Contains(ref, "#") {
		return errors.New("reference must name a package file without a fragment")
	}
	target, err := resolveProjectPath(skillRoot, sourceFile, ref)
	if err != nil {
		return err
	}
	expectedRoot, err := filepath.Abs(filepath.Join(skillRoot, contentRoot))
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(expectedRoot, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return fmt.Errorf("referenced file must be under %s/", contentRoot)
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if info.IsDir() || info.Size() == 0 {
		return errors.New("referenced file must be non-empty")
	}
	return nil
}

func canonicalSkillID(name string) string {
	return strings.ReplaceAll(name, "-", "_")
}

func uniqueNonEmpty(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func displayPath(projectRoot, path string, scope SkillScope) string {
	relative, err := filepath.Rel(projectRoot, path)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(relative)
	}
	return string(scope) + "/" + filepath.Base(filepath.Dir(path)) + "/" + filepath.Base(path)
}
