package runtime

import (
	"context"
	"path/filepath"
	"testing"
)

func TestApplyArtifactVersionPropagatesLatestUpstream(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, initial, _, storyVersion := prepareStoryBibleForImpact(t, store, "最新上游传播")
	downstreamVersionID := addConfirmedImpactDownstream(
		t, store, project.ProjectID, initial.Run.RunID, storyVersion.ArtifactVersionID,
	)
	sourceArtifact := initial.Artifacts[0]
	sourceVersion, err := store.GetArtifactVersion(ctx, sourceArtifact.CurrentVersionID)
	if err != nil {
		t.Fatalf("GetArtifactVersion(source) error = %v", err)
	}

	result, err := store.ApplyArtifactVersion(ctx, CreateVersionCommand{
		CommandMeta: CommandMeta{
			Scope:          project.ProjectID,
			CommandType:    "apply_artifact_version",
			IdempotencyKey: "8c6fb515-c322-4fd1-9ca0-06eb42a6222e",
			RequestHash:    "latest-upstream-v1",
		},
		ArtifactID:    sourceArtifact.ArtifactID,
		BaseVersionID: sourceVersion.ArtifactVersionID,
		BaseVersion:   sourceVersion.Version,
		ChangeMode:    "whole_artifact",
		NewPayload:    sourceVersion.Payload,
		ActorRef:      "test-user",
	})
	if err != nil {
		t.Fatalf("ApplyArtifactVersion() error = %v", err)
	}
	if result.Propagation == nil || result.Propagation.RegenerationPlan == nil ||
		result.ImpactReview == nil || result.ImpactReview.Status != "regeneration_planned" ||
		result.ArtifactVersion.Status != "confirmed" || result.Approval.Status != "approved" {
		t.Fatalf("applied result = %+v", result)
	}
	stale, err := store.GetArtifactVersion(ctx, downstreamVersionID)
	if err != nil || stale.Status != "stale" {
		t.Fatalf("downstream version = %+v, error = %v", stale, err)
	}
	listed, err := store.ListArtifactsByRun(ctx, initial.Run.RunID)
	if err != nil {
		t.Fatalf("ListArtifactsByRun() error = %v", err)
	}
	listedStale := false
	for _, artifact := range listed {
		if artifact.CurrentVersionID == downstreamVersionID && artifact.Status == "stale" {
			listedStale = true
		}
	}
	if !listedStale {
		t.Fatalf("stale current-version status missing from artifact list: %+v", listed)
	}
	if result.Propagation.RunSnapshot.Run.Status != "paused" ||
		result.Propagation.RunSnapshot.Run.CurrentStepRunID == nil {
		t.Fatalf("propagated run = %+v", result.Propagation.RunSnapshot.Run)
	}
}

func TestApplyArtifactVersionReopensCompletedRun(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, initial, _, storyVersion := prepareStoryBibleForImpact(t, store, "完成后修改")
	addConfirmedImpactDownstream(
		t, store, project.ProjectID, initial.Run.RunID, storyVersion.ArtifactVersionID,
	)
	now := formatTime(store.now())
	if _, err := store.db.ExecContext(ctx, `
		UPDATE runs SET status = 'completed', ended_at = ?, updated_at = ? WHERE run_id = ?`,
		now, now, initial.Run.RunID,
	); err != nil {
		t.Fatalf("complete run: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		UPDATE projects SET status = 'ready', active_write_run_id = NULL,
			current_capability_id = NULL, updated_at = ? WHERE project_id = ?`,
		now, project.ProjectID,
	); err != nil {
		t.Fatalf("release project write run: %v", err)
	}
	sourceArtifact := initial.Artifacts[0]
	sourceVersion, err := store.GetArtifactVersion(ctx, sourceArtifact.CurrentVersionID)
	if err != nil {
		t.Fatalf("GetArtifactVersion(source) error = %v", err)
	}

	result, err := store.ApplyArtifactVersion(ctx, CreateVersionCommand{
		CommandMeta: CommandMeta{
			Scope:          project.ProjectID,
			CommandType:    "apply_artifact_version",
			IdempotencyKey: "4d571b89-dc56-482d-a1cf-77cdfce7ae29",
			RequestHash:    "reopen-completed-v1",
		},
		ArtifactID:    sourceArtifact.ArtifactID,
		BaseVersionID: sourceVersion.ArtifactVersionID,
		BaseVersion:   sourceVersion.Version,
		ChangeMode:    "whole_artifact",
		NewPayload:    sourceVersion.Payload,
		ActorRef:      "test-user",
	})
	if err != nil {
		t.Fatalf("ApplyArtifactVersion(completed) error = %v", err)
	}
	if result.Propagation == nil || result.Propagation.RunSnapshot.Run.Status != "paused" ||
		result.Propagation.RunSnapshot.Run.EndedAt != nil {
		t.Fatalf("reopened run = %+v", result.Propagation)
	}
	updatedProject, err := store.GetProject(ctx, project.ProjectID)
	if err != nil || updatedProject.ActiveWriteRunID == nil ||
		*updatedProject.ActiveWriteRunID != initial.Run.RunID {
		t.Fatalf("project write run = %+v, error = %v", updatedProject, err)
	}
}
