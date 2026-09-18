package httpapi

import (
	"encoding/json"
	"net/http"

	businessruntime "content-agent/backend/internal/runtime"
)

func (s *Server) getAgentInstructions(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	result, err := s.runtime.GetAgentInstructions(request.Context(), request.URL.Query().Get("project_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) updateAgentInstructions(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	request.Body = http.MaxBytesReader(writer, request.Body, 64*1024)
	var command businessruntime.UpdateAgentInstructionsCommand
	if !decodeBody(writer, request, &command) {
		return
	}
	// Personal instructions are not copied into shared command/event payloads.
	result, err := s.runtime.UpdateAgentInstructions(request.Context(), command)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) resolveAgentInstructionSnapshot(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if !authorizeInternalAgent(writer, request) {
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 4096)
	var command struct {
		ProjectID   string `json:"project_id"`
		ActivityKey string `json:"activity_key"`
	}
	if !decodeBody(writer, request, &command) {
		return
	}
	activity, ok := businessruntime.AgentActivityFromContext(request.Context())
	key := "execution:" + activity.ExecutionAttemptID
	if activity.MemoryGenerationID != "" {
		key = "memory:" + activity.MemoryGenerationID
	}
	if activity.AgentTurnID != "" {
		key = "turn:" + activity.AgentTurnID
	}
	if activity.AgentTaskAttemptID != "" {
		key = "background:" + activity.AgentTaskAttemptID
	}
	if !ok || activity.ProjectID != command.ProjectID || key != command.ActivityKey {
		writeError(writer, http.StatusForbidden, "AGENT_ACTIVITY_SCOPE_MISMATCH", "指令请求与当前执行身份不一致。")
		return
	}
	result, err := s.runtime.ResolveAgentInstructionSnapshot(request.Context())
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) getAgentInstructionProposal(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	result, err := s.runtime.GetAgentInstructionProposal(request.Context(), request.PathValue("agent_tool_call_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) applyAgentInstructionTool(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if !authorizeInternalAgent(writer, request) {
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 96*1024)
	var body struct {
		SDKToolCallID string          `json:"sdk_tool_call_id"`
		Arguments     json.RawMessage `json:"arguments"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	result, err := s.runtime.ApplyAgentInstructionTool(request.Context(), request.PathValue("agent_tool_call_id"), body.SDKToolCallID, body.Arguments)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}
