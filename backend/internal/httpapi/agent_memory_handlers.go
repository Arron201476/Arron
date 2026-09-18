package httpapi

import (
	"net/http"

	businessruntime "content-agent/backend/internal/runtime"
)

func (s *Server) getAgentMemoryToolProposal(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	result, err := s.runtime.GetAgentMemoryToolProposal(request.Context(), request.PathValue("agent_tool_call_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) getAgentMemoryPreferences(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	value, err := s.runtime.GetAgentMemoryPreferences(request.Context(), request.PathValue("project_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": value})
}

func (s *Server) updateAgentMemoryPreferences(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	request.Body = http.MaxBytesReader(writer, request.Body, 4096)
	var command struct {
		ArchiveEnabled   *bool  `json:"archive_enabled"`
		GenerateEnabled  *bool  `json:"generate_enabled"`
		ExpectedRevision *int   `json:"expected_revision"`
		RequestID        string `json:"request_id"`
	}
	if !decodeBody(writer, request, &command) {
		return
	}
	if command.ArchiveEnabled == nil || command.GenerateEnabled == nil || command.ExpectedRevision == nil {
		writeError(writer, http.StatusBadRequest, "REQUEST_VALIDATION_FAILED", "Explicit memory consent flags and revision are required.")
		return
	}
	value, err := s.runtime.UpdateAgentMemoryPreferences(request.Context(), businessruntime.UpdateAgentMemoryPreferencesCommand{
		ProjectID: request.PathValue("project_id"), ArchiveEnabled: *command.ArchiveEnabled, GenerateEnabled: *command.GenerateEnabled, ExpectedRevision: *command.ExpectedRevision, RequestID: command.RequestID})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": value})
}

func (s *Server) listAgentMemorySources(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	result, err := s.runtime.ListAgentMemorySources(request.Context(), request.PathValue("project_id"), request.URL.Query().Get("cursor"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) queueAgentMemoryGeneration(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	request.Body = http.MaxBytesReader(writer, request.Body, 4096)
	var command struct {
		ActivityKey string `json:"activity_key"`
		SegmentID   string `json:"segment_id"`
		SourceHash  string `json:"source_hash"`
	}
	if !decodeBody(writer, request, &command) {
		return
	}
	result, err := s.runtime.QueueAgentMemoryGeneration(request.Context(), request.PathValue("project_id"), command.ActivityKey, command.SegmentID, command.SourceHash)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) listAgentMemoryGenerations(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	result, err := s.runtime.ListAgentMemoryGenerations(request.Context(), request.PathValue("project_id"), request.URL.Query().Get("cursor"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) getAgentMemoryGeneration(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	result, err := s.runtime.GetAgentMemoryGeneration(request.Context(), request.PathValue("project_id"), request.PathValue("generation_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) controlAgentMemoryGeneration(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	request.Body = http.MaxBytesReader(writer, request.Body, 4096)
	var command struct {
		Action           string `json:"action"`
		ExpectedRevision int    `json:"expected_revision"`
	}
	if !decodeBody(writer, request, &command) {
		return
	}
	result, err := s.runtime.ControlAgentMemoryGeneration(request.Context(), request.PathValue("project_id"), request.PathValue("generation_id"), command.Action, command.ExpectedRevision)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) getAgentMemoryProposal(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	result, err := s.runtime.GetAgentMemoryProposal(request.Context(), request.PathValue("agent_tool_call_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) resolveAgentMemoryArchivePolicy(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if !authorizeInternalAgent(writer, request) {
		return
	}
	value, err := s.runtime.ResolveAgentMemoryArchivePolicy(request.Context())
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": value})
}

func (s *Server) archiveAgentMemoryRollout(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if !authorizeInternalAgent(writer, request) {
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, (8<<20)+4096)
	var command struct {
		ConsentRevision int                                `json:"consent_revision"`
		Rollout         businessruntime.AgentMemoryRollout `json:"rollout"`
	}
	if !decodeBody(writer, request, &command) {
		return
	}
	value, err := s.runtime.ArchiveAgentMemoryRollout(request.Context(), command.Rollout, command.ConsentRevision)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": value})
}

func (s *Server) recoverAgentMemoryArchive(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if !authorizeInternalAgent(writer, request) {
		return
	}
	_, delegated := businessruntime.AgentActivityFromContext(request.Context())
	_, headers := activityHeaders(request)
	if delegated || headers {
		writeError(writer, http.StatusForbidden, "AGENT_ACTIVITY_FORBIDDEN", "Memory archive recovery requires an independent worker.")
		return
	}
	var command struct {
		ConsentRevision int                                `json:"consent_revision"`
		Rollout         businessruntime.AgentMemoryRollout `json:"rollout"`
	}
	if !decodeBodyWithLimit(writer, request, &command, (8<<20)+4096) {
		return
	}
	value, err := s.runtime.RecoverAgentMemoryArchive(request.Context(), command.Rollout, command.ConsentRevision)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": value})
}

func (s *Server) readAgentMemoryArchiveReceipt(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if !authorizeInternalAgent(writer, request) {
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 4096)
	var command struct {
		SegmentID       string `json:"segment_id"`
		ContentHash     string `json:"content_hash"`
		ConsentRevision int    `json:"consent_revision"`
	}
	if !decodeBody(writer, request, &command) {
		return
	}
	value, err := s.runtime.ReadAgentMemoryArchiveReceipt(request.Context(), command.SegmentID, command.ContentHash, command.ConsentRevision)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": value})
}

func (s *Server) saveAgentMemoryRollout(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if !authorizeInternalAgent(writer, request) {
		return
	}
	// Legacy callers must not bypass explicit per-user archival consent.
	writeError(writer, http.StatusGone, "AGENT_MEMORY_ARCHIVE_CONSENT_REQUIRED", "Legacy memory source writes are disabled; use the consent-bound archive endpoint.")
}

func (s *Server) readAgentMemoryRollout(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if !authorizeInternalAgent(writer, request) {
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 4096)
	var command struct {
		SegmentID string `json:"segment_id"`
	}
	if !decodeBody(writer, request, &command) {
		return
	}
	result, err := s.runtime.ReadAgentMemoryRollout(request.Context(), command.SegmentID)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) getAgentMemory(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	result, err := s.runtime.GetAgentMemory(request.Context(), request.PathValue("project_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) resolveAgentMemorySnapshot(writer http.ResponseWriter, request *http.Request) {
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
		writeError(writer, http.StatusForbidden, "AGENT_ACTIVITY_SCOPE_MISMATCH", "记忆请求与当前执行身份不一致。")
		return
	}
	result, err := s.runtime.ResolveAgentMemorySnapshot(request.Context())
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) updateAgentMemory(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	request.Body = http.MaxBytesReader(writer, request.Body, 2*1024*1024)
	var command businessruntime.UpdateAgentMemoryCommand
	if !decodeBody(writer, request, &command) {
		return
	}
	projectID := request.PathValue("project_id")
	if command.ProjectID != "" && command.ProjectID != projectID {
		writeError(writer, http.StatusBadRequest, "RESOURCE_PROJECT_MISMATCH", "记忆请求不属于当前作品。")
		return
	}
	command.ProjectID = projectID
	result, err := s.runtime.UpdateAgentMemory(request.Context(), command)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}
