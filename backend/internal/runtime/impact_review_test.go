package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestImpactReviewLifecycle(t *testing.T) {
	t.Run("invalid change set rolls back version transaction", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer store.Close()

		_, _, storyArtifact, storyVersion := prepareStoryBibleForImpact(t, store, "变更集回滚")
		_, err = store.CreateArtifactVersion(ctx, CreateVersionCommand{
			ArtifactID:    storyArtifact.ArtifactID,
			BaseVersionID: storyVersion.ArtifactVersionID,
			BaseVersion:   storyVersion.Version,
			ChangeMode:    "scoped",
			NewPayload:    storyVersion.Payload,
		})
		assertDomainCode(t, err, "CHANGE_SET_INVALID")
		artifactAfter, err := store.GetArtifact(ctx, storyArtifact.ArtifactID)
		if err != nil {
			t.Fatalf("GetArtifact() error = %v", err)
		}
		versions, err := store.ListArtifactVersions(ctx, storyArtifact.ArtifactID)
		if err != nil {
			t.Fatalf("ListArtifactVersions() error = %v", err)
		}
		currentVersion, err := store.GetArtifactVersion(ctx, storyVersion.ArtifactVersionID)
		if err != nil {
			t.Fatalf("GetArtifactVersion() error = %v", err)
		}
		if artifactAfter.CurrentVersionID != storyVersion.ArtifactVersionID ||
			len(versions) != 1 ||
			currentVersion.Status != "pending_approval" {
			t.Fatalf(
				"rollback artifact = %+v, versions = %+v, current = %+v",
				artifactAfter,
				versions,
				currentVersion,
			)
		}
		var changeSetCount, reviewCount int
		if err := store.db.QueryRowContext(ctx, `
			SELECT
				(SELECT COUNT(*) FROM artifact_version_change_sets),
				(SELECT COUNT(*) FROM impact_reviews)`,
		).Scan(&changeSetCount, &reviewCount); err != nil {
			t.Fatalf("count rolled back records: %v", err)
		}
		if changeSetCount != 0 || reviewCount != 0 {
			t.Fatalf("rolled back change sets = %d, reviews = %d", changeSetCount, reviewCount)
		}
	})

	t.Run("edit without downstream records change set only", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer store.Close()

		_, _, storyArtifact, storyVersion := prepareStoryBibleForImpact(t, store, "无下游影响")
		result, err := store.CreateArtifactVersion(ctx, CreateVersionCommand{
			ArtifactID:    storyArtifact.ArtifactID,
			BaseVersionID: storyVersion.ArtifactVersionID,
			BaseVersion:   storyVersion.Version,
			ChangeMode:    "whole_artifact",
			NewPayload:    storyVersion.Payload,
		})
		if err != nil {
			t.Fatalf("CreateArtifactVersion() error = %v", err)
		}
		if result.ImpactReview != nil {
			t.Fatalf("impact review without downstream = %+v", result.ImpactReview)
		}
		var changeSetCount, reviewCount int
		if err := store.db.QueryRowContext(ctx, `
			SELECT
				(SELECT COUNT(*) FROM artifact_version_change_sets
					WHERE artifact_version_id = ?),
				(SELECT COUNT(*) FROM impact_reviews
					WHERE new_version_id = ?)`,
			result.ArtifactVersion.ArtifactVersionID,
			result.ArtifactVersion.ArtifactVersionID,
		).Scan(&changeSetCount, &reviewCount); err != nil {
			t.Fatalf("count change set/review: %v", err)
		}
		if changeSetCount != 1 || reviewCount != 0 {
			t.Fatalf("change sets = %d, reviews = %d", changeSetCount, reviewCount)
		}
	})

	t.Run("multi level preview remains non destructive and reedit recomputes ancestors", func(t *testing.T) {
		ctx := context.Background()
		store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		defer store.Close()

		project, initial, _, storyVersion := prepareStoryBibleForImpact(
			t,
			store,
			"多层影响",
		)
		episodeVersionID := addConfirmedImpactDownstream(
			t,
			store,
			project.ProjectID,
			initial.Run.RunID,
			storyVersion.ArtifactVersionID,
		)
		sourceArtifact := initial.Artifacts[0]
		sourceVersion, err := store.GetArtifactVersion(ctx, sourceArtifact.CurrentVersionID)
		if err != nil {
			t.Fatalf("GetArtifactVersion(source) error = %v", err)
		}
		first, err := store.CreateArtifactVersion(ctx, CreateVersionCommand{
			ArtifactID:    sourceArtifact.ArtifactID,
			BaseVersionID: sourceVersion.ArtifactVersionID,
			BaseVersion:   sourceVersion.Version,
			ChangeMode:    "whole_artifact",
			NewPayload:    sourceVersion.Payload,
		})
		if err != nil {
			t.Fatalf("CreateArtifactVersion(first) error = %v", err)
		}
		assertTwoLevelImpactReview(
			t,
			first.ImpactReview,
			storyVersion.ArtifactVersionID,
			episodeVersionID,
		)
		storedFirst, err := store.GetImpactReview(ctx, first.ImpactReview.ImpactReviewID)
		if err != nil {
			t.Fatalf("GetImpactReview(first) error = %v", err)
		}
		if storedFirst.SnapshotHash != first.ImpactReview.SnapshotHash ||
			storedFirst.Status != "pending" {
			t.Fatalf("stored first review = %+v", storedFirst)
		}
		storyAfter, err := store.GetArtifactVersion(ctx, storyVersion.ArtifactVersionID)
		if err != nil || storyAfter.Status != "pending_approval" {
			t.Fatalf("story status after preview = %+v, error = %v", storyAfter, err)
		}
		episodeAfter, err := store.GetArtifactVersion(ctx, episodeVersionID)
		if err != nil || episodeAfter.Status != "confirmed" {
			t.Fatalf("episode status after preview = %+v, error = %v", episodeAfter, err)
		}
		_, err = store.ResolveApproval(ctx, ResolveApprovalCommand{
			ApprovalRequestID:       first.Approval.ApprovalRequestID,
			Action:                  "approve",
			ExpectedApprovalVersion: first.Approval.Version,
			SubjectSnapshotHash:     first.Approval.SubjectSnapshotHash,
		})
		assertDomainCode(t, err, "IMPACT_REVIEW_REQUIRED")

		second, err := store.CreateArtifactVersion(ctx, CreateVersionCommand{
			ArtifactID:    sourceArtifact.ArtifactID,
			BaseVersionID: first.ArtifactVersion.ArtifactVersionID,
			BaseVersion:   first.ArtifactVersion.Version,
			ChangeMode:    "whole_artifact",
			NewPayload:    first.ArtifactVersion.Payload,
		})
		if err != nil {
			t.Fatalf("CreateArtifactVersion(second) error = %v", err)
		}
		assertTwoLevelImpactReview(
			t,
			second.ImpactReview,
			storyVersion.ArtifactVersionID,
			episodeVersionID,
		)
		expiredFirst, err := store.GetImpactReview(ctx, first.ImpactReview.ImpactReviewID)
		if err != nil || expiredFirst.Status != "expired" ||
			expiredFirst.ResolvedAt == nil {
			t.Fatalf("expired first review = %+v, error = %v", expiredFirst, err)
		}
		if second.ImpactReview.Status != "pending" ||
			second.ImpactReview.OldVersionID != first.ArtifactVersion.ArtifactVersionID {
			t.Fatalf("second review = %+v", second.ImpactReview)
		}
		cancelled, err := store.CancelRun(ctx, CancelRunCommand{
			RunID:     initial.Run.RunID,
			Confirmed: true,
		})
		if err != nil || cancelled.Run.Status != "cancelled" {
			t.Fatalf("CancelRun() = %+v, error = %v", cancelled, err)
		}
		expiredSecond, err := store.GetImpactReview(ctx, second.ImpactReview.ImpactReviewID)
		if err != nil || expiredSecond.Status != "expired" ||
			expiredSecond.ResolvedAt == nil {
			t.Fatalf("impact review after cancel = %+v, error = %v", expiredSecond, err)
		}
	})
}

