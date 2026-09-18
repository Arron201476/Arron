package runtime

import (
	"archive/zip"
	"bytes"
	"context"
	"path/filepath"
	"testing"
)

func TestArchiveUploadCreatesVisibleParentAndOrderedChildAssets(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "ZIP 通用上传")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	archive := testZIP(t, map[string][]byte{
		"正片/第02集.mov": bytes.Repeat([]byte{0}, 1024),
		"正片/第01集.mp4": bytes.Repeat([]byte{1}, 1024),
		"说明.exe":      []byte("ignored"),
	})
	session, err := store.CreateUploadSession(ctx, project.ProjectID, []UploadItemSpec{{
		ClientItemKey: "archive-1", Kind: "archive", OriginalFilename: "整部剧.zip",
		DeclaredMIMEType: "application/zip", DeclaredSizeBytes: int64(len(archive)),
	}})
	if err != nil {
		t.Fatalf("CreateUploadSession() error = %v", err)
	}
	itemID := session.Items[0].UploadItemID
	if _, err := store.WriteUploadContent(ctx, itemID, bytes.NewReader(archive)); err != nil {
		t.Fatalf("WriteUploadContent() error = %v", err)
	}
	result, err := store.CompleteUploadItem(ctx, itemID)
	if err != nil {
		t.Fatalf("CompleteUploadItem() error = %v", err)
	}
	if result.Asset.Kind != "archive" || result.Asset.DisplayName != "整部剧.zip" {
		t.Fatalf("archive parent = %+v", result.Asset)
	}
	if len(result.ExtractedAssets) != 2 || result.ExtractedAssets[0].Asset.DisplayName != "正片/第01集.mp4" || result.ExtractedAssets[1].Asset.DisplayName != "正片/第02集.mov" {
		t.Fatalf("ordered extracted assets = %+v", result.ExtractedAssets)
	}
	if len(result.IgnoredEntries) != 1 || result.IgnoredEntries[0] != "说明.exe" {
		t.Fatalf("ignored entries = %#v", result.IgnoredEntries)
	}
	for _, child := range result.ExtractedAssets {
		if child.Asset.Kind != "video" || child.Asset.SourceType != "archive_entry" || child.Asset.RetentionPolicyID == nil {
			t.Fatalf("archive child = %+v", child.Asset)
		}
	}
	repeated, err := store.CompleteUploadItem(ctx, itemID)
	if err != nil {
		t.Fatalf("repeated CompleteUploadItem() error = %v", err)
	}
	if len(repeated.ExtractedAssets) != 2 || repeated.ExtractedAssets[0].Asset.AssetID != result.ExtractedAssets[0].Asset.AssetID {
		t.Fatalf("repeated archive result = %+v", repeated)
	}
	assets, err := store.ListAssets(ctx, project.ProjectID)
	if err != nil || len(assets) != 3 {
		t.Fatalf("ListAssets() count = %d, error = %v", len(assets), err)
	}
}

func TestArchiveUploadRejectsUnsafeEntry(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, _ := store.CreateProject(ctx, "ZIP 安全边界")
	archive := testZIP(t, map[string][]byte{"../escape.mp4": bytes.Repeat([]byte{0}, 1024)})
	session, err := store.CreateUploadSession(ctx, project.ProjectID, []UploadItemSpec{{
		ClientItemKey: "unsafe", Kind: "archive", OriginalFilename: "unsafe.zip",
		DeclaredMIMEType: "application/zip", DeclaredSizeBytes: int64(len(archive)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	itemID := session.Items[0].UploadItemID
	if _, err := store.WriteUploadContent(ctx, itemID, bytes.NewReader(archive)); err != nil {
		t.Fatal(err)
	}
	_, err = store.CompleteUploadItem(ctx, itemID)
	assertDomainCode(t, err, "ARCHIVE_ENTRY_UNSAFE")
	item, loadErr := store.getUploadItem(ctx, itemID)
	if loadErr != nil || item.Status != "failed" {
		t.Fatalf("unsafe archive item = %+v, error = %v", item, loadErr)
	}
}

func testZIP(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
