package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"content-agent/backend/internal/identity"
)

func TestMemoryGenerationListResumeAvailability(t *testing.T) {
	store, job, _ := queuedMemoryGenerationFixture(t, "conversation")
	for _, test := range []struct {
		status     string
		started    bool
		checkpoint string
		available  bool
	}{
		{"queued", false, "", false},
		{"running", true, "{}", false},
		{"paused", false, "", true},
		{"paused", true, "", false},
		{"paused", true, "{}", true},
		{"completed", true, "{}", false},
	} {
		if _, err := store.db.Exec(`UPDATE agent_memory_generations SET status=?,started=?,checkpoint_json=?,checkpoint_hash=? WHERE generation_id=?`, test.status, test.started, test.checkpoint, sha256Hex([]byte(test.checkpoint)), job.GenerationID); err != nil {
			t.Fatal(err)
		}
		page, err := store.ListAgentMemoryGenerations(mcpOwnerContext(), job.ProjectID, "")
		if err != nil || len(page.Items) != 1 || page.Items[0].ResumeAvailable != test.available {
			t.Fatalf("resume availability for %+v: %+v %v", test, page, err)
		}
	}
}

func TestMemoryGenerationListIsOwnerScopedAndDoesNotReadPrivateBodies(t *testing.T) {
	store, job, original := queuedMemoryGenerationFixture(t, "conversation")
	if _, err := store.db.Exec(`UPDATE agent_memory_generations SET checkpoint_json='PRIVATE_INVALID_CHECKPOINT',token_hash='PRIVATE_TOKEN' WHERE generation_id=?`, job.GenerationID); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListAgentMemoryGenerations(mcpOwnerContext(), job.ProjectID, "")
	if err != nil || len(page.Items) != 1 || page.Items[0].GenerationID != job.GenerationID || page.NextCursor != "" {
		t.Fatalf("list: %+v %v", page, err)
	}
	body, err := json.Marshal(page)
	if err != nil || strings.Contains(string(body), "PRIVATE") || strings.Contains(string(body), "checkpoint") {
		t.Fatal("private state leaked to listing")
	}
	after, err := store.ListAgentMemoryGenerations(mcpOwnerContext(), job.ProjectID, job.GenerationID)
	if err != nil || len(after.Items) != 0 {
		t.Fatalf("cursor: %+v %v", after, err)
	}
	other := identity.Principal{Kind: identity.KindUser, UserID: "other-list-editor", WorkspaceID: job.WorkspaceID, Role: identity.RoleEditor}
	if err := store.BootstrapPrincipal(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	otherContext := identity.WithPrincipal(context.Background(), other)
	hidden, err := store.ListAgentMemoryGenerations(otherContext, job.ProjectID, "")
	if err != nil || len(hidden.Items) != 0 {
		t.Fatalf("other owner: %+v %v", hidden, err)
	}
	_, err = store.ListAgentMemoryGenerations(otherContext, job.ProjectID, job.GenerationID)
	assertDomainCode(t, err, "AGENT_MEMORY_GENERATION_NOT_FOUND")
	_, err = store.ListAgentMemoryGenerations(original, job.ProjectID, "")
	assertDomainCode(t, err, "AUTHENTICATION_REQUIRED")
}
