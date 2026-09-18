package runtime

import (
	"context"
	"database/sql"
)

// ApplyArtifactVersion is the user-facing mutation boundary. A saved upstream
// version becomes authoritative immediately; any existing downstream derived
// from the replaced version is invalidated and scheduled for regeneration.
// CreateArtifactVersion remains the low-level primitive used by Runtime internals.
func (s *Store) ApplyArtifactVersion(
	ctx context.Context,
	command CreateVersionCommand,
) (VersionResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return VersionResult{}, err
	}
	defer tx.Rollback()
	result, err := s.applyArtifactVersionTx(ctx, tx, command)
	if err != nil {
		return VersionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return VersionResult{}, err
	}
	return result, nil
}

func (s *Store) applyArtifactVersionTx(ctx context.Context, tx *sql.Tx, command CreateVersionCommand) (VersionResult, error) {
	artifact, err := getArtifactQuery(ctx, tx, command.ArtifactID)
	if err != nil {
		return VersionResult{}, err
	}
	if artifact.CapabilityID == "agent_shell" && (artifact.ArtifactType == "generic_document" || artifact.ArtifactType == "generic_table") {
		return s.createConfirmedGenericArtifactVersionTx(ctx, tx, command)
	}
	result, err := s.createArtifactVersionTx(ctx, tx, command)
	if err != nil {
		return VersionResult{}, err
	}
	if result.ImpactReview == nil || result.HandoffRefreshRequired {
		return result, nil
	}

	meta := command.CommandMeta
	meta.CommandType += ":propagate_latest_upstream"
	if meta.IdempotencyKey != "" {
		meta.IdempotencyKey += ":propagate_latest_upstream"
	}
	meta.RequestHash += ":regenerate_downstream"
	propagation, err := s.resolveImpactReviewTx(ctx, tx, ResolveImpactReviewCommand{
		CommandMeta:    meta,
		ImpactReviewID: result.ImpactReview.ImpactReviewID,
		Action:         "regenerate_downstream",
		SnapshotHash:   result.ImpactReview.SnapshotHash,
		ActorRef:       command.ActorRef,
	})
	if err != nil {
		return VersionResult{}, err
	}

	result.Propagation = &propagation
	result.ImpactReview = &propagation.ImpactReview
	result.ArtifactVersion, err = getArtifactVersionQuery(
		ctx,
		tx,
		result.ArtifactVersion.ArtifactVersionID,
	)
	if err != nil {
		return VersionResult{}, err
	}
	result.Approval, err = getApprovalTx(ctx, tx, result.Approval.ApprovalRequestID)
	if err != nil {
		return VersionResult{}, err
	}
	return result, nil
}
