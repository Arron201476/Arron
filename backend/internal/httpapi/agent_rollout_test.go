package httpapi

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/capability"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestAgentRolloutPolicyIsFailClosedWithoutLegacyFallback(t *testing.T) {
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, err := store.CreateProject(context.Background(), "Rollout disabled")
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithRuntime(shell.New(registry), store, nil)
	server.ConfigureAgentRollout(NewAgentRolloutPolicy(false, nil, "release-test"))
	response := performJSONWithHeaders(
		t, server.Handler(), http.MethodPost,
		"/api/v1/conversations/"+project.PrimaryConversationID+"/messages",
		map[string]any{"content": "must not fall back", "attachment_refs": []any{}, "client_context": map[string]any{}},
		map[string]string{"Idempotency-Key": "97777777-7777-4777-8777-777777777777"},
		http.StatusServiceUnavailable,
	)
	if errorPayload := objectAt(t, response, "error"); stringAt(t, errorPayload, "code") != "SDK_AGENT_ROLLOUT_DISABLED" {
		t.Fatalf("rollout response = %#v", response)
	}
	turns, err := store.ListProjectAgentTurns(context.Background(), project.ProjectID, 10)
	if err != nil || len(turns) != 0 {
		t.Fatalf("disabled rollout accepted turns = %+v, error = %v", turns, err)
	}
}

func TestAgentRolloutPolicySupportsWorkspaceCanaryAndSafeStatus(t *testing.T) {
	policy := NewAgentRolloutPolicy(true, []string{"workspace_a", " workspace_b ", ""}, "release-31")
	if !policy.Allows("workspace_a") || !policy.Allows("workspace_b") || policy.Allows("workspace_c") {
		t.Fatal("workspace canary policy did not enforce its allowlist")
	}
	status := policy.Status()
	if status["mode"] != "canary" || status["canary_workspace_count"] != 2 || status["release_id"] != "release-31" {
		t.Fatalf("rollout status = %#v", status)
	}
	unsafe := NewAgentRolloutPolicy(true, nil, "release secret/value").Status()
	if unsafe["release_id"] != "dev" {
		t.Fatalf("unsafe release identifier leaked: %#v", unsafe)
	}
}
