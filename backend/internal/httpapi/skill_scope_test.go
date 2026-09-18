package httpapi

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestSkillScopeHTTPVisibilityManagementAndResources(t *testing.T) {
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "scope.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authenticator, err := NewStaticTokenAuthenticator([]StaticTokenPrincipal{
		{Token: "scope-owner", UserID: "scope_owner", WorkspaceID: "scope_workspace", Role: identity.RoleOwner},
		{Token: "scope-editor", UserID: "scope_editor", WorkspaceID: "scope_workspace", Role: identity.RoleEditor},
		{Token: "scope-foreign", UserID: "scope_foreign", WorkspaceID: "foreign_workspace", Role: identity.RoleOwner},
		{Token: "scope-viewer", UserID: "scope_viewer", WorkspaceID: "scope_workspace", Role: identity.RoleViewer},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithRuntime(shell.New(registry), store, nil)
	if err := server.ConfigureAuthentication(context.Background(), authenticator); err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	get := func(token, path string, status int) map[string]any {
		t.Helper()
		return performJSONWithHeaders(t, handler, http.MethodGet, path, nil, bearer(token, ""), status)
	}
	project := objectAt(t, performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/projects", map[string]any{"title": "Scope HTTP"}, bearer("scope-owner", "10101010-1010-4010-8010-101010101010"), http.StatusCreated), "data")
	projectID := stringAt(t, project, "project_id")
	for token, count := range map[string]int{"scope-owner": 3, "scope-editor": 2, "scope-viewer": 0} {
		options := objectAt(t, get(token, "/api/v1/skill-management-options", http.StatusOK), "data")
		if len(arrayAt(t, options, "install_scopes")) != count {
			t.Fatalf("scope options=%+v", options)
		}
	}
	upload := func(token, query, version, body string, status int) map[string]any {
		t.Helper()
		archive := buildSkillAPIArchive(t, "http-scope", "http_scope", version, body)
		response := performSkillZIPRequest(t, handler, http.MethodPost, "/api/v1/skills"+query, archive, bearer(token, ""), status)
		if status == http.StatusCreated {
			return objectAt(t, response, "data")
		}
		return response
	}
	workspace := upload("scope-owner", "?scope=workspace", "1.0.0", "WORKSPACE_ONLY", http.StatusCreated)
	upload("scope-editor", "?scope=project&project_id="+projectID, "1.0.0", "PROJECT_ONLY", http.StatusCreated)
	personal := upload("scope-editor", "?scope=user", "1.0.0", "PRIVATE_EDITOR_ONLY", http.StatusCreated)
	personalID := stringAt(t, personal, "skill_installation_id")
	if stringAt(t, personal, "scope_ref") != "scope_editor" {
		t.Fatalf("personal identity=%+v", personal)
	}
	for _, token := range []string{"scope-owner", "scope-viewer", "scope-foreign"} {
		get(token, "/api/v1/skills/"+personalID, http.StatusNotFound)
		get(token, "/api/v1/skills/"+personalID+"/directory-update", http.StatusNotFound)
	}
	get("scope-editor", "/api/v1/skills/"+stringAt(t, workspace, "skill_installation_id")+"/directory-update", http.StatusForbidden)
	for token, expectedCount := range map[string]int{"scope-owner": 2, "scope-editor": 3, "scope-foreign": 0} {
		for _, path := range []string{"/api/v1/skills", "/api/v1/skills/install-attempts"} {
			items := arrayAt(t, objectAt(t, get(token, path, http.StatusOK), "data"), "items")
			if len(items) != expectedCount {
				t.Fatalf("%s %s count=%d", token, path, len(items))
			}
		}
	}
	performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/skills/"+stringAt(t, workspace, "skill_installation_id")+"/disable", nil, bearer("scope-editor", ""), http.StatusForbidden)
	performJSONWithHeaders(t, handler, http.MethodDelete, "/api/v1/skills/"+personalID, nil, bearer("scope-owner", ""), http.StatusNotFound)
	upload("scope-editor", "?scope=workspace", "2.0.0", "DENIED", http.StatusForbidden)
	upload("scope-editor", "?scope=system", "2.0.0", "DENIED", http.StatusBadRequest)
	upload("scope-foreign", "?scope=project&project_id="+projectID, "2.0.0", "DENIED", http.StatusNotFound)
	resourcePath := "/api/v1/projects/" + projectID + "/capabilities/http_scope/skill-resources?version=1.0.0&path=SKILL.md"
	for token, expected := range map[string]string{"scope-owner": "PROJECT_ONLY", "scope-editor": "PRIVATE_EDITOR_ONLY"} {
		page := objectAt(t, get(token, resourcePath, http.StatusOK), "data")
		if !strings.Contains(stringAt(t, page, "content"), expected) {
			t.Fatalf("resource selection=%+v", page)
		}
	}
	upgraded := objectAt(t, performSkillZIPRequest(t, handler, http.MethodPost, "/api/v1/skills/"+personalID+"/versions?scope=workspace", buildSkillAPIArchive(t, "http-scope", "http_scope", "2.0.0", "PRIVATE_V2"), bearer("scope-editor", ""), http.StatusCreated), "data")
	if stringAt(t, upgraded, "scope") != "user" || stringAt(t, upgraded, "scope_ref") != "scope_editor" {
		t.Fatal("upgrade moved ownership")
	}
	performJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/skills/"+personalID+"/versions/1.0.0/activate", skillLifecycleBody(t, upgraded), bearer("scope-editor", ""), http.StatusOK)
	page := objectAt(t, get("scope-editor", resourcePath, http.StatusOK), "data")
	if !strings.Contains(stringAt(t, page, "content"), "PRIVATE_EDITOR_ONLY") {
		t.Fatal("rollback did not change runtime resource")
	}
}
