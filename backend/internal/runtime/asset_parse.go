package runtime

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"unicode/utf8"
)

func (s *Store) RetryAssetParse(ctx context.Context, command RetryAssetParseCommand) (AssetParseResult, error) {
	if command.AssetID == "" {
		return AssetParseResult{}, domainError("REQUEST_VALIDATION_FAILED", "解析重试缺少材料。")
	}
	asset, err := s.GetAsset(ctx, command.AssetID)
	if err != nil {
		return AssetParseResult{}, err
	}
	if asset.Status != "available" {
		return AssetParseResult{}, domainError("ASSET_SOURCE_DELETED", "材料源文件不可用，无法重新解析。")
	}
	if asset.Kind != "text" && asset.Kind != "document" {
		return AssetParseResult{}, domainError("ASSET_PARSE_UNSUPPORTED", "该材料类型不支持通用文本解析。")
	}
	if command.Scope == "" {
		command.Scope = asset.ProjectID
	}
	var storageRef string
	if err := s.db.QueryRowContext(ctx, `
		SELECT storage_ref FROM asset_blobs
		WHERE blob_id = ? AND asset_id = ? AND status = 'available'`,
		asset.OriginalBlobID, asset.AssetID,
	).Scan(&storageRef); errors.Is(err, sql.ErrNoRows) {
		return AssetParseResult{}, domainError("ASSET_SOURCE_UNAVAILABLE", "材料源文件不可用，无法重新解析。")
	} else if err != nil {
		return AssetParseResult{}, err
	}
	path, err := resolveDataPath(s.dataRoot, storageRef)
	if err != nil {
		return AssetParseResult{}, err
	}
	status := "failed"
	parserID := "unavailable"
	parserVersion := "1.0.0"
	content := ""
	errorCode := "ASSET_PARSE_FAILED"
	if asset.Kind == "text" {
		parserID = "builtin.utf8"
		body, readErr := os.ReadFile(path)
		if readErr == nil && utf8.Valid(body) {
			content = string(body)
			status = "completed"
			errorCode = ""
		}
	} else if s.documentParser == nil || !s.documentParser.Supports("."+strings.TrimPrefix(asset.Extension, ".")) {
		errorCode = "DOCUMENT_PARSER_UNAVAILABLE"
	} else {
		parsed, parseErr := s.documentParser.Parse(ctx, path, "."+strings.TrimPrefix(asset.Extension, "."))
		if parsed.ParserID != "" {
			parserID = parsed.ParserID
		}
		if parsed.Version != "" {
			parserVersion = parsed.Version
		}
		if parseErr == nil {
			content = parsed.Text
			status = "completed"
			errorCode = ""
		}
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AssetParseResult{}, err
	}
	defer tx.Rollback()
	cached, hit, err := s.beginIdempotency(ctx, tx, command.CommandMeta)
	if err != nil {
		return AssetParseResult{}, err
	}
	if hit {
		return decodeIdempotentResult[AssetParseResult](cached)
	}
	var currentSnapshotID, currentStatus string
	if err := tx.QueryRowContext(ctx, `SELECT current_snapshot_id, status FROM assets WHERE asset_id = ?`, asset.AssetID).Scan(&currentSnapshotID, &currentStatus); err != nil {
		return AssetParseResult{}, err
	}
	if currentSnapshotID != asset.CurrentSnapshotID || currentStatus != "available" {
		return AssetParseResult{}, domainError("ASSET_VERSION_CONFLICT", "材料状态已经变化，请重新发起解析。")
	}
	contentHash := sha256Hex([]byte(content))
	resultID := ""
	if err := tx.QueryRowContext(ctx, `SELECT asset_parse_result_id FROM asset_parse_results WHERE asset_snapshot_id = ?`, currentSnapshotID).Scan(&resultID); errors.Is(err, sql.ErrNoRows) {
		resultID = s.newID("aprse")
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO asset_parse_results(
				asset_parse_result_id, asset_id, asset_snapshot_id, status, parser_id,
				parser_version, content_text, content_hash, error_code, created_at
			) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			resultID, asset.AssetID, currentSnapshotID, status, parserID, parserVersion,
			content, contentHash, nullableStringPointer(errorCode), formatTime(now),
		); err != nil {
			return AssetParseResult{}, err
		}
	} else if err != nil {
		return AssetParseResult{}, err
	} else if _, err := tx.ExecContext(ctx, `
		UPDATE asset_parse_results
		SET status = ?, parser_id = ?, parser_version = ?, content_text = ?,
			content_hash = ?, error_code = ?, created_at = ?
		WHERE asset_parse_result_id = ?`,
		status, parserID, parserVersion, content, contentHash,
		nullableStringPointer(errorCode), formatTime(now), resultID,
	); err != nil {
		return AssetParseResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE assets SET parse_status = ?, updated_at = ? WHERE asset_id = ?`, status, formatTime(now), asset.AssetID); err != nil {
		return AssetParseResult{}, err
	}
	eventType := "asset.parse_completed"
	if status != "completed" {
		eventType = "asset.parse_failed"
	}
	if _, err := s.appendEvent(ctx, tx, asset.ProjectID, nil, nil, eventType, "asset", asset.AssetID,
		map[string]any{
			"asset_snapshot_id": currentSnapshotID,
			"parser_id":         parserID,
			"error_code":        errorCode,
			"retry":             true,
		},
	); err != nil {
		return AssetParseResult{}, err
	}
	var errorCodeRef *string
	if errorCode != "" {
		errorCodeRef = &errorCode
	}
	result := AssetParseResult{
		AssetParseResultID: resultID,
		AssetID:            asset.AssetID,
		AssetSnapshotID:    currentSnapshotID,
		Status:             status,
		ParserID:           parserID,
		ParserVersion:      parserVersion,
		ContentHash:        contentHash,
		ErrorCode:          errorCodeRef,
		CreatedAt:          now,
	}
	if err := completeIdempotency(ctx, tx, command.CommandMeta, result, now); err != nil {
		return AssetParseResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return AssetParseResult{}, err
	}
	return result, nil
}
