package httpapi

import (
	"mime"
	"net/http"
	"path"
	"strconv"

	businessruntime "content-agent/backend/internal/runtime"
)

func (s *Server) listProjectFiles(writer http.ResponseWriter, request *http.Request) {
	files, err := s.runtime.ListProjectFiles(request.Context(), request.PathValue("project_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": files})
}

func (s *Server) readProjectFile(writer http.ResponseWriter, request *http.Request) {
	version, offset, limit, ok := projectFileReadRange(writer, request)
	if !ok {
		return
	}
	result, err := s.runtime.ReadProjectFile(request.Context(), request.PathValue("project_id"), request.URL.Query().Get("path"), version, offset, limit)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func projectFileReadRange(writer http.ResponseWriter, request *http.Request) (int, int, int, bool) {
	values := []int{0, 0, 16000}
	for index, key := range []string{"version", "offset", "limit"} {
		if raw := request.URL.Query().Get(key); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil {
				writeError(writer, http.StatusBadRequest, "REQUEST_VALIDATION_FAILED", "文件版本或读取范围无效。")
				return 0, 0, 0, false
			}
			values[index] = value
		}
	}
	return values[0], values[1], values[2], true
}

func (s *Server) downloadProjectFile(writer http.ResponseWriter, request *http.Request) {
	version, _, _, ok := projectFileReadRange(writer, request)
	if !ok {
		return
	}
	file, content, err := s.runtime.ReadProjectFileBytes(request.Context(), request.PathValue("project_id"), request.URL.Query().Get("path"), version)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writer.Header().Set("Content-Type", "application/octet-stream")
	writer.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(file.Path)}))
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("Content-Length", strconv.Itoa(len(content)))
	writer.Header().Set("X-Project-File-Version", strconv.Itoa(file.Version))
	writer.Header().Set("X-Project-File-Sha256", file.ContentHash)
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(content)
}

func (s *Server) applyInternalProjectFilePatch(writer http.ResponseWriter, request *http.Request) {
	if !authorizeInternalAgent(writer, request) {
		return
	}
	var body struct {
		SDKToolCallID string                           `json:"sdk_tool_call_id"`
		Patch         businessruntime.ProjectFilePatch `json:"patch"`
		Content       string                           `json:"content"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	file, err := s.runtime.ApplyProjectFilePatch(request.Context(), request.PathValue("agent_tool_call_id"), body.SDKToolCallID, body.Patch, body.Content)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": file})
}
