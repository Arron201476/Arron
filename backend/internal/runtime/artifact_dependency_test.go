package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestArtifactVersionDependencyGraph(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, sourceAsset, snapshot := startNovelRunForLifecycle(t, store, "依赖图 A")
	sourceArtifact := snapshot.Artifacts[0]
	sourceVersion, err := store.GetArtifactVersion(ctx, sourceArtifact.CurrentVersionID)
	if err != nil {
		t.Fatalf("GetArtifactVersion() error = %v", err)
	}
	lineage, err := store.GetArtifactVersionLineage(ctx, sourceVersion.ArtifactVersionID, 1)
	if err != nil {
		t.Fatalf("GetArtifactVersionLineage(source) error = %v", err)
	}
	if len(lineage.Upstream) != 1 ||
		lineage.Upstream[0].UpstreamKind != "asset_snapshot" ||
		lineage.Upstream[0].UpstreamRefID != sourceAsset.Snapshot.AssetSnapshotID ||
		lineage.Upstream[0].Relation != "references_source" {
		t.Fatalf("source lineage = %+v", lineage)
	}

	edited, err := store.CreateArtifactVersion(ctx, CreateVersionCommand{
		CommandMeta: CommandMeta{
			Scope:          project.ProjectID,
			CommandType:    "create_artifact_version",
			IdempotencyKey: "b400c329-35e8-4d0a-93ea-dca308512f01",
			RequestHash:    "dependency_edit_v1",
		},
		ArtifactID:    sourceArtifact.ArtifactID,
		BaseVersionID: sourceVersion.ArtifactVersionID,
		BaseVersion:   sourceVersion.Version,
		NewPayload:    sourceVersion.Payload,
	})
	if err != nil {
		t.Fatalf("CreateArtifactVersion() error = %v", err)
	}
	editedLineage, err := store.GetArtifactVersionLineage(
		ctx,
		edited.ArtifactVersion.ArtifactVersionID,
		1,
	)
	if err != nil {
		t.Fatalf("GetArtifactVersionLineage(edited) error = %v", err)
	}
	if len(editedLineage.Upstream) != 1 ||
		editedLineage.Upstream[0].UpstreamRefID != sourceAsset.Snapshot.AssetSnapshotID {
		t.Fatalf("edited lineage = %+v", editedLineage)
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx() error = %v", err)
	}
	err = store.insertArtifactDependencyTx(ctx, tx, ArtifactDependency{
		ProjectID:                   project.ProjectID,
		RunID:                       snapshot.Run.RunID,
		DownstreamArtifactVersionID: edited.ArtifactVersion.ArtifactVersionID,
		UpstreamKind:                "artifact_version",
		UpstreamRefID:               sourceVersion.ArtifactVersionID,
		Relation:                    "derived_from",
		UpstreamScope:               dependencyScope("artifact", "singleton"),
		DownstreamScope:             dependencyScope("artifact", "singleton"),
		ImpactPolicyID:              "whole_artifact",
	}, time.Now().UTC())
	if err != nil {
		tx.Rollback()
		t.Fatalf("insert first graph edge error = %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit first graph edge error = %v", err)
	}

	tx, err = store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx(cycle) error = %v", err)
	}
	err = store.insertArtifactDependencyTx(ctx, tx, ArtifactDependency{
		ProjectID:                   project.ProjectID,
		RunID:                       snapshot.Run.RunID,
		DownstreamArtifactVersionID: sourceVersion.ArtifactVersionID,
		UpstreamKind:                "artifact_version",
		UpstreamRefID:               edited.ArtifactVersion.ArtifactVersionID,
		Relation:                    "derived_from",
		UpstreamScope:               dependencyScope("artifact", "singleton"),
		DownstreamScope:             dependencyScope("artifact", "singleton"),
		ImpactPolicyID:              "whole_artifact",
	}, time.Now().UTC())
	assertDomainCode(t, err, "DEPENDENCY_CYCLE_DETECTED")
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback(cycle) error = %v", err)
	}

	otherProject, _, otherSnapshot := startNovelRunForLifecycle(t, store, "依赖图 B")
	tx, err = store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx(cross project) error = %v", err)
	}
	err = store.insertArtifactDependencyTx(ctx, tx, ArtifactDependency{
		ProjectID:                   project.ProjectID,
		RunID:                       snapshot.Run.RunID,
		DownstreamArtifactVersionID: sourceVersion.ArtifactVersionID,
		UpstreamKind:                "artifact_version",
		UpstreamRefID:               otherSnapshot.Artifacts[0].CurrentVersionID,
		Relation:                    "derived_from",
		UpstreamScope:               dependencyScope("artifact", "singleton"),
		DownstreamScope:             dependencyScope("artifact", "singleton"),
		ImpactPolicyID:              "whole_artifact",
	}, time.Now().UTC())
	assertDomainCode(t, err, "DEPENDENCY_PROJECT_MISMATCH")
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback(cross project) error = %v", err)
	}
	if otherProject.ProjectID == project.ProjectID {
		t.Fatal("test setup did not create separate projects")
	}
}

