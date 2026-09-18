package httpapi

import (
	"mime"
	"net/http"
	"strconv"
)

func (s *Server) getArtifactDelivery(writer http.ResponseWriter, request *http.Request) {
	delivery, err := s.runtime.GetArtifactDelivery(request.Context(), request.PathValue("artifact_version_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writer.Header().Set("Cache-Control", "private, no-store")
	writeJSON(writer, http.StatusOK, map[string]any{"data": delivery})
}

func (s *Server) downloadArtifactVersion(writer http.ResponseWriter, request *http.Request) {
	download, data, err := s.runtime.DownloadArtifactVersion(request.Context(), request.PathValue("artifact_version_id"), request.URL.Query().Get("format"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writer.Header().Set("Content-Type", download.ContentType)
	writer.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": download.Filename}))
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("Content-Length", strconv.Itoa(len(data)))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(data)
}
