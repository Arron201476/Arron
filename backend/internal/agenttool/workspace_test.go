package agenttool

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkspaceCatalogFiltersBeforeOverridesAndDoesNotMutateOperatorConfig(t *testing.T) {
	config := Config{SchemaVersion: SchemaVersion, MCPServers: []MCPServerConfig{{
		ID: "private", Description: "Private service", WorkspaceIDs: []string{"workspace_a"},
		Transport: "streamable_http", URL: "https://example.com/mcp", Enabled: false,
		HeaderEnvironment: map[string]string{"Authorization": "PRIVATE_ENV_NAME"},
		AllowedTools:      []MCPToolConfig{{Name: "lookup", Description: "Lookup", Access: AccessRead}},
	}}, HostedTools: []HostedToolConfig{{ID: "private-files", Type: "file_search", Description: "Private library",
		WorkspaceIDs: []string{"workspace_a"}, VectorStoreIDs: []string{"vs_private"}, Enabled: false, Access: AccessRead}}}
	registry, err := Compile(config)
	if err != nil {
		t.Fatal(err)
	}
	settings := WorkspaceSettings{Enabled: map[string]bool{"mcp:private": true, "hosted:private-files": true, "hosted:native-code-interpreter": true}}
	a, err := registry.ForWorkspace("workspace_a", settings)
	if err != nil {
		t.Fatal(err)
	}
	b, err := registry.ForWorkspace("workspace_b", settings)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor, ok := a.Get("mcp:private/lookup"); !ok || !descriptor.Enabled || descriptor.ConfigurationHash == "" {
		t.Fatalf("missing granted tool: %+v", descriptor)
	}
	if _, ok := b.Get("mcp:private/lookup"); ok {
		t.Fatal("override revived another workspace's MCP")
	}
	if _, ok := b.Get("hosted:private-files"); ok {
		t.Fatal("override revived another workspace's library")
	}
	if descriptor, _ := registry.Get("mcp:private/lookup"); descriptor.Enabled {
		t.Fatal("workspace enabled the global definition")
	}
	public, _ := json.Marshal(a.PublicCatalog())
	for _, secret := range []string{"PRIVATE_ENV_NAME", "example.com", "vs_private"} {
		if strings.Contains(string(public), secret) {
			t.Fatalf("public catalog leaked %s", secret)
		}
	}
	for _, option := range b.ConfigurationOptions() {
		if option.ID == "hosted:native-file-search" && (option.Configurable || option.UserMessage == "") {
			t.Fatal("ungranted file search is configurable")
		}
	}
	if _, err := registry.ForWorkspace("workspace_b", WorkspaceSettings{Enabled: map[string]bool{"hosted:native-file-search": true}}); err == nil {
		t.Fatal("file search enabled without a trusted vector store")
	}
}

func TestToolConfigurationHashChangesForPrivateExecutionParameters(t *testing.T) {
	config := Config{SchemaVersion: SchemaVersion, MCPServers: []MCPServerConfig{{ID: "service", Description: "Service", Transport: "streamable_http", URL: "https://example.com/old", Enabled: true,
		AllowedTools: []MCPToolConfig{{Name: "save", Description: "Save", Access: AccessWrite}}}}}
	first, err := Compile(config)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := first.Get("mcp:service/save")
	config.MCPServers[0].URL = "https://example.com/new"
	second, err := Compile(config)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := second.Get("mcp:service/save")
	if old.ConfigurationHash == current.ConfigurationHash {
		t.Fatal("endpoint replacement reused the approval identity")
	}
	cloned, err := first.ForWorkspace("workspace_a", WorkspaceSettings{})
	if err != nil {
		t.Fatal(err)
	}
	clone, _ := cloned.Get(old.ID)
	if clone.ConfigurationHash != old.ConfigurationHash {
		t.Fatal("unchanged workspace compilation changed fingerprint")
	}
}
