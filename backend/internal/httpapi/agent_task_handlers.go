package httpapi

import (
	"encoding/json"
	"net/http"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
)

func (s *Server) appendAgentTaskInput(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Content        string                        `json:"content"`
		AttachmentRefs []agentcontract.AttachmentRef `json:"attachment_refs,omitempty"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	meta, ok := commandMeta(writer, request, request.PathValue("agent_task_id"), "append_agent_task_input", body)
	if !ok {
		return
	}
	input, err := s.runtime.AppendAgentTaskInput(request.Context(), businessruntime.AppendAgentTaskInputCommand{CommandMeta: meta, AgentTaskID: request.PathValue("agent_task_id"), Content: body.Content, AttachmentRefs: body.AttachmentRefs})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]any{"data": input})
}

func (s *Server) listAgentTasks(writer http.ResponseWriter, request *http.Request) {
	items, err := s.runtime.ListAgentTasks(request.Context(), request.PathValue("project_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"items": items}})
}

func (s *Server) getAgentTask(writer http.ResponseWriter, request *http.Request) {
	task, err := s.runtime.GetAgentTask(request.Context(), request.PathValue("agent_task_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": task})
}

func (s *Server) listAgentTaskAttempts(writer http.ResponseWriter, request *http.Request) {
	items, err := s.runtime.ListAgentTaskAttempts(request.Context(), request.PathValue("agent_task_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"items": items}})
}

func (s *Server) startAgentTask(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		CapabilityID      string          `json:"capability_id"`
		CapabilityVersion string          `json:"capability_version"`
		ConversationID    string          `json:"conversation_id"`
		Input             json.RawMessage `json:"input"`
		Config            json.RawMessage `json:"config"`
		Confirmation      struct {
			Confirmed     bool   `json:"confirmed"`
			ActionVersion int    `json:"action_version"`
			MessageID     string `json:"confirmation_message_id"`
			SnapshotHash  string `json:"snapshot_hash"`
		} `json:"confirmation"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	action, err := s.runtime.GetProposedAction(request.Context(), request.PathValue("proposed_action_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, action.ProjectID, "start_agent_task", body)
	if !ok {
		return
	}
	task, err := s.runtime.StartAgentTask(request.Context(), businessruntime.StartAgentTaskCommand{
		CommandMeta: meta, ProjectID: action.ProjectID, ConversationID: body.ConversationID,
		CapabilityID: body.CapabilityID, CapabilityVersion: body.CapabilityVersion,
		Input: body.Input, Config: body.Config, Confirmed: body.Confirmation.Confirmed,
		ProposedActionID: action.ProposedActionID, ProposedActionVersion: body.Confirmation.ActionVersion,
		ConfirmationMessageID:    body.Confirmation.MessageID,
		ConfirmationSnapshotHash: body.Confirmation.SnapshotHash,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"data": task})
}

func (s *Server) cancelAgentTask(writer http.ResponseWriter, request *http.Request) {
	task, err := s.runtime.GetAgentTask(request.Context(), request.PathValue("agent_task_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, task.ProjectID, "cancel_agent_task", struct{}{})
	if !ok {
		return
	}
	cancelled, err := s.runtime.CancelAgentTask(request.Context(), businessruntime.CancelAgentTaskCommand{
		CommandMeta: meta, AgentTaskID: task.AgentTaskID, ActorRef: identity.ActorRefFromContext(request.Context()),
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]any{"data": cancelled})
}

func (s *Server) retryAgentTask(writer http.ResponseWriter, request *http.Request) {
	task, err := s.runtime.GetAgentTask(request.Context(), request.PathValue("agent_task_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, task.ProjectID, "retry_agent_task", struct{}{})
	if !ok {
		return
	}
	retried, err := s.runtime.RetryAgentTask(request.Context(), businessruntime.RetryAgentTaskCommand{
		CommandMeta: meta, AgentTaskID: task.AgentTaskID, ActorRef: identity.ActorRefFromContext(request.Context()),
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]any{"data": retried})
}

func (s *Server) claimAgentTask(writer http.ResponseWriter, request *http.Request) {
	if !authorizeInternalAgent(writer, request) {
		return
	}
	var body struct {
		WorkerID     string `json:"worker_id"`
		ProviderID   string `json:"provider_id"`
		ModelID      string `json:"model_id"`
		LeaseSeconds int    `json:"lease_seconds"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	claim, err := s.runtime.ClaimAgentTask(request.Context(), businessruntime.ClaimAgentTaskCommand{
		WorkerID: body.WorkerID, ProviderID: body.ProviderID,
		ModelID: body.ModelID, LeaseSeconds: body.LeaseSeconds,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	if claim == nil {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": claim})
}

func (s *Server) updateAgentTaskProgress(writer http.ResponseWriter, request *http.Request) {
	if !authorizeInternalAgent(writer, request) {
		return
	}
	var body struct {
		Current      int    `json:"current"`
		Total        int    `json:"total"`
		Message      string `json:"message"`
		LeaseSeconds int    `json:"lease_seconds"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	task, err := s.runtime.UpdateAgentTaskProgress(request.Context(), businessruntime.UpdateAgentTaskProgressCommand{
		AgentTaskAttemptID: request.PathValue("attempt_id"),
		AttemptToken:       request.Header.Get("X-Attempt-Token"),
		Current:            body.Current, Total: body.Total, Message: body.Message,
		LeaseSeconds: body.LeaseSeconds,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": task})
}

func (s *Server) completeAgentTask(writer http.ResponseWriter, request *http.Request) {
	if !authorizeInternalAgent(writer, request) {
		return
	}
	var body struct {
		Result           json.RawMessage              `json:"result"`
		ArtifactDraft    *agentcontract.ArtifactDraft `json:"artifact_draft"`
		Usage            json.RawMessage              `json:"usage"`
		TraceRef         string                       `json:"trace_ref"`
		IncludedInputIDs []string                     `json:"included_input_ids"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	task, err := s.runtime.CompleteAgentTask(request.Context(), businessruntime.CompleteAgentTaskCommand{
		AgentTaskAttemptID: request.PathValue("attempt_id"),
		AttemptToken:       request.Header.Get("X-Attempt-Token"), Result: body.Result,
		ArtifactDraft: body.ArtifactDraft, Usage: body.Usage, TraceRef: body.TraceRef,
		IncludedInputIDs: body.IncludedInputIDs,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": task})
}

func (s *Server) pauseAgentTaskForApproval(writer http.ResponseWriter, request *http.Request) {
	s.checkpointAgentTaskPause(writer, request, false)
}

func (s *Server) completeAgentTaskPause(writer http.ResponseWriter, request *http.Request) {
	s.checkpointAgentTaskPause(writer, request, true)
}

func (s *Server) checkpointAgentTaskPause(writer http.ResponseWriter, request *http.Request, userPause bool) {
	if !authorizeInternalAgent(writer, request) {
		return
	}
	var body struct {
		businessruntime.PauseAgentTurnForApprovalCommand
		IncludedInputIDs []string `json:"included_input_ids"`
	}
	if !decodeBodyWithLimit(writer, request, &body, agentcontract.MaxSDKRunStateEnvelopeBytes) {
		return
	}
	command := businessruntime.PauseAgentTaskForApprovalCommand{
		AgentTaskAttemptID: request.PathValue("attempt_id"), AttemptToken: request.Header.Get("X-Attempt-Token"),
		PauseAgentTurnForApprovalCommand: body.PauseAgentTurnForApprovalCommand, IncludedInputIDs: body.IncludedInputIDs,
	}
	checkpoint := s.runtime.PauseAgentTaskForApproval
	if userPause {
		checkpoint = s.runtime.CompleteAgentTaskPause
	}
	task, err := checkpoint(request.Context(), command)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": task})
}

func (s *Server) requestAgentTaskPause(writer http.ResponseWriter, request *http.Request) {
	s.controlAgentTaskPause(writer, request, false)
}

func (s *Server) resumeAgentTask(writer http.ResponseWriter, request *http.Request) {
	s.controlAgentTaskPause(writer, request, true)
}

func (s *Server) controlAgentTaskPause(writer http.ResponseWriter, request *http.Request, resume bool) {
	task, err := s.runtime.GetAgentTask(request.Context(), request.PathValue("agent_task_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	action := "pause_agent_task"
	if resume {
		action = "resume_agent_task"
	}
	meta, ok := commandMeta(writer, request, task.ProjectID, action, struct{}{})
	if !ok {
		return
	}
	if resume {
		task, err = s.runtime.ResumeAgentTask(request.Context(), businessruntime.ResumeAgentTaskCommand{CommandMeta: meta, AgentTaskID: task.AgentTaskID, ActorRef: identity.ActorRefFromContext(request.Context())})
	} else {
		task, err = s.runtime.RequestAgentTaskPause(request.Context(), businessruntime.PauseAgentTaskCommand{CommandMeta: meta, AgentTaskID: task.AgentTaskID, ActorRef: identity.ActorRefFromContext(request.Context())})
	}
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]any{"data": task})
}

func (s *Server) failAgentTask(writer http.ResponseWriter, request *http.Request) {
	if !authorizeInternalAgent(writer, request) {
		return
	}
	var body struct {
		ErrorCode        string   `json:"error_code"`
		ErrorMessage     string   `json:"error_message"`
		Retryable        bool     `json:"retryable"`
		IncludedInputIDs []string `json:"included_input_ids"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	task, err := s.runtime.FailAgentTask(request.Context(), businessruntime.FailAgentTaskCommand{
		AgentTaskAttemptID: request.PathValue("attempt_id"),
		AttemptToken:       request.Header.Get("X-Attempt-Token"), ErrorCode: body.ErrorCode,
		ErrorMessage: body.ErrorMessage, Retryable: body.Retryable, IncludedInputIDs: body.IncludedInputIDs,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": task})
}
