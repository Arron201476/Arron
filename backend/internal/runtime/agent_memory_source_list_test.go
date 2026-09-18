package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"content-agent/backend/internal/identity"
)

func TestMemorySourceListingScopesCursorAndHidesForgottenSources(t *testing.T) {
	store, job, original := queuedMemoryGenerationFixture(t, "conversation")
	page, err := store.ListAgentMemorySources(mcpOwnerContext(), job.ProjectID, "")
	if err != nil || page.UserID != job.UserID || len(page.Items) != 1 || page.Items[0].GenerationID != job.GenerationID || page.Items[0].SourceHash != job.SourceHash {
		t.Fatalf("sources: %+v %v", page, err)
	}
	raw, err := json.Marshal([]string{job.ActivityKey, job.SegmentID})
	if err != nil {
		t.Fatal(err)
	}
	cursor := base64.RawURLEncoding.EncodeToString(raw)
	last, err := store.ListAgentMemorySources(mcpOwnerContext(), job.ProjectID, cursor)
	if err != nil || len(last.Items) != 0 {
		t.Fatalf("cursor: %+v %v", last, err)
	}
	other := identity.Principal{Kind: identity.KindUser, UserID: "other-source-owner", WorkspaceID: job.WorkspaceID, Role: identity.RoleEditor}
	if err := store.BootstrapPrincipal(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	otherContext := identity.WithPrincipal(context.Background(), other)
	hidden, err := store.ListAgentMemorySources(otherContext, job.ProjectID, "")
	if err != nil || len(hidden.Items) != 0 {
		t.Fatalf("scope: %+v %v", hidden, err)
	}
	_, err = store.ListAgentMemorySources(otherContext, job.ProjectID, cursor)
	assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
	_, err = store.ListAgentMemorySources(original, job.ProjectID, "")
	assertDomainCode(t, err, "AUTHENTICATION_REQUIRED")
	if _, err := store.db.Exec(`UPDATE agent_memory_rollouts SET forgotten=1 WHERE activity_key=? AND segment_id=?`, job.ActivityKey, job.SegmentID); err != nil {
		t.Fatal(err)
	}
	forgotten, err := store.ListAgentMemorySources(mcpOwnerContext(), job.ProjectID, "")
	if err != nil || len(forgotten.Items) != 0 {
		t.Fatalf("forgotten: %+v %v", forgotten, err)
	}
	_, err = store.ListAgentMemorySources(mcpOwnerContext(), job.ProjectID, cursor)
	assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
}
