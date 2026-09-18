package runtime

import (
	"context"
	"path/filepath"
	"testing"
)

func TestProjectDeleteCancelsActiveRunsAtomically(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "待删除作品")
	if err != nil {
		t.Fatal(err)
	}
	source := createTextAsset(t, store, project.ProjectID, "source.txt", "保留派生产物，删除来源。")
	now := formatTime(store.now())
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO runs(
			run_id, project_id, conversation_id, capability_id, capability_version,
			run_kind, write_intent, status, current_input_snapshot_version_id,
			input_snapshot_status, config_snapshot_json, created_at, updated_at
		) VALUES('run_project_delete', ?, ?, 'text_to_script_novel', '1.0.0',
			'production', 1, 'paused', 'ris_project_delete', 'sealed', '{}', ?, ?)`,
		project.ProjectID, project.PrimaryConversationID, now, now,
	); err != nil {
		t.Fatal(err)
	}
	preview, err := store.CreateProjectDeletePreview(ctx, CreateProjectDeletePreviewCommand{ProjectID: project.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.ActiveRunImpacts) != 1 || preview.Impact.AssetCount != 1 {
		t.Fatalf("delete preview = %+v", preview)
	}
	result, err := store.ConfirmProjectDelete(ctx, ConfirmProjectDeleteCommand{
		ProjectID: project.ProjectID, PreviewHash: preview.SnapshotHash, Confirmed: true,
	})
	if err != nil {
		t.Fatalf("ConfirmProjectDelete() error = %v", err)
	}
	if result.ProjectID != project.ProjectID || len(result.SourceDeletionJobIDs) != 1 || !result.DerivedRecordsRetained {
		t.Fatalf("delete result = %+v", result)
	}
	var runStatus string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM runs WHERE run_id = 'run_project_delete'`).Scan(&runStatus); err != nil || runStatus != "cancelled" {
		t.Fatalf("run status = %q, error = %v", runStatus, err)
	}
	if _, err := store.GetProject(ctx, project.ProjectID); err == nil {
		t.Fatal("deleted project is still visible")
	} else {
		assertDomainCode(t, err, "PROJECT_NOT_FOUND")
	}
	asset, err := store.GetAsset(ctx, source.Asset.AssetID)
	if err != nil || asset.Status != "deleted" || asset.DeleteReason == nil || *asset.DeleteReason != "project_deleted" {
		t.Fatalf("deleted source = %+v, error = %v", asset, err)
	}
}
