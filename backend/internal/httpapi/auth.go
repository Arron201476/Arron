package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
)

const browserSessionCookie = "content_agent_session"

type Authenticator interface {
	AuthenticateToken(string) (identity.Principal, bool)
	ImplicitPrincipal() (identity.Principal, bool)
	Principals() []identity.Principal
}

type localAuthenticator struct {
	principal identity.Principal
}

func NewLocalAuthenticator(principal identity.Principal) Authenticator {
	return &localAuthenticator{principal: principal}
}

func (a *localAuthenticator) AuthenticateToken(string) (identity.Principal, bool) {
	return identity.Principal{}, false
}

func (a *localAuthenticator) ImplicitPrincipal() (identity.Principal, bool) {
	return a.principal, true
}

func (a *localAuthenticator) Principals() []identity.Principal {
	return []identity.Principal{a.principal}
}

type StaticTokenPrincipal struct {
	Token         string
	TokenSHA256   string
	UserID        string
	DisplayName   string
	WorkspaceID   string
	WorkspaceName string
	Role          identity.Role
}

type staticTokenAuthenticator struct {
	byHash     map[[sha256.Size]byte]identity.Principal
	principals []identity.Principal
}

func NewStaticTokenAuthenticator(entries []StaticTokenPrincipal) (Authenticator, error) {
	authenticator := &staticTokenAuthenticator{byHash: map[[sha256.Size]byte]identity.Principal{}}
	seenPrincipals := map[string]struct{}{}
	for _, entry := range entries {
		principal := identity.Principal{
			Kind: identity.KindUser, UserID: strings.TrimSpace(entry.UserID),
			DisplayName:   strings.TrimSpace(entry.DisplayName),
			WorkspaceID:   strings.TrimSpace(entry.WorkspaceID),
			WorkspaceName: strings.TrimSpace(entry.WorkspaceName),
			Role:          entry.Role, AuthMethod: "bearer_token",
		}
		if !principal.ValidUser() {
			return nil, fmt.Errorf("invalid principal %q in authentication config", principal.UserID)
		}
		if principal.DisplayName == "" {
			principal.DisplayName = principal.UserID
		}
		if principal.WorkspaceName == "" {
			principal.WorkspaceName = principal.WorkspaceID
		}
		var hash [sha256.Size]byte
		if strings.TrimSpace(entry.Token) != "" {
			hash = sha256.Sum256([]byte(strings.TrimSpace(entry.Token)))
		} else {
			decoded, err := hex.DecodeString(strings.TrimSpace(entry.TokenSHA256))
			if err != nil || len(decoded) != sha256.Size {
				return nil, fmt.Errorf("principal %q must provide a valid token_sha256", principal.UserID)
			}
			copy(hash[:], decoded)
		}
		if _, duplicate := authenticator.byHash[hash]; duplicate {
			return nil, errors.New("authentication config contains a duplicate token")
		}
		authenticator.byHash[hash] = principal
		key := principal.WorkspaceID + "\x00" + principal.UserID
		if _, duplicate := seenPrincipals[key]; !duplicate {
			authenticator.principals = append(authenticator.principals, principal)
			seenPrincipals[key] = struct{}{}
		}
	}
	if len(authenticator.byHash) == 0 {
		return nil, errors.New("authentication config must contain at least one principal")
	}
	return authenticator, nil
}

func (a *staticTokenAuthenticator) AuthenticateToken(token string) (identity.Principal, bool) {
	hash := sha256.Sum256([]byte(strings.TrimSpace(token)))
	for candidateHash, principal := range a.byHash {
		if hmac.Equal(hash[:], candidateHash[:]) {
			return principal, true
		}
	}
	return identity.Principal{}, false
}

func (a *staticTokenAuthenticator) ImplicitPrincipal() (identity.Principal, bool) {
	return identity.Principal{}, false
}

func (a *staticTokenAuthenticator) Principals() []identity.Principal {
	return append([]identity.Principal(nil), a.principals...)
}

