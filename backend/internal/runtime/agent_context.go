package runtime

import (
	"context"
	"database/sql"
	"errors"

	"content-agent/backend/internal/agentcontract"
)

// GetAgentRuntimeContext builds the compact, server-owned workflow context
// used for message routing.
func (s *Store) GetAgentRuntimeContext(ctx context.Context, projectID string) (agentcontract.RuntimeContext, error) {
	return s.getAgentRuntimeContext(ctx, projectID, nil, nil)
}

func (s *Store) GetAgentRuntimeContextForView(
	ctx context.Context,
	projectID string,
	viewedRunID *string,
) (agentcontract.RuntimeContext, error) {
	return s.getAgentRuntimeContext(ctx, projectID, viewedRunID, nil)
}

// GetAgentRuntimeContextForRequest adds a bounded, request-focused Artifact
// context to the project-wide execution facts. Client fields are only used to
// resolve what the user is viewing; persisted Runtime data remains authoritative.
func (s *Store) GetAgentRuntimeContextForRequest(
	ctx context.Context,
	projectID string,
	request agentcontract.MessageRequest,
) (agentcontract.RuntimeContext, error) {
	return s.getAgentRuntimeContext(ctx, projectID, request.ClientContext.ViewedRunID, &request)
}

func (s *Store) getAgentRuntimeContext(
	ctx context.Context,
	projectID string,
	viewedRunID *string,
	request *agentcontract.MessageRequest,
) (agentcontract.RuntimeContext, error) {
	project, err := s.GetProject(ctx, projectID)
	if err != nil {
		return agentcontract.RuntimeContext{}, err
	}
	result := agentcontract.RuntimeContext{
		LatestCapabilityID: project.LatestCapabilityID,
		LatestRunStatus:    project.LatestRunStatus,
		RunIndex:           []agentcontract.RunIndexItem{},
		ArtifactSets:       []agentcontract.ArtifactSetContext{},
		FocusedArtifacts:   []agentcontract.FocusedArtifactContext{},
	}
	var revision agentcontract.RuntimeRevisionContext
	var revisionArtifactID, revisionBaseVersionID sql.NullString
	err = s.db.QueryRowContext(ctx, `
		SELECT revision_request_id, status, instruction, artifact_id, base_artifact_version_id
		FROM revision_requests
		WHERE project_id = ? AND status IN (
			'waiting_target_confirmation', 'waiting_safe_checkpoint', 'queued', 'running', 'proposed'
		)
		ORDER BY created_at DESC, revision_request_id DESC LIMIT 1`, projectID).Scan(
		&revision.RevisionRequestID, &revision.Status, &revision.Instruction,
		&revisionArtifactID, &revisionBaseVersionID,
	)
	if err == nil {
		if revisionArtifactID.Valid {
			value := revisionArtifactID.String
			revision.ArtifactID = &value
		}
		if revisionBaseVersionID.Valid {
			value := revisionBaseVersionID.String
			revision.BaseVersionID = &value
		}
		result.CurrentRevision = &revision
	} else if !errors.Is(err, sql.ErrNoRows) {
		return agentcontract.RuntimeContext{}, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT run_id, capability_id, status, created_at, updated_at
		FROM runs WHERE project_id = ?
		ORDER BY created_at DESC, run_id DESC LIMIT 20`, projectID)
	if err != nil {
		return agentcontract.RuntimeContext{}, err
	}
	for rows.Next() {
		var item agentcontract.RunIndexItem
		if err := rows.Scan(&item.RunID, &item.CapabilityID, &item.Status, &item.CreatedAt, &item.UpdatedAt); err != nil {
			rows.Close()
			return agentcontract.RuntimeContext{}, err
		}
		result.RunIndex = append(result.RunIndex, item)
		if viewedRunID != nil && item.RunID == *viewedRunID {
			result.ViewedRun = &agentcontract.ViewedRunContext{
				RunID: item.RunID, CapabilityID: item.CapabilityID, Status: item.Status,
			}
		}
	}
	if err := rows.Close(); err != nil {
		return agentcontract.RuntimeContext{}, err
	}
	if err := rows.Err(); err != nil {
		return agentcontract.RuntimeContext{}, err
	}
	if viewedRunID != nil && result.ViewedRun == nil {
		return agentcontract.RuntimeContext{}, domainError("RUN_NOT_FOUND", "viewed run does not exist")
	}
	result.ArtifactSets, result.FocusedArtifacts, result.ArtifactFocus,
		result.ArtifactFocusTruncated, err = s.loadAgentArtifactContext(ctx, projectID, request)
	if err != nil {
		return agentcontract.RuntimeContext{}, err
	}
	result.ConfirmedAssetSet = s.confirmedAssetSetContext(ctx, projectID, request)
	if project.ActiveWriteRunID == nil {
		return result, nil
	}

	snapshot, err := s.GetRunSnapshot(ctx, *project.ActiveWriteRunID)
	if err != nil {
		return agentcontract.RuntimeContext{}, err
	}
	active := &agentcontract.ActiveRunContext{
		RunID:            snapshot.Run.RunID,
		CapabilityID:     snapshot.Run.CapabilityID,
		Status:           snapshot.Run.Status,
		CurrentStepRunID: snapshot.Run.CurrentStepRunID,
		AvailableActions: make([]agentcontract.RuntimeActionHint, 0, len(snapshot.AvailableActions)),
	}
	if snapshot.Run.CurrentStepRunID != nil {
		for _, step := range snapshot.Steps {
			if step.StepRunID != *snapshot.Run.CurrentStepRunID {
				continue
			}
			stepID, stepStatus := step.StepID, step.Status
			active.CurrentStepID = &stepID
			active.CurrentStepStatus = &stepStatus
			var previousSameStep int
			if err := s.db.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM step_runs
				WHERE run_id = ? AND step_id = ? AND step_run_id <> ?`,
				snapshot.Run.RunID, step.StepID, step.StepRunID,
			).Scan(&previousSameStep); err != nil {
				return agentcontract.RuntimeContext{}, err
			}
			if previousSameStep > 0 {
				active.ExecutionReason = "regeneration"
			} else {
				active.ExecutionReason = "initial_generation"
			}
			break
		}
		tasks, err := s.ListTaskItems(ctx, *snapshot.Run.CurrentStepRunID)
		if err != nil {
			return agentcontract.RuntimeContext{}, err
		}
		for _, task := range tasks {
			active.TaskItems = append(active.TaskItems, agentcontract.RuntimeTaskItem{
				ItemKey: task.ItemKey,
				Status:  task.Status,
				Failure: task.Failure,
			})
		}
	}
	if snapshot.CurrentApproval != nil {
		active.CurrentApproval = &agentcontract.RuntimeApproval{
			ApprovalRequestID: snapshot.CurrentApproval.ApprovalRequestID,
			Title:             snapshot.CurrentApproval.Title,
			Reason:            snapshot.CurrentApproval.Reason,
			Status:            snapshot.CurrentApproval.Status,
		}
	}
	for _, action := range snapshot.AvailableActions {
		active.AvailableActions = append(active.AvailableActions, agentcontract.RuntimeActionHint{
			ActionID: action.ActionID,
			Enabled:  action.Enabled,
		})
	}
	result.ActiveRun = active
	return result, nil
}