func prepareStoryBibleForImpact(
	t *testing.T,
	store *Store,
	title string,
) (Project, RunSnapshot, Artifact, ArtifactVersion) {
	return prepareStoryBibleForImpactWithContent(t, store, title, "第一章 少年下山。")
}

func prepareStoryBibleForImpactWithContent(
	t *testing.T,
	store *Store,
	title string,
	content string,
) (Project, RunSnapshot, Artifact, ArtifactVersion) {
	t.Helper()
	ctx := context.Background()
	project, _, initial := startNovelRunWithContent(t, store, title, content)
	_ = approveAndResumeLifecycleRun(t, store, project, initial)
	claim := claimStructuredTask(t, store)
	received, err := store.SubmitExecutionResult(ctx, SubmitExecutionResultCommand{
		AttemptID:         claim.Attempt.AttemptID,
		AttemptToken:      claim.AttemptToken,
		InputSnapshotHash: claim.Attempt.InputSnapshotHash,
		ResponsePayload:   validStoryBibleProviderResponseForClaim(t, claim),
	})
	if err != nil || received.ResponseHash == nil {
		t.Fatalf("SubmitExecutionResult() = %+v, error = %v", received, err)
	}
	committed, err := store.CommitExecutionResult(ctx, CommitExecutionResultCommand{
		AttemptID:            claim.Attempt.AttemptID,
		ExpectedResponseHash: *received.ResponseHash,
	})
	if err != nil {
		t.Fatalf("CommitExecutionResult() error = %v", err)
	}
	return project, initial, committed.Artifact, committed.ArtifactVersion
}

