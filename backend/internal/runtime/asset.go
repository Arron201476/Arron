package runtime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxUploadItems  = 100
	maxTextBytes    = 20 << 20
	maxImageBytes   = 20 << 20
	maxVideoBytes   = 500 << 20
	maxArchiveBytes = 1 << 30
)

var allowedExtensions = map[string]string{
	".txt":  "text",
	".md":   "text",
	".csv":  "text",
	".json": "text",
	".docx": "document",
	".pdf":  "document",
	".jpg":  "image",
	".jpeg": "image",
	".png":  "image",
	".webp": "image",
	".mp4":  "video",
	".mov":  "video",
	".zip":  "archive",
}

func (s *Store) CreateUploadSession(ctx context.Context, projectID string, specs []UploadItemSpec) (UploadSession, error) {
	return s.CreateUploadSessionCommand(ctx, projectID, specs, CommandMeta{})
}

func (s *Store) CreateUploadSessionCommand(
	ctx context.Context,
	projectID string,
	specs []UploadItemSpec,
	meta CommandMeta,
) (UploadSession, error) {
	if _, err := s.GetProject(ctx, projectID); err != nil {
		return UploadSession{}, err
	}
	if len(specs) == 0 || len(specs) > maxUploadItems {
		return UploadSession{}, domainError("REQUEST_VALIDATION_FAILED", "上传文件数量无效。")
	}
	seenKeys := make(map[string]struct{}, len(specs))
	var declaredBytes int64
	for _, spec := range specs {
		if _, duplicate := seenKeys[spec.ClientItemKey]; duplicate {
			return UploadSession{}, domainError("REQUEST_VALIDATION_FAILED", "client_item_key 不能重复。")
		}
		seenKeys[spec.ClientItemKey] = struct{}{}
		if err := validateUploadSpec(spec); err != nil {
			return UploadSession{}, err
		}
		declaredBytes += spec.DeclaredSizeBytes
	}

	now := s.now()
	sessionID := s.newID("upl")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return UploadSession{}, err
	}
	defer tx.Rollback()
	if meta.Scope == "" {
		meta.Scope = projectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, meta)
	if err != nil {
		return UploadSession{}, err
	}
	if hit {
		return decodeIdempotentResult[UploadSession](cached)
	}
	var workspaceID string
	if err := tx.QueryRowContext(ctx, `
		SELECT workspace_id FROM projects WHERE project_id = ? AND deleted_at IS NULL`,
		projectID,
	).Scan(&workspaceID); errors.Is(err, sql.ErrNoRows) {
		return UploadSession{}, domainError("PROJECT_NOT_FOUND", "作品不存在。")
	} else if err != nil {
		return UploadSession{}, err
	}
	if err := s.enforceWorkspaceQuotaTx(ctx, tx, workspaceID, "storage_bytes", declaredBytes); err != nil {
		return UploadSession{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO upload_sessions(
			upload_session_id, project_id, status, declared_item_count,
			completed_item_count, failed_item_count, created_at
		) VALUES(?, ?, 'open', ?, 0, 0, ?)`,
		sessionID, projectID, len(specs), formatTime(now),
	); err != nil {
		return UploadSession{}, err
	}
	session := UploadSession{
		UploadSessionID:   sessionID,
		ProjectID:         projectID,
		Status:            "open",
		DeclaredItemCount: len(specs),
		CreatedAt:         now,
		Items:             make([]UploadItem, 0, len(specs)),
	}
	for _, spec := range specs {
		itemID := s.newID("upi")
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO upload_items(
				upload_item_id, upload_session_id, client_item_key, kind, original_filename,
				declared_mime_type, declared_size_bytes, received_size_bytes, status, created_at
			) VALUES(?, ?, ?, ?, ?, ?, ?, 0, 'pending', ?)`,
			itemID, sessionID, spec.ClientItemKey, spec.Kind, filepath.Base(spec.OriginalFilename),
			spec.DeclaredMIMEType, spec.DeclaredSizeBytes, formatTime(now),
		); err != nil {
			return UploadSession{}, err
		}
		session.Items = append(session.Items, UploadItem{
			UploadItemID:      itemID,
			UploadSessionID:   sessionID,
			ClientItemKey:     spec.ClientItemKey,
			Kind:              spec.Kind,
			OriginalFilename:  filepath.Base(spec.OriginalFilename),
			DeclaredMIMEType:  spec.DeclaredMIMEType,
			DeclaredSizeBytes: spec.DeclaredSizeBytes,
			Status:            "pending",
			CreatedAt:         now,
		})
	}
	if err := completeIdempotency(ctx, tx, meta, session, now); err != nil {
		return UploadSession{}, err
	}
	if err := tx.Commit(); err != nil {
		return UploadSession{}, err
	}
	return session, nil
}