type authenticationFile struct {
	Principals []struct {
		TokenSHA256   string        `json:"token_sha256"`
		UserID        string        `json:"user_id"`
		DisplayName   string        `json:"display_name"`
		WorkspaceID   string        `json:"workspace_id"`
		WorkspaceName string        `json:"workspace_name"`
		Role          identity.Role `json:"role"`
	} `json:"principals"`
}

func LoadAuthenticator(path string, allowImplicitLocal bool) (Authenticator, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		if !allowImplicitLocal {
			return nil, errors.New("CONTENT_AGENT_AUTH_CONFIG_PATH is required for non-loopback listeners")
		}
		return NewLocalAuthenticator(identity.DefaultLocalPrincipal()), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read authentication config: %w", err)
	}
	var file authenticationFile
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("decode authentication config: %w", err)
	}
	entries := make([]StaticTokenPrincipal, 0, len(file.Principals))
	for _, configured := range file.Principals {
		entries = append(entries, StaticTokenPrincipal{
			TokenSHA256: configured.TokenSHA256, UserID: configured.UserID,
			DisplayName: configured.DisplayName, WorkspaceID: configured.WorkspaceID,
			WorkspaceName: configured.WorkspaceName, Role: configured.Role,
		})
	}
	return NewStaticTokenAuthenticator(entries)
}

func IsLoopbackListenAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type browserSession struct {
	Principal identity.Principal
	ExpiresAt time.Time
}

func (s *Server) securityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/healthz" {
			next.ServeHTTP(writer, request)
			return
		}
		requestID := strings.TrimSpace(request.Header.Get("X-Request-ID"))
		if requestID == "" || len(requestID) > 128 {
			requestID = randomOpaqueToken(16)
		}
		writer.Header().Set("X-Request-ID", requestID)

		if request.Method == http.MethodDelete && request.URL.Path == "/api/v1/auth/session" {
			if !sameOriginRequest(request) {
				writeError(writer, http.StatusForbidden, "CSRF_REJECTED", "跨站写请求已被拒绝。")
				return
			}
			next.ServeHTTP(writer, request)
			return
		}
		principal, sessionAuth, ok := s.authenticateRequest(request)
		if !ok {
			writeError(writer, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "需要登录后才能访问。")
			s.auditRequest(request, identity.Principal{}, requestID, "denied", http.StatusUnauthorized, "", "")
			return
		}
		if strings.HasPrefix(request.URL.Path, "/internal/") && principal.Kind != identity.KindService {
			writeError(writer, http.StatusUnauthorized, "INTERNAL_AGENT_UNAUTHORIZED", "内部服务凭据无效。")
			s.auditRequest(request, principal, requestID, "denied", http.StatusUnauthorized, "", "")
			return
		}
		if request.URL.Path == "/api/v1/auth/session" && request.Method == http.MethodPost && principal.AuthMethod != "bearer_token" && principal.AuthMethod != "local_loopback" {
			writeError(writer, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "请使用访问令牌建立登录会话。")
			return
		}
		if principal.Kind == identity.KindUser && s.runtime != nil {
			resolved, err := s.runtime.ResolvePrincipal(request.Context(), principal)
			if err != nil {
				writeError(writer, http.StatusForbidden, "WORKSPACE_ACCESS_DENIED", "当前用户无权访问该工作区。")
				s.auditRequest(request, principal, requestID, "denied", http.StatusForbidden, "", "")
				return
			}
			resolved.AuthMethod = principal.AuthMethod
			principal = resolved
		}
		if principal.Kind == identity.KindService && strings.HasPrefix(request.URL.Path, "/api/") && request.Method != http.MethodGet && request.Method != http.MethodHead {
			writeError(writer, http.StatusForbidden, "SERVICE_SCOPE_DENIED", "服务凭据不能调用最终用户写接口。")
			return
		}
		request, effectivePrincipal, bound := s.bindAgentActivity(writer, request, principal)
		if !bound {
			return
		}
		if effectivePrincipal.Kind == identity.KindUser {
			required := requiredRole(request)
			if !effectivePrincipal.Allows(required) {
				writeError(writer, http.StatusForbidden, "ROLE_FORBIDDEN", "当前角色无权执行该操作。")
				s.auditRequest(request, principal, requestID, "denied", http.StatusForbidden, "", "")
				return
			}
			if (sessionAuth || principal.AuthMethod == "local_loopback") && isMutation(request.Method) && !sameOriginRequest(request) {
				writeError(writer, http.StatusForbidden, "CSRF_REJECTED", "跨站写请求已被拒绝。")
				return
			}
		}
		resourceType, resourceID := requestResource(request.URL.Path)
		if effectivePrincipal.Kind == identity.KindUser && resourceID != "" && s.runtime != nil {
			workspaceID, err := s.runtime.ResolveResourceWorkspace(request.Context(), resourceType, resourceID)
			if err != nil || workspaceID != effectivePrincipal.WorkspaceID {
				writeError(writer, http.StatusNotFound, "RESOURCE_NOT_FOUND", "请求的资源不存在。")
				s.auditRequest(request, principal, requestID, "denied", http.StatusNotFound, resourceType, resourceID)
				return
			}
			if activity, present := businessruntime.AgentActivityFromContext(request.Context()); present && resourceType == "project" && resourceID != activity.ProjectID {
				writeError(writer, http.StatusNotFound, "RESOURCE_NOT_FOUND", "请求的资源不存在。")
				return
			}
		}
		request = request.WithContext(identity.WithPrincipal(request.Context(), principal))
		recorder := &securityResponseWriter{ResponseWriter: writer, status: http.StatusOK}
		next.ServeHTTP(recorder, request)
		if isMutation(request.Method) {
			outcome := "succeeded"
			if recorder.status >= http.StatusBadRequest {
				outcome = "failed"
			}
			s.auditRequest(request, principal, requestID, outcome, recorder.status, resourceType, resourceID)
		}
	})
}

