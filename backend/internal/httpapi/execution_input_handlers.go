package httpapi

import (
	"net/http"

	"content-agent/backend/internal/agentcontract"
	businessruntime "content-agent/backend/internal/runtime"
)

func (s *Server) getExecutionInputs(writer http.ResponseWriter, request *http.Request) {
	view, err := s.runtime.GetExecutionInputs(request.Context(), request.PathValue("project_id"), request.PathValue("attempt_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": view})
}

func (s *Server) listExecutionInputAttempts(writer http.ResponseWriter, request *http.Request) {
	items, next, err := s.runtime.ListExecutionInputAttempts(request.Context(), request.PathValue("project_id"), request.PathValue("run_id"), request.URL.Query().Get("after"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"items": items, "next_cursor": next}})
}

func (s *Server) changeExecutionInput(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Mode        string `json:"mode"`
		ExecutionID string `json:"execution_id"`
		InputID     string `json:"input_id"`
		Action      string `json:"action"`
		Content     string `json:"content"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	meta, ok := commandMeta(writer, request, body.Mode+":"+body.InputID, "change_execution_input", body)
	if !ok {
		return
	}
	result, err := s.runtime.ChangeExecutionInput(request.Context(), businessruntime.ChangeExecutionInputCommand{CommandMeta: meta, ProjectID: request.PathValue("project_id"), Mode: body.Mode, ExecutionID: body.ExecutionID, InputID: body.InputID, Action: body.Action, Content: body.Content})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) appendExecutionInput(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Content        string                        `json:"content"`
		AttachmentRefs []agentcontract.AttachmentRef `json:"attachment_refs,omitempty"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	meta, ok := commandMeta(writer, request, request.PathValue("attempt_id"), "append_execution_input", body)
	if !ok {
		return
	}
	input, err := s.runtime.AppendExecutionInput(request.Context(), businessruntime.AppendExecutionInputCommand{
		CommandMeta: meta, ProjectID: request.PathValue("project_id"), AttemptID: request.PathValue("attempt_id"), Content: body.Content, AttachmentRefs: body.AttachmentRefs})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]any{"data": input})
}

func (s *Server) recordExecutionInputsIncluded(writer http.ResponseWriter, request *http.Request) {
	if !authorizeInternalAgent(writer, request) {
		return
	}
	var command businessruntime.RecordExecutionInputsCommand
	if !decodeBody(writer, request, &command) {
		return
	}
	command.AttemptID = request.PathValue("attempt_id")
	command.AttemptToken = request.Header.Get("X-Attempt-Token")
	if err := s.runtime.RecordExecutionInputsIncluded(request.Context(), command); err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"attempt_id": command.AttemptID, "included_input_ids": command.IncludedInputIDs}})
}
