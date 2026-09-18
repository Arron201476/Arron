package runtime

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const exportRetention = 24 * time.Hour

var unsafeFilenameCharacters = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

func (s *Store) CreateScriptExport(ctx context.Context, command CreateScriptExportCommand) (ScriptExport, error) {
	if command.CandidateID == "" || command.ArtifactVersionID == "" ||
		(command.Format != "txt" && command.Format != "docx") {
		return ScriptExport{}, domainError("REQUEST_VALIDATION_FAILED", "导出请求无效。")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ScriptExport{}, err
	}
	defer tx.Rollback()
	var projectID, scriptsVersionID, title string
	err = tx.QueryRowContext(ctx, `
		SELECT sc.project_id, sc.scripts_artifact_version_id, p.title
		FROM script_candidates sc JOIN projects p ON p.project_id = sc.project_id
		WHERE sc.candidate_id = ?`, command.CandidateID).
		Scan(&projectID, &scriptsVersionID, &title)
	if errors.Is(err, sql.ErrNoRows) {
		return ScriptExport{}, domainError("SCRIPT_CANDIDATE_NOT_FOUND", "剧本候选不存在。")
	}
	if err != nil {
		return ScriptExport{}, err
	}
	if err := authorizeCandidateCommandTx(ctx, tx, projectID, command.Scope); err != nil {
		return ScriptExport{}, err
	}
	if command.Scope == "" {
		command.Scope = projectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return ScriptExport{}, err
	}
	if hit {
		return s.decodeScriptExportReceiptTx(ctx, tx, cached, command, projectID)
	}
	if scriptsVersionID != command.ArtifactVersionID {
		return ScriptExport{}, domainError("EXPORT_VERSION_CONFLICT", "导出版本与剧本候选不一致。")
	}
	if _, _, err := getSelectableCandidateTx(ctx, tx, projectID, command.CandidateID); err != nil {
		return ScriptExport{}, err
	}
	paragraphs, err := scriptExportParagraphs(ctx, tx, projectID, command.ArtifactVersionID)
	if err != nil {
		return ScriptExport{}, err
	}
	var content []byte
	contentType := "text/plain; charset=utf-8"
	if command.Format == "txt" {
		content = []byte(strings.Join(paragraphs, "\r\n"))
	} else {
		content, err = buildDOCX(paragraphs)
		if err != nil {
			return ScriptExport{}, err
		}
		contentType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	}
	now := s.now()
	exportID := s.newID("exp")
	exportDir := filepath.Join(s.dataRoot, "exports")
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		return ScriptExport{}, err
	}
	filenameBase := strings.TrimSpace(unsafeFilenameCharacters.ReplaceAllString(title, "_"))
	if filenameBase == "" {
		filenameBase = "script"
	}
	filename := filenameBase + "." + command.Format
	storageRef := filepath.Join(exportDir, exportID+"."+command.Format)
	if err := os.WriteFile(storageRef, content, 0o600); err != nil {
		return ScriptExport{}, err
	}
	digest := sha256.Sum256(content)
	result := ScriptExport{
		ExportID: exportID, ProjectID: projectID, CandidateID: command.CandidateID,
		ArtifactVersionID: command.ArtifactVersionID, Format: command.Format, Status: "ready",
		ContentType: contentType, Filename: filename, Checksum: hex.EncodeToString(digest[:]),
		SizeBytes: int64(len(content)), CreatedAt: now, ExpiresAt: now.Add(exportRetention),
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO exports(export_id, project_id, candidate_id, artifact_version_id,
			format, status, content_type, filename, storage_ref, checksum, size_bytes,
			created_at, expires_at)
		VALUES(?, ?, ?, ?, ?, 'ready', ?, ?, ?, ?, ?, ?, ?)`,
		exportID, projectID, command.CandidateID, command.ArtifactVersionID, command.Format,
		contentType, filename, storageRef, result.Checksum, result.SizeBytes,
		formatTime(now), formatTime(result.ExpiresAt)); err != nil {
		_ = os.Remove(storageRef)
		return ScriptExport{}, err
	}
	if _, err := s.appendEvent(ctx, tx, projectID, nil, nil, "export.ready", "export", exportID,
		map[string]any{"candidate_id": command.CandidateID, "artifact_version_id": command.ArtifactVersionID, "format": command.Format}); err != nil {
		_ = os.Remove(storageRef)
		return ScriptExport{}, err
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, result, now); err != nil {
		_ = os.Remove(storageRef)
		return ScriptExport{}, err
	}
	if err := tx.Commit(); err != nil {
		// Do not delete bytes after an uncertain commit; a committed receipt may
		// already reference this file. A retry checks the original command key.
		return ScriptExport{}, err
	}
	return result, nil
}

func scriptExportParagraphs(ctx context.Context, query rowQueryer, projectID, scriptsVersionID string) ([]string, error) {
	var artifactType, status, payloadJSON string
	err := query.QueryRowContext(ctx, `
		SELECT a.artifact_type, av.status, av.payload_json
		FROM artifact_versions av JOIN artifacts a ON a.artifact_id = av.artifact_id
		WHERE av.artifact_version_id = ? AND a.project_id = ?`, scriptsVersionID, projectID).
		Scan(&artifactType, &status, &payloadJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domainError("ARTIFACT_VERSION_NOT_FOUND", "导出剧本版本不存在。")
	}
	if err != nil {
		return nil, err
	}
	if artifactType != "scripts" || (status != "confirmed" && status != "superseded") {
		return nil, domainError("EXPORT_VERSION_CONFLICT", "目标版本不是可导出的完整剧本。")
	}
	var payload struct {
		EpisodeCount int    `json:"episode_count"`
		Completeness string `json:"completeness"`
		UnitRefs     []struct {
			EpisodeNo         int    `json:"episode_no"`
			ArtifactVersionID string `json:"artifact_version_id"`
		} `json:"unit_refs"`
	}
	if json.Unmarshal([]byte(payloadJSON), &payload) != nil || payload.Completeness != "complete" ||
		payload.EpisodeCount <= 0 || len(payload.UnitRefs) != payload.EpisodeCount {
		return nil, domainError("EXPORT_SOURCE_INCOMPLETE", "剧本候选不是完整集数。")
	}
	sort.Slice(payload.UnitRefs, func(i, j int) bool { return payload.UnitRefs[i].EpisodeNo < payload.UnitRefs[j].EpisodeNo })
	paragraphs := make([]string, 0, payload.EpisodeCount*4)
	for index, ref := range payload.UnitRefs {
		if ref.EpisodeNo != index+1 {
			return nil, domainError("EXPORT_SOURCE_INCOMPLETE", "剧本候选集号不连续。")
		}
		var unitJSON string
		if err := query.QueryRowContext(ctx, `
			SELECT av.payload_json FROM artifact_versions av JOIN artifacts a ON a.artifact_id = av.artifact_id
			WHERE av.artifact_version_id = ? AND a.project_id = ? AND a.artifact_type = 'script_unit' AND av.status IN ('confirmed','superseded')`,
			ref.ArtifactVersionID, projectID).Scan(&unitJSON); err != nil {
			return nil, domainError("EXPORT_SOURCE_INCOMPLETE", "剧本候选缺少单集正文。")
		}
		var unit struct {
			EpisodeNo  int    `json:"episode_no"`
			Title      string `json:"title"`
			ScriptText string `json:"script_text"`
		}
		if json.Unmarshal([]byte(unitJSON), &unit) != nil || unit.EpisodeNo != ref.EpisodeNo || strings.TrimSpace(unit.ScriptText) == "" {
			return nil, domainError("EXPORT_SOURCE_INCOMPLETE", "单集剧本正文不可读取。")
		}
		title := strings.TrimSpace(unit.Title)
		if title == "" {
			title = fmt.Sprintf("第%d集", ref.EpisodeNo)
		}
		paragraphs = append(paragraphs, title)
		for _, line := range strings.Split(strings.ReplaceAll(unit.ScriptText, "\r\n", "\n"), "\n") {
			paragraphs = append(paragraphs, line)
		}
		if index != len(payload.UnitRefs)-1 {
			paragraphs = append(paragraphs, "")
		}
	}
	return paragraphs, nil
}

func buildDOCX(paragraphs []string) ([]byte, error) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	files := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
		"_rels/.rels":         `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`,
	}
	var document strings.Builder
	document.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, paragraph := range paragraphs {
		document.WriteString(`<w:p><w:r><w:t xml:space="preserve">`)
		document.WriteString(html.EscapeString(paragraph))
		document.WriteString(`</w:t></w:r></w:p>`)
	}
	document.WriteString(`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440"/></w:sectPr></w:body></w:document>`)
	files["word/document.xml"] = document.String()
	for _, name := range []string{"[Content_Types].xml", "_rels/.rels", "word/document.xml"} {
		writer, err := archive.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := writer.Write([]byte(files[name])); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func (s *Store) GetScriptExport(ctx context.Context, exportID string) (ScriptExport, error) {
	result, _, err := s.getScriptExport(ctx, exportID)
	return result, err
}

func (s *Store) OpenScriptExportContent(ctx context.Context, exportID string) (ScriptExportContent, error) {
	result, storageRef, err := s.getScriptExport(ctx, exportID)
	if err != nil {
		return ScriptExportContent{}, err
	}
	if !s.now().Before(result.ExpiresAt) {
		return ScriptExportContent{}, domainError("EXPORT_EXPIRED", "导出文件已过期，请重新导出。")
	}
	absRoot, _ := filepath.Abs(filepath.Join(s.dataRoot, "exports"))
	absRef, _ := filepath.Abs(storageRef)
	if filepath.Dir(absRef) != absRoot {
		return ScriptExportContent{}, domainError("EXPORT_STORAGE_INVALID", "导出文件存储引用无效。")
	}
	file, err := os.Open(absRef)
	if errors.Is(err, os.ErrNotExist) {
		return ScriptExportContent{}, domainError("EXPORT_EXPIRED", "导出文件已清理，请重新导出。")
	}
	if err != nil {
		return ScriptExportContent{}, err
	}
	return ScriptExportContent{Export: result, File: file}, nil
}

func (s *Store) getScriptExport(ctx context.Context, exportID string) (ScriptExport, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ScriptExport{}, "", err
	}
	defer tx.Rollback()
	result, storageRef, err := getScriptExportQuery(ctx, tx, exportID)
	if err != nil {
		return ScriptExport{}, "", err
	}
	if _, err := projectFilesWorkspace(ctx, tx, result.ProjectID, false); err != nil {
		return ScriptExport{}, "", err
	}
	return result, storageRef, nil
}

func getScriptExportQuery(ctx context.Context, query rowQueryer, exportID string) (ScriptExport, string, error) {
	var result ScriptExport
	var storageRef, createdAt, expiresAt string
	err := query.QueryRowContext(ctx, `
		SELECT export_id, project_id, candidate_id, artifact_version_id, format, status,
			content_type, filename, storage_ref, checksum, size_bytes, created_at, expires_at
		FROM exports WHERE export_id = ?`, exportID).Scan(
		&result.ExportID, &result.ProjectID, &result.CandidateID, &result.ArtifactVersionID,
		&result.Format, &result.Status, &result.ContentType, &result.Filename, &storageRef,
		&result.Checksum, &result.SizeBytes, &createdAt, &expiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ScriptExport{}, "", domainError("EXPORT_NOT_FOUND", "导出记录不存在。")
	}
	if err != nil {
		return ScriptExport{}, "", err
	}
	result.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return ScriptExport{}, "", err
	}
	result.ExpiresAt, err = parseTime(expiresAt)
	return result, storageRef, err
}

