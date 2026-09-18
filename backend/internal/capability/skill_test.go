package capability

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillMarkdownRequiresStandardMetadataAndInstructions(t *testing.T) {
	metadata, instructions, err := parseSkillMarkdown([]byte("---\r\nname: test-skill\r\ndescription: Test it.\r\n---\r\n\r\nFollow the request.\r\n"))
	if err != nil {
		t.Fatalf("parseSkillMarkdown() error = %v", err)
	}
	if metadata.Name != "test-skill" || metadata.Description != "Test it." || instructions != "Follow the request." {
		t.Fatalf("parsed Skill = %+v, instructions = %q", metadata, instructions)
	}

	for name, source := range map[string]string{
		"missing frontmatter":  "# Instructions",
		"missing description":  "---\nname: test-skill\n---\nInstructions",
		"missing instructions": "---\nname: test-skill\ndescription: Test it.\n---\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parseSkillMarkdown([]byte(source)); err == nil {
				t.Fatal("invalid SKILL.md was accepted")
			}
		})
	}
}

func TestExportedSkillInspectionEnforcesDeclaredRoot(t *testing.T) {
	root := t.TempDir()
	skillRoot := filepath.Join(root, "skills")
	directory := writeTestSkill(t, skillRoot, "inspected-skill", "Inspect me.", "1.0.0")
	documentBytes, err := os.ReadFile(filepath.Join(directory, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	document, err := ParseSkillDocument(documentBytes)
	if err != nil || document.Name != "inspected-skill" || document.Instructions == "" {
		t.Fatalf("ParseSkillDocument() = %+v, error = %v", document, err)
	}
	skill, err := InspectSkillPackage(
		root,
		SkillRoot{Scope: SkillScopeWorkspace, Path: skillRoot},
		directory,
	)
	if err != nil || skill.Public(false).Instructions != "" || skill.Public(true).Instructions == "" {
		t.Fatalf("InspectSkillPackage() = %+v, error = %v", skill, err)
	}
	outside := writeTestSkill(t, filepath.Join(root, "outside"), "outside-skill", "Outside.", "1.0.0")
	if _, err := InspectSkillPackage(
		root,
		SkillRoot{Scope: SkillScopeWorkspace, Path: skillRoot},
		outside,
	); err == nil || !strings.Contains(err.Error(), "inside") {
		t.Fatalf("outside inspection error = %v", err)
	}
}

func TestNativeSkillDiscoversPythonScriptsWithoutPlatformManifest(t *testing.T) {
	root := t.TempDir()
	directory := writeTestSkill(t, root, "native-scripts", "Native scripts.", "1.0.0")
	if err := os.Remove(filepath.Join(directory, "content-agent", "manifest.json")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"render.py", "nested/render.py", "__init__.py"} {
		file := filepath.Join(directory, "scripts", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("print('native')"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	inspect := func() *SkillPackage {
		t.Helper()
		skill, err := InspectSkillPackage(root, SkillRoot{Scope: SkillScopeWorkspace, Path: root}, directory)
		if err != nil {
			t.Fatal(err)
		}
		return skill
	}
	first := inspect()
	if len(first.Scripts) != 2 || first.Scripts[0].ID == first.Scripts[1].ID || first.Scripts[0].Runtime != "python" {
		t.Fatalf("native scripts: %+v", first.Scripts)
	}
	if err := os.WriteFile(filepath.Join(directory, "scripts", "render.py"), []byte("print('changed')"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := inspect()
	if second.Scripts[0].ID != first.Scripts[0].ID || second.Scripts[1].ID != first.Scripts[1].ID || second.ContentHash == first.ContentHash {
		t.Fatal("script identity must remain stable while package identity changes")
	}
	if err := os.WriteFile(filepath.Join(directory, "content-agent", "manifest.json"), []byte(`{"schema_version":"1.0.0","scripts":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if skill := inspect(); len(skill.Scripts) != 0 {
		t.Fatal("explicit empty script allowlist did not override discovery")
	}
}

func TestSkillScriptManifestRequiresDeclaredSafePythonEntrypoint(t *testing.T) {
	root := t.TempDir()
	skillRoot := filepath.Join(root, "skills")
	directory := writeTestSkill(t, skillRoot, "scripted-skill", "Script fixture.", "1.0.0")
	if err := os.MkdirAll(filepath.Join(directory, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "scripts", "render.py"), []byte("print('ok')"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(directory, "content-agent", "manifest.json")
	writeManifest := func(scriptPath, runtime string) {
		t.Helper()
		manifest := `{"schema_version":"1.0.0","version":"1.0.0","execution_mode":"inline","scripts":[{"id":"render","path":"` + scriptPath + `","runtime":"` + runtime + `","description":"Render output."}],"ui":{}}`
		if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeManifest("scripts/render.py", "python")
	skill, err := InspectSkillPackage(root, SkillRoot{Scope: SkillScopeWorkspace, Path: skillRoot}, directory)
	if err != nil || len(skill.Scripts) != 1 || skill.Public(false).Scripts[0].ID != "render" {
		t.Fatalf("scripted Skill = %+v, error = %v", skill, err)
	}

	writeManifest("scripts/../escape.py", "python")
	if _, err := InspectSkillPackage(root, SkillRoot{Scope: SkillScopeWorkspace, Path: skillRoot}, directory); err == nil || !strings.Contains(err.Error(), "normalized") {
		t.Fatalf("traversal manifest error = %v", err)
	}
	writeManifest("scripts/render.py", "shell")
	if _, err := InspectSkillPackage(root, SkillRoot{Scope: SkillScopeWorkspace, Path: skillRoot}, directory); err == nil || !strings.Contains(err.Error(), "python") {
		t.Fatalf("runtime manifest error = %v", err)
	}
}

func TestSkillRootsUseDeterministicPriorityAndVersionPin(t *testing.T) {
	root := t.TempDir()
	systemRoot := filepath.Join(root, "system")
	userRoot := filepath.Join(root, "user")
	writeTestSkill(t, systemRoot, "shared-skill", "System description", "1.0.0")
	writeTestSkill(t, userRoot, "shared-skill", "User description", "2.0.0")

	registry := NewEmptyRegistry()
	registry.projectRoot = root
	registry.skillRoots = []SkillRoot{
		{Scope: SkillScopeSystem, Path: systemRoot, Priority: 100},
		{Scope: SkillScopeUser, Path: userRoot, Priority: 400},
	}
	if err := registry.RefreshSkills(); err != nil {
		t.Fatalf("RefreshSkills() error = %v", err)
	}
	entry, ok := registry.Get("shared_skill")
	if !ok || entry.Skill == nil || entry.Skill.Scope != SkillScopeUser || entry.Definition.Version != "2.0.0" {
		t.Fatalf("selected Skill = %+v", entry)
	}
	if diagnostics := registry.SkillDiagnostics(); len(diagnostics) != 1 || diagnostics[0].Code != "SKILL_SHADOWED" {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}

	if !registry.PinSkillVersion("shared_skill", "1.0.0") {
		t.Fatal("PinSkillVersion() rejected registered Skill")
	}
	entry, _ = registry.Get("shared_skill")
	if entry.Skill.Scope != SkillScopeSystem || entry.Definition.Version != "1.0.0" {
		t.Fatalf("pinned Skill = %+v", entry)
	}
	if !registry.SetSkillEnabled("shared_skill", false) {
		t.Fatal("SetSkillEnabled() rejected registered Skill")
	}
	entry, _ = registry.Get("shared_skill")
	if entry.Status != Unavailable || entry.ReasonCode != "SKILL_DISABLED" {
		t.Fatalf("disabled Skill = %+v", entry)
	}
	if !registry.SetSkillEnabled("shared_skill", true) {
		t.Fatal("SetSkillEnabled() could not re-enable Skill")
	}
	entry, _ = registry.Get("shared_skill")
	if entry.Status != Available {
		t.Fatalf("re-enabled Skill = %+v", entry)
	}
}

func TestSkillRefreshDiscoversDirectoryWithoutCoreRegistration(t *testing.T) {
	root := t.TempDir()
	skillRoot := filepath.Join(root, ".agents", "skills")
	registry := NewEmptyRegistry()
	registry.projectRoot = root
	registry.skillRoots = []SkillRoot{{Scope: SkillScopeWorkspace, Path: skillRoot, Priority: 200}}

	if err := registry.RefreshSkills(); err != nil {
		t.Fatalf("initial RefreshSkills() error = %v", err)
	}
	if len(registry.Entries()) != 0 {
		t.Fatal("empty skill root produced entries")
	}

	writeTestSkill(t, skillRoot, "new-skill", "A newly added skill.", "1.0.0")
	if err := registry.RefreshSkills(); err != nil {
		t.Fatalf("second RefreshSkills() error = %v", err)
	}
	entry, ok := registry.Get("new_skill")
	if !ok || entry.Skill == nil || entry.Definition.ExecutionMode != "inline" {
		t.Fatalf("new Skill was not discovered: %+v", entry)
	}
}

func TestSkillPackageRejectsEscapingMetadataPath(t *testing.T) {
	root := t.TempDir()
	skillRoot := filepath.Join(root, "skills")
	directory := writeTestSkill(t, skillRoot, "unsafe-skill", "Unsafe path test.", "1.0.0")
	agentDir := filepath.Join(directory, "agents")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := "interface:\n  icon_small: ../../outside.svg\n"
	if err := os.WriteFile(filepath.Join(agentDir, "openai.yaml"), []byte(metadata), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadSkillPackage(root, SkillRoot{Scope: SkillScopeWorkspace, Path: skillRoot, Priority: 200}, directory)
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("escaping icon result = %v", err)
	}
}

func TestSkillPackageRejectsDependencyURLInjection(t *testing.T) {
	root := t.TempDir()
	skillRoot := filepath.Join(root, "skills")
	directory := writeTestSkill(t, skillRoot, "unsafe-tool-skill", "Unsafe tool dependency.", "1.0.0")
	agentDir := filepath.Join(directory, "agents")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := `dependencies:
  tools:
    - type: mcp
      value: injected/read
      url: https://attacker.example.test/mcp
`
	if err := os.WriteFile(filepath.Join(agentDir, "openai.yaml"), []byte(metadata), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadSkillPackage(
		root,
		SkillRoot{Scope: SkillScopeWorkspace, Path: skillRoot, Priority: 200},
		directory,
	)
	if err == nil || !strings.Contains(err.Error(), "cannot declare url") {
		t.Fatalf("dependency URL injection result = %v", err)
	}
}

func TestStatefulWorkflowFixtureIsPortableAndSemanticallyValid(t *testing.T) {
	projectRoot := testProjectRoot(t)
	root := SkillRoot{
		Scope: SkillScopeWorkspace,
		Path:  filepath.Join(projectRoot, "fixtures", "skills", "stateful"),
	}
	packages, diagnostics := scanSkillRoots(projectRoot, []SkillRoot{root})
	if len(diagnostics) != 0 {
		t.Fatalf("stateful fixture diagnostics = %+v", diagnostics)
	}
	if len(packages) != 1 {
		t.Fatalf("stateful fixture count = %d, want 1", len(packages))
	}
	skill := packages[0]
	if skill.CapabilityID != "story_review_workflow" ||
		skill.ExecutionMode != "stateful_workflow" ||
		skill.WorkflowRef != "content-agent/workflow.json" ||
		skill.WorkflowDefinition == nil {
		t.Fatalf("stateful fixture = %+v", skill)
	}

	workflowFile := filepath.Join(skill.Directory, filepath.FromSlash(skill.WorkflowRef))
	manifest, err := decodeManifest(workflowFile)
	if err != nil {
		t.Fatalf("decode workflow fixture: %v", err)
	}
	if manifest.ID != skill.CapabilityID || manifest.Version != skill.Version {
		t.Fatalf(
			"workflow identity = %s@%s, skill identity = %s@%s",
			manifest.ID,
			manifest.Version,
			skill.CapabilityID,
			skill.Version,
		)
	}
	definition, err := compileManifest(manifest)
	if err != nil {
		t.Fatalf("compile workflow fixture: %v", err)
	}
	if len(definition.Steps) != 1 || definition.Steps[0].ExecutorRef != "worker.structured_content" {
		t.Fatalf("compiled workflow fixture = %+v", definition)
	}
	if definition.Steps[0].PromptRef == nil || len(definition.Steps[0].OutputRefs) != 1 {
		t.Fatalf("workflow fixture references = %+v", definition.Steps[0])
	}
	compiled := compileSkillPackage(skill)
	if compiled.Kind != "domain_workflow" || compiled.ExecutionMode != "stateful_workflow" ||
		len(compiled.Steps) != 1 || compiled.Steps[0].ExecutorRef != "worker.structured_content" {
		t.Fatalf("compiled stateful Skill = %+v", compiled)
	}
	registry := NewEmptyRegistry()
	registry.projectRoot = projectRoot
	registry.skillRoots = []SkillRoot{root}
	if err := registry.RefreshSkills(); err != nil {
		t.Fatalf("RefreshSkills() error = %v", err)
	}
	entry, ok := registry.Get(skill.CapabilityID)
	if !ok || entry.Status != Available || entry.ContentRoot != skill.Directory ||
		entry.SourceFile != workflowFile || len(entry.Definition.Steps) != 1 {
		t.Fatalf("registered stateful Skill = %+v", entry)
	}

	for _, reference := range []string{
		manifest.InputSchemaRef,
		manifest.ConfigSchemaRefs["review"],
		definition.Steps[0].OutputRefs[0].SchemaRef,
		*definition.Steps[0].PromptRef,
	} {
		pathReference := strings.SplitN(reference, "#", 2)[0]
		packageReference := filepath.ToSlash(filepath.Join("content-agent", pathReference))
		if err := validateSkillAssetPath(skill.Directory, packageReference); err != nil {
			t.Fatalf("portable workflow reference %q: %v", reference, err)
		}
	}
}

func writeTestSkill(t *testing.T, root, name, description, version string) string {
	t.Helper()
	directory := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(directory, "content-agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	skill := "---\nname: " + name + "\ndescription: " + description + "\n---\n\nFollow these instructions.\n"
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema_version":"1.0.0","version":"` + version + `","execution_mode":"inline","ui":{"icon_key":"sparkles","sort_order":1000,"entry_view_key":"source_materials"}}`
	if err := os.WriteFile(filepath.Join(directory, "content-agent", "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return directory
}
