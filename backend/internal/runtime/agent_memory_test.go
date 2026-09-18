package runtime

import (
	"context"
	"testing"

	"content-agent/backend/internal/identity"
)

func TestAgentMemoryForgetRedactsReceiptsAndRejectsStaleWriters(t *testing.T) {
	store, _, _ := mcpConnectionStore(t)
	ctx := mcpOwnerContext()
	project, err := store.CreateProject(ctx, "Memory")
	if err != nil {
		t.Fatal(err)
	}
	command := UpdateAgentMemoryCommand{ProjectID: project.ProjectID, Files: map[string]string{"memory_summary.md": "private summary", "MEMORY.md": "private detail"}, Enabled: true, RequestID: "save-1"}
	first, err := store.UpdateAgentMemory(ctx, command)
	if err != nil || first.Version != 1 {
		t.Fatalf("save: %+v %v", first, err)
	}
	again, err := store.UpdateAgentMemory(ctx, command)
	if err != nil || again.ContentHash != first.ContentHash {
		t.Fatalf("receipt: %+v %v", again, err)
	}
	forget := UpdateAgentMemoryCommand{ProjectID: project.ProjectID, ExpectedVersion: 1, Forget: true, RequestID: "forget-1"}
	forgotten, err := store.UpdateAgentMemory(ctx, forget)
	if err != nil || forgotten.Version != 2 || !forgotten.Forgotten || len(forgotten.Files) != 0 {
		t.Fatalf("forget: %+v %v", forgotten, err)
	}
	again, err = store.UpdateAgentMemory(ctx, command)
	if err != nil || !again.Forgotten || len(again.Files) != 0 || again.Enabled {
		t.Fatalf("old receipt exposed forgotten body: %+v %v", again, err)
	}
	command.RequestID = "stale-worker"
	command.ExpectedVersion = 1
	_, err = store.UpdateAgentMemory(ctx, command)
	assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
	other := identity.Principal{Kind: identity.KindUser, UserID: "memory-other", WorkspaceID: identity.DefaultWorkspaceID, Role: identity.RoleEditor}
	if err := store.BootstrapPrincipal(ctx, other); err != nil {
		t.Fatal(err)
	}
	view, err := store.GetAgentMemory(identity.WithPrincipal(context.Background(), other), project.ProjectID)
	if err != nil || view.Version != 0 || len(view.Files) != 0 {
		t.Fatalf("private memory leak: %+v %v", view, err)
	}
}

func TestAgentMemoryRejectsUnsafeBundles(t *testing.T) {
	for _, files := range []map[string]string{
		{"../memory_summary.md": "x"}, {"CON": "x"}, {"bad?.md": "x"},
		{"memory_summary.md": "x", "MEMORY_SUMMARY.md": "y"},
		{"notes/A/a.md": "x", "notes/a/b.md": "y"},
		{"notes": "x", "notes/a.md": "y"},
		{"memory_summary.md": "bad\x00text"},
	} {
		_, err := validateAgentMemory(UpdateAgentMemoryCommand{ProjectID: "project", RequestID: "request", Files: files})
		assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
	}
}