func (s *Store) decodeScriptExportReceiptTx(ctx context.Context, tx *sql.Tx, cached json.RawMessage, command CreateScriptExportCommand, projectID string) (ScriptExport, error) {
	receipt, err := decodeIdempotentResult[ScriptExport](cached)
	if err != nil {
		return ScriptExport{}, err
	}
	mismatch := func() (ScriptExport, error) {
		return ScriptExport{}, domainError("IDEMPOTENCY_KEY_REUSED", "该请求标识不属于当前候选稿导出。")
	}
	if receipt.ProjectID != projectID || receipt.CandidateID != command.CandidateID || receipt.ArtifactVersionID != command.ArtifactVersionID || receipt.Format != command.Format || receipt.Status != "ready" {
		return mismatch()
	}
	stored, storageRef, err := getScriptExportQuery(ctx, tx, receipt.ExportID)
	if err != nil {
		return ScriptExport{}, err
	}
	if stored.ProjectID != receipt.ProjectID || stored.CandidateID != receipt.CandidateID || stored.ArtifactVersionID != receipt.ArtifactVersionID || stored.Format != receipt.Format || stored.Status != "ready" || stored.Checksum != receipt.Checksum || stored.SizeBytes != receipt.SizeBytes || stored.Filename != receipt.Filename || stored.ContentType != receipt.ContentType || !stored.ExpiresAt.Equal(receipt.ExpiresAt) {
		return mismatch()
	}
	if !s.now().Before(stored.ExpiresAt) {
		return ScriptExport{}, domainError("EXPORT_EXPIRED", "导出文件已过期，请重新导出。")
	}
	root, err := filepath.Abs(filepath.Join(s.dataRoot, "exports"))
	if err != nil {
		return ScriptExport{}, err
	}
	path, err := filepath.Abs(storageRef)
	if err != nil {
		return ScriptExport{}, err
	}
	if filepath.Dir(path) != root {
		return ScriptExport{}, domainError("EXPORT_STORAGE_INVALID", "导出文件存储引用无效。")
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ScriptExport{}, domainError("EXPORT_EXPIRED", "导出文件已清理，请重新导出。")
	}
	if err != nil {
		return ScriptExport{}, err
	}
	if !info.Mode().IsRegular() || info.Size() != stored.SizeBytes {
		return ScriptExport{}, domainError("EXPORT_STORAGE_INVALID", "导出文件与回执不一致。")
	}
	return stored, nil
}
