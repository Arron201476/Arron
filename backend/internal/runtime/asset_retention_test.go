package runtime

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVideoRetentionExpiresBlobAndPreservesTombstone(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	clock := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }

	project, err := store.CreateProject(ctx, "Video Retention")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	source := createVideoAsset(t, store, project.ProjectID, "episode-01.mp4")
	jobs, err := store.ListRetentionJobs(ctx, source.Asset.AssetID)
	if err != nil {
		t.Fatalf("ListRetentionJobs() error = %v", err)
	}
	if len(jobs) != 1 ||
		jobs[0].Action != "expire_video_source" ||
		jobs[0].Status != "scheduled" ||
		!jobs[0].DueAt.Equal(clock.Add(7*24*time.Hour)) {
		t.Fatalf("scheduled jobs = %+v", jobs)
	}

	content, err := store.OpenAssetContent(ctx, source.Asset.AssetID)
	if err != nil {
		t.Fatalf("OpenAssetContent() error = %v", err)
	}
	received, err := io.ReadAll(content.File)
	content.File.Close()
	if err != nil || !bytes.Equal(received, testMP4Bytes()) {
		t.Fatalf("asset content = %x, error = %v", received, err)
	}

	clock = clock.Add(8 * 24 * time.Hour)
	content, err = store.OpenAssetContent(ctx, source.Asset.AssetID)
	if content.File != nil {
		content.File.Close()
	}
	assertDomainCode(t, err, "ASSET_SOURCE_EXPIRED")

	sweep, err := store.RunRetentionSweep(ctx, "retention-test", 10, 60, 3)
	if err != nil {
		t.Fatalf("RunRetentionSweep() error = %v", err)
	}
	if sweep.Claimed != 1 || sweep.Completed != 1 || sweep.Retried != 0 || sweep.Failed != 0 {
		t.Fatalf("retention sweep = %+v", sweep)
	}
	asset, err := store.GetAsset(ctx, source.Asset.AssetID)
	if err != nil ||
		asset.Status != "expired" ||
		asset.DeletedAt == nil ||
		asset.DeleteReason == nil ||
		*asset.DeleteReason != "retention_expired" {
		t.Fatalf("expired asset = %+v, error = %v", asset, err)
	}
	snapshot, err := store.GetAssetSnapshot(ctx, source.Snapshot.AssetSnapshotID)
	if err != nil || snapshot.Status != "expired" {
		t.Fatalf("expired snapshot = %+v, error = %v", snapshot, err)
	}
	var blobStatus, storageRef string
	if err := store.db.QueryRowContext(ctx, `
		SELECT status, storage_ref FROM asset_blobs WHERE blob_id = ?`,
		source.Asset.OriginalBlobID,
	).Scan(&blobStatus, &storageRef); err != nil {
		t.Fatalf("load expired blob: %v", err)
	}
	if blobStatus != "deleted" {
		t.Fatalf("blob status = %q, want deleted", blobStatus)
	}
	blobPath, err := resolveDataPath(store.dataRoot, storageRef)
	if err != nil {
		t.Fatalf("resolveDataPath() error = %v", err)
	}
	if _, err := os.Stat(blobPath); !os.IsNotExist(err) {
		t.Fatalf("expired blob still exists or stat failed unexpectedly: %v", err)
	}
	jobs, err = store.ListRetentionJobs(ctx, source.Asset.AssetID)
	if err != nil || jobs[0].Status != "completed" || jobs[0].AttemptCount != 1 {
		t.Fatalf("completed jobs = %+v, error = %v", jobs, err)
	}
	repeated, err := store.RunRetentionSweep(ctx, "retention-test", 10, 60, 3)
	if err != nil || repeated.Claimed != 0 {
		t.Fatalf("repeated retention sweep = %+v, error = %v", repeated, err)
	}
}

