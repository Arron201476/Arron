package agenttool

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"content-agent/backend/internal/capability"
)

func TestCompileBuildsUnifiedCatalogAndResolvesSkillDependency(t *testing.T) {
	registry, err := Compile(Config{
		SchemaVersion: SchemaVersion,
		MCPServers: []MCPServerConfig{{
			ID: "story-data", Description: "Story facts fixture",
			Transport: "streamable_http", URL: "http://127.0.0.1:9123/mcp",
			Enabled: true, DeferLoading: true, TimeoutSeconds: 5,
			MaxRetries: 0, MaxResultBytes: 32 * 1024,
			AllowedTools: []MCPToolConfig{
				{Name: "lookup_fact", Description: "Read one fact", Access: AccessRead},
				{Name: "save_fact", Description: "Save one fact", Access: AccessWrite},
			},
		}},
		HostedTools: []HostedToolConfig{{
			ID: "web", Type: "web_search", Description: "Search public web",
			Enabled: true, Access: AccessRead,
		}},
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}

	catalog := registry.PublicCatalog()
	if len(catalog.Tools) != 38 {
		t.Fatalf("tool count = %d, want 38", len(catalog.Tools))
	}
	native, ok := registry.Get("runtime:exec_command")
	if !ok || native.Access != AccessSensitive || native.Approval != ApprovalAlways || native.MaxRetries != 0 {
		t.Fatalf("native shell authority: %+v", native)
	}
	stdin, ok := registry.Get("runtime:write_stdin")
	if !ok || stdin.Access != AccessSensitive || stdin.Approval != ApprovalAlways || stdin.MaxRetries != 0 {
		t.Fatalf("native stdin authority: %+v", stdin)
	}
	native, ok = registry.Get("runtime:apply_patch")
	if !ok || native.Access != AccessWrite || native.Approval != ApprovalAlways || native.MaxRetries != 0 {
		t.Fatalf("native patch authority: %+v", native)
	}
	publication, ok := registry.Get("runtime:publish_workspace_files")
	if !ok || publication.Access != AccessWrite || publication.Approval != ApprovalAlways || publication.MaxRetries != 0 {
		t.Fatalf("publication authority: %+v", publication)
	}
	image, ok := registry.Get("runtime:view_image")
	if !ok || !image.Enabled || image.Access != AccessRead || image.Approval != ApprovalNever || image.MaxResultBytes != 16*1024*1024 {
		t.Fatalf("native image authority: %+v", image)
	}
	for name, access := range map[string]Access{"list_workspace_files": AccessRead, "read_workspace_file": AccessRead, "get_artifact_downloads": AccessRead, "apply_workspace_patch": AccessWrite} {
		tool, ok := registry.Get("runtime:" + name)
		if !ok || tool.Access != access || !tool.Enabled || tool.Approval != ApprovalNever {
			t.Fatalf("workspace file tool descriptor = %+v, present = %v", tool, ok)
		}
	}
	for _, name := range []string{"list_execution_targets", "inspect_execution_controls", "control_execution"} {
		tool, ok := registry.Get("runtime:" + name)
		access, approval := AccessRead, ApprovalNever
		if name == "control_execution" {
			access, approval = AccessWrite, ApprovalAlways
		}
		if !ok || !tool.Enabled || tool.Access != access || tool.Approval != approval || tool.MaxRetries != 0 {
			t.Fatalf("execution control descriptor = %+v, present = %v", tool, ok)
		}
	}
	for _, name := range []string{"get_saved_instructions", "update_saved_instructions"} {
		tool, ok := registry.Get("runtime:" + name)
		access, approval := AccessRead, ApprovalNever
		if name == "update_saved_instructions" {
			access, approval = AccessWrite, ApprovalAlways
		}
		if !ok || !tool.Enabled || tool.Access != access || tool.Approval != approval || tool.MaxRetries != 0 {
			t.Fatalf("saved instruction descriptor = %+v, present = %v", tool, ok)
		}
	}
	subtask, ok := registry.Get("runtime:delegate_subtask")
	if !ok || !subtask.Enabled || subtask.Access != AccessRead || subtask.Approval != ApprovalNever || subtask.TimeoutSeconds != 180 || subtask.MaxRetries != 0 {
		t.Fatalf("subtask descriptor = %+v, present = %v", subtask, ok)
	}
	scriptTool, ok := registry.Get("runtime:execute_skill_script")
	if !ok || scriptTool.Access != AccessSensitive || scriptTool.Approval != ApprovalAlways ||
		scriptTool.TimeoutSeconds != 300 {
		t.Fatalf("script tool descriptor = %+v, present = %v", scriptTool, ok)
	}
	encoded, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "127.0.0.1") {
		t.Fatalf("public catalog leaked MCP endpoint: %s", encoded)
	}

	resolution := registry.ResolveSkillDependencies([]capability.SkillDependency{{
		Type: "mcp", Value: "story-data/lookup_fact", Transport: "streamable_http",
	}})
	if !resolution.Available || len(resolution.Dependencies) != 1 ||
		resolution.Dependencies[0].Status != capability.Available {
		t.Fatalf("dependency resolution = %+v", resolution)
	}

	private := registry.PrivateCatalog()
	if len(private.MCPServers) != 1 || private.MCPServers[0].URL == "" {
		t.Fatalf("private catalog omitted runtime MCP config: %+v", private)
	}
	private.MCPServers[0].AllowedTools[0].Name = "mutated"
	if registry.PrivateCatalog().MCPServers[0].AllowedTools[0].Name != "lookup_fact" {
		t.Fatal("PrivateCatalog returned mutable registry state")
	}
}

