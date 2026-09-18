package runtime

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestTXTAndDOCXExportsUseSameScriptSource(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "导出同源测试")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	candidate := createCandidateFixture(t, store, project, "export")
	seedExportScriptUnit(t, store, candidate)

	txtCommand := CreateScriptExportCommand{
		CommandMeta: CommandMeta{Scope: project.ProjectID, CommandType: "create_script_export", IdempotencyKey: "script-export-txt", RequestHash: "script-export-txt-v1"},
		CandidateID: candidate.CandidateID, ArtifactVersionID: candidate.ScriptsArtifactVersionID, Format: "txt",
	}
	txtExport, err := store.CreateScriptExport(ctx, txtCommand)
	if err != nil {
		t.Fatalf("CreateScriptExport(txt) error = %v", err)
	}
	replayedTXT, err := store.CreateScriptExport(ctx, txtCommand)
	if err != nil || replayedTXT.ExportID != txtExport.ExportID {
		t.Fatalf("CreateScriptExport(txt replay) = %+v, error = %v", replayedTXT, err)
	}
	docxExport, err := store.CreateScriptExport(ctx, CreateScriptExportCommand{
		CandidateID: candidate.CandidateID, ArtifactVersionID: candidate.ScriptsArtifactVersionID, Format: "docx",
	})
	if err != nil {
		t.Fatalf("CreateScriptExport(docx) error = %v", err)
	}
	if txtExport.SizeBytes == 0 || docxExport.SizeBytes == 0 || txtExport.ArtifactVersionID != docxExport.ArtifactVersionID {
		t.Fatalf("exports = %+v %+v", txtExport, docxExport)
	}
	txtContent, err := store.OpenScriptExportContent(ctx, txtExport.ExportID)
	if err != nil {
		t.Fatalf("OpenScriptExportContent(txt) error = %v", err)
	}
	txtBytes, _ := io.ReadAll(txtContent.File)
	txtContent.File.Close()
	docxContent, err := store.OpenScriptExportContent(ctx, docxExport.ExportID)
	if err != nil {
		t.Fatalf("OpenScriptExportContent(docx) error = %v", err)
	}
	docxPath := docxContent.File.Name()
	docxContent.File.Close()
	docxText := readDOCXText(t, docxPath)
	for _, expected := range []string{"第一集 失物", "1-1", "△林夏推开档案室的门。", "林夏：账本在这里。"} {
		if !strings.Contains(string(txtBytes), expected) || !strings.Contains(docxText, expected) {
			t.Fatalf("expected %q in txt=%q docx=%q", expected, txtBytes, docxText)
		}
	}
	_, err = store.CreateScriptExport(ctx, CreateScriptExportCommand{
		CandidateID: candidate.CandidateID, ArtifactVersionID: "av_wrong", Format: "txt",
	})
	assertDomainCode(t, err, "EXPORT_VERSION_CONFLICT")
}

func seedExportScriptUnit(t *testing.T, store *Store, candidate ScriptCandidate) {
	t.Helper()
	ctx := context.Background()
	now := store.now()
	var stepRunID string
	if err := store.db.QueryRowContext(ctx, `SELECT current_step_run_id FROM runs WHERE run_id = ?`, candidate.SourceRunID).Scan(&stepRunID); err != nil {
		t.Fatalf("load candidate step: %v", err)
	}
	unitArtifactID, unitVersionID := "art_export_unit", "av_export_unit"
	unitPayload := mustJSON(t, map[string]any{
		"episode_no": 1, "title": "第一集 失物",
		"script_text": "1-1\n地点：档案室 | 内 | 夜\n△林夏推开档案室的门。\n林夏：账本在这里。",
	})
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifacts(artifact_id, project_id, run_id, step_run_id, capability_id,
			artifact_type, scope_key, current_version_id, created_at, updated_at)
		VALUES(?, ?, ?, ?, 'novel_to_script', 'script_unit', 'episode:1', ?, ?, ?)`,
		unitArtifactID, candidate.ProjectID, candidate.SourceRunID, stepRunID, unitVersionID,
		formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifact_versions(artifact_version_id, artifact_id, version, status, payload_json,
			schema_id, schema_version, created_by_kind, actor_ref, creation_reason, created_at, confirmed_at)
		VALUES(?, ?, 1, 'confirmed', ?, 'script_unit', '1.0.0', 'worker', 'export_test', 'generation', ?, ?)`,
		unitVersionID, unitArtifactID, string(unitPayload), formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	scriptsPayload := mustJSON(t, map[string]any{
		"episode_count": 1,
		"unit_refs":     []any{map[string]any{"episode_no": 1, "artifact_version_id": unitVersionID}},
		"completeness":  "complete", "global_continuity_state": map[string]any{}, "quality_flags": []string{},
	})
	if _, err := tx.ExecContext(ctx, `UPDATE artifact_versions SET payload_json = ? WHERE artifact_version_id = ?`,
		string(scriptsPayload), candidate.ScriptsArtifactVersionID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func readDOCXText(t *testing.T, path string) string {
	t.Helper()
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open docx: %v", err)
	}
	defer archive.Close()
	for _, file := range archive.File {
		if file.Name != "word/document.xml" {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		decoder := xml.NewDecoder(reader)
		var parts []string
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if chars, ok := token.(xml.CharData); ok {
				parts = append(parts, string(chars))
			}
		}
		return strings.Join(parts, "\n")
	}
	t.Fatal("word/document.xml missing")
	return ""
}