func addConfirmedImpactDownstream(
	t *testing.T,
	store *Store,
	projectID string,
	runID string,
	upstreamVersionID string,
) string {
	t.Helper()
	ctx := context.Background()
	now := store.now()
	stepRunID := store.newID("step")
	artifactID := store.newID("art")
	versionID := store.newID("av")
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx() error = %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO step_runs(
			step_run_id, run_id, step_id, status, attempt_count,
			approval_policy, input_version_snapshot_json, task_cursor_json,
			started_at, ended_at
		) VALUES(?, ?, 'split_episodes', 'completed', 1, 'checkpoint',
			'[]', '{}', ?, ?)`,
		stepRunID,
		runID,
		formatTime(now),
		formatTime(now),
	); err != nil {
		t.Fatalf("insert downstream step: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifacts(
			artifact_id, project_id, run_id, step_run_id, capability_id,
			artifact_type, scope_key, current_version_id, created_at, updated_at
		) VALUES(?, ?, ?, ?, 'novel_to_script', 'episode_split',
			'singleton', ?, ?, ?)`,
		artifactID,
		projectID,
		runID,
		stepRunID,
		versionID,
		formatTime(now),
		formatTime(now),
	); err != nil {
		t.Fatalf("insert downstream artifact: %v", err)
	}
	payload, _ := json.Marshal(map[string]any{"episodes": []any{}})
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifact_versions(
			artifact_version_id, artifact_id, version, status, payload_json,
			schema_id, schema_version, created_by_kind, actor_ref,
			creation_reason, created_at, confirmed_at
		) VALUES(?, ?, 1, 'confirmed', ?, 'episode_split', '1.0.0',
			'model', 'impact_test', 'initial', ?, ?)`,
		versionID,
		artifactID,
		string(payload),
		formatTime(now),
		formatTime(now),
	); err != nil {
		t.Fatalf("insert downstream version: %v", err)
	}
	if err := store.insertArtifactDependencyTx(ctx, tx, ArtifactDependency{
		ProjectID:                   projectID,
		RunID:                       runID,
		DownstreamArtifactVersionID: versionID,
		UpstreamKind:                "artifact_version",
		UpstreamRefID:               upstreamVersionID,
		Relation:                    "derived_from",
		UpstreamScope:               dependencyScope("artifact", "singleton"),
		DownstreamScope:             dependencyScope("artifact", "singleton"),
		ImpactPolicyID:              "whole_downstream",
	}, now); err != nil {
		t.Fatalf("insert downstream dependency: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit downstream fixture: %v", err)
	}
	return versionID
}

func assertTwoLevelImpactReview(
	t *testing.T,
	review *ImpactReview,
	storyVersionID string,
	episodeVersionID string,
) {
	t.Helper()
	if review == nil || review.Status != "pending" ||
		len(review.AffectedItems) != 2 ||
		review.SnapshotHash == "" {
		t.Fatalf("impact review = %+v", review)
	}
	if review.AffectedItems[0].ArtifactVersionID != storyVersionID ||
		review.AffectedItems[0].RegenerateFromStepID != "build_story_bible" ||
		len(review.AffectedItems[0].ImpactPath) < 2 {
		t.Fatalf("first affected item = %+v", review.AffectedItems[0])
	}
	if review.AffectedItems[1].ArtifactVersionID != episodeVersionID ||
		review.AffectedItems[1].RegenerateFromStepID != "split_episodes" ||
		len(review.AffectedItems[1].ImpactPath) < 3 {
		t.Fatalf("second affected item = %+v", review.AffectedItems[1])
	}
	if len(review.RecommendedRegenerationStart) != 1 ||
		review.RecommendedRegenerationStart[0] != "build_story_bible" {
		t.Fatalf("recommended starts = %+v", review.RecommendedRegenerationStart)
	}
}
