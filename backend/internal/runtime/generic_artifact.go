package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// CreateGenericArtifact persists a one-off Agent or Skill result without a
// Business Run. The explicit origin keeps lineage without manufacturing a
// workflow session.
func (s *Store) CreateGenericArtifact(
	ctx context.Context,
	command CreateGenericArtifactCommand,
) (Artifact, error) {
	var err error
	command, err = s.normalizeGenericArtifactCommand(command)
	if err != nil {
		return Artifact{}, err
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Artifact{}, err
	}
	defer tx.Rollback()
	if command.Scope == "" {
		command.Scope = command.ProjectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return Artifact{}, err
	}
	if hit {
		return decodeIdempotentResult[Artifact](cached)
	}
	artifact, err := s.createGenericArtifactTx(ctx, tx, command, now)
	if err != nil {
		return Artifact{}, err
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, artifact, now); err != nil {
		return Artifact{}, err
	}
	if err := tx.Commit(); err != nil {
		return Artifact{}, err
	}
	return artifact, nil
}

func (s *Store) normalizeGenericArtifactCommand(command CreateGenericArtifactCommand) (CreateGenericArtifactCommand, error) {
	if command.Draft.ArtifactType != "generic_document" && command.Draft.ArtifactType != "generic_table" {
		return command, domainError("GENERIC_ARTIFACT_TYPE_INVALID", "通用产物类型无效。")
	}
	if strings.TrimSpace(command.Draft.Title) == "" || !jsonObject(command.Draft.Payload) {
		return command, domainError("GENERIC_ARTIFACT_PAYLOAD_INVALID", "通用产物内容无效。")
	}
	if command.ActorRef == "" {
		command.ActorRef = "content_agent_shell"
	}
	if command.OriginType == "" {
		command.OriginType = "agent_turn"
	}
	if command.OriginID == "" {
		command.OriginID = command.IdempotencyKey
		if command.OriginID == "" {
			command.OriginID = s.newID("turn")
		}
	}
	if command.CapabilityID == "" {
		command.CapabilityID = "agent_shell"
	}
	if !slices.Contains([]string{"agent_turn", "invocation", "agent_task"}, command.OriginType) ||
		strings.TrimSpace(command.OriginID) == "" {
		return command, domainError("GENERIC_ARTIFACT_ORIGIN_INVALID", "通用产物缺少有效来源。")
	}
	return command, nil
}

func (s *Store) createGenericArtifactTx(
	ctx context.Context,
	tx *sql.Tx,
	command CreateGenericArtifactCommand,
	now time.Time,
) (Artifact, error) {
	var conversationProject string
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM conversations WHERE conversation_id = ?`, command.ConversationID).Scan(&conversationProject); errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, domainError("CONVERSATION_NOT_FOUND", "对话不存在。")
	} else if err != nil {
		return Artifact{}, err
	}
	if conversationProject != command.ProjectID {
		return Artifact{}, domainError("PROJECT_SCOPE_MISMATCH", "对话不属于当前作品。")
	}
	if len(command.SourceArtifactVersionIDs) > 12 {
		return Artifact{}, domainError("GENERIC_ARTIFACT_SOURCES_INVALID", "通用产物来源版本过多。")
	}
	sourceVersionIDs, err := validateGenericArtifactSourcesTx(
		ctx, tx, command.ProjectID, command.SourceArtifactVersionIDs,
	)
	if err != nil {
		return Artifact{}, err
	}

	artifactID, versionID := s.newID("art"), s.newID("av")
	payload := command.Draft.Payload
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		return Artifact{}, err
	}
	label, err := json.Marshal(strings.TrimSpace(command.Draft.Title))
	if err != nil {
		return Artifact{}, err
	}
	if _, exists := object["title"]; !exists {
		object["title"] = label
	}
	if _, exists := object["artifact_label"]; !exists {
		object["artifact_label"] = label
	}
	payload, err = json.Marshal(object)
	if err != nil {
		return Artifact{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifacts(
			artifact_id, project_id, run_id, step_run_id, origin_type, origin_id,
			capability_id, artifact_type, scope_key, current_version_id, created_at, updated_at
		) VALUES(?, ?, NULL, NULL, ?, ?, ?, ?, ?, ?, ?, ?)`,
		artifactID, command.ProjectID, command.OriginType, command.OriginID,
		command.CapabilityID, command.Draft.ArtifactType,
		"artifact:"+artifactID, versionID, formatTime(now), formatTime(now)); err != nil {
		return Artifact{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifact_versions(
			artifact_version_id, artifact_id, version, status, payload_json, schema_id,
			schema_version, created_by_kind, actor_ref, creation_reason, created_at, confirmed_at
		) VALUES(?, ?, 1, 'confirmed', ?, ?, '1.0.0', 'agent', ?, 'generic_creation', ?, ?)`,
		versionID, artifactID, string(payload), command.Draft.ArtifactType, command.ActorRef,
		formatTime(now), formatTime(now)); err != nil {
		return Artifact{}, err
	}
	for _, sourceVersionID := range sourceVersionIDs {
		if err := s.insertArtifactDependencyTx(ctx, tx, ArtifactDependency{
			ProjectID:                   command.ProjectID,
			OriginType:                  command.OriginType,
			OriginID:                    command.OriginID,
			DownstreamArtifactVersionID: versionID,
			UpstreamKind:                "artifact_version",
			UpstreamRefID:               sourceVersionID,
			Relation:                    "derived_from",
			UpstreamScope:               dependencyScope("artifact", "artifact_version:"+sourceVersionID),
			DownstreamScope:             dependencyScope("artifact", "singleton"),
			ImpactPolicyID:              "whole_artifact",
		}, now); err != nil {
			return Artifact{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects SET version = version + 1, current_focus_artifact_version_id = ?, updated_at = ?
		WHERE project_id = ? AND deleted_at IS NULL`, versionID, formatTime(now), command.ProjectID); err != nil {
		return Artifact{}, err
	}
	if _, err := s.appendEvent(ctx, tx, command.ProjectID, nil, nil,
		"artifact.created", "artifact", artifactID, nil); err != nil {
		return Artifact{}, err
	}
	artifact := Artifact{
		ArtifactID: artifactID, ProjectID: command.ProjectID,
		OriginType: command.OriginType, OriginID: command.OriginID,
		CapabilityID: command.CapabilityID, ArtifactType: command.Draft.ArtifactType,
		ScopeKey: "artifact:" + artifactID, Title: strings.TrimSpace(command.Draft.Title), CurrentVersionID: versionID, Status: "confirmed",
		CreatedAt: now, UpdatedAt: now,
	}
	return artifact, nil
}

