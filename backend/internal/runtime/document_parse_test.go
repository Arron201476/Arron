package runtime

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestDOCXUploadCreatesSnapshotBoundParseResult(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "DOCX 解析测试")
	if err != nil {
		t.Fatal(err)
	}
	docx, err := buildDOCX([]string{"故事大纲", "林夏在档案室找到失物账本。", "第二天她公开旧案证据。"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.CreateUploadSession(ctx, project.ProjectID, []UploadItemSpec{{
		ClientItemKey: "docx_1", Kind: "document", OriginalFilename: "故事大纲.docx",
		DeclaredMIMEType:  "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		DeclaredSizeBytes: int64(len(docx)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.WriteUploadContent(ctx, session.Items[0].UploadItemID, bytes.NewReader(docx))
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.CompleteUploadItem(ctx, item.UploadItemID)
	if err != nil || result.Asset.ParseStatus != "completed" {
		t.Fatalf("CompleteUploadItem(docx) = %+v, error = %v", result, err)
	}
	var status, content string
	if err := store.db.QueryRowContext(ctx, `
		SELECT status, content_text FROM asset_parse_results
		WHERE asset_id = ? AND asset_snapshot_id = ?`,
		result.Asset.AssetID, result.Snapshot.AssetSnapshotID).Scan(&status, &content); err != nil {
		t.Fatalf("load parse result: %v", err)
	}
	if status != "completed" || !strings.Contains(content, "林夏在档案室找到失物账本") {
		t.Fatalf("parse result status=%s content=%q", status, content)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	assets, err := store.resolveContextAssets(ctx, tx, project.ProjectID, []assetInputReference{{
		AssetID: result.Asset.AssetID, AssetSnapshotID: result.Snapshot.AssetSnapshotID,
		Role: "primary_source", Order: 1,
	}}, store.now())
	tx.Rollback()
	if err != nil || len(assets) != 1 || !strings.Contains(assets[0].Content, "第二天她公开旧案证据") {
		t.Fatalf("resolveContextAssets(docx) = %+v, error = %v", assets, err)
	}
}