func (s *Server) authenticateRequest(request *http.Request) (identity.Principal, bool, bool) {
	authorization := strings.TrimSpace(request.Header.Get("Authorization"))
	if token := bearerToken(authorization); token != "" && internalTokenMatches(token) {
		return identity.ServicePrincipal(), false, true
	}
	if cookie, err := request.Cookie(browserSessionCookie); err == nil {
		s.sessionMu.Lock()
		session, ok := s.sessions[cookie.Value]
		if ok && s.now().Before(session.ExpiresAt) {
			s.sessionMu.Unlock()
			session.Principal.AuthMethod = "session_cookie"
			return session.Principal, true, true
		}
		delete(s.sessions, cookie.Value)
		s.sessionMu.Unlock()
	}
	if token := bearerToken(authorization); token != "" {
		if principal, ok := s.auth.AuthenticateToken(token); ok {
			return principal, false, true
		}
		return identity.Principal{}, false, false
	}
	principal, ok := s.auth.ImplicitPrincipal()
	return principal, false, ok
}

func internalTokenMatches(token string) bool {
	expected := strings.TrimSpace(os.Getenv("CONTENT_AGENT_SIDECAR_INTERNAL_TOKEN"))
	return expected != "" && hmac.Equal([]byte(token), []byte(expected))
}

func bearerToken(value string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(value, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(value, prefix))
}

func requiredRole(request *http.Request) identity.Role {
	if !isMutation(request.Method) {
		return identity.RoleViewer
	}
	if strings.HasPrefix(request.URL.Path, "/api/v1/workspace-mcp-credentials") ||
		request.URL.Path == "/api/v1/agent-tools/configuration" ||
		strings.HasPrefix(request.URL.Path, "/api/v1/script-sandbox-policy") {
		return identity.RoleAdmin
	}
	if request.Method == http.MethodDelete && request.URL.Path == "/api/v1/workspace" {
		return identity.RoleOwner
	}
	return identity.RoleEditor
}