func (s *Store) confirmedAssetSetContext(
	ctx context.Context,
	projectID string,
	request *agentcontract.MessageRequest,
) *agentcontract.ConfirmedAssetSetContext {
	if request == nil || request.ClientContext.CurrentAssetSetVersionID == nil {
		return nil
	}
	snapshot, err := s.GetAssetSetVersion(ctx, *request.ClientContext.CurrentAssetSetVersionID)
	if err != nil || !assetSetMatchesRequest(snapshot, projectID, request.AttachmentRefs) {
		return nil
	}
	return confirmedAssetSetContextFromSnapshot(snapshot)
}

func assetSetMatchesRequest(snapshot AssetSetSnapshot, projectID string, attachments []agentcontract.AttachmentRef) bool {
	if snapshot.AssetSet.ProjectID != projectID || snapshot.AssetSet.Status != "sealed" ||
		snapshot.Version.Status != "sealed" || !snapshot.Version.Completeness.OrderConfirmed {
		return false
	}

	attached := make(map[string]agentcontract.AttachmentRef, len(attachments))
	for _, attachment := range attachments {
		attached[attachment.AssetID] = attachment
	}
	included := 0
	allowedContainers := map[string]struct{}{}
	for _, member := range snapshot.Members {
		if !member.Included {
			continue
		}
		included++
		attachment, ok := attached[member.AssetID]
		if !ok {
			return false
		}
		if attachment.ContainerAssetID != nil {
			allowedContainers[*attachment.ContainerAssetID] = struct{}{}
		}
	}
	if included == 0 {
		return false
	}
	for assetID := range attached {
		if _, isMember := findIncludedAssetSetMember(snapshot.Members, assetID); isMember {
			continue
		}
		if _, isContainer := allowedContainers[assetID]; !isContainer {
			return false
		}
	}
	return true
}

func findIncludedAssetSetMember(members []AssetSetMember, assetID string) (AssetSetMember, bool) {
	for _, member := range members {
		if member.Included && member.AssetID == assetID {
			return member, true
		}
	}
	return AssetSetMember{}, false
}

func confirmedAssetSetContextFromSnapshot(snapshot AssetSetSnapshot) *agentcontract.ConfirmedAssetSetContext {
	return &agentcontract.ConfirmedAssetSetContext{
		AssetSetID: snapshot.AssetSet.AssetSetID, AssetSetVersionID: snapshot.Version.AssetSetVersionID,
		Purpose: snapshot.AssetSet.Purpose, Status: snapshot.AssetSet.Status, MemberCount: snapshot.Version.MemberCount,
	}
}
