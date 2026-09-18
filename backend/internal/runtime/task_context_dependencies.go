package runtime

import (
	"context"
	"database/sql"
	"time"
)

func (s *Store) insertTaskContextDependenciesTx(
	ctx context.Context, tx *sql.Tx, projectID, runID, versionID, scopeKey string,
	pack StepExecutionContextPack, includeAssets bool, now time.Time,
) error {
	downstream := dependencyScope(scopeKind(scopeKey), scopeKey)
	if includeAssets {
		for _, asset := range pack.AssetContext {
			if err := s.insertArtifactDependencyTx(ctx, tx, ArtifactDependency{
				ProjectID: projectID, RunID: runID, DownstreamArtifactVersionID: versionID,
				UpstreamKind: "asset_snapshot", UpstreamRefID: asset.AssetSnapshotID,
				Relation: "derived_from", UpstreamScope: dependencyScope("asset", "asset:"+asset.AssetID),
				DownstreamScope: downstream, ImpactPolicyID: "asset_exact",
			}, now); err != nil {
				return err
			}
		}
	}
	if config := pack.ConfigSnapshotRef; config != nil {
		if err := s.insertArtifactDependencyTx(ctx, tx, ArtifactDependency{
			ProjectID: projectID, RunID: runID, DownstreamArtifactVersionID: versionID,
			UpstreamKind: "config_snapshot", UpstreamRefID: config.ConfigSnapshotID,
			Relation: "configured_by", UpstreamScope: dependencyScope("config", config.ConfigRef),
			DownstreamScope: downstream, ImpactPolicyID: "whole_downstream",
		}, now); err != nil {
			return err
		}
	}
	for _, decision := range pack.DecisionSnapshots {
		if err := s.insertArtifactDependencyTx(ctx, tx, ArtifactDependency{
			ProjectID: projectID, RunID: runID, DownstreamArtifactVersionID: versionID,
			UpstreamKind: "decision_snapshot", UpstreamRefID: decision.DecisionSnapshotID,
			Relation: "decided_by", UpstreamScope: dependencyScope("decision", decision.DecisionType),
			DownstreamScope: downstream, ImpactPolicyID: "whole_downstream",
		}, now); err != nil {
			return err
		}
	}
	return nil
}