func TestCompileRejectsHostedConfigurationsTheSDKAdapterCannotExecute(t *testing.T) {
	for _, tool := range []HostedToolConfig{
		{Type: "file_search", Access: AccessRead},
		{Type: "code_interpreter", Access: AccessRead},
		{Type: "image_generation", Access: AccessRead},
		{Type: "web_search", Access: AccessSensitive, Approval: ApprovalNever},
		{Type: "file_search", Access: AccessRead, VectorStoreIDs: []string{"not-a-store"}},
		{Type: "web_search", Access: AccessRead, VectorStoreIDs: []string{"vs_other"}},
	} {
		tool.ID, tool.Description, tool.Enabled = "hosted", "Hosted fixture", true
		if _, err := Compile(Config{SchemaVersion: SchemaVersion, HostedTools: []HostedToolConfig{tool}}); err == nil {
			t.Errorf("unsupported enabled hosted tool accepted: %+v", tool)
		}
	}
}

func TestCompileSupportsAllHostedAdaptersWithRequiredPolicies(t *testing.T) {
	for _, tool := range []HostedToolConfig{
		{Type: "web_search", Access: AccessRead},
		{Type: "web_search", Access: AccessRead, Approval: ApprovalAlways},
		{Type: "file_search", Access: AccessRead, VectorStoreIDs: []string{"vs_private"}},
		{Type: "code_interpreter", Access: AccessSensitive},
		{Type: "image_generation", Access: AccessWrite},
	} {
		tool.ID, tool.Description, tool.Enabled = "hosted", "Hosted fixture", true
		registry, err := Compile(Config{SchemaVersion: SchemaVersion, HostedTools: []HostedToolConfig{tool}})
		if err != nil {
			t.Fatalf("%s: %v", tool.Type, err)
		}
		if tool.Type == "file_search" {
			private := registry.PrivateCatalog()
			private.HostedTools[0].VectorStoreIDs[0] = "vs_changed"
			if registry.PrivateCatalog().HostedTools[0].VectorStoreIDs[0] != "vs_private" {
				t.Fatal("hosted config is not isolated from callers")
			}
			public, _ := json.Marshal(registry.PublicCatalog())
			if strings.Contains(string(public), "vs_private") {
				t.Fatal("public catalog leaked vector store identity")
			}
		}
	}
}

