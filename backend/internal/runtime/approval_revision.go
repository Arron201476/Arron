package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// RequestApprovalRevision creates a normal Artifact revision request from an
// approval card. It intentionally does not resolve the approval or create a
// regeneration plan; accepting the resulting proposal creates the new version.
func (s *Store) RequestApprovalRevision(
	ctx context.Context,
	command RequestApprovalRegenerationCommand,
) (RevisionRequest, error) {
	if command.Action != "request_ai_revision" || strings.TrimSpace(command.Instruction) == "" {
		return RevisionRequest{}, domainError("REQUEST_VALIDATION_FAILED", "Agent 修改要求不完整。")
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RevisionRequest{}, err
	}
	defer tx.Rollback()
	approval, err := getApprovalTx(ctx, tx, command.ApprovalRequestID)
	if err != nil {
		return RevisionRequest{}, err
	}
	if err := authorizeApprovalMutationTx(ctx, tx, approval, command.Scope); err != nil {
		return RevisionRequest{}, err
	}
	if command.Scope == "" {
		command.Scope = approval.ProjectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return RevisionRequest{}, err
	}
	if hit {
		receipt, err := decodeApprovalRevisionReceiptTx(ctx, tx, cached, approval)
		if err == nil && command.TargetArtifactVersionID != "" && (receipt.BaseVersionID == nil || *receipt.BaseVersionID != command.TargetArtifactVersionID) {
			return RevisionRequest{}, approvalReceiptMismatch()
		}
		return receipt, err
	}
	if approval.Status != "pending" || approval.Version != command.ExpectedApprovalVersion ||
		approval.SubjectSnapshotHash != command.SubjectSnapshotHash {
		return RevisionRequest{}, domainError("APPROVAL_SUBJECT_CHANGED", "确认对象已经变化，请刷新后重试。")
	}
	if !slices.Contains(approval.Options, command.Action) || approval.Scope == "quality_review" || approval.Scope == "final_selection" {
		return RevisionRequest{}, domainError("APPROVAL_ACTION_NOT_ALLOWED", "当前确认请求不允许此返修操作。")
	}
	if blocked, blockErr := approvalHasActiveRevisionTx(ctx, tx, approval); blockErr != nil {
		return RevisionRequest{}, blockErr
	} else if blocked {
		return RevisionRequest{}, domainError("REVISION_IN_PROGRESS", "当前版本存在未处理的修改请求。")
	}
	targets, err := s.approvalRevisionTargetsTx(ctx, tx, approval)
	if err != nil {
		return RevisionRequest{}, err
	}
	var target regenerationTarget
	if command.TargetArtifactVersionID != "" {
		for _, candidate := range targets {
			if candidate.CurrentVersion.ArtifactVersionID == command.TargetArtifactVersionID {
				target = candidate
				break
			}
		}
		if target.Artifact.ArtifactID == "" {
			return RevisionRequest{}, domainError("TARGET_CANDIDATE_INVALID", "所选产物版本不属于当前审批的可修改对象。")
		}
	} else {
		target, err = approvalRevisionTarget(targets, command.Instruction)
	}
	if err != nil {
		return RevisionRequest{}, err
	}
	var conversationID string
	if err := tx.QueryRowContext(ctx, `SELECT primary_conversation_id FROM projects WHERE project_id = ?`, approval.ProjectID).Scan(&conversationID); err != nil {
		return RevisionRequest{}, err
	}
	userMessageID := s.newID("msg")
	agentMessageID := s.newID("msg")
	for _, message := range []struct{ id, role, content string }{
		{userMessageID, "user", strings.TrimSpace(command.Instruction)},
		{agentMessageID, "assistant", "已定位当前待确认产物，正在通过 SDK 生成局部修改稿。"},
	} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO messages(message_id, conversation_id, project_id, role, content, created_at) VALUES(?, ?, ?, ?, ?, ?)`,
			message.id, conversationID, approval.ProjectID, message.role, message.content, formatTime(now)); err != nil {
			return RevisionRequest{}, err
		}
	}
	display, _ := json.Marshal(map[string]string{
		"artifact_label": artifactLabel(target.Artifact.ArtifactType),
		"location_label": scopeDisplay(target.Artifact.ScopeKey),
	})
	resolution := TargetResolution{
		TargetResolutionID: s.newID("tr"), ProjectID: approval.ProjectID,
		ConversationID: conversationID, RequestMessageID: userMessageID,
		Status: "resolved", Source: "approval_revision",
		ArtifactID:        exactStringPointer(target.Artifact.ArtifactID),
		ArtifactVersionID: exactStringPointer(target.CurrentVersion.ArtifactVersionID),
		ArtifactType:      exactStringPointer(target.Artifact.ArtifactType),
		ScopeKey:          optionalStringPointer(target.Artifact.ScopeKey),
		Entity:            json.RawMessage(`{}`), Display: display, CreatedAt: now,
	}
	resolvedAt := now
	resolution.ResolvedAt = &resolvedAt
	targetHash := targetResolutionHash(resolution)
	resolution.TargetHash = &targetHash
	if err := s.persistTargetResolutionTx(ctx, tx, resolution); err != nil {
		return RevisionRequest{}, err
	}
	revision, err := s.createRevisionRequestTx(ctx, tx, resolution, command.Instruction, false, now)
	if err != nil {
		return RevisionRequest{}, err
	}
	revision.SourceApprovalRequestID = exactStringPointer(approval.ApprovalRequestID)
	if _, err := s.appendEvent(ctx, tx, approval.ProjectID, nil, nil,
		"revision_request.created", "revision_request", revision.RevisionRequestID,
		map[string]any{"source": "approval_revision", "approval_request_id": approval.ApprovalRequestID}); err != nil {
		return RevisionRequest{}, err
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, revision, now); err != nil {
		return RevisionRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return RevisionRequest{}, err
	}
	return revision, nil
}

func approvalRevisionTarget(targets []regenerationTarget, instruction string) (regenerationTarget, error) {
	if len(targets) == 0 {
		return regenerationTarget{}, domainError("ARTIFACT_VERSION_NOT_FOUND", "确认请求没有可修改的产物。")
	}
	if len(targets) == 1 {
		return targets[0], nil
	}
	sharedScope := targets[0].Artifact.ScopeKey
	allSameScope := true
	for _, target := range targets[1:] {
		if target.Artifact.ScopeKey != sharedScope {
			allSameScope = false
			break
		}
	}
	if allSameScope {
		for _, target := range targets {
			if target.Artifact.ArtifactType == "script_unit" {
				return target, nil
			}
		}
	}
	wantedEpisode := 0
	if match := episodeTargetPattern.FindStringSubmatch(strings.ToLower(instruction)); len(match) == 2 {
		wantedEpisode, _ = strconv.Atoi(match[1])
	}
	if wantedEpisode > 0 {
		matching := []regenerationTarget{}
		for _, target := range targets {
			if revisionEpisodeFromScope(target.Artifact.ScopeKey) == wantedEpisode {
				matching = append(matching, target)
			}
		}
		if len(matching) == 1 {
			return matching[0], nil
		}
		for _, target := range matching {
			if target.Artifact.ArtifactType == "script_unit" {
				return target, nil
			}
		}
	}
	return regenerationTarget{}, domainError("TARGET_AMBIGUOUS", fmt.Sprintf("该确认包含 %d 个产物，请选择需要修改的具体产物。", len(targets)))
}
