package httpapi

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/scriptsandbox"
	"content-agent/backend/internal/shell"
)

type availablePolicySandbox struct{}

func (availablePolicySandbox) Status() scriptsandbox.Status {
	return scriptsandbox.Status{
		Available: true,
		Adapter:   "linux",
		Engine:    "docker",
		Runtimes:  []string{"python"},
	}
}

func (availablePolicySandbox) Execute(context.Context, scriptsandbox.Request) (scriptsandbox.Result, error) {
	return scriptsandbox.Result{}, errors.New("unexpected sandbox execution")
}

func TestScriptSandboxPolicyRequiresAdminAndUsesAuthenticatedWorkspace(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "script-sidecar-token")
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetScriptSandbox(availablePolicySandbox{})
	authenticator, err := NewStaticTokenAuthenticator([]StaticTokenPrincipal{
		{Token: "admin-a", UserID: "admin_user_a", WorkspaceID: "workspace_a", WorkspaceName: "Workspace A", Role: identity.RoleAdmin},
		{Token: "editor-a", UserID: "editor_user_a", WorkspaceID: "workspace_a", WorkspaceName: "Workspace A", Role: identity.RoleEditor},
		{Token: "admin-b", UserID: "admin_user_b", WorkspaceID: "workspace_b", WorkspaceName: "Workspace B", Role: identity.RoleAdmin},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithRuntime(shell.New(registry), store, nil)
	if err := server.ConfigureAuthentication(context.Background(), authenticator); err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()

	initial := objectAt(t, performJSONWithHeaders(
		t, handler, http.MethodGet, "/api/v1/script-sandbox-policy", nil,
		bearer("editor-a", ""), http.StatusOK,
	), "data")
	if initial["enabled"] != false || initial["version"] != float64(0) {
		t.Fatalf("initial policy = %#v", initial)
	}
	performJSONWithHeaders(
		t, handler, http.MethodPut, "/api/v1/script-sandbox-policy",
		map[string]any{"expected_version": 0, "enabled": true},
		bearer("editor-a", "11111111-1111-4111-8111-111111111111"), http.StatusForbidden,
	)
	enabled := objectAt(t, performJSONWithHeaders(
		t, handler, http.MethodPut, "/api/v1/script-sandbox-policy",
		map[string]any{"expected_version": 0, "enabled": true, "actor_ref": "forged_actor"},
		bearer("admin-a", "22222222-2222-4222-8222-222222222222"), http.StatusOK,
	), "data")
	if enabled["enabled"] != true || enabled["version"] != float64(1) || enabled["updated_by"] != "admin_user_a" {
		t.Fatalf("enabled policy = %#v", enabled)
	}
	workspaceB := objectAt(t, performJSONWithHeaders(
		t, handler, http.MethodGet, "/api/v1/script-sandbox-policy", nil,
		bearer("admin-b", ""), http.StatusOK,
	), "data")
	if workspaceB["enabled"] != false || workspaceB["version"] != float64(0) {
		t.Fatalf("workspace B policy = %#v", workspaceB)
	}

	performJSONWithHeaders(
		t, handler, http.MethodPost, "/internal/v1/agent-tool-calls/call-1/skill-script-executions",
		map[string]any{}, bearer("admin-a", ""), http.StatusUnauthorized,
	)
	performJSONWithHeaders(
		t, handler, http.MethodPost, "/internal/v1/agent-tool-calls/call-1/skill-script-executions",
		map[string]any{}, map[string]string{"Authorization": "Bearer script-sidecar-token"}, http.StatusBadRequest,
	)
}
