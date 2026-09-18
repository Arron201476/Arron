package httpapi

import (
	"net/http"

	businessruntime "content-agent/backend/internal/runtime"
)

func (s *Server) listExecutionControlTargets(writer http.ResponseWriter, request *http.Request) {
	items, next, err := s.runtime.ListExecutionControlTargets(request.Context(), request.PathValue("project_id"), request.URL.Query().Get("target_type"), request.URL.Query().Get("after_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"items": items, "next_after_id": next}})
}

func (s *Server) inspectExecutionControl(writer http.ResponseWriter, request *http.Request) {
	result, err := s.runtime.InspectExecutionControl(request.Context(), request.PathValue("project_id"), request.URL.Query().Get("target_type"), request.URL.Query().Get("target_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) controlInternalExecution(writer http.ResponseWriter, request *http.Request) {
	if !authorizeInternalAgent(writer, request) {
		return
	}
	var body struct {
		SDKToolCallID string                                    `json:"sdk_tool_call_id"`
		Arguments     businessruntime.ExecutionControlArguments `json:"arguments"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	result, err := s.runtime.ControlExecution(request.Context(), request.PathValue("agent_tool_call_id"), body.SDKToolCallID, body.Arguments)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}
