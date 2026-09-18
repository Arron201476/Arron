package httpapi

import (
	"context"
	"fmt"
	"net/http"

	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
)

func (s *Server) listRevisionRequests(writer http.ResponseWriter, request *http.Request) {
	items, err := s.runtime.ListRevisionRequests(request.Context(), request.PathValue("project_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": map[string]any{"items": items}})
}

func (s *Server) getRevisionRequest(writer http.ResponseWriter, request *http.Request) {
	item, err := s.runtime.GetRevisionRequest(request.Context(), request.PathValue("revision_request_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": item})
}

func (s *Server) executeRevisionRequest(writer http.ResponseWriter, request *http.Request) {
	if s.revisions == nil {
		writeError(writer, http.StatusServiceUnavailable, "REVISION_ADAPTER_UNAVAILABLE", "Agent 修改能力暂不可用。")
		return
	}
	var body struct {
		ExpectedVersion int `json:"expected_revision_version"`
	}
	if request.ContentLength != 0 && !decodeBody(writer, request, &body) {
		return
	}
	item, err := s.runtime.GetRevisionRequest(request.Context(), request.PathValue("revision_request_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, item.ProjectID, "execute_revision", body)
	if !ok {
		return
	}
	item, err = s.runtime.RequestRevisionExecution(request.Context(), businessruntime.RequestRevisionExecutionCommand{CommandMeta: meta, RevisionRequestID: item.RevisionRequestID, ExpectedVersion: body.ExpectedVersion})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]any{"data": item})
}

func (s *Server) resolveTargetCandidate(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		CandidateID     string `json:"candidate_id"`
		ExpectedVersion int    `json:"expected_revision_version"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	resolution, err := s.runtime.GetTargetResolution(request.Context(), request.PathValue("target_resolution_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, resolution.ProjectID, "resolve_target_candidate", body)
	if !ok {
		return
	}
	item, err := s.runtime.ResolveTargetCandidate(request.Context(), businessruntime.ResolveTargetCandidateCommand{
		CommandMeta: meta, TargetResolutionID: resolution.TargetResolutionID, CandidateID: body.CandidateID, ExpectedVersion: body.ExpectedVersion,
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": item})
}

func (s *Server) acceptRevisionRequest(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		ExpectedVersion int `json:"expected_revision_version"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	item, err := s.runtime.GetRevisionRequest(request.Context(), request.PathValue("revision_request_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, item.ProjectID, "accept_revision", body)
	if !ok {
		return
	}
	result, err := s.runtime.AcceptRevision(request.Context(), businessruntime.AcceptRevisionCommand{
		CommandMeta: meta, RevisionRequestID: item.RevisionRequestID,
		ExpectedVersion: body.ExpectedVersion, ActorRef: identity.ActorRefFromContext(request.Context()),
	})
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	if err := s.completeAcceptedRevisionHandoff(request.Context(), meta, &result); err != nil {
		s.logger.Error("complete accepted revision handoff refresh", "revision_request_id", item.RevisionRequestID, "error", err)
		writeError(writer, http.StatusConflict, "REVISION_HANDOFF_REFRESH_FAILED", "修改已保存，但剧本交接刷新未能启动，请刷新后重试。")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": result})
}

func (s *Server) completeAcceptedRevisionHandoff(
	ctx context.Context,
	meta businessruntime.CommandMeta,
	result *businessruntime.RevisionAcceptResult,
) error {
	if result == nil || !result.Version.HandoffRefreshRequired {
		return nil
	}
	artifact, err := s.runtime.GetArtifact(ctx, result.Version.ArtifactVersion.ArtifactID)
	if err != nil {
		return err
	}
	meta.CommandType = "complete_script_edit"
	meta.IdempotencyKey += ":complete_script_edit"
	meta.RequestHash += ":complete_script_edit"
	versions, err := acceptedRevisionScriptVersions(result.Version)
	if err != nil {
		return err
	}
	_, err = s.runtime.CompleteScriptEdit(ctx, businessruntime.CompleteScriptEditCommand{
		CommandMeta:              meta,
		RunID:                    artifact.RunID,
		ExpectedScriptVersionIDs: versions,
	})
	return err
}

func acceptedRevisionScriptVersions(result businessruntime.VersionResult) ([]string, error) {
	versions := append([]string(nil), result.PendingRefreshVersionIDs...)
	if len(versions) == 0 && len(result.PendingRefreshScopes) <= 1 {
		versions = []string{result.ArtifactVersion.ArtifactVersionID}
	}
	seen := make(map[string]bool, len(versions))
	for _, versionID := range versions {
		if versionID == "" || seen[versionID] {
			return nil, fmt.Errorf("accepted revision has an invalid handoff version set")
		}
		seen[versionID] = true
	}
	if !seen[result.ArtifactVersion.ArtifactVersionID] || len(result.PendingRefreshScopes) > 0 && len(versions) != len(result.PendingRefreshScopes) {
		return nil, fmt.Errorf("accepted revision has an incomplete handoff version set")
	}
	return versions, nil
}

func (s *Server) rejectRevisionRequest(writer http.ResponseWriter, request *http.Request) {
	s.finishRevisionRequest(writer, request, "reject")
}

func (s *Server) cancelRevisionRequest(writer http.ResponseWriter, request *http.Request) {
	s.finishRevisionRequest(writer, request, "cancel")
}

func (s *Server) finishRevisionRequest(writer http.ResponseWriter, request *http.Request, action string) {
	var body struct {
		ExpectedVersion int `json:"expected_revision_version"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	item, err := s.runtime.GetRevisionRequest(request.Context(), request.PathValue("revision_request_id"))
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	meta, ok := commandMeta(writer, request, item.ProjectID, action+"_revision", body)
	if !ok {
		return
	}
	if action == "reject" {
		item, err = s.runtime.RejectRevision(request.Context(), businessruntime.RejectRevisionCommand{
			CommandMeta: meta, RevisionRequestID: item.RevisionRequestID, ExpectedVersion: body.ExpectedVersion,
		})
	} else {
		item, err = s.runtime.CancelRevision(request.Context(), businessruntime.CancelRevisionCommand{
			CommandMeta: meta, RevisionRequestID: item.RevisionRequestID, ExpectedVersion: body.ExpectedVersion,
		})
	}
	if err != nil {
		s.handleRuntimeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"data": item})
}