func (s *Store) GetUploadSession(ctx context.Context, sessionID string) (UploadSession, error) {
	var session UploadSession
	var createdAt string
	var closedAt sql.NullString
	if err := s.db.QueryRowContext(ctx, `
		SELECT upload_session_id, project_id, status, declared_item_count,
			completed_item_count, failed_item_count, created_at, closed_at
		FROM upload_sessions WHERE upload_session_id = ?`, sessionID).Scan(
		&session.UploadSessionID, &session.ProjectID, &session.Status, &session.DeclaredItemCount,
		&session.CompletedItemCount, &session.FailedItemCount, &createdAt, &closedAt,
	); errors.Is(err, sql.ErrNoRows) {
		return UploadSession{}, domainError("UPLOAD_SESSION_NOT_FOUND", "上传批次不存在。")
	} else if err != nil {
		return UploadSession{}, err
	}
	var err error
	session.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return UploadSession{}, err
	}
	session.ClosedAt, err = optionalTime(closedAt)
	if err != nil {
		return UploadSession{}, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT upload_item_id, upload_session_id, client_item_key, kind, original_filename,
			declared_mime_type, declared_size_bytes, received_size_bytes, status, asset_id,
			failure, created_at, completed_at
		FROM upload_items WHERE upload_session_id = ?
		ORDER BY created_at ASC, upload_item_id ASC`, sessionID)
	if err != nil {
		return UploadSession{}, err
	}
	defer rows.Close()
	for rows.Next() {
		item, err := scanUploadItem(rows)
		if err != nil {
			return UploadSession{}, err
		}
		session.Items = append(session.Items, item)
	}
	if session.Items == nil {
		session.Items = []UploadItem{}
	}
	return session, rows.Err()
}

func (s *Store) UploadItemProjectID(ctx context.Context, itemID string) (string, error) {
	var projectID string
	if err := s.db.QueryRowContext(ctx, `
		SELECT us.project_id
		FROM upload_items ui
		JOIN upload_sessions us ON us.upload_session_id = ui.upload_session_id
		WHERE ui.upload_item_id = ?`, itemID).Scan(&projectID); errors.Is(err, sql.ErrNoRows) {
		return "", domainError("UPLOAD_ITEM_NOT_FOUND", "上传项不存在。")
	} else if err != nil {
		return "", err
	}
	return projectID, nil
}

func (s *Store) WriteUploadContent(ctx context.Context, itemID string, source io.Reader) (UploadItem, error) {
	return s.WriteUploadContentCommand(ctx, itemID, source, CommandMeta{})
}

func (s *Store) WriteUploadContentCommand(
	ctx context.Context,
	itemID string,
	source io.Reader,
	meta CommandMeta,
) (UploadItem, error) {
	item, err := s.getUploadItem(ctx, itemID)
	if err != nil {
		return UploadItem{}, err
	}
	projectID, err := s.UploadItemProjectID(ctx, itemID)
	if err != nil {
		return UploadItem{}, err
	}
	if meta.IdempotencyKey != "" && meta.RequestHash == "" {
		return UploadItem{}, domainError("IDEMPOTENCY_METADATA_INVALID", "流式上传幂等元数据不完整。")
	}

	attemptFile, err := os.CreateTemp(filepath.Join(s.dataRoot, "staging"), itemID+".*.attempt")
	if err != nil {
		if item.Status == "pending" {
			s.failUploadItem(ctx, itemID, "STAGING_CREATE_FAILED")
		}
		return UploadItem{}, fmt.Errorf("create upload attempt: %w", err)
	}
	attemptPath := attemptFile.Name()
	defer os.Remove(attemptPath)

	hash := sha256.New()
	maxBytes := maxBytesForKind(item.Kind)
	written, copyErr := io.Copy(io.MultiWriter(attemptFile, hash), io.LimitReader(source, maxBytes+1))
	closeErr := attemptFile.Close()
	if copyErr != nil || closeErr != nil || written > maxBytes || written != item.DeclaredSizeBytes {
		reason := "UPLOAD_SIZE_MISMATCH"
		if written > maxBytes {
			reason = "ASSET_FILE_TOO_LARGE"
		}
		if item.Status == "pending" {
			s.failUploadItem(ctx, itemID, reason)
		}
		return UploadItem{}, domainError(reason, "上传内容大小不符合声明或限制。")
	}
	checksum := hex.EncodeToString(hash.Sum(nil))
	if meta.Scope == "" {
		meta.Scope = projectID
	}
	if meta.CommandType == "" {
		meta.CommandType = "write_upload_content"
	}
	if meta.IdempotencyKey != "" {
		requestHash := sha256.Sum256([]byte(fmt.Sprintf(
			"%s\n%s\n%d\n%s",
			meta.RequestHash,
			itemID,
			written,
			checksum,
		)))
		meta.RequestHash = hex.EncodeToString(requestHash[:])
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return UploadItem{}, err
	}
	defer tx.Rollback()
	cached, hit, err := s.beginIdempotency(ctx, tx, meta)
	if err != nil {
		return UploadItem{}, err
	}
	if hit {
		return decodeIdempotentResult[UploadItem](cached)
	}

	currentItem, err := scanUploadItem(tx.QueryRowContext(ctx, `
		SELECT upload_item_id, upload_session_id, client_item_key, kind, original_filename,
			declared_mime_type, declared_size_bytes, received_size_bytes, status, asset_id,
			failure, created_at, completed_at
		FROM upload_items WHERE upload_item_id = ?`, itemID))
	if errors.Is(err, sql.ErrNoRows) {
		return UploadItem{}, domainError("UPLOAD_ITEM_NOT_FOUND", "上传项不存在。")
	}
	if err != nil {
		return UploadItem{}, err
	}
	if currentItem.Status != "pending" {
		return UploadItem{}, domainError("UPLOAD_ITEM_STATE_CONFLICT", "上传项当前状态不允许写入内容。")
	}

	stagingPath := filepath.Join(s.dataRoot, "staging", itemID+".part")
	if err := os.Rename(attemptPath, stagingPath); err != nil {
		return UploadItem{}, fmt.Errorf("commit upload attempt: %w", err)
	}
	restoreAttempt := func() {
		if err := os.Rename(stagingPath, attemptPath); err != nil {
			_ = os.Remove(stagingPath)
		}
	}
	stagingRef := filepath.ToSlash(filepath.Join("staging", itemID+".part"))
	result, err := tx.ExecContext(ctx, `
		UPDATE upload_items
		SET status = 'validating', received_size_bytes = ?, staging_ref = ?, checksum = ?
		WHERE upload_item_id = ? AND status = 'pending'`,
		written, stagingRef, checksum, itemID,
	)
	if err != nil {
		restoreAttempt()
		return UploadItem{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		restoreAttempt()
		return UploadItem{}, err
	}
	if affected != 1 {
		restoreAttempt()
		return UploadItem{}, domainError("UPLOAD_ITEM_STATE_CONFLICT", "上传项状态发生冲突。")
	}
	receivedItem, err := scanUploadItem(tx.QueryRowContext(ctx, `
		SELECT upload_item_id, upload_session_id, client_item_key, kind, original_filename,
			declared_mime_type, declared_size_bytes, received_size_bytes, status, asset_id,
			failure, created_at, completed_at
		FROM upload_items WHERE upload_item_id = ?`, itemID))
	if err != nil {
		restoreAttempt()
		return UploadItem{}, err
	}
	now := s.now()
	if err := completeIdempotency(ctx, tx, meta, receivedItem, now); err != nil {
		restoreAttempt()
		return UploadItem{}, err
	}
	if err := tx.Commit(); err != nil {
		restoreAttempt()
		return UploadItem{}, err
	}
	return receivedItem, nil
}

func (s *Store) CompleteUploadItem(ctx context.Context, itemID string) (AssetResult, error) {
	return s.CompleteUploadItemCommand(ctx, itemID, CommandMeta{})
}

func (s *Store) CompleteUploadItemCommand(
	ctx context.Context,
	itemID string,
	meta CommandMeta,
) (AssetResult, error) {
	item, err := s.getUploadItem(ctx, itemID)
	if err != nil {
		return AssetResult{}, err
	}
	if item.Kind == "archive" {
		return s.completeArchiveUploadItemCommand(ctx, itemID, meta)
	}
	return s.completeUploadItemAssetCommand(ctx, itemID, meta)
}

func (s *Store) completeUploadItemAssetCommand(
	ctx context.Context,
	itemID string,
	meta CommandMeta,
) (AssetResult, error) {
	return s.completeUploadItemAsset(ctx, itemID, meta, nil)
}

func (s *Store) completeUploadItemAsset(ctx context.Context, itemID string, meta CommandMeta, toolOutput *AgentToolCall) (AssetResult, error) {
	var projectID string
	if err := s.db.QueryRowContext(ctx, `
		SELECT us.project_id
		FROM upload_items ui
		JOIN upload_sessions us ON us.upload_session_id = ui.upload_session_id
		WHERE ui.upload_item_id = ?`, itemID).Scan(&projectID); errors.Is(err, sql.ErrNoRows) {
		return AssetResult{}, domainError("UPLOAD_ITEM_NOT_FOUND", "上传项不存在。")
	} else if err != nil {
		return AssetResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AssetResult{}, err
	}
	defer tx.Rollback()
	if meta.Scope == "" {
		meta.Scope = projectID
	}
	cached, hit, err := s.beginIdempotency(ctx, tx, meta)
	if err != nil {
		return AssetResult{}, err
	}
	if hit {
		return decodeIdempotentResult[AssetResult](cached)
	}
	sourceType, metadataJSON := "upload", "{}"
	if toolOutput != nil {
		if err := s.validateAgentTaskToolCallTx(ctx, tx, toolOutput.AgentToolCallID); err != nil {
			return AssetResult{}, err
		}
		call, err := getAgentToolCallTx(ctx, tx, toolOutput.AgentToolCallID)
		if err != nil {
			return AssetResult{}, err
		}
		if call.Status != "running" || (call.ToolKind != "hosted" && call.ToolKind != "mcp") || call.ProjectID != projectID || call.SDKToolCallID != toolOutput.SDKToolCallID {
			return AssetResult{}, domainError("AGENT_TOOL_CALL_STATE_CONFLICT", "工具调用已结束或不属于当前项目，不能提交产物。")
		}
		sourceType = call.ToolKind + "_tool"
		metadata, _ := json.Marshal(map[string]string{"agent_tool_call_id": call.AgentToolCallID, "sdk_tool_call_id": call.SDKToolCallID})
		metadataJSON = string(metadata)
	}

	var item UploadItem
	var stagingRef, checksum, createdAt string
	if err := tx.QueryRowContext(ctx, `
		SELECT ui.upload_item_id, ui.upload_session_id, ui.client_item_key, ui.kind,
			ui.original_filename, ui.declared_mime_type, ui.declared_size_bytes,
			ui.received_size_bytes, ui.status, ui.staging_ref, ui.checksum, ui.created_at,
			us.project_id
		FROM upload_items ui
		JOIN upload_sessions us ON us.upload_session_id = ui.upload_session_id
		WHERE ui.upload_item_id = ?`, itemID).Scan(
		&item.UploadItemID, &item.UploadSessionID, &item.ClientItemKey, &item.Kind,
		&item.OriginalFilename, &item.DeclaredMIMEType, &item.DeclaredSizeBytes,
		&item.ReceivedSizeBytes, &item.Status, &stagingRef, &checksum, &createdAt, &projectID,
	); errors.Is(err, sql.ErrNoRows) {
		return AssetResult{}, domainError("UPLOAD_ITEM_NOT_FOUND", "上传项不存在。")
	} else if err != nil {
		return AssetResult{}, err
	}
	if item.Status != "validating" {
		return AssetResult{}, domainError("UPLOAD_ITEM_STATE_CONFLICT", "上传项尚未完成内容接收。")
	}
	stagingPath, err := resolveDataPath(s.dataRoot, stagingRef)
	if err != nil {
		return AssetResult{}, err
	}
	detectedMIME, err := detectMIME(stagingPath)
	if err != nil {
		return AssetResult{}, err
	}
	if !mimeCompatible(item.Kind, filepath.Ext(item.OriginalFilename), item.DeclaredMIMEType, detectedMIME) {
		_ = os.Remove(stagingPath)
		_ = tx.Rollback()
		s.failUploadItem(ctx, itemID, "ASSET_MIME_MISMATCH")
		return AssetResult{}, domainError("ASSET_MIME_MISMATCH", "文件内容与声明格式不一致。")
	}

	now := s.now()
	assetID := s.newID("ast")
	snapshotID := s.newID("ass")
	blobID := s.newID("blb")
	relativeStorageRef := filepath.ToSlash(filepath.Join("assets", projectID, assetID, "original"))
	finalPath, err := resolveDataPath(s.dataRoot, relativeStorageRef)
	if err != nil {
		return AssetResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return AssetResult{}, err
	}
	if err := os.Rename(stagingPath, finalPath); err != nil {
		return AssetResult{}, fmt.Errorf("commit original asset: %w", err)
	}

	extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(item.OriginalFilename)), ".")
	parseStatus := "pending"
	parsedText := ""
	parserID := ""
	parserVersion := ""
	parseErrorCode := ""
	if item.Kind == "text" {
		content, readErr := os.ReadFile(finalPath)
		if readErr == nil && utf8.Valid(content) {
			parsedText = string(content)
			parserID, parserVersion, parseStatus = "builtin.utf8", "1.0.0", "completed"
		} else {
			parserID, parserVersion, parseStatus, parseErrorCode = "builtin.utf8", "1.0.0", "failed", "ASSET_PARSE_FAILED"
		}
	} else if item.Kind == "document" {
		parserID, parserVersion = "unavailable", "1.0.0"
		if s.documentParser != nil && s.documentParser.Supports("."+extension) {
			parsed, parseErr := s.documentParser.Parse(ctx, finalPath, "."+extension)
			parserID, parserVersion = parsed.ParserID, parsed.Version
			if parseErr == nil {
				parsedText, parseStatus = parsed.Text, "completed"
			} else {
				parseStatus, parseErrorCode = "failed", "ASSET_PARSE_FAILED"
			}
		} else {
			parseStatus, parseErrorCode = "failed", "DOCUMENT_PARSER_UNAVAILABLE"
		}
	} else if item.Kind == "archive" {
		parseStatus = "completed"
	}
	retentionPolicyID := (*string)(nil)
	expiresAt := (*time.Time)(nil)
	if toolOutput == nil && (item.Kind == "video" || item.Kind == "archive") {
		policy := "video_source_7d_v1"
		if item.Kind == "archive" {
			policy = "source_archive_7d_v1"
		}
		retentionPolicyID = &policy
		expires := now.Add(7 * 24 * time.Hour)
		expiresAt = &expires
	}
	snapshotPayload, err := json.Marshal(map[string]any{
		"asset_id":           assetID,
		"project_id":         projectID,
		"kind":               item.Kind,
		"status":             "available",
		"original_blob_id":   blobID,
		"storage_ref":        relativeStorageRef,
		"size_bytes":         item.ReceivedSizeBytes,
		"checksum_algorithm": "sha256",
		"checksum":           checksum,
		"detected_mime_type": detectedMIME,
	})
	if err != nil {
		_ = os.Rename(finalPath, stagingPath)
		return AssetResult{}, err
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO asset_blobs(
			blob_id, project_id, asset_id, role, storage_ref, size_bytes,
			checksum_algorithm, checksum, status, created_at
		) VALUES(?, ?, ?, 'original', ?, ?, 'sha256', ?, 'available', ?)`,
		blobID, projectID, assetID, relativeStorageRef, item.ReceivedSizeBytes, checksum, formatTime(now),
	); err != nil {
		_ = os.Rename(finalPath, stagingPath)
		return AssetResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO assets(
			asset_id, project_id, current_snapshot_id, kind, source_type, display_name,
			original_filename, extension, declared_mime_type, detected_mime_type,
			size_bytes, checksum_algorithm, checksum, status, parse_status, original_blob_id,
			metadata_json, retention_policy_id, uploaded_at, expires_at, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'sha256', ?, 'available', ?, ?, ?, ?, ?, ?, ?, ?)`,
		assetID, projectID, snapshotID, item.Kind, sourceType, item.OriginalFilename, item.OriginalFilename,
		extension, item.DeclaredMIMEType, detectedMIME, item.ReceivedSizeBytes, checksum,
		parseStatus, blobID, metadataJSON, nullableString(retentionPolicyID), formatTime(now), nullableTime(expiresAt),
		formatTime(now), formatTime(now),
	); err != nil {
		_ = os.Rename(finalPath, stagingPath)
		return AssetResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO asset_snapshots(
			asset_snapshot_id, asset_id, project_id, snapshot_version, status, payload_json, created_at
		) VALUES(?, ?, ?, 1, 'available', ?, ?)`,
		snapshotID, assetID, projectID, string(snapshotPayload), formatTime(now),
	); err != nil {
		_ = os.Rename(finalPath, stagingPath)
		return AssetResult{}, err
	}
	if item.Kind == "text" || item.Kind == "document" {
		contentHash := sha256Hex([]byte(parsedText))
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO asset_parse_results(
				asset_parse_result_id, asset_id, asset_snapshot_id, status,
				parser_id, parser_version, content_text, content_hash, error_code, created_at
			) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			s.newID("aprse"), assetID, snapshotID, parseStatus, parserID, parserVersion,
			parsedText, contentHash, nullableStringPointer(parseErrorCode), formatTime(now)); err != nil {
			_ = os.Rename(finalPath, stagingPath)
			return AssetResult{}, err
		}
	}
	if expiresAt != nil && retentionPolicyID != nil {
		job, err := s.createRetentionJobTx(
			ctx,
			tx,
			projectID,
			assetID,
			*retentionPolicyID,
			"expire_video_source",
			*expiresAt,
			now,
		)
		if err != nil {
			_ = os.Rename(finalPath, stagingPath)
			return AssetResult{}, err
		}
		if _, err := s.appendEvent(
			ctx,
			tx,
			projectID,
			nil,
			nil,
			"asset.expiration_scheduled",
			"asset",
			assetID,
			map[string]any{
				"retention_job_id": job.RetentionJobID,
				"policy_id":        job.PolicyID,
				"due_at":           job.DueAt,
			},
		); err != nil {
			_ = os.Rename(finalPath, stagingPath)
			return AssetResult{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE upload_items
		SET status = 'completed', asset_id = ?, completed_at = ?, staging_ref = NULL
		WHERE upload_item_id = ?`,
		assetID, formatTime(now), itemID,
	); err != nil {
		_ = os.Rename(finalPath, stagingPath)
		return AssetResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE upload_sessions
		SET completed_item_count = completed_item_count + 1,
			status = CASE WHEN completed_item_count + 1 = declared_item_count THEN 'completed' ELSE status END,
			closed_at = CASE WHEN completed_item_count + 1 = declared_item_count THEN ? ELSE closed_at END
		WHERE upload_session_id = ?`,
		formatTime(now), item.UploadSessionID,
	); err != nil {
		_ = os.Rename(finalPath, stagingPath)
		return AssetResult{}, err
	}
	if _, err := s.appendEvent(ctx, tx, projectID, nil, nil, "asset.upload_completed", "asset", assetID, map[string]any{"asset_snapshot_id": snapshotID}); err != nil {
		_ = os.Rename(finalPath, stagingPath)
		return AssetResult{}, err
	}
	if item.Kind == "text" || item.Kind == "document" {
		eventType := "asset.parse_completed"
		if parseStatus == "failed" {
			eventType = "asset.parse_failed"
		}
		if _, err := s.appendEvent(ctx, tx, projectID, nil, nil, eventType, "asset", assetID,
			map[string]any{"asset_snapshot_id": snapshotID, "parser_id": parserID, "error_code": parseErrorCode}); err != nil {
			_ = os.Rename(finalPath, stagingPath)
			return AssetResult{}, err
		}
	}
	asset := Asset{
		AssetID:           assetID,
		ProjectID:         projectID,
		CurrentSnapshotID: snapshotID,
		Kind:              item.Kind,
		SourceType:        sourceType,
		DisplayName:       item.OriginalFilename,
		OriginalFilename:  item.OriginalFilename,
		Extension:         extension,
		DeclaredMIMEType:  item.DeclaredMIMEType,
		DetectedMIMEType:  detectedMIME,
		SizeBytes:         item.ReceivedSizeBytes,
		ChecksumAlgorithm: "sha256",
		Checksum:          checksum,
		Status:            "available",
		ParseStatus:       parseStatus,
		OriginalBlobID:    blobID,
		Metadata:          json.RawMessage(metadataJSON),
		RetentionPolicyID: retentionPolicyID,
		UploadedAt:        now,
		ExpiresAt:         expiresAt,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	snapshot := AssetSnapshot{
		AssetSnapshotID: snapshotID,
		AssetID:         assetID,
		ProjectID:       projectID,
		SnapshotVersion: 1,
		Status:          "available",
		Payload:         snapshotPayload,
		CreatedAt:       now,
	}
	result := AssetResult{Asset: asset, Snapshot: snapshot}
	if err := completeIdempotency(ctx, tx, meta, result, now); err != nil {
		_ = os.Rename(finalPath, stagingPath)
		return AssetResult{}, err
	}
	if err := tx.Commit(); err != nil {
		_ = os.Rename(finalPath, stagingPath)
		return AssetResult{}, err
	}
	return result, nil
}

func (s *Store) ListAssets(ctx context.Context, projectID string) ([]Asset, error) {
	if _, err := s.GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, assetSelect+` WHERE project_id = ? ORDER BY updated_at DESC, asset_id ASC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Asset
	for rows.Next() {
		asset, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, asset)
	}
	if result == nil {
		result = []Asset{}
	}
	return result, rows.Err()
}

func (s *Store) GetAsset(ctx context.Context, assetID string) (Asset, error) {
	asset, err := scanAsset(s.db.QueryRowContext(ctx, assetSelect+` WHERE asset_id = ?`, assetID))
	if errors.Is(err, sql.ErrNoRows) {
		return Asset{}, domainError("ASSET_NOT_FOUND", "材料不存在。")
	}
	return asset, err
}

func (s *Store) GetAssetSnapshot(ctx context.Context, snapshotID string) (AssetSnapshot, error) {
	var snapshot AssetSnapshot
	var payloadJSON, createdAt string
	if err := s.db.QueryRowContext(ctx, `
		SELECT asset_snapshot_id, asset_id, project_id, snapshot_version, status, payload_json, created_at
		FROM asset_snapshots WHERE asset_snapshot_id = ?`, snapshotID).Scan(
		&snapshot.AssetSnapshotID, &snapshot.AssetID, &snapshot.ProjectID, &snapshot.SnapshotVersion,
		&snapshot.Status, &payloadJSON, &createdAt,
	); errors.Is(err, sql.ErrNoRows) {
		return AssetSnapshot{}, domainError("ASSET_SNAPSHOT_NOT_FOUND", "材料快照不存在。")
	} else if err != nil {
		return AssetSnapshot{}, err
	}
	snapshot.Payload = json.RawMessage(payloadJSON)
	var err error
	snapshot.CreatedAt, err = parseTime(createdAt)
	return snapshot, err
}

func (s *Store) GetParsedAssetText(ctx context.Context, assetID, snapshotID string) (string, error) {
	var currentSnapshotID, kind, assetStatus, parseStatus, resultStatus, content string
	err := s.db.QueryRowContext(ctx, `
		SELECT a.current_snapshot_id, a.kind, a.status, a.parse_status,
			apr.status, apr.content_text
		FROM assets a
		JOIN asset_parse_results apr
			ON apr.asset_id = a.asset_id AND apr.asset_snapshot_id = ?
		WHERE a.asset_id = ?`, snapshotID, assetID).Scan(
		&currentSnapshotID, &kind, &assetStatus, &parseStatus, &resultStatus, &content,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domainError("ASSET_PARSE_RESULT_NOT_FOUND", "材料的文本解析结果不存在。")
	}
	if err != nil {
		return "", err
	}
	if currentSnapshotID != snapshotID || assetStatus != "available" || parseStatus != "completed" || resultStatus != "completed" {
		return "", domainError("ASSET_SOURCE_UNAVAILABLE", "材料的当前文本版本不可用。")
	}
	if kind != "text" && kind != "document" {
		return "", domainError("ASSET_KIND_UNSUPPORTED", "该材料不是可读取的文本或文档。")
	}
	return content, nil
}

func (s *Store) getUploadItem(ctx context.Context, itemID string) (UploadItem, error) {
	item, err := scanUploadItem(s.db.QueryRowContext(ctx, `
		SELECT upload_item_id, upload_session_id, client_item_key, kind, original_filename,
			declared_mime_type, declared_size_bytes, received_size_bytes, status, asset_id,
			failure, created_at, completed_at
		FROM upload_items WHERE upload_item_id = ?`, itemID))
	if errors.Is(err, sql.ErrNoRows) {
		return UploadItem{}, domainError("UPLOAD_ITEM_NOT_FOUND", "上传项不存在。")
	}
	return item, err
}

func (s *Store) failUploadItem(ctx context.Context, itemID, reason string) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	var sessionID, currentStatus string
	if err := tx.QueryRowContext(ctx, `
		SELECT upload_session_id, status FROM upload_items WHERE upload_item_id = ?`,
		itemID,
	).Scan(&sessionID, &currentStatus); err != nil || currentStatus == "failed" {
		return
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE upload_items SET status = 'failed', failure = ? WHERE upload_item_id = ?`,
		reason, itemID,
	); err != nil {
		return
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE upload_sessions
		SET failed_item_count = failed_item_count + 1,
			status = CASE
				WHEN completed_item_count > 0 THEN 'partial_failed'
				WHEN failed_item_count + 1 = declared_item_count THEN 'failed'
				ELSE status
			END
		WHERE upload_session_id = ?`,
		sessionID,
	); err != nil {
		return
	}
	_ = tx.Commit()
}

func validateUploadSpec(spec UploadItemSpec) error {
	if strings.TrimSpace(spec.ClientItemKey) == "" || spec.OriginalFilename != filepath.Base(spec.OriginalFilename) ||
		spec.DeclaredSizeBytes <= 0 || spec.DeclaredMIMEType == "" {
		return domainError("REQUEST_VALIDATION_FAILED", "上传项声明不完整或文件名不安全。")
	}
	extension := strings.ToLower(filepath.Ext(spec.OriginalFilename))
	expectedKind, ok := allowedExtensions[extension]
	if !ok || expectedKind != spec.Kind {
		return domainError("ASSET_TYPE_NOT_SUPPORTED", "文件格式或材料类型不受支持。")
	}
	if spec.DeclaredSizeBytes > maxBytesForKind(spec.Kind) {
		return domainError("ASSET_FILE_TOO_LARGE", "文件超过当前上传限制。")
	}
	return nil
}

func maxBytesForKind(kind string) int64 {
	switch kind {
	case "archive":
		return maxArchiveBytes
	case "video":
		return maxVideoBytes
	case "image":
		return maxImageBytes
	default:
		return maxTextBytes
	}
}

func detectMIME(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	buffer := make([]byte, 512)
	count, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return "", err
	}
	return http.DetectContentType(buffer[:count]), nil
}

func mimeCompatible(kind, extension, declared, detected string) bool {
	declaredBase, _, _ := mime.ParseMediaType(declared)
	detectedBase, _, _ := mime.ParseMediaType(detected)
	extension = strings.ToLower(extension)
	switch kind {
	case "text":
		declaredText := declaredBase == "text/plain" || declaredBase == "text/markdown" ||
			(extension == ".csv" && declaredBase == "text/csv") || (extension == ".json" && declaredBase == "application/json")
		return declaredText && detectedBase == "text/plain"
	case "document":
		if extension == ".pdf" {
			return declaredBase == "application/pdf" && detectedBase == "application/pdf"
		}
		return extension == ".docx" && declaredBase == "application/vnd.openxmlformats-officedocument.wordprocessingml.document" &&
			(detectedBase == "application/zip" || detectedBase == "application/octet-stream")
	case "image":
		return strings.HasPrefix(declaredBase, "image/") && strings.HasPrefix(detectedBase, "image/")
	case "video":
		return strings.HasPrefix(declaredBase, "video/") &&
			(strings.HasPrefix(detectedBase, "video/") || detectedBase == "application/octet-stream")
	case "archive":
		return extension == ".zip" &&
			(declaredBase == "application/zip" || declaredBase == "application/x-zip-compressed" || declaredBase == "application/octet-stream") &&
			(detectedBase == "application/zip" || detectedBase == "application/octet-stream")
	default:
		return false
	}
}

func resolveDataPath(dataRoot, reference string) (string, error) {
	target, err := filepath.Abs(filepath.Join(dataRoot, filepath.FromSlash(reference)))
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(dataRoot)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", domainError("STORAGE_REF_INVALID", "存储引用越过数据目录。")
	}
	return target, nil
}

func scanUploadItem(row rowScanner) (UploadItem, error) {
	var item UploadItem
	var assetID, failure, completedAt sql.NullString
	var createdAt string
	if err := row.Scan(
		&item.UploadItemID, &item.UploadSessionID, &item.ClientItemKey, &item.Kind,
		&item.OriginalFilename, &item.DeclaredMIMEType, &item.DeclaredSizeBytes,
		&item.ReceivedSizeBytes, &item.Status, &assetID, &failure, &createdAt, &completedAt,
	); err != nil {
		return UploadItem{}, err
	}
	item.AssetID = stringPointer(assetID)
	item.Failure = stringPointer(failure)
	var err error
	item.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return UploadItem{}, err
	}
	item.CompletedAt, err = optionalTime(completedAt)
	return item, err
}

const assetSelect = `
	SELECT asset_id, project_id, current_snapshot_id, kind, source_type, display_name,
		original_filename, extension, declared_mime_type, detected_mime_type, size_bytes,
		checksum_algorithm, checksum, status, parse_status, original_blob_id, metadata_json,
		retention_policy_id, uploaded_at, expires_at, created_at, updated_at, deleted_at, delete_reason
	FROM assets`

func scanAsset(row rowScanner) (Asset, error) {
	var asset Asset
	var metadataJSON, uploadedAt, createdAt, updatedAt string
	var retentionPolicyID, expiresAt, deletedAt, deleteReason sql.NullString
	if err := row.Scan(
		&asset.AssetID, &asset.ProjectID, &asset.CurrentSnapshotID, &asset.Kind, &asset.SourceType,
		&asset.DisplayName, &asset.OriginalFilename, &asset.Extension, &asset.DeclaredMIMEType,
		&asset.DetectedMIMEType, &asset.SizeBytes, &asset.ChecksumAlgorithm, &asset.Checksum,
		&asset.Status, &asset.ParseStatus, &asset.OriginalBlobID, &metadataJSON, &retentionPolicyID,
		&uploadedAt, &expiresAt, &createdAt, &updatedAt, &deletedAt, &deleteReason,
	); err != nil {
		return Asset{}, err
	}
	asset.Metadata = json.RawMessage(metadataJSON)
	asset.RetentionPolicyID = stringPointer(retentionPolicyID)
	asset.DeleteReason = stringPointer(deleteReason)
	var err error
	asset.UploadedAt, err = parseTime(uploadedAt)
	if err != nil {
		return Asset{}, err
	}
	asset.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return Asset{}, err
	}
	asset.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return Asset{}, err
	}
	asset.ExpiresAt, err = optionalTime(expiresAt)
	if err != nil {
		return Asset{}, err
	}
	asset.DeletedAt, err = optionalTime(deletedAt)
	return asset, err
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}
