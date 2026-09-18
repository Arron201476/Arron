package httpapi

import (
	"encoding/json"
	"io"
	"net/http"

	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
)

func (s *Server) storeInternalAgentToolOutput(writer http.ResponseWriter, request *http.Request) {
	if !authorizeInternalAgent(writer, request) {
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 20*1024*1024)
	content, err := io.ReadAll(request.Body)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "REQUEST_VALIDATION_FAILED", "工具产物超过大小限制或读取失败。")
		return
	}
	result, err := s.runtime.StoreAgentToolOutput(request.Context(), request.PathValue("agent_tool_call_id"), request.URL.Query().Get("sdk_tool_call_id"), request.URL.Query().Get("filename"), content)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"data": result})
}

func (s *Server) listAgentTools(writer http.ResponseWriter, request *http.Request) {
	if s.runtime == nil {
		writeJSON(writer, http.StatusOK, map[string]any{"data": s.agentTools.PublicCatalog()})
		return
	}
	catalog, err := s.runtime.AgentToolCatalog(request.Context(), false)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": catalog})
}

func (s *Server) getInternalAgentToolCatalog(writer http.ResponseWriter, request *http.Request) {
	if !authorizeInternalAgent(writer, request) {
		return
	}
	if s.runtime == nil {
		writeJSON(writer, http.StatusOK, map[string]any{"data": s.agentTools.PrivateCatalog()})
		return
	}
	catalog, err := s.runtime.AgentToolCatalog(request.Context(), true)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": catalog})
}

func (s *Server) beginInternalAgentToolCall(writer http.ResponseWriter, request *http.Request) {
	if !authorizeInternalAgent(writer, request) {
		return
	}
	var body businessruntime.BeginAgentToolCallCommand
	if !decodeBody(writer, request, &body) {
		return
	}
	if !validateAgentActivityTarget(writer, request, body.ProjectID, body.AgentTurnID, body.AgentTaskAttemptID, body.ExecutionAttemptID, body.AttemptToken) {
		return
	}
	call, err := s.runtime.BeginAgentToolCall(request.Context(), body)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"data": call})
}

