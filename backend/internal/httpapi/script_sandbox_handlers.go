package httpapi

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
)

func (s *Server) getScriptSandboxPolicy(writer http.ResponseWriter, request *http.Request) {
	policy, err := s.runtime.GetScriptSandboxPolicy(request.Context())
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": policy})
}

func (s *Server) updateScriptSandboxPolicy(writer http.ResponseWriter, request *http.Request) {
	var body businessruntime.UpdateScriptSandboxPolicyCommand
	if !decodeBody(writer, request, &body) {
		return
	}
	body.ActorRef = identity.ActorRefFromContext(request.Context())
	policy, err := s.runtime.UpdateScriptSandboxPolicy(request.Context(), body)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": policy})
}

func (s *Server) executeInternalSkillScript(writer http.ResponseWriter, request *http.Request) {
	if !authorizeInternalAgent(writer, request) {
		return
	}
	var body struct {
		ExpectedSDKToolCallID string          `json:"expected_sdk_tool_call_id"`
		Arguments             json.RawMessage `json:"arguments"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	execution, err := s.runtime.ExecuteSkillScript(request.Context(), businessruntime.ExecuteSkillScriptCommand{
		AgentToolCallID:       request.PathValue("agent_tool_call_id"),
		ExpectedSDKToolCallID: body.ExpectedSDKToolCallID,
		Arguments:             body.Arguments,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": execution})
}

func (s *Server) listSkillScriptExecutions(writer http.ResponseWriter, request *http.Request) {
	items, err := s.runtime.ListSkillScriptExecutions(request.Context(), request.PathValue("project_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"items": items}})
}

func (s *Server) getSkillScriptExecution(writer http.ResponseWriter, request *http.Request) {
	execution, err := s.runtime.GetSkillScriptExecution(request.Context(), request.PathValue("skill_script_execution_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": execution})
}

func (s *Server) getSkillScriptArtifact(writer http.ResponseWriter, request *http.Request) {
	artifactPath := strings.TrimSpace(request.URL.Query().Get("path"))
	if artifactPath == "" {
		writeError(writer, http.StatusBadRequest, "REQUEST_VALIDATION_FAILED", "缺少脚本产物路径。")
		return
	}
	file, size, err := s.runtime.OpenSkillScriptArtifact(
		request.Context(), request.PathValue("skill_script_execution_id"), artifactPath,
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	defer file.Close()
	contentType := mime.TypeByExtension(filepath.Ext(artifactPath))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	writer.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
		"filename": filepath.Base(artifactPath),
	}))
	writer.WriteHeader(http.StatusOK)
	_, _ = io.Copy(writer, file)
}