func TestArtifactDependencyAcceptsRunSnapshots(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, _, snapshot := startNovelRunForLifecycle(t, store, "Snapshot dependencies")
	if snapshot.Run.CurrentStepRunID == nil {
		t.Fatal("run has no current step")
	}
	downstreamVersionID := snapshot.Artifacts[0].CurrentVersionID

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx() error = %v", err)
	}
	defer tx.Rollback()

	var configSnapshotID string
	if err := tx.QueryRowContext(ctx, `
		SELECT config_snapshot_id
		FROM run_config_snapshots
		WHERE run_id = ? AND config_ref = 'creation'`,
		snapshot.Run.RunID,
	).Scan(&configSnapshotID); err != nil {
		t.Fatalf("query config snapshot error = %v", err)
	}
	if err := store.insertArtifactDependencyTx(ctx, tx, ArtifactDependency{
		ProjectID:                   project.ProjectID,
		RunID:                       snapshot.Run.RunID,
		DownstreamArtifactVersionID: downstreamVersionID,
		UpstreamKind:                "config_snapshot",
		UpstreamRefID:               configSnapshotID,
		Relation:                    "configured_by",
		UpstreamScope:               dependencyScope("config", "creation"),
		DownstreamScope:             dependencyScope("artifact", "singleton"),
		ImpactPolicyID:              "whole_downstream",
	}, time.Now().UTC()); err != nil {
		t.Fatalf("insert config snapshot dependency error = %v", err)
	}

	decision, err := store.createRunDecisionSnapshotTx(
		ctx,
		tx,
		project.ProjectID,
		snapshot.Run.RunID,
		*snapshot.Run.CurrentStepRunID,
		"test_decision",
		"approval",
		"approval_test",
		[]byte(`{"confirmed":true}`),
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("create decision snapshot error = %v", err)
	}
	if err := store.insertArtifactDependencyTx(ctx, tx, ArtifactDependency{
		ProjectID:                   project.ProjectID,
		RunID:                       snapshot.Run.RunID,
		DownstreamArtifactVersionID: downstreamVersionID,
		UpstreamKind:                "decision_snapshot",
		UpstreamRefID:               decision.DecisionSnapshotID,
		Relation:                    "decided_by",
		UpstreamScope:               dependencyScope("decision", decision.DecisionType),
		DownstreamScope:             dependencyScope("artifact", "singleton"),
		ImpactPolicyID:              "whole_downstream",
	}, time.Now().UTC()); err != nil {
		t.Fatalf("insert decision snapshot dependency error = %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	lineage, err := store.GetArtifactVersionLineage(ctx, downstreamVersionID, 1)
	if err != nil {
		t.Fatalf("GetArtifactVersionLineage() error = %v", err)
	}
	kinds := map[string]bool{}
	for _, edge := range lineage.Upstream {
		kinds[edge.UpstreamKind] = true
	}
	if !kinds["config_snapshot"] || !kinds["decision_snapshot"] {
		t.Fatalf("snapshot dependencies missing from lineage: %+v", lineage.Upstream)
	}
}
