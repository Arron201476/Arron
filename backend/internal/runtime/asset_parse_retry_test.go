package runtime

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/documentparser"
)

type retryDocumentParser struct {
	result documentparser.Result
	err    error
}

func (p retryDocumentParser) Supports(extension string) bool {
	return extension == ".docx"
}

func (p retryDocumentParser) Parse(context.Context, string, string) (documentparser.Result, error) {
	return p.result, p.err
}

func TestRetryAssetParseUpdatesCurrentSnapshotResult(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "解析重试")
	if err != nil {
		t.Fatal(err)
	}
	docx, err := buildDOCX([]string{"原始文档"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.CreateUploadSession(ctx, project.ProjectID, []UploadItemSpec{{
		ClientItemKey: "retry_docx", Kind: "document", OriginalFilename: "outline.docx",
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
	uploaded, err := store.CompleteUploadItem(ctx, item.UploadItemID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		UPDATE asset_parse_results SET status = 'failed', content_text = '', content_hash = '', error_code = 'ASSET_PARSE_FAILED'
		WHERE asset_snapshot_id = ?`, uploaded.Snapshot.AssetSnapshotID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE assets SET parse_status = 'failed' WHERE asset_id = ?`, uploaded.Asset.AssetID); err != nil {
		t.Fatal(err)
	}
	store.documentParser = retryDocumentParser{result: documentparser.Result{
		Text: "重试后恢复的故事大纲", ParserID: "test.docx", Version: "2.0.0",
	}}
	result, err := store.RetryAssetParse(ctx, RetryAssetParseCommand{AssetID: uploaded.Asset.AssetID})
	if err != nil {
		t.Fatalf("RetryAssetParse() error = %v", err)
	}
	if result.Status != "completed" || result.ParserID != "test.docx" || result.ErrorCode != nil {
		t.Fatalf("parse result = %+v", result)
	}
	var status, content string
	if err := store.db.QueryRowContext(ctx, `SELECT status, content_text FROM asset_parse_results WHERE asset_snapshot_id = ?`, uploaded.Snapshot.AssetSnapshotID).Scan(&status, &content); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || content != "重试后恢复的故事大纲" {
		t.Fatalf("stored parse result status=%q content=%q", status, content)
	}
	store.documentParser = retryDocumentParser{err: errors.New("parser failed")}
	failed, err := store.RetryAssetParse(ctx, RetryAssetParseCommand{AssetID: uploaded.Asset.AssetID})
	if err != nil {
		t.Fatalf("RetryAssetParse(failed) error = %v", err)
	}
	if failed.Status != "failed" || failed.ErrorCode == nil || *failed.ErrorCode != "ASSET_PARSE_FAILED" {
		t.Fatalf("failed parse result = %+v", failed)
	}
}