func isMutation(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func sameOriginRequest(request *http.Request) bool {
	if site := strings.ToLower(strings.TrimSpace(request.Header.Get("Sec-Fetch-Site"))); site == "cross-site" {
		return false
	}
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || !strings.EqualFold(parsed.Host, request.Host) {
		return false
	}
	requestScheme := "http"
	if request.TLS != nil {
		requestScheme = "https"
	}
	return strings.EqualFold(parsed.Scheme, requestScheme)
}

func requestResource(path string) (string, string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 4 || parts[0] != "api" || parts[1] != "v1" {
		return "", ""
	}
	kindByCollection := map[string]string{
		"projects": "project", "conversations": "conversation", "agent-turns": "agent_turn",
		"skills": "skill", "upload-sessions": "upload_session", "upload-items": "upload_item",
		"assets": "asset", "asset-sets": "asset_set", "asset-set-versions": "asset_set_version",
		"runs": "run", "steps": "step", "artifacts": "artifact",
		"artifact-versions": "artifact_version", "approvals": "approval",
		"quality-reviews": "quality_review", "revision-requests": "revision_request",
		"target-resolutions": "target_resolution", "proposed-actions": "proposed_action",
		"agent-tasks": "agent_task", "agent-tool-calls": "agent_tool_call",
		"agent-tool-approvals": "agent_tool_approval", "script-candidates": "script_candidate",
		"exports": "export", "impact-reviews": "impact_review",
		"regeneration-plans": "regeneration_plan", "workspace-mcp-credentials": "mcp_credential",
	}
	resourceType, ok := kindByCollection[parts[2]]
	if !ok || parts[3] == "" {
		return "", ""
	}
	if parts[2] == "skills" && (parts[3] == "install-attempts" || parts[3] == "refresh" || parts[3] == "discovered") {
		return "", ""
	}
	return resourceType, parts[3]
}

func (s *Server) createAuthSession(writer http.ResponseWriter, request *http.Request) {
	principal, ok := identity.UserFromContext(request.Context())
	if !ok {
		writeError(writer, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "需要用户身份才能建立会话。")
		return
	}
	token := randomOpaqueToken(32)
	expiresAt := s.now().Add(12 * time.Hour)
	s.sessionMu.Lock()
	s.sessions[token] = browserSession{Principal: principal, ExpiresAt: expiresAt}
	s.sessionMu.Unlock()
	http.SetCookie(writer, &http.Cookie{
		Name: browserSessionCookie, Value: token, Path: "/", Expires: expiresAt,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: request.TLS != nil,
	})
	writeJSON(writer, http.StatusOK, map[string]any{"data": principal})
}

func (s *Server) deleteAuthSession(writer http.ResponseWriter, request *http.Request) {
	if cookie, err := request.Cookie(browserSessionCookie); err == nil {
		s.sessionMu.Lock()
		delete(s.sessions, cookie.Value)
		s.sessionMu.Unlock()
	}
	http.SetCookie(writer, &http.Cookie{
		Name: browserSessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: request.TLS != nil,
	})
	writeJSON(writer, http.StatusOK, map[string]any{"data": nil})
}

func (s *Server) getCurrentPrincipal(writer http.ResponseWriter, request *http.Request) {
	principal, ok := identity.UserFromContext(request.Context())
	if !ok {
		writeError(writer, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "需要用户身份。")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": principal})
}

func (s *Server) auditRequest(
	request *http.Request, principal identity.Principal, requestID, outcome string,
	status int, resourceType, resourceID string,
) {
	if s.runtime == nil {
		return
	}
	remoteAddr := request.RemoteAddr
	if host, _, err := net.SplitHostPort(request.RemoteAddr); err == nil {
		remoteAddr = host
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), 2*time.Second)
	defer cancel()
	if err := s.runtime.RecordSecurityAudit(ctx, businessruntime.SecurityAuditEvent{
		WorkspaceID: principal.WorkspaceID, UserID: principal.UserID,
		PrincipalKind: principal.Kind, Action: request.Method + " " + request.URL.Path,
		ResourceType: resourceType, ResourceID: resourceID, Outcome: outcome,
		StatusCode: status, RequestID: requestID, RemoteAddr: remoteAddr,
		UserAgent: request.UserAgent(),
	}); err != nil {
		s.logger.Warn("record security audit", "error", err, "request_id", requestID)
	}
}

type securityResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *securityResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *securityResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *securityResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func randomOpaqueToken(size int) string {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		panic(fmt.Sprintf("generate authentication token: %v", err))
	}
	return hex.EncodeToString(buffer)
}