func validateGenericArtifactSourcesTx(
	ctx context.Context,
	tx *sql.Tx,
	projectID string,
	versionIDs []string,
) ([]string, error) {
	seen := make(map[string]struct{}, len(versionIDs))
	validated := make([]string, 0, len(versionIDs))
	for _, rawVersionID := range versionIDs {
		versionID := strings.TrimSpace(rawVersionID)
		if versionID == "" {
			return nil, domainError("GENERIC_ARTIFACT_SOURCES_INVALID", "通用产物来源版本无效。")
		}
		if _, duplicate := seen[versionID]; duplicate {
			continue
		}
		seen[versionID] = struct{}{}
		var sourceProjectID, currentVersionID, status string
		err := tx.QueryRowContext(ctx, `
			SELECT a.project_id, a.current_version_id, av.status
			FROM artifact_versions av
			JOIN artifacts a ON a.artifact_id = av.artifact_id
			WHERE av.artifact_version_id = ?`, versionID,
		).Scan(&sourceProjectID, &currentVersionID, &status)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domainError("DEPENDENCY_UPSTREAM_NOT_FOUND", "通用产物来源版本不存在。")
		}
		if err != nil {
			return nil, err
		}
		if sourceProjectID != projectID {
			return nil, domainError("DEPENDENCY_PROJECT_MISMATCH", "通用产物来源不能跨作品。")
		}
		if currentVersionID != versionID || status != "confirmed" {
			return nil, domainError("GENERIC_ARTIFACT_SOURCE_STALE", "通用产物来源必须是当前已确认版本。")
		}
		validated = append(validated, versionID)
	}
	return validated, nil
}

// CreateConfirmedGenericArtifactVersion applies an accepted Agent revision to
// a generic Agent Shell artifact. Generic artifacts have no domain workflow or
// approval template, so an explicit user acceptance creates a confirmed
// version immediately while preserving the previous version in history.
func (s *Store) CreateConfirmedGenericArtifactVersion(
	ctx context.Context,
	command CreateVersionCommand,
) (VersionResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return VersionResult{}, err
	}
	defer tx.Rollback()
	result, err := s.createConfirmedGenericArtifactVersionTx(ctx, tx, command)
	if err != nil {
		return VersionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return VersionResult{}, err
	}
	return result, nil
}

