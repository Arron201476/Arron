package main

import "net/http"

func newAPIHandler(server *apiServer) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", server.health)
	mux.HandleFunc("GET /api/projects", server.listProjects)
	mux.HandleFunc("POST /api/projects", server.createProject)
	mux.HandleFunc("GET /api/projects/{project_id}", server.getProject)
	mux.HandleFunc("DELETE /api/projects/{project_id}", server.deleteProject)
	mux.HandleFunc("GET /api/projects/{project_id}/messages", server.listProjectMessages)
	mux.HandleFunc("POST /api/projects/{project_id}/messages", server.projectMessage)
	mux.HandleFunc("GET /api/projects/{project_id}/files", server.listProjectFiles)
	mux.HandleFunc("POST /api/projects/{project_id}/files", server.uploadProjectFile)
	mux.HandleFunc("DELETE /api/files/{file_id}", server.deleteFile)
	mux.HandleFunc("PATCH /api/artifacts/{artifact_id}", server.updateArtifact)
	mux.HandleFunc("POST /api/approvals/{approval_request_id}/resolve", server.resolveApproval)
	mux.HandleFunc("GET /api/runs/{run_id}", server.getRun)
	mux.HandleFunc("POST /api/runs/{run_id}/pause", server.pauseRunningRun)
	mux.HandleFunc("POST /api/runs/{run_id}/resume", server.resumePausedRun)
	mux.HandleFunc("POST /api/runs/{run_id}/steps/{step_id}/rerun", server.rerunStep)
	mux.HandleFunc("GET /api/runs/{run_id}/events", server.getEvents)
	mux.HandleFunc("GET /api/runs/{run_id}/events/stream", server.streamEvents)
	mux.HandleFunc("GET /api/runs/{run_id}/artifacts", server.getArtifacts)
	mux.HandleFunc("POST /api/system/shutdown", server.shutdownService)
	if directory := webDirectory(); directory != "" {
		mux.Handle("GET /", newSPAHandler(directory))
	}
	return withRequestLimit(withCORS(mux), 16<<20)
}