func TestAssetDeleteRequiresSafeRunAndPreservesArtifacts(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, source, run := startNovelRunForLifecycle(t, store, "Delete Guard")

	var artifactCountBefore, dependencyCountBefore int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM artifacts WHERE project_id = ?`,
		project.ProjectID,
	).Scan(&artifactCountBefore); err != nil {
		t.Fatalf("count artifacts before delete: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM artifact_dependencies
		WHERE project_id = ? AND upstream_kind = 'asset_snapshot'
			AND upstream_ref_id = ?`,
		project.ProjectID,
		source.Snapshot.AssetSnapshotID,
	).Scan(&dependencyCountBefore); err != nil {
		t.Fatalf("count dependencies before delete: %v", err)
	}
	if artifactCountBefore == 0 || dependencyCountBefore == 0 {
		t.Fatalf(
			"delete fixture missing artifacts=%d dependencies=%d",
			artifactCountBefore,
			dependencyCountBefore,
		)
	}

	blockedPreview, err := store.CreateAssetDeletePreview(
		ctx,
		CreateAssetDeletePreviewCommand{AssetID: source.Asset.AssetID},
	)
	if err != nil {
		t.Fatalf("CreateAssetDeletePreview(blocked) error = %v", err)
	}
	if len(blockedPreview.ActiveRunImpacts) != 1 ||
		!blockedPreview.ActiveRunImpacts[0].BlocksDelete ||
		len(blockedPreview.ArtifactImpacts) == 0 ||
		!blockedPreview.ExistingArtifactsPreserved {
		t.Fatalf("blocked preview = %+v", blockedPreview)
	}
	_, err = store.ConfirmAssetDelete(ctx, ConfirmAssetDeleteCommand{
		AssetID:     source.Asset.AssetID,
		PreviewHash: blockedPreview.SnapshotHash,
		Confirmed:   true,
	})
	assertDomainCode(t, err, "ASSET_DELETE_BLOCKED_BY_RUN")

	paused, err := store.RequestRunPause(ctx, PauseRunCommand{RunID: run.Run.RunID})
	if err != nil || paused.Run.Status != "paused" {
		t.Fatalf("RequestRunPause() = %+v, error = %v", paused.Run, err)
	}
	previewCommand := CreateAssetDeletePreviewCommand{
		CommandMeta: CommandMeta{
			Scope:          project.ProjectID,
			CommandType:    "create_asset_delete_preview",
			IdempotencyKey: "delete-preview-safe",
			RequestHash:    "delete-preview-safe-hash",
		},
		AssetID: source.Asset.AssetID,
	}
	safePreview, err := store.CreateAssetDeletePreview(ctx, previewCommand)
	if err != nil {
		t.Fatalf("CreateAssetDeletePreview(safe) error = %v", err)
	}
	if safePreview.SnapshotHash == blockedPreview.SnapshotHash ||
		len(safePreview.ActiveRunImpacts) != 1 ||
		safePreview.ActiveRunImpacts[0].BlocksDelete {
		t.Fatalf("safe preview = %+v", safePreview)
	}
	confirmCommand := ConfirmAssetDeleteCommand{
		CommandMeta: CommandMeta{
			Scope:          project.ProjectID,
			CommandType:    "confirm_asset_delete",
			IdempotencyKey: "delete-confirm-safe",
			RequestHash:    "delete-confirm-safe-hash",
		},
		AssetID:     source.Asset.AssetID,
		PreviewHash: safePreview.SnapshotHash,
		Confirmed:   true,
	}
	deleted, err := store.ConfirmAssetDelete(ctx, confirmCommand)
	if err != nil {
		t.Fatalf("ConfirmAssetDelete() error = %v", err)
	}
	if deleted.Asset.Status != "deleted" ||
		deleted.Job.Action != userDeleteAction ||
		deleted.Job.Status != "scheduled" {
		t.Fatalf("delete result = %+v", deleted)
	}
	repeated, err := store.ConfirmAssetDelete(ctx, confirmCommand)
	if err != nil ||
		repeated.Job.RetentionJobID != deleted.Job.RetentionJobID ||
		repeated.Asset.Status != "deleted" {
		t.Fatalf("repeated delete result = %+v, error = %v", repeated, err)
	}
	_, err = store.OpenAssetContent(ctx, source.Asset.AssetID)
	assertDomainCode(t, err, "ASSET_SOURCE_DELETED")

	sweep, err := store.RunRetentionSweep(ctx, "delete-test", 10, 60, 3)
	if err != nil || sweep.Completed != 1 {
		t.Fatalf("delete sweep = %+v, error = %v", sweep, err)
	}
	var artifactCountAfter, dependencyCountAfter int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM artifacts WHERE project_id = ?`,
		project.ProjectID,
	).Scan(&artifactCountAfter); err != nil {
		t.Fatalf("count artifacts after delete: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM artifact_dependencies
		WHERE project_id = ? AND upstream_kind = 'asset_snapshot'
			AND upstream_ref_id = ?`,
		project.ProjectID,
		source.Snapshot.AssetSnapshotID,
	).Scan(&dependencyCountAfter); err != nil {
		t.Fatalf("count dependencies after delete: %v", err)
	}
	if artifactCountAfter != artifactCountBefore || dependencyCountAfter != dependencyCountBefore {
		t.Fatalf(
			"delete changed formal records: artifacts %d->%d dependencies %d->%d",
			artifactCountBefore,
			artifactCountAfter,
			dependencyCountBefore,
			dependencyCountAfter,
		)
	}
}