func (s *Server) startInternalAgentToolCall(writer http.ResponseWriter, request *http.Request) {
	if !authorizeInternalAgent(writer, request) {
		return
	}
	var body struct {
		ExpectedSDKToolCallID string `json:"expected_sdk_tool_call_id"`
		ConfigurationHash     string `json:"configuration_hash,omitempty"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	call, err := s.runtime.StartAgentToolCall(request.Context(), businessruntime.StartAgentToolCallCommand{
		AgentToolCallID:       request.PathValue("agent_tool_call_id"),
		ExpectedSDKToolCallID: body.ExpectedSDKToolCallID,
		ConfigurationHash:     body.ConfigurationHash,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": call})
}

func (s *Server) completeInternalAgentToolCall(writer http.ResponseWriter, request *http.Request) {
	if !authorizeInternalAgent(writer, request) {
		return
	}
	var body struct {
		Result          json.RawMessage `json:"result"`
		ResultSizeBytes int             `json:"result_size_bytes"`
		TraceRef        string          `json:"trace_ref"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	call, err := s.runtime.CompleteAgentToolCall(request.Context(), businessruntime.CompleteAgentToolCallCommand{
		AgentToolCallID: request.PathValue("agent_tool_call_id"),
		Result:          body.Result,
		ResultSizeBytes: body.ResultSizeBytes,
		TraceRef:        body.TraceRef,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": call})
}

func (s *Server) getAgentToolConfiguration(writer http.ResponseWriter, request *http.Request) {
	configuration, err := s.runtime.GetAgentToolConfiguration(request.Context())
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": configuration})
}

func (s *Server) updateAgentToolConfiguration(writer http.ResponseWriter, request *http.Request) {
	var command businessruntime.UpdateAgentToolConfigurationCommand
	if !decodeBody(writer, request, &command) {
		return
	}
	configuration, err := s.runtime.UpdateAgentToolConfiguration(request.Context(), command)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": configuration})
}

func (s *Server) failInternalAgentToolCall(writer http.ResponseWriter, request *http.Request) {
	if !authorizeInternalAgent(writer, request) {
		return
	}
	var body struct {
		ErrorCode    string `json:"error_code"`
		ErrorMessage string `json:"error_message"`
		TraceRef     string `json:"trace_ref"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	call, err := s.runtime.FailAgentToolCall(request.Context(), businessruntime.FailAgentToolCallCommand{
		AgentToolCallID: request.PathValue("agent_tool_call_id"),
		ErrorCode:       body.ErrorCode,
		ErrorMessage:    body.ErrorMessage,
		TraceRef:        body.TraceRef,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": call})
}

func (s *Server) cancelInternalAgentToolCall(writer http.ResponseWriter, request *http.Request) {
	if !authorizeInternalAgent(writer, request) {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	call, err := s.runtime.CancelAgentToolCall(request.Context(), businessruntime.CancelAgentToolCallCommand{
		AgentToolCallID: request.PathValue("agent_tool_call_id"), Reason: body.Reason,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": call})
}

func (s *Server) listAgentToolCalls(writer http.ResponseWriter, request *http.Request) {
	items, err := s.runtime.ListAgentToolCalls(request.Context(), request.PathValue("project_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"items": items}})
}

func (s *Server) getAgentToolCall(writer http.ResponseWriter, request *http.Request) {
	call, err := s.runtime.GetAgentToolCall(request.Context(), request.PathValue("agent_tool_call_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": call})
}

func (s *Server) getAgentSubtaskResult(writer http.ResponseWriter, request *http.Request) {
	result, err := s.runtime.GetAgentSubtaskResult(request.Context(), request.PathValue("agent_tool_call_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) getAgentToolOutcomeReview(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	result, err := s.runtime.GetAgentToolOutcomeReview(request.Context(), request.PathValue("agent_tool_call_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) resolveAgentToolOutcome(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	request.Body = http.MaxBytesReader(writer, request.Body, 16*1024)
	var command businessruntime.ResolveAgentToolOutcomeCommand
	if !decodeBody(writer, request, &command) {
		return
	}
	command.AgentToolCallID = request.PathValue("agent_tool_call_id")
	result, err := s.runtime.ResolveAgentToolOutcome(request.Context(), command)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) listAgentToolApprovals(writer http.ResponseWriter, request *http.Request) {
	items, err := s.runtime.ListAgentToolApprovals(
		request.Context(), request.PathValue("project_id"), request.URL.Query().Get("status"),
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"items": items}})
}

func (s *Server) getAgentToolApproval(writer http.ResponseWriter, request *http.Request) {
	approval, err := s.runtime.GetAgentToolApproval(
		request.Context(), request.PathValue("agent_tool_approval_id"),
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": approval})
}

func (s *Server) resolveAgentToolApproval(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		ExpectedVersion     int    `json:"expected_version"`
		SubjectSnapshotHash string `json:"subject_snapshot_hash"`
		Action              string `json:"action"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	approval, err := s.runtime.GetAgentToolApproval(
		request.Context(), request.PathValue("agent_tool_approval_id"),
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, approval.ProjectID, "resolve_agent_tool_approval", body)
	if !ok {
		return
	}
	resolved, err := s.runtime.ResolveAgentToolApproval(
		request.Context(),
		businessruntime.ResolveAgentToolApprovalCommand{
			CommandMeta: meta, AgentToolApprovalID: approval.AgentToolApprovalID,
			ExpectedVersion:     body.ExpectedVersion,
			SubjectSnapshotHash: body.SubjectSnapshotHash,
			Action:              body.Action, ActorRef: identity.ActorRefFromContext(request.Context()),
		},
	)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	if s.agentTurns != nil {
		s.agentTurns.notify()
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": resolved})
}
