package runtime

import (
	"context"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
)

func TestSkillScopeSelectionSeparatesUsersProjectsAndWorkspaces(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "scopes.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, "Scoped project")
	if err != nil {
		t.Fatal(err)
	}
	otherProject, err := store.CreateProject(ctx, "Another project")
	if err != nil {
		t.Fatal(err)
	}
	install := func(scope, ref, instructions string) SkillInstallation {
		directory := writeSkillTestPackage(t, t.TempDir(), "scoped-choice", "scoped_choice", "1.0.0", "inline", instructions)
		target := SkillInstallTarget{Scope: capability.SkillScope(scope)}
		if target.Scope == capability.SkillScopeProject {
			target.ProjectID = ref
		}
		installed, err := store.InstallSkillDirectory(ctx, directory, "fixture", target)
		if err != nil {
			t.Fatal(err)
		}
		return installed
	}
	personal := install("user", identity.DefaultUserID, "PERSONAL_CONTENT")
	install("project", project.ProjectID, "PROJECT_CONTENT")
	install("workspace", identity.DefaultWorkspaceID, "WORKSPACE_CONTENT")
	otherUser := identity.Principal{Kind: identity.KindUser, UserID: "other-user", WorkspaceID: identity.DefaultWorkspaceID, Role: identity.RoleEditor}
	if err := store.BootstrapPrincipal(ctx, otherUser); err != nil {
		t.Fatal(err)
	}
	otherCtx := identity.WithPrincipal(ctx, otherUser)
	serviceCtx := identity.WithPrincipal(ctx, identity.ServicePrincipal())
	for _, tc := range []struct {
		ctx                 context.Context
		projectID, expected string
	}{
		{ctx, project.ProjectID, "PERSONAL_CONTENT"}, {ctx, otherProject.ProjectID, "PERSONAL_CONTENT"},
		{otherCtx, project.ProjectID, "PROJECT_CONTENT"}, {otherCtx, otherProject.ProjectID, "WORKSPACE_CONTENT"},
		{serviceCtx, project.ProjectID, "PROJECT_CONTENT"},
	} {
		registry, err := store.buildSelectionRegistry(tc.ctx, store.db, identity.DefaultWorkspaceID, tc.projectID)
		if err != nil {
			t.Fatal(err)
		}
		entry, ok := registry.Get("scoped_choice")
		if !ok || entry.Status != capability.Available || entry.Skill.Instructions != tc.expected {
			t.Fatalf("expected=%s entry=%+v", tc.expected, entry)
		}
	}
	foreign, err := store.buildSelectionRegistry(otherCtx, store.db, "other-workspace", project.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := foreign.Get("scoped_choice"); ok {
		t.Fatal("workspace Skill leaked")
	}
	if _, err := store.UninstallSkill(ctx, personal.SkillInstallationID, ""); err != nil {
		t.Fatal(err)
	}
	registry, err := store.buildSelectionRegistry(ctx, store.db, identity.DefaultWorkspaceID, project.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := registry.Get("scoped_choice")
	if !ok || entry.Status != capability.Unavailable || entry.Skill.Scope != capability.SkillScopeUser {
		t.Fatalf("personal tombstone fell back: %+v", entry)
	}
	otherRegistry, err := store.buildSelectionRegistry(otherCtx, store.db, identity.DefaultWorkspaceID, project.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	otherEntry, _ := otherRegistry.Get("scoped_choice")
	if otherEntry.Status != capability.Available || otherEntry.Skill.Instructions != "PROJECT_CONTENT" {
		t.Fatal("personal uninstall changed another user's selection")
	}
}

func TestUnboundDirectoryScopesCannotBecomeGlobal(t *testing.T) {
	local := selectedSkillScope(context.Background(), identity.DefaultWorkspaceID, "project-a")
	foreign := selectedSkillScope(context.Background(), "workspace-b", "project-b")
	for _, scope := range []capability.SkillScope{capability.SkillScopeWorkspace, capability.SkillScopeUser} {
		if !local.includes(capability.SkillRoot{Scope: scope}) || foreign.includes(capability.SkillRoot{Scope: scope}) {
			t.Fatalf("legacy root leaked: %s", scope)
		}
	}
	if local.includes(capability.SkillRoot{Scope: capability.SkillScopeProject}) {
		t.Fatal("unbound project root became visible to every project")
	}
	if !local.includes(capability.SkillRoot{Scope: capability.SkillScopeProject, ScopeRef: "project-a"}) {
		t.Fatal("bound project root missing")
	}
}
