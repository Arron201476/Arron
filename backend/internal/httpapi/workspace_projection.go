package httpapi

import (
	"context"
	"net/http"
	"strconv"

	"content-agent/backend/internal/capability"
	"content-agent/backend/internal/identity"
	"content-agent/backend/internal/workspaceview"
)

func (s *Server) getProjectSkillResources(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	projectID, capabilityID, version := request.PathValue("project_id"), request.PathValue("capability_id"), query.Get("version")
	if query.Get("path") == "" {
		items, err := s.runtime.ListSkillResources(request.Context(), projectID, capabilityID, version)
		if err != nil {
			s.handleRuntimeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"items": items}})
		return
	}
	offset, limit := 0, 16000
	var err error
	if query.Has("offset") {
		offset, err = strconv.Atoi(query.Get("offset"))
	}
	if err == nil && query.Has("limit") {
		limit, err = strconv.Atoi(query.Get("limit"))
	}
	if err != nil || offset < 0 || limit < 1 || limit > 16000 {
		writeError(writer, http.StatusBadRequest, "SKILL_RESOURCE_RANGE_INVALID", "offset 必须非负，limit 必须在 1 到 16000 之间。")
		return
	}
	page, err := s.runtime.ReadSkillResource(request.Context(), projectID, capabilityID, version, query.Get("path"), offset, limit)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": page})
}

type projectWorkspaceProjection struct {
	ContractVersion  string                   `json:"contract_version"`
	RegistryRevision string                   `json:"registry_revision"`
	Snapshot         projectSnapshot          `json:"snapshot"`
	Registries       workspaceview.Registries `json:"registries"`
}

func (s *Server) registryForRequest(ctx context.Context, projectID string) (*capability.Registry, error) {
	if s.runtime == nil {
		return s.shell.Registry(), nil
	}
	if projectID != "" {
		return s.runtime.CapabilityRegistryForProject(ctx, projectID)
	}
	return s.runtime.CapabilityRegistryForSelection(ctx, identity.WorkspaceIDFromContext(ctx))
}

func (s *Server) workspaceCatalog(ctx context.Context, projectID string) (workspaceview.Catalog, error) {
	registry, err := s.registryForRequest(ctx, projectID)
	if err != nil {
		return workspaceview.Catalog{}, err
	}
	return workspaceview.Compile(
		registry.PublicList(), registry.PublicArtifactPresentations(),
	), nil
}

func (s *Server) getWorkspaceProjection(writer http.ResponseWriter, request *http.Request) {
	catalog, err := s.workspaceCatalog(request.Context(), "")
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": catalog})
}

func (s *Server) getProjectWorkspaceProjection(writer http.ResponseWriter, request *http.Request) {
	snapshot, err := s.loadProjectSnapshot(request.Context(), request.PathValue("project_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	catalog, err := s.workspaceCatalog(request.Context(), request.PathValue("project_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": projectWorkspaceProjection{
		ContractVersion:  catalog.ContractVersion,
		RegistryRevision: catalog.Revision,
		Snapshot:         snapshot,
		Registries:       catalog.Registries,
	}})
}

func (s *Server) listProjectCapabilities(writer http.ResponseWriter, request *http.Request) {
	registry, err := s.registryForRequest(request.Context(), request.PathValue("project_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{
		"items": registry.PublicList(), "diagnostics": registry.SkillDiagnostics(),
	}})
}

func (s *Server) getProjectCapability(writer http.ResponseWriter, request *http.Request) {
	definition, ok, err := s.runtime.CapabilityDefinitionForProject(
		request.Context(),
		request.PathValue("project_id"),
		request.PathValue("capability_id"),
		request.URL.Query().Get("version"),
		request.URL.Query().Get("proposed_action_id"),
	)
	if err != nil {
		writeError(writer, http.StatusUnprocessableEntity, "CAPABILITY_SCHEMA_UNAVAILABLE", "能力配置 Schema 当前不可用。")
		return
	}
	if !ok {
		writeError(writer, http.StatusNotFound, "CAPABILITY_NOT_FOUND", "请求的能力不存在。")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": definition})
}