func TestCompileDefaultsSensitiveToolToApproval(t *testing.T) {
	registry, err := Compile(Config{
		SchemaVersion: SchemaVersion,
		MCPServers: []MCPServerConfig{{
			ID: "writer", Description: "Writer fixture", Transport: "sse",
			URL: "https://tools.example.test/sse", Enabled: true,
			AllowedTools: []MCPToolConfig{{
				Name: "publish", Description: "Publish content", Access: AccessSensitive,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	resolution := registry.ResolveSkillDependencies([]capability.SkillDependency{{
		Type: "mcp", Value: "writer/publish",
	}})
	if !resolution.Available {
		t.Fatalf("dependency resolution = %+v", resolution)
	}
	var descriptor Descriptor
	for _, item := range registry.PublicCatalog().Tools {
		if item.ID == "mcp:writer/publish" {
			descriptor = item
		}
	}
	if descriptor.Approval != ApprovalAlways {
		t.Fatalf("sensitive tool approval = %q, want always", descriptor.Approval)
	}
}

func TestResolveSkillDependencyReturnsActionableFailures(t *testing.T) {
	registry, err := Compile(Config{
		SchemaVersion: SchemaVersion,
		MCPServers: []MCPServerConfig{{
			ID: "disabled", Description: "Disabled fixture", Transport: "streamable_http",
			URL: "http://localhost:9123/mcp", Enabled: false,
			AllowedTools: []MCPToolConfig{{
				Name: "read", Description: "Read content", Access: AccessRead,
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		dependency capability.SkillDependency
		code       string
	}{
		{name: "invalid", dependency: capability.SkillDependency{Type: "mcp", Value: "bad"}, code: "SKILL_TOOL_REFERENCE_INVALID"},
		{name: "missing", dependency: capability.SkillDependency{Type: "mcp", Value: "missing/read"}, code: "SKILL_TOOL_DEPENDENCY_MISSING"},
		{name: "disabled", dependency: capability.SkillDependency{Type: "mcp", Value: "disabled/read"}, code: "SKILL_TOOL_DEPENDENCY_DISABLED"},
		{name: "transport", dependency: capability.SkillDependency{Type: "mcp", Value: "disabled/read", Transport: "sse"}, code: "SKILL_TOOL_DEPENDENCY_DISABLED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolution := registry.ResolveSkillDependencies([]capability.SkillDependency{test.dependency})
			if resolution.Available || resolution.ReasonCode != test.code || resolution.Message == "" {
				t.Fatalf("resolution = %+v", resolution)
			}
		})
	}
}

func TestCompileRejectsUnsafeRemoteAndStdioConfiguration(t *testing.T) {
	tests := []struct {
		name     string
		config   Config
		contains string
	}{
		{
			name:     "remote plaintext",
			config:   mcpConfig("http://tools.example.test/mcp"),
			contains: "must use https",
		},
		{
			name:     "url secret",
			config:   mcpConfig("https://tools.example.test/mcp?token=secret"),
			contains: "query parameters",
		},
		{
			name: "stdio disabled",
			config: Config{
				SchemaVersion: SchemaVersion,
				MCPServers: []MCPServerConfig{{
					ID: "local", Description: "Local fixture", Transport: "stdio",
					Command: filepath.Join(t.TempDir(), "server.exe"), Cwd: t.TempDir(), Enabled: true,
					AllowedTools: []MCPToolConfig{{Name: "read", Description: "Read", Access: AccessRead}},
				}},
			},
			contains: "disabled by policy",
		},
		{
			name: "approval with retries",
			config: Config{
				SchemaVersion: SchemaVersion,
				MCPServers: []MCPServerConfig{{
					ID: "writer", Description: "Writer fixture", Transport: "streamable_http",
					URL: "https://tools.example.test/mcp", Enabled: true, MaxRetries: 1,
					AllowedTools: []MCPToolConfig{{
						Name: "publish", Description: "Publish", Access: AccessWrite,
					}},
				}},
			},
			contains: "max_retries must be 0",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Compile(test.config)
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("Compile() error = %v, want containing %q", err, test.contains)
			}
		})
	}
}

func TestFixtureSkillResolvesTrustedMCPDependenciesWithoutCoreRegistration(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source file")
	}
	projectRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", ".."))
	command := filepath.Join(
		projectRoot, ".tmp", "sidecar-test-venv", "Scripts", "python.exe",
	)
	fixtureRoot := filepath.Join(projectRoot, "fixtures", "skills", "mcp")
	toolRegistry, err := Compile(Config{
		SchemaVersion: SchemaVersion,
		StdioPolicy: StdioPolicy{
			Enabled: true, AllowedCommands: []string{command}, AllowedCwds: []string{projectRoot},
		},
		MCPServers: []MCPServerConfig{{
			ID: "story-fixture", Description: "Story protocol fixture", Transport: "stdio",
			Command: command, Cwd: fixtureRoot, Enabled: true,
			AllowedTools: []MCPToolConfig{
				{Name: "lookup_story_fact", Description: "Read story fact", Access: AccessRead},
				{Name: "save_story_fact", Description: "Save story fact", Access: AccessWrite},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	capabilityRegistry, err := capability.LoadRegistry(capability.LoadOptions{
		ProjectRoot: projectRoot,
		SkillRoots: []capability.SkillRoot{{
			Scope: capability.SkillScopeWorkspace, Path: fixtureRoot, Priority: 200,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := capabilityRegistry.SetSkillDependencyResolver(toolRegistry); err != nil {
		t.Fatal(err)
	}
	entry, ok := capabilityRegistry.Get("story_fact_check")
	if !ok || entry.Status != capability.Available || entry.Skill == nil {
		t.Fatalf(
			"MCP fixture Skill entry = %+v, found = %v, diagnostics = %+v",
			entry, ok, capabilityRegistry.SkillDiagnostics(),
		)
	}
	if len(entry.Skill.Dependencies) != 2 {
		t.Fatalf("MCP fixture dependency count = %d, want 2", len(entry.Skill.Dependencies))
	}
	for _, dependency := range entry.Skill.Dependencies {
		if dependency.Status != capability.Available {
			t.Fatalf("MCP fixture dependency = %+v", dependency)
		}
	}
}

func mcpConfig(endpoint string) Config {
	return Config{
		SchemaVersion: SchemaVersion,
		MCPServers: []MCPServerConfig{{
			ID: "remote", Description: "Remote fixture", Transport: "streamable_http",
			URL: endpoint, Enabled: true,
			AllowedTools: []MCPToolConfig{{Name: "read", Description: "Read", Access: AccessRead}},
		}},
	}
}