func (s *Store) createConfirmedGenericArtifactVersionTx(ctx context.Context, tx *sql.Tx, command CreateVersionCommand) (VersionResult, error) {
	if !jsonObject(command.NewPayload) {
		return VersionResult{}, domainError("ARTIFACT_PAYLOAD_INVALID", "产物内容必须是 JSON 对象。")
	}
	if command.ActorRef == "" {
		command.ActorRef = actorRefFromContext(ctx)
	}

	now := s.now()
	newVersionID := s.newID("av")

	var artifact Artifact
	if err := tx.QueryRowContext(ctx, `
		SELECT artifact_id, project_id, COALESCE(run_id, ''), COALESCE(step_run_id, ''),
			origin_type, origin_id, capability_id,
			artifact_type, scope_key, current_version_id
		FROM artifacts WHERE artifact_id = ?`, command.ArtifactID).Scan(
		&artifact.ArtifactID, &artifact.ProjectID, &artifact.RunID, &artifact.StepRunID,
		&artifact.OriginType, &artifact.OriginID,
		&artifact.CapabilityID, &artifact.ArtifactType, &artifact.ScopeKey,
		&artifact.CurrentVersionID,
	); errors.Is(err, sql.ErrNoRows) {
		return VersionResult{}, domainError("ARTIFACT_NOT_FOUND", "产物不存在。")
	} else if err != nil {
		return VersionResult{}, err
	}
	if artifact.CapabilityID != "agent_shell" ||
		(artifact.ArtifactType != "generic_document" && artifact.ArtifactType != "generic_table") {
		return VersionResult{}, domainError("GENERIC_ARTIFACT_TYPE_INVALID", "该产物不属于通用 Agent 产物。")
	}
	if err := authorizeArtifactCommandTx(ctx, tx, artifact.ProjectID, command.Scope); err != nil {
		return VersionResult{}, err
	}
	if command.Scope == "" {
		command.Scope = artifact.ProjectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return VersionResult{}, err
	}
	if hit {
		return decodeArtifactVersionReceiptTx(ctx, tx, cached, artifact, command)
	}

	var currentVersion int
	if err := tx.QueryRowContext(ctx, `
		SELECT version FROM artifact_versions
		WHERE artifact_version_id = ? AND artifact_id = ?`,
		artifact.CurrentVersionID, artifact.ArtifactID,
	).Scan(&currentVersion); err != nil {
		return VersionResult{}, err
	}
	if command.BaseVersionID != artifact.CurrentVersionID || command.BaseVersion != currentVersion {
		return VersionResult{}, domainError(
			"ARTIFACT_VERSION_CONFLICT",
			fmt.Sprintf("产物已更新，当前版本为 %d。", currentVersion),
		)
	}
	nextVersion, err := nextArtifactVersionTx(ctx, tx, artifact.ArtifactID)
	if err != nil {
		return VersionResult{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE artifact_versions SET status = 'superseded'
		WHERE artifact_version_id = ? AND artifact_id = ? AND status = 'confirmed'`,
		"产物当前版本已经变化。", artifact.CurrentVersionID, artifact.ArtifactID); err != nil {
		return VersionResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifact_versions(
			artifact_version_id, artifact_id, version, status, payload_json, schema_id,
			schema_version, created_by_kind, actor_ref, creation_reason,
			base_version_id, created_at, confirmed_at
		) VALUES(?, ?, ?, 'confirmed', ?, ?, '1.0.0', 'user', ?, 'manual_edit', ?, ?, ?)`,
		newVersionID, artifact.ArtifactID, nextVersion, string(command.NewPayload),
		artifact.ArtifactType, command.ActorRef, artifact.CurrentVersionID,
		formatTime(now), formatTime(now)); err != nil {
		return VersionResult{}, err
	}
	if err := s.copyArtifactDependenciesTx(
		ctx, tx, artifact.ProjectID, artifact.RunID,
		artifact.CurrentVersionID, newVersionID, now,
	); err != nil {
		return VersionResult{}, err
	}
	if err := updateExactlyOne(ctx, tx, `
		UPDATE artifacts SET current_version_id = ?, updated_at = ?
		WHERE artifact_id = ? AND current_version_id = ?`,
		"产物当前版本已经变化。", newVersionID, formatTime(now),
		artifact.ArtifactID, artifact.CurrentVersionID); err != nil {
		return VersionResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects
		SET version = version + 1, current_focus_artifact_version_id = ?, updated_at = ?
		WHERE project_id = ? AND deleted_at IS NULL`,
		newVersionID, formatTime(now), artifact.ProjectID); err != nil {
		return VersionResult{}, err
	}

	var runRef, stepRef *string
	if artifact.RunID != "" {
		runRef, stepRef = &artifact.RunID, &artifact.StepRunID
	}
	if _, err := s.appendEvent(ctx, tx, artifact.ProjectID, runRef, stepRef,
		"artifact.version_created", "artifact_version", newVersionID,
		map[string]any{"version": nextVersion}); err != nil {
		return VersionResult{}, err
	}
	if _, err := s.appendEvent(ctx, tx, artifact.ProjectID, runRef, stepRef,
		"artifact.confirmed", "artifact_version", newVersionID, nil); err != nil {
		return VersionResult{}, err
	}

	baseVersionID := artifact.CurrentVersionID
	confirmedAt := now
	result := VersionResult{ArtifactVersion: ArtifactVersion{
		ArtifactVersionID: newVersionID,
		ArtifactID:        artifact.ArtifactID,
		Version:           nextVersion,
		Status:            "confirmed",
		Payload:           slices.Clone(command.NewPayload),
		SchemaID:          artifact.ArtifactType,
		SchemaVersion:     "1.0.0",
		CreatedByKind:     "user",
		ActorRef:          command.ActorRef,
		CreationReason:    "manual_edit",
		BaseVersionID:     &baseVersionID,
		CreatedAt:         now,
		ConfirmedAt:       &confirmedAt,
	}}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, result, now); err != nil {
		return VersionResult{}, err
	}
	return result, nil
}
