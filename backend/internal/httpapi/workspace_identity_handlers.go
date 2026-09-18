package httpapi

import (
	"net/http"

	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
)

func (s *Server) getWorkspaceQuota(writer http.ResponseWriter, request *http.Request) {
	principal, ok := identity.UserFromContext(request.Context())
	if !ok {
		writeError(writer, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "需要用户身份。")
		return
	}
	quota, err := s.runtime.GetWorkspaceQuota(request.Context(), principal.WorkspaceID)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": quota})
}

func (s *Server) listWorkspaceMCPCredentials(writer http.ResponseWriter, request *http.Request) {
	principal, ok := identity.UserFromContext(request.Context())
	if !ok {
		writeError(writer, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "需要用户身份。")
		return
	}
	items, err := s.runtime.ListWorkspaceMCPCredentials(request.Context(), principal.WorkspaceID)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"items": items}})
}

func (s *Server) putWorkspaceMCPCredential(writer http.ResponseWriter, request *http.Request) {
	principal, ok := identity.UserFromContext(request.Context())
	if !ok {
		writeError(writer, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "需要用户身份。")
		return
	}
	var body struct {
		ServerID       string `json:"server_id"`
		CredentialName string `json:"credential_name"`
		SecretRef      string `json:"secret_ref"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	meta, ok := commandMeta(
		writer, request, principal.WorkspaceID, "put_workspace_mcp_credential", body,
	)
	if !ok {
		return
	}
	item, err := s.runtime.PutWorkspaceMCPCredentialCommand(
		request.Context(),
		businessruntime.PutWorkspaceMCPCredentialCommand{
			CommandMeta: meta, WorkspaceID: principal.WorkspaceID, UserID: principal.UserID,
			ServerID: body.ServerID, CredentialName: body.CredentialName, SecretRef: body.SecretRef,
		},
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": item})
}

func (s *Server) deleteWorkspaceMCPCredential(writer http.ResponseWriter, request *http.Request) {
	principal, ok := identity.UserFromContext(request.Context())
	if !ok {
		writeError(writer, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "需要用户身份。")
		return
	}
	body := struct{}{}
	meta, ok := commandMeta(
		writer, request, principal.WorkspaceID, "delete_workspace_mcp_credential", body,
	)
	if !ok {
		return
	}
	result, err := s.runtime.DeleteWorkspaceMCPCredentialCommand(
		request.Context(),
		businessruntime.DeleteWorkspaceMCPCredentialCommand{
			CommandMeta: meta, WorkspaceID: principal.WorkspaceID,
			CredentialID: request.PathValue("credential_id"),
		},
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) deleteWorkspace(writer http.ResponseWriter, request *http.Request) {
	principal, ok := identity.UserFromContext(request.Context())
	if !ok {
		writeError(writer, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "需要用户身份。")
		return
	}
	var body struct {
		Confirmation string `json:"confirmation"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	meta, ok := commandMeta(writer, request, principal.WorkspaceID, "delete_workspace", body)
	if !ok {
		return
	}
	result, err := s.runtime.DeleteWorkspace(request.Context(), businessruntime.DeleteWorkspaceCommand{
		CommandMeta: meta, WorkspaceID: principal.WorkspaceID, UserID: principal.UserID,
		Confirmation: body.Confirmation,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}
