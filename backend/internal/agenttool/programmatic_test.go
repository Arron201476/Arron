package agenttool

import "testing"

func TestProgrammaticConfigurationScopeAndIsolation(t *testing.T) {
	config := Config{SchemaVersion: SchemaVersion, Programmatic: ProgrammaticConfig{
		Enabled: true, WorkspaceIDs: []string{"workspace-a"}, ToolIDs: []string{"runtime:read_workspace_file"},
	}}
	registry, err := Compile(config)
	if err != nil {
		t.Fatal(err)
	}
	config.Programmatic.ToolIDs[0] = "runtime:exec_command"
	config.Programmatic.WorkspaceIDs[0] = "workspace-b"
	for _, workspace := range []string{"workspace-a", "workspace-b"} {
		scoped, err := registry.ForWorkspace(workspace, WorkspaceSettings{})
		if err != nil {
			t.Fatal(err)
		}
		private := scoped.PrivateCatalog()
		want := 0
		if workspace == "workspace-a" {
			want = 1
		}
		if len(private.ProgrammaticToolIDs) != want {
			t.Fatalf("workspace %s received unexpected programmatic grants", workspace)
		}
		if len(scoped.PublicCatalog().ProgrammaticToolIDs) != 0 {
			t.Fatal("private configuration leaked in public catalog")
		}
		if want == 1 {
			private.ProgrammaticToolIDs[0] = "mutated"
			if scoped.PrivateCatalog().ProgrammaticToolIDs[0] != "runtime:read_workspace_file" {
				t.Fatal("private catalog aliases registry configuration")
			}
		}
		descriptor, _ := scoped.Get("runtime:read_workspace_file")
		if (descriptor.ConfigurationHash != "") != (want == 1) {
			t.Fatal("programmatic grant not bound to configuration hash")
		}
	}
}

func TestProgrammaticConfigurationRejectsUnsafeOrAmbiguousGrants(t *testing.T) {
	for _, ids := range [][]string{nil, {"runtime:missing"}, {"runtime:exec_command"},
		{"runtime:install_workspace_skill"}, {"runtime:read_workspace_file", "runtime:read_workspace_file"}} {
		_, err := Compile(Config{SchemaVersion: SchemaVersion, Programmatic: ProgrammaticConfig{Enabled: true, ToolIDs: ids}})
		if err == nil {
			t.Fatalf("invalid programmatic tools accepted: %v", ids)
		}
	}
	for _, ids := range [][]string{{""}, {" workspace-a"}, {"workspace-a", "workspace-a"}} {
		_, err := Compile(Config{SchemaVersion: SchemaVersion, Programmatic: ProgrammaticConfig{WorkspaceIDs: ids}})
		if err == nil {
			t.Fatalf("invalid workspace IDs accepted: %v", ids)
		}
	}
}

func TestProgrammaticConfigurationDefaultDisabled(t *testing.T) {
	registry, err := Compile(Config{SchemaVersion: SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.PrivateCatalog().ProgrammaticToolIDs) != 0 {
		t.Fatal("programmatic execution enabled without operator configuration")
	}
}

func TestProgrammaticWorkspaceToggleCannotExpandOperatorScope(t *testing.T) {
	registry, err := Compile(Config{SchemaVersion: SchemaVersion, Programmatic: ProgrammaticConfig{
		WorkspaceIDs: []string{"workspace-a"}, ToolIDs: []string{"runtime:read_workspace_file"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, workspace := range []string{"workspace-a", "workspace-b"} {
		for _, enabled := range []bool{false, true} {
			scoped, err := registry.ForWorkspace(workspace, WorkspaceSettings{Enabled: map[string]bool{ProgrammaticOptionID: enabled}, Revision: 2})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, option := range scoped.ConfigurationOptions() {
				if option.ID != ProgrammaticOptionID {
					continue
				}
				found = true
				if option.Configurable != (workspace == "workspace-a") || option.Enabled != (workspace == "workspace-a" && enabled) {
					t.Fatal("workspace toggle expanded operator scope")
				}
				if (len(scoped.PrivateCatalog().ProgrammaticToolIDs) > 0) != option.Enabled {
					t.Fatal("configuration option disagrees with private grants")
				}
			}
			if !found {
				t.Fatal("programmatic configuration option missing")
			}
		}
	}
}
