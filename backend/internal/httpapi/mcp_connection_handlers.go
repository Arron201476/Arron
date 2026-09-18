package httpapi

import (
	"net/http"

	businessruntime "content-agent/backend/internal/runtime"
)

func (s *Server) listMCPConnections(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	result, err := s.runtime.ListMCPConnections(request.Context())
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) updateMCPConnection(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	request.Body = http.MaxBytesReader(writer, request.Body, 96*1024)
	var command businessruntime.UpdateMCPConnectionCommand
	if !decodeBody(writer, request, &command) {
		return
	}
	// Do not pass secret-bearing bodies into generic command hashes or ledgers.
	result, err := s.runtime.UpdateMCPConnection(request.Context(), command)
	clear(command.Values)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) resolveMCPCredentials(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if !authorizeInternalAgent(writer, request) {
		return
	}
	var command businessruntime.ResolveMCPCredentialsCommand
	if !decodeBody(writer, request, &command) {
		return
	}
	result, err := s.runtime.ResolveMCPCredentials(request.Context(), command)
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	defer clear(result.Values)
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}