func TestRetentionDeletionFailureRetriesAfterLease(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	clock := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }
	project, err := store.CreateProject(ctx, "Delete Retry")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	source := createTextAsset(t, store, project.ProjectID, "source.txt", "待删除文本")
	preview, err := store.CreateAssetDeletePreview(
		ctx,
		CreateAssetDeletePreviewCommand{AssetID: source.Asset.AssetID},
	)
	if err != nil {
		t.Fatalf("CreateAssetDeletePreview() error = %v", err)
	}
	deleted, err := store.ConfirmAssetDelete(ctx, ConfirmAssetDeleteCommand{
		AssetID:     source.Asset.AssetID,
		PreviewHash: preview.SnapshotHash,
		Confirmed:   true,
	})
	if err != nil {
		t.Fatalf("ConfirmAssetDelete() error = %v", err)
	}

	var storageRef string
	if err := store.db.QueryRowContext(ctx, `
		SELECT storage_ref FROM asset_blobs WHERE blob_id = ?`,
		source.Asset.OriginalBlobID,
	).Scan(&storageRef); err != nil {
		t.Fatalf("load blob storage ref: %v", err)
	}
	blobPath, err := resolveDataPath(store.dataRoot, storageRef)
	if err != nil {
		t.Fatalf("resolveDataPath() error = %v", err)
	}
	if err := os.Remove(blobPath); err != nil {
		t.Fatalf("remove fixture blob: %v", err)
	}
	if err := os.Mkdir(blobPath, 0o755); err != nil {
		t.Fatalf("replace fixture blob with directory: %v", err)
	}
	blockerPath := filepath.Join(blobPath, "blocker")
	if err := os.WriteFile(blockerPath, []byte("block"), 0o600); err != nil {
		t.Fatalf("write delete blocker: %v", err)
	}

	firstSweep, err := store.RunRetentionSweep(ctx, "retry-test", 10, 60, 3)
	if err != nil {
		t.Fatalf("RunRetentionSweep(first) error = %v", err)
	}
	if firstSweep.Claimed != 1 || firstSweep.Retried != 1 || firstSweep.Completed != 0 {
		t.Fatalf("first retry sweep = %+v", firstSweep)
	}
	jobs, err := store.ListRetentionJobs(ctx, source.Asset.AssetID)
	if err != nil ||
		len(jobs) != 1 ||
		jobs[0].RetentionJobID != deleted.Job.RetentionJobID ||
		jobs[0].Status != "retry_wait" ||
		jobs[0].AttemptCount != 1 ||
		jobs[0].LastFailure == nil {
		t.Fatalf("retry job = %+v, error = %v", jobs, err)
	}

	if err := os.Remove(blockerPath); err != nil {
		t.Fatalf("remove delete blocker: %v", err)
	}
	if err := os.Remove(blobPath); err != nil {
		t.Fatalf("remove replacement directory: %v", err)
	}
	clock = clock.Add(2 * time.Minute)
	secondSweep, err := store.RunRetentionSweep(ctx, "retry-test", 10, 60, 3)
	if err != nil {
		t.Fatalf("RunRetentionSweep(second) error = %v", err)
	}
	if secondSweep.Claimed != 1 || secondSweep.Completed != 1 || secondSweep.Retried != 0 {
		t.Fatalf("second retry sweep = %+v", secondSweep)
	}
	jobs, err = store.ListRetentionJobs(ctx, source.Asset.AssetID)
	if err != nil || jobs[0].Status != "completed" || jobs[0].AttemptCount != 2 {
		t.Fatalf("completed retry job = %+v, error = %v", jobs, err)
	}
}

func createVideoAsset(t *testing.T, store *Store, projectID, filename string) AssetResult {
	t.Helper()
	content := testMP4Bytes()
	session, err := store.CreateUploadSession(context.Background(), projectID, []UploadItemSpec{{
		ClientItemKey:     "video_1",
		Kind:              "video",
		OriginalFilename:  filename,
		DeclaredMIMEType:  "video/mp4",
		DeclaredSizeBytes: int64(len(content)),
	}})
	if err != nil {
		t.Fatalf("CreateUploadSession(video) error = %v", err)
	}
	item, err := store.WriteUploadContent(
		context.Background(),
		session.Items[0].UploadItemID,
		bytes.NewReader(content),
	)
	if err != nil {
		t.Fatalf("WriteUploadContent(video) error = %v", err)
	}
	result, err := store.CompleteUploadItem(context.Background(), item.UploadItemID)
	if err != nil {
		t.Fatalf("CompleteUploadItem(video) error = %v", err)
	}
	return result
}

func testMP4Bytes() []byte {
	return []byte{
		0x00, 0x00, 0x00, 0x18,
		'f', 't', 'y', 'p',
		'i', 's', 'o', 'm',
		0x00, 0x00, 0x02, 0x00,
		'i', 's', 'o', 'm',
		'i', 's', 'o', '2',
	}
}
