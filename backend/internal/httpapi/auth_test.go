package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

func TestStaticAuthenticationEnforcesTenantRoleAndActorBoundaries(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN", "sidecar-service-token")
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authenticator, err := NewStaticTokenAuthenticator([]StaticTokenPrincipal{
		{Token: "token-a", UserID: "user_a", DisplayName: "用户 A", WorkspaceID: "workspace_a", WorkspaceName: "工作区 A", Role: identity.RoleOwner},
		{Token: "token-b", UserID: "user_b", DisplayName: "用户 B", WorkspaceID: "workspace_b", WorkspaceName: "工作区 B", Role: identity.RoleOwner},
		{Token: "token-viewer", UserID: "user_viewer", DisplayName: "只读用户", WorkspaceID: "workspace_a", WorkspaceName: "工作区 A", Role: identity.RoleViewer},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithRuntime(shell.New(registry), store, nil)
	if err := server.ConfigureAuthentication(context.Background(), authenticator); err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()

	performJSON(t, handler, http.MethodGet, "/api/v1/projects", nil, http.StatusUnauthorized)
	for _, path := range []string{"/api/v1/skills/refresh", "/api/v1/skills/discovered/example/install"} {
		performJSON(t, handler, http.MethodPost, path, nil, http.StatusUnauthorized)
		performJSONWithHeaders(t, handler, http.MethodPost, path, nil, bearer("token-viewer", ""), http.StatusForbidden)
		performJSONWithHeaders(t, handler, http.MethodPost, path, nil, bearer("sidecar-service-token", ""), http.StatusForbidden)
	}
	me := performJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/auth/me", nil, bearer("token-a", ""), http.StatusOK)
	if principal := objectAt(t, me, "data"); stringAt(t, principal, "workspace_id") != "workspace_a" || stringAt(t, principal, "user_id") != "user_a" {
		t.Fatalf("principal = %#v", principal)
	}

	projectA := objectAt(t, performJSONWithHeaders(
		t, handler, http.MethodPost, "/api/v1/projects", map[string]any{"title": "同名作品"},
		bearer("token-a", "11111111-1111-4111-8111-111111111111"), http.StatusCreated,
	), "data")
	projectB := objectAt(t, performJSONWithHeaders(
		t, handler, http.MethodPost, "/api/v1/projects", map[string]any{"title": "同名作品"},
		bearer("token-b", "22222222-2222-4222-8222-222222222222"), http.StatusCreated,
	), "data")
	projectAID := stringAt(t, projectA, "project_id")
	projectBID := stringAt(t, projectB, "project_id")
	if stringAt(t, projectA, "workspace_id") != "workspace_a" || stringAt(t, projectA, "owner_user_id") != "user_a" {
		t.Fatalf("project A identity = %#v", projectA)
	}
	if stringAt(t, projectB, "workspace_id") != "workspace_b" || stringAt(t, projectB, "owner_user_id") != "user_b" {
		t.Fatalf("project B identity = %#v", projectB)
	}
	assertOnlyProject(t, handler, "token-a", projectAID)
	assertOnlyProject(t, handler, "token-b", projectBID)
	performJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/projects/"+projectAID, nil, bearer("token-b", ""), http.StatusNotFound)
	performJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/projects/"+projectAID+"/capabilities/outline_critic/skill-resources?version=1.0.0", nil, bearer("token-b", ""), http.StatusNotFound)
	performJSONWithHeaders(
		t, handler, http.MethodPatch, "/api/v1/projects/"+projectAID,
		map[string]any{"title": "越权修改", "expected_version": 1},
		bearer("token-b", "33333333-3333-4333-8333-333333333333"), http.StatusNotFound,
	)
	performJSONWithHeaders(
		t, handler, http.MethodPost, "/api/v1/projects", map[string]any{"title": "只读越权"},
		bearer("token-viewer", "44444444-4444-4444-8444-444444444444"), http.StatusForbidden,
	)
	performJSONWithHeaders(
		t, handler, http.MethodPost, "/api/v1/projects", map[string]any{"title": "服务越权"},
		bearer("sidecar-service-token", "55555555-5555-4555-8555-555555555555"), http.StatusForbidden,
	)
	performJSONWithHeaders(
		t, handler, http.MethodPost, "/internal/v1/agent/turn-commits", map[string]any{},
		bearer("token-a", "66666666-6666-4666-8666-666666666666"), http.StatusUnauthorized,
	)

	archiveA := buildSkillAPIArchive(t, "tenant-skill", "tenant_shared_skill", "1.0.0", "Only workspace A policy.")
	archiveB := buildSkillAPIArchive(t, "tenant-skill", "tenant_shared_skill", "1.0.0", "Only workspace B policy.")
	skillA := objectAt(t, performSkillZIPRequest(
		t, handler, http.MethodPost, "/api/v1/skills", archiveA,
		bearer("token-a", "77777777-7777-4777-8777-777777777777"), http.StatusCreated,
	), "data")
	skillB := objectAt(t, performSkillZIPRequest(
		t, handler, http.MethodPost, "/api/v1/skills", archiveB,
		bearer("token-b", "88888888-8888-4888-8888-888888888888"), http.StatusCreated,
	), "data")
	if stringAt(t, skillA, "created_by") != "user_a" || stringAt(t, skillB, "created_by") != "user_b" {
		t.Fatalf("Skill actors = A:%#v B:%#v", skillA, skillB)
	}
	performJSONWithHeaders(
		t, handler, http.MethodGet, "/api/v1/skills/"+stringAt(t, skillA, "skill_installation_id"),
		nil, bearer("token-b", ""), http.StatusNotFound,
	)
	registryA, err := store.CapabilityRegistryForWorkspace(context.Background(), "workspace_a")
	if err != nil {
		t.Fatal(err)
	}
	registryB, err := store.CapabilityRegistryForWorkspace(context.Background(), "workspace_b")
	if err != nil {
		t.Fatal(err)
	}
	entryA, okA := registryA.Get("tenant_shared_skill")
	entryB, okB := registryB.Get("tenant_shared_skill")
	if !okA || !okB || entryA.Skill == nil || entryB.Skill == nil ||
		!strings.Contains(entryA.Skill.Instructions, "workspace A") ||
		!strings.Contains(entryB.Skill.Instructions, "workspace B") {
		t.Fatalf("workspace Skill registries not isolated: A=%+v B=%+v", entryA, entryB)
	}

	forgedCredential := map[string]any{
		"server_id": "research", "credential_name": "default", "secret_ref": "env://WORKSPACE_A_TOKEN",
		"created_by_user_id": "user_b",
	}
	performJSONWithHeaders(
		t, handler, http.MethodPut, "/api/v1/workspace-mcp-credentials", forgedCredential,
		bearer("token-a", "99999999-9999-4999-8999-999999999999"), http.StatusBadRequest,
	)
	credential := objectAt(t, performJSONWithHeaders(
		t, handler, http.MethodPut, "/api/v1/workspace-mcp-credentials",
		map[string]any{"server_id": "research", "credential_name": "default", "secret_ref": "env://WORKSPACE_A_TOKEN"},
		bearer("token-a", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"), http.StatusOK,
	), "data")
	if stringAt(t, credential, "workspace_id") != "workspace_a" || stringAt(t, credential, "created_by_user_id") != "user_a" {
		t.Fatalf("credential identity = %#v", credential)
	}
	listB := performJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/workspace-mcp-credentials", nil, bearer("token-b", ""), http.StatusOK)
	if items := arrayAt(t, objectAt(t, listB, "data"), "items"); len(items) != 0 {
		t.Fatalf("workspace B credentials = %#v", items)
	}

	assertConcurrentCrossTenantReadsAreHidden(t, handler, projectAID, projectBID)
}

func TestBearerSessionCookieIsHttpOnlyAndCSRFProtected(t *testing.T) {
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authenticator, err := NewStaticTokenAuthenticator([]StaticTokenPrincipal{{
		Token: "session-token", UserID: "session_user", DisplayName: "会话用户",
		WorkspaceID: "session_workspace", WorkspaceName: "会话工作区", Role: identity.RoleOwner,
	}})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithRuntime(shell.New(registry), store, nil)
	if err := server.ConfigureAuthentication(context.Background(), authenticator); err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()

	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/session", nil)
	login.Header.Set("Authorization", "Bearer session-token")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login status = %d; body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	cookies := loginResponse.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookies = %#v", cookies)
	}
	me := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	me.AddCookie(cookies[0])
	meResponse := httptest.NewRecorder()
	handler.ServeHTTP(meResponse, me)
	if meResponse.Code != http.StatusOK {
		t.Fatalf("cookie auth status = %d; body=%s", meResponse.Code, meResponse.Body.String())
	}
	crossSite := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(`{"title":"cross-site"}`))
	crossSite.AddCookie(cookies[0])
	crossSite.Header.Set("Content-Type", "application/json")
	crossSite.Header.Set("Sec-Fetch-Site", "cross-site")
	crossSite.Header.Set("Idempotency-Key", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	crossSiteResponse := httptest.NewRecorder()
	handler.ServeHTTP(crossSiteResponse, crossSite)
	if crossSiteResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-site mutation status = %d; body=%s", crossSiteResponse.Code, crossSiteResponse.Body.String())
	}
	schemeMismatch := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(`{"title":"scheme-mismatch"}`))
	schemeMismatch.AddCookie(cookies[0])
	schemeMismatch.Header.Set("Content-Type", "application/json")
	schemeMismatch.Header.Set("Origin", "https://"+schemeMismatch.Host)
	schemeMismatch.Header.Set("Idempotency-Key", "cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	schemeMismatchResponse := httptest.NewRecorder()
	handler.ServeHTTP(schemeMismatchResponse, schemeMismatch)
	if schemeMismatchResponse.Code != http.StatusForbidden {
		t.Fatalf("scheme-mismatch mutation status = %d; body=%s", schemeMismatchResponse.Code, schemeMismatchResponse.Body.String())
	}
}

func bearer(token, idempotencyKey string) map[string]string {
	headers := map[string]string{"Authorization": "Bearer " + token}
	if idempotencyKey != "" {
		headers["Idempotency-Key"] = idempotencyKey
	}
	return headers
}

func TestImplicitLocalAuthenticationRejectsCrossSiteWrites(t *testing.T) {
	registry, err := capability.LoadRegistry(capability.LoadOptions{ProjectRoot: testRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := businessruntime.Open(filepath.Join(t.TempDir(), "content_agent.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := NewWithRuntime(shell.New(registry), store, nil).Handler()
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/projects"},
		{http.MethodPost, "/api/v1/auth/session"},
		{http.MethodDelete, "/api/v1/auth/session"},
	} {
		request := httptest.NewRequest(route.method, "http://127.0.0.1:8850"+route.path, strings.NewReader(`{"title":"cross-site"}`))
		request.Header.Set("Origin", "https://untrusted.example")
		request.Header.Set("Content-Type", "text/plain")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "CSRF_REJECTED") {
			t.Fatalf("%s %s: status=%d, body=%s", route.method, route.path, response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8850/api/v1/auth/session", nil)
	request.Header.Set("Origin", "http://127.0.0.1:8850")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("same origin login: %d %s", response.Code, response.Body.String())
	}
}

func assertOnlyProject(t *testing.T, handler http.Handler, token, projectID string) {
	t.Helper()
	payload := performJSONWithHeaders(t, handler, http.MethodGet, "/api/v1/projects", nil, bearer(token, ""), http.StatusOK)
	items := arrayAt(t, objectAt(t, payload, "data"), "items")
	if len(items) != 1 || stringAt(t, items[0].(map[string]any), "project_id") != projectID {
		t.Fatalf("projects for %s = %#v", token, items)
	}
}

func assertConcurrentCrossTenantReadsAreHidden(
	t *testing.T, handler http.Handler, projectAID, projectBID string,
) {
	t.Helper()
	type result struct {
		status int
		body   string
	}
	results := make(chan result, 24)
	var wait sync.WaitGroup
	for index := 0; index < 12; index++ {
		wait.Add(2)
		go func() {
			defer wait.Done()
			request := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectBID, nil)
			request.Header.Set("Authorization", "Bearer token-a")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			results <- result{status: response.Code, body: response.Body.String()}
		}()
		go func() {
			defer wait.Done()
			request := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectAID, nil)
			request.Header.Set("Authorization", "Bearer token-b")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			results <- result{status: response.Code, body: response.Body.String()}
		}()
	}
	wait.Wait()
	close(results)
	for item := range results {
		if item.status != http.StatusNotFound || !strings.Contains(item.body, "RESOURCE_NOT_FOUND") {
			t.Fatalf("cross-tenant read = %d %s", item.status, item.body)
		}
	}
}
