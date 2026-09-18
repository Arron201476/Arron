package httpapi

import (
	"encoding/json"
	"net/http"

	businessruntime "content-agent/backend/internal/runtime"
)

func (s *Server) memoryGenerationWorker(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if !authorizeInternalAgent(writer, request) {
		return
	}
	if _, delegated := businessruntime.AgentActivityFromContext(request.Context()); delegated {
		writeError(writer, http.StatusForbidden, "AGENT_ACTIVITY_FORBIDDEN", "Memory generation scheduling requires an independent worker.")
		return
	}
	operation := request.PathValue("operation")
	if operation != "claim" && operation != "start" && operation != "renew" && operation != "checkpoint" && operation != "pause" && operation != "extraction" && operation != "inputs" && operation != "complete" {
		writeError(writer, http.StatusNotFound, "RESOURCE_NOT_FOUND", "Memory generation operation was not found.")
		return
	}
	limit := int64(4096)
	if operation == "checkpoint" || operation == "pause" || operation == "extraction" {
		limit += 4 << 20
	}
	if operation == "inputs" {
		limit += 16 << 20
	}
	request.Body = http.MaxBytesReader(writer, request.Body, limit)
	if operation == "claim" {
		var command struct {
			WorkerID     string `json:"worker_id"`
			ModelID      string `json:"model_id"`
			LeaseSeconds int    `json:"lease_seconds"`
		}
		if !decodeBody(writer, request, &command) {
			return
		}
		claim, err := s.runtime.ClaimAgentMemoryGeneration(request.Context(), command.WorkerID, command.ModelID, command.LeaseSeconds)
		if err != nil {
			s.handleRuntimeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"claim": claim}})
		return
	}
	var command struct {
		GenerationID string                               `json:"generation_id"`
		WorkerID     string                               `json:"worker_id"`
		AttemptToken string                               `json:"attempt_token"`
		Attempt      int                                  `json:"attempt"`
		LeaseSeconds int                                  `json:"lease_seconds,omitempty"`
		ExpectedHash string                               `json:"expected_hash,omitempty"`
		Checkpoint   json.RawMessage                      `json:"checkpoint,omitempty"`
		Output       json.RawMessage                      `json:"output,omitempty"`
		InputPlan    businessruntime.AgentMemoryInputPlan `json:"input_plan,omitempty"`
	}
	if !decodeBody(writer, request, &command) {
		return
	}
	if operation == "inputs" {
		result, err := s.runtime.PrepareAgentMemoryInputs(request.Context(), command.GenerationID, command.WorkerID, command.AttemptToken, command.Attempt, command.InputPlan)
		if err != nil {
			s.handleRuntimeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"receipt": result}})
		return
	}
	if operation == "extraction" {
		result, err := s.runtime.CompleteAgentMemoryExtraction(request.Context(), command.GenerationID, command.WorkerID, command.AttemptToken, command.Attempt, command.Output)
		if err != nil {
			s.handleRuntimeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"receipt": result}})
		return
	}
	var job businessruntime.AgentMemoryGeneration
	var err error
	switch operation {
	case "complete":
		job, err = s.runtime.CompleteAgentMemoryGeneration(request.Context(), command.GenerationID, command.WorkerID, command.AttemptToken, command.Attempt)
	case "start":
		job, err = s.runtime.StartAgentMemoryGeneration(request.Context(), command.GenerationID, command.WorkerID, command.AttemptToken, command.Attempt)
	case "renew":
		job, err = s.runtime.RenewAgentMemoryGeneration(request.Context(), command.GenerationID, command.WorkerID, command.AttemptToken, command.Attempt, command.LeaseSeconds)
	case "checkpoint":
		job, err = s.runtime.CheckpointAgentMemoryGeneration(request.Context(), command.GenerationID, command.WorkerID, command.AttemptToken, command.Attempt, command.ExpectedHash, command.Checkpoint)
	case "pause":
		job, err = s.runtime.PauseAgentMemoryGeneration(request.Context(), command.GenerationID, command.WorkerID, command.AttemptToken, command.Attempt, command.ExpectedHash, command.Checkpoint)
	}
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"job": job}})
}
