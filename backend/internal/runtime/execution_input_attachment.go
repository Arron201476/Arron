package runtime

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"content-agent/backend/internal/agentcontract"
)

const maxInputAttachmentBytes = 5 << 20
const maxExecutionAttachmentBytes = 8 << 20

func validAdditionalInputEnvelope(content string, attachmentCount int) bool {
	return (content != "" || attachmentCount > 0) && len(content) <= 32<<10 && utf8.ValidString(content) && !strings.ContainsRune(content, 0)
}

func migrateExecutionInputAttachments(db *sql.DB) error {
	for _, table := range []string{"agent_turn_inputs", "agent_task_inputs", "execution_inputs"} {
		present, err := tableHasColumn(db, table, "attachments_json")
		if err != nil {
			return err
		}
		if !present {
			if _, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN attachments_json TEXT NOT NULL DEFAULT '[]'`); err != nil {
				return err
			}
		}
	}
	return nil
}

func validInputAttachment(item agentcontract.ExecutionInputAttachment) bool {
	for _, value := range []string{item.AssetID, item.AssetSnapshotID, item.Kind, item.Name, item.MIMEType, item.Checksum} {
		if value == "" || len(value) > 1024 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return false
		}
	}
	if item.Kind == "text" || item.Kind == "document" {
		textDigest, err := hex.DecodeString(item.TextHash)
		if err != nil || len(textDigest) != 32 || strings.ToLower(item.TextHash) != item.TextHash {
			return false
		}
	} else if item.TextHash != "" {
		return false
	}
	digest, err := hex.DecodeString(item.Checksum)
	return err == nil && len(digest) == 32 && strings.ToLower(item.Checksum) == item.Checksum && item.SizeBytes > 0 && item.SizeBytes <= maxInputAttachmentBytes &&
		containsString([]string{"text", "document", "image", "video", "archive"}, item.Kind)
}

func decodeInputAttachments(raw string) ([]agentcontract.ExecutionInputAttachment, error) {
	var items []agentcontract.ExecutionInputAttachment
	if len(raw) > 32<<10 || json.Unmarshal([]byte(raw), &items) != nil || items == nil || len(items) > 4 {
		return nil, domainError("AGENT_RUN_STATE_CORRUPT", "追加附件快照无效。")
	}
	seen, size := map[string]bool{}, int64(0)
	for _, item := range items {
		if !validInputAttachment(item) || seen[item.AssetID] {
			return nil, domainError("AGENT_RUN_STATE_CORRUPT", "追加附件身份或大小无效。")
		}
		seen[item.AssetID] = true
		size += item.SizeBytes
	}
	if size > maxExecutionAttachmentBytes {
		return nil, domainError("AGENT_RUN_STATE_CORRUPT", "追加附件超过执行上限。")
	}
	return items, nil
}

// Keep old text-only checkpoint digests unchanged; attached inputs bind every
// snapshot field with an unambiguous, cross-language NUL-delimited encoding.
func executionInputContentHash(content string, items []agentcontract.ExecutionInputAttachment) string {
	if len(items) == 0 {
		return sha256Hex([]byte(content))
	}
	fields := []string{content}
	for _, item := range items {
		fields = append(fields, item.AssetID, item.AssetSnapshotID, item.Kind, item.Name, item.MIMEType, item.Checksum, strconv.FormatInt(item.SizeBytes, 10))
		if item.TextHash != "" {
			fields = append(fields, item.TextHash)
		}
	}
	return sha256Hex([]byte(strings.Join(fields, "\x00")))
}

func (s *Store) freezeInputAttachmentsTx(ctx context.Context, tx *sql.Tx, project, table, ownerColumn, ownerID string, refs []agentcontract.AttachmentRef) ([]agentcontract.ExecutionInputAttachment, string, error) {
	items := []agentcontract.ExecutionInputAttachment{}
	if len(refs) > 4 {
		return nil, "", domainError("REQUEST_VALIDATION_FAILED", "每条追加要求最多附带 4 份材料。")
	}
	seen, addedBytes := map[string]bool{}, int64(0)
	for _, ref := range refs {
		if ref.AssetID == "" || ref.AssetSnapshotID == "" || seen[ref.AssetID] {
			return nil, "", domainError("REQUEST_VALIDATION_FAILED", "追加附件引用缺失或重复。")
		}
		seen[ref.AssetID] = true
		if err := validateAssetOwnership(ctx, tx, project, []assetInputReference{{AssetID: ref.AssetID, AssetSnapshotID: ref.AssetSnapshotID}}); err != nil {
			return nil, "", err
		}
		asset, err := scanAsset(tx.QueryRowContext(ctx, assetSelect+` WHERE asset_id=?`, ref.AssetID))
		if err != nil {
			return nil, "", err
		}
		if asset.CurrentSnapshotID != ref.AssetSnapshotID || asset.ChecksumAlgorithm != "sha256" || asset.DeletedAt != nil || (asset.ExpiresAt != nil && !asset.ExpiresAt.After(s.now())) {
			return nil, "", domainError("ASSET_SOURCE_UNAVAILABLE", "追加附件快照已变化或不可用。")
		}
		item := agentcontract.ExecutionInputAttachment{AssetID: asset.AssetID, AssetSnapshotID: asset.CurrentSnapshotID, Kind: asset.Kind, Name: asset.OriginalFilename, MIMEType: asset.DetectedMIMEType, Checksum: asset.Checksum, SizeBytes: asset.SizeBytes}
		if asset.Kind == "text" || asset.Kind == "document" {
			err := tx.QueryRowContext(ctx, `SELECT content_hash FROM asset_parse_results WHERE asset_id=? AND asset_snapshot_id=? AND status='completed'`, asset.AssetID, asset.CurrentSnapshotID).Scan(&item.TextHash)
			if errors.Is(err, sql.ErrNoRows) || asset.ParseStatus != "completed" {
				return nil, "", domainError("ASSET_SOURCE_UNAVAILABLE", "追加材料尚无可用的文本解析版本。")
			}
			if err != nil {
				return nil, "", err
			}
		}
		if !validInputAttachment(item) {
			return nil, "", domainError("REQUEST_VALIDATION_FAILED", "追加附件不可用或超过单文件 5 MiB 上限。")
		}
		var blobAvailable bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM asset_blobs WHERE blob_id=? AND asset_id=? AND project_id=? AND role='original' AND status='available' AND checksum_algorithm='sha256' AND checksum=? AND size_bytes=?)`, asset.OriginalBlobID, asset.AssetID, project, asset.Checksum, asset.SizeBytes).Scan(&blobAvailable); err != nil {
			return nil, "", err
		}
		if !blobAvailable {
			return nil, "", domainError("ASSET_SOURCE_UNAVAILABLE", "追加附件原文件记录不可用或与材料快照不一致。")
		}
		items = append(items, item)
		addedBytes += item.SizeBytes
	}
	if len(items) > 0 {
		var existing int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(CAST(json_extract(j.value,'$.size_bytes') AS INTEGER)),0) FROM `+table+` i,json_each(i.attachments_json) j WHERE i.`+ownerColumn+`=?`, ownerID).Scan(&existing); err != nil {
			return nil, "", err
		}
		if existing+addedBytes > maxExecutionAttachmentBytes {
			return nil, "", domainError("REQUEST_VALIDATION_FAILED", "本次执行的追加附件累计不能超过 8 MiB，撤回和修订保留历史计入上限。")
		}
	}
	raw, err := json.Marshal(items)
	return items, string(raw), err
}
