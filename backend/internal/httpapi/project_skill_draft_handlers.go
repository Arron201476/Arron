package httpapi

import (
	"mime"
	"net/http"
	"strconv"

	businessruntime "content-agent/backend/internal/runtime"
)

func (s *Server) previewProjectSkillDraft(writer http.ResponseWriter, request *http.Request) {
	preview, err := s.runtime.PreviewProjectSkillDraft(request.Context(), request.PathValue("project_id"), request.URL.Query().Get("root_path"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": preview})
}

func (s *Server) exportProjectSkillDraft(writer http.ResponseWriter, request *http.Request) {
	preview, archive, err := s.runtime.ExportProjectSkillDraft(request.Context(), request.PathValue("project_id"), request.URL.Query().Get("root_path"), request.URL.Query().Get("snapshot_hash"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writer.Header().Set("Content-Type", "application/zip")
	writer.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": preview.Manifest.Name + ".zip"}))
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Content-Length", strconv.Itoa(len(archive)))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(archive)
}

func (s *Server) installProjectSkillDraft(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		businessruntime.ProjectSkillInstallArguments
		Confirmed bool `json:"confirmed"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	meta, ok := commandMeta(writer, request, request.PathValue("project_id"), "install_project_skill_draft", body)
	if !ok {
		return
	}
	result, err := s.runtime.InstallProjectSkillDraft(request.Context(), request.PathValue("project_id"), body.ProjectSkillInstallArguments, meta.IdempotencyKey, body.Confirmed)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) installInternalProjectSkillDraft(writer http.ResponseWriter, request *http.Request) {
	if !authorizeInternalAgent(writer, request) {
		return
	}
	var body struct {
		SDKToolCallID string                                       `json:"sdk_tool_call_id"`
		Arguments     businessruntime.ProjectSkillInstallArguments `json:"arguments"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	result, err := s.runtime.InstallProjectSkillDraftForTool(request.Context(), request.PathValue("agent_tool_call_id"), body.SDKToolCallID, body.Arguments)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}
