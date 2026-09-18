package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"novel2script-agent/backend/internal/agent"
	agentruntime "novel2script-agent/backend/internal/agent/runtime"
	"novel2script-agent/backend/internal/worker"
)

func (s *apiServer) getRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run_id")
	run, ok := s.runtime.GetRun(runID)
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("run not found"))
		return
	}
	approval, _ := s.runtime.CurrentApproval(runID)
	writeJSON(w, http.StatusOK, map[string]any{
		"run": run, "events": s.runtime.Events(runID), "artifacts": s.runtime.Artifacts(runID), "approval_request": approval,
	})
}

func (s *apiServer) pauseRunningRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	run, events, err := s.runtime.PauseRunningRun(r.PathValue("run_id"), req.Reason)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	project, err := s.updateProjectFromRun(run)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run, "events": events, "artifacts": s.runtime.Artifacts(run.RunID), "project": project})
}

func (s *apiServer) resumePausedRun(w http.ResponseWriter, r *http.Request) {
	run, events, err := s.runtime.ResumePausedRun(r.PathValue("run_id"))
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	project, err := s.updateProjectFromRun(run)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run, "events": events, "artifacts": s.runtime.Artifacts(run.RunID), "project": project})
}

func (s *apiServer) getEvents(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run_id")
	if _, ok := s.runtime.GetRun(runID); !ok {
		writeError(w, http.StatusNotFound, errors.New("run not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run_id": runID, "events": s.runtime.Events(runID)})
}

func (s *apiServer) streamEvents(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run_id")
	if _, ok := s.runtime.GetRun(runID); !ok {
		writeError(w, http.StatusNotFound, errors.New("run not found"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming is unavailable"))
		return
	}
	lastEventID := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if queryID := strings.TrimSpace(r.URL.Query().Get("after")); queryID != "" {
		lastEventID = queryID
	}
	sent := eventIDsThrough(s.runtime.Events(runID), lastEventID)
	sendPending := func() {
		wrote := false
		for _, event := range s.runtime.Events(runID) {
			if sent[event.EventID] {
				continue
			}
			data, err := json.Marshal(event)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", event.EventID, event.Type, data)
			sent[event.EventID] = true
			wrote = true
		}
		if wrote {
			flusher.Flush()
		}
	}
	sendPending()
	ticker := time.NewTicker(500 * time.Millisecond)
	keepalive := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			sendPending()
		case <-keepalive.C:
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func eventIDsThrough(events []agent.RunEvent, lastEventID string) map[string]bool {
	sent := map[string]bool{}
	if lastEventID == "" {
		return sent
	}
	found := false
	for _, event := range events {
		if event.EventID == lastEventID {
			found = true
			break
		}
	}
	if !found {
		return sent
	}
	for _, event := range events {
		sent[event.EventID] = true
		if event.EventID == lastEventID {
			break
		}
	}
	return sent
}

func (s *apiServer) rerunStep(w http.ResponseWriter, r *http.Request) {
	var req agent.RerunStepRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resp, err := s.runtime.RerunStep(r.PathValue("run_id"), r.PathValue("step_id"), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *apiServer) getArtifacts(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run_id")
	if _, ok := s.runtime.GetRun(runID); !ok {
		writeError(w, http.StatusNotFound, errors.New("run not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run_id": runID, "artifacts": s.runtime.Artifacts(runID)})
}

func (s *apiServer) updateArtifact(w http.ResponseWriter, r *http.Request) {
	artifactID := r.PathValue("artifact_id")
	var req artifactUpdateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Payload) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("payload is required"))
		return
	}
	if req.BaseVersion < 1 {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "base_version is required", true, false, nil)
		return
	}
	currentArtifact, ok := s.runtime.ArtifactByID(artifactID)
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("artifact not found"))
		return
	}
	if run, exists := s.runtime.GetRun(currentArtifact.RunID); exists && run.Status == agent.RunRunning {
		writeAPIError(w, http.StatusConflict, "RUN_ACTIVE_EDIT_LOCKED", "当前正在生成下游内容。为保证结果使用同一版本，请先暂停流程，或等待生成完成后再编辑。", true, false, map[string]any{"run_id": run.RunID, "run_status": run.Status})
		return
	}
	if currentArtifact.ArtifactType != "source_input" && currentArtifact.ArtifactType != "scripts" {
		config := map[string]any{}
		if run, exists := s.runtime.GetRun(currentArtifact.RunID); exists {
			config, _ = run.Metadata["generation_config"].(map[string]any)
		}
		if editedConfig, exists := req.Payload["generation_config"].(map[string]any); exists && len(editedConfig) > 0 {
			config = editedConfig
		}
		if err := worker.ValidateArtifactAgainstGenerationConfig(currentArtifact.ArtifactType, req.Payload, config); err != nil {
			writeAPIError(w, http.StatusBadRequest, "VALIDATION_FAILED", "保存内容未通过结构校验："+err.Error(), true, false, nil)
			return
		}
	}
	artifact, events, artifacts, current, outcome := s.runtime.UpdateArtifactAtVersion(artifactID, req.BaseVersion, req.Payload)
	if outcome == agentruntime.ArtifactUpdateNotFound {
		writeError(w, http.StatusNotFound, errors.New("artifact not found"))
		return
	}
	if outcome == agentruntime.ArtifactUpdateConflict {
		writeAPIError(w, http.StatusConflict, "CONFLICT", "当前内容已有新版本，已加载最新版本，请重新检查后再修改。", true, false, map[string]any{"current_artifact": current})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifact": artifact, "events": events, "artifacts": artifacts})
}
