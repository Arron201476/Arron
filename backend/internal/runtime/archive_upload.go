package runtime

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxArchiveEntries           = 100
	maxArchiveUncompressedBytes = 1 << 30
)

type preparedArchiveEntry struct {
	path     string
	filePath string
	kind     string
	mimeType string
	size     int64
}

type archiveAssetMetadata struct {
	ExpandedAssetIDs []string `json:"expanded_asset_ids,omitempty"`
	IgnoredEntries   []string `json:"ignored_entries,omitempty"`
	EntryCount       int      `json:"entry_count"`
}

func (s *Store) completeArchiveUploadItemCommand(ctx context.Context, itemID string, meta CommandMeta) (AssetResult, error) {
	item, err := s.getUploadItem(ctx, itemID)
	if err != nil {
		return AssetResult{}, err
	}
	if item.Kind != "archive" {
		return AssetResult{}, domainError("ASSET_TYPE_NOT_SUPPORTED", "该上传项不是 ZIP 文件。")
	}
	if item.Status == "completed" && item.AssetID != nil {
		if cached, ok, loadErr := s.loadExpandedArchiveResult(ctx, *item.AssetID); loadErr != nil {
			return AssetResult{}, loadErr
		} else if ok {
			return cached, nil
		}
	}

	sourcePath, err := s.archiveUploadSourcePath(ctx, item)
	if err != nil {
		return AssetResult{}, err
	}
	tempRoot, err := os.MkdirTemp(filepath.Join(s.dataRoot, "staging"), itemID+".archive.*")
	if err != nil {
		return AssetResult{}, fmt.Errorf("create archive staging directory: %w", err)
	}
	defer os.RemoveAll(tempRoot)
	entries, ignored, err := prepareArchiveEntries(sourcePath, tempRoot)
	if err != nil {
		if item.Status != "completed" {
			_ = os.Remove(sourcePath)
			s.failUploadItem(ctx, itemID, archiveFailureCode(err))
		}
		return AssetResult{}, err
	}

	var parent AssetResult
	if item.Status == "completed" && item.AssetID != nil {
		parent, err = s.assetResult(ctx, *item.AssetID)
	} else {
		parent, err = s.completeUploadItemAssetCommand(ctx, itemID, CommandMeta{})
	}
	if err != nil {
		return AssetResult{}, err
	}

	specs := make([]UploadItemSpec, len(entries))
	for index, entry := range entries {
		specs[index] = UploadItemSpec{
			ClientItemKey: fmt.Sprintf("archive-entry-%04d", index+1), Kind: entry.kind,
			OriginalFilename: filepath.Base(filepath.FromSlash(entry.path)),
			DeclaredMIMEType: entry.mimeType, DeclaredSizeBytes: entry.size,
		}
	}
	session, err := s.CreateUploadSession(ctx, parent.Asset.ProjectID, specs)
	if err != nil {
		return AssetResult{}, err
	}
	extracted := make([]AssetResult, 0, len(entries))
	for index, entry := range entries {
		file, openErr := os.Open(entry.filePath)
		if openErr != nil {
			return AssetResult{}, openErr
		}
		_, writeErr := s.WriteUploadContent(ctx, session.Items[index].UploadItemID, file)
		closeErr := file.Close()
		if writeErr != nil {
			return AssetResult{}, writeErr
		}
		if closeErr != nil {
			return AssetResult{}, closeErr
		}
		child, completeErr := s.completeUploadItemAssetCommand(ctx, session.Items[index].UploadItemID, CommandMeta{})
		if completeErr != nil {
			return AssetResult{}, completeErr
		}
		if updateErr := s.markArchiveChild(ctx, &child, parent.Asset.AssetID, entry.path); updateErr != nil {
			return AssetResult{}, updateErr
		}
		extracted = append(extracted, child)
	}
	if err := s.markArchiveExpanded(ctx, &parent, extracted, ignored); err != nil {
		return AssetResult{}, err
	}
	parent.ExtractedAssets = extracted
	parent.IgnoredEntries = ignored
	return parent, nil
}

func prepareArchiveEntries(sourcePath, tempRoot string) ([]preparedArchiveEntry, []string, error) {
	reader, err := zip.OpenReader(sourcePath)
	if err != nil {
		return nil, nil, domainError("ARCHIVE_INVALID", "ZIP 文件损坏或无法读取。")
	}
	defer reader.Close()
	files := append([]*zip.File(nil), reader.File...)
	sort.SliceStable(files, func(left, right int) bool {
		return archiveEntryLess(files[left].Name, files[right].Name)
	})
	entries := make([]preparedArchiveEntry, 0, len(files))
	ignored := make([]string, 0)
	var total int64
	for _, file := range files {
		name := filepath.ToSlash(strings.TrimSpace(file.Name))
		if name == "" || file.FileInfo().IsDir() || archiveSystemEntry(name) {
			continue
		}
		if file.Flags&0x1 != 0 {
			return nil, nil, domainError("ARCHIVE_ENCRYPTED", "暂不支持加密 ZIP，请解密后重新上传。")
		}
		if file.Mode()&os.ModeSymlink != 0 || !safeArchiveEntryPath(name) {
			return nil, nil, domainError("ARCHIVE_ENTRY_UNSAFE", "ZIP 中包含不安全的文件路径。")
		}
		extension := strings.ToLower(filepath.Ext(name))
		kind, supported := allowedExtensions[extension]
		if !supported || kind == "archive" {
			ignored = append(ignored, name)
			continue
		}
		if int64(file.UncompressedSize64) <= 0 || int64(file.UncompressedSize64) > maxBytesForKind(kind) {
			return nil, nil, domainError("ARCHIVE_ENTRY_TOO_LARGE", "ZIP 中有文件超过对应类型的上传限制。")
		}
		total += int64(file.UncompressedSize64)
		if total > maxArchiveUncompressedBytes {
			return nil, nil, domainError("ARCHIVE_EXPANDED_TOO_LARGE", "ZIP 解压后的总大小超过当前限制。")
		}
		if len(entries) >= maxArchiveEntries {
			return nil, nil, domainError("ARCHIVE_TOO_MANY_FILES", "ZIP 中可用文件数量超过当前限制。")
		}
		target := filepath.Join(tempRoot, fmt.Sprintf("%04d%s", len(entries)+1, extension))
		input, openErr := file.Open()
		if openErr != nil {
			return nil, nil, domainError("ARCHIVE_INVALID", "ZIP 中有文件无法读取。")
		}
		output, createErr := os.Create(target)
		if createErr != nil {
			input.Close()
			return nil, nil, createErr
		}
		written, copyErr := io.Copy(output, io.LimitReader(input, maxBytesForKind(kind)+1))
		inputCloseErr := input.Close()
		outputCloseErr := output.Close()
		if copyErr != nil || inputCloseErr != nil || outputCloseErr != nil || written != int64(file.UncompressedSize64) {
			return nil, nil, domainError("ARCHIVE_INVALID", "ZIP 中有文件解压失败。")
		}
		detected, detectErr := detectMIME(target)
		if detectErr != nil {
			return nil, nil, detectErr
		}
		declared := declaredMIMEForExtension(extension)
		if !mimeCompatible(kind, extension, declared, detected) {
			return nil, nil, domainError("ARCHIVE_ENTRY_MIME_MISMATCH", "ZIP 中有文件内容与扩展名不一致。")
		}
		entries = append(entries, preparedArchiveEntry{path: name, filePath: target, kind: kind, mimeType: declared, size: written})
	}
	if len(entries) == 0 {
		return nil, ignored, domainError("ARCHIVE_NO_SUPPORTED_FILES", "ZIP 中没有可用的图片、视频或文本文件。")
	}
	return entries, ignored, nil
}

func (s *Store) archiveUploadSourcePath(ctx context.Context, item UploadItem) (string, error) {
	if item.Status == "completed" && item.AssetID != nil {
		var storageRef string
		if err := s.db.QueryRowContext(ctx, `SELECT storage_ref FROM asset_blobs WHERE asset_id = ? AND role = 'original' AND status = 'available'`, *item.AssetID).Scan(&storageRef); err != nil {
			return "", err
		}
		return resolveDataPath(s.dataRoot, storageRef)
	}
	var stagingRef string
	if err := s.db.QueryRowContext(ctx, `SELECT staging_ref FROM upload_items WHERE upload_item_id = ? AND status = 'validating'`, item.UploadItemID).Scan(&stagingRef); errors.Is(err, sql.ErrNoRows) {
		return "", domainError("UPLOAD_ITEM_STATE_CONFLICT", "ZIP 上传项尚未完成内容接收。")
	} else if err != nil {
		return "", err
	}
	return resolveDataPath(s.dataRoot, stagingRef)
}

func (s *Store) assetResult(ctx context.Context, assetID string) (AssetResult, error) {
	asset, err := s.GetAsset(ctx, assetID)
	if err != nil {
		return AssetResult{}, err
	}
	snapshot, err := s.GetAssetSnapshot(ctx, asset.CurrentSnapshotID)
	if err != nil {
		return AssetResult{}, err
	}
	return AssetResult{Asset: asset, Snapshot: snapshot}, nil
}

func (s *Store) markArchiveChild(ctx context.Context, result *AssetResult, parentAssetID, entryPath string) error {
	metadata, err := json.Marshal(map[string]any{"container_asset_id": parentAssetID, "archive_entry_path": entryPath})
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE assets SET source_type = 'archive_entry', display_name = ?, metadata_json = ?, updated_at = ? WHERE asset_id = ?`, entryPath, string(metadata), formatTime(s.now()), result.Asset.AssetID); err != nil {
		return err
	}
	result.Asset.SourceType = "archive_entry"
	result.Asset.DisplayName = entryPath
	result.Asset.Metadata = metadata
	return nil
}

func (s *Store) markArchiveExpanded(ctx context.Context, parent *AssetResult, children []AssetResult, ignored []string) error {
	ids := make([]string, len(children))
	for index := range children {
		ids[index] = children[index].Asset.AssetID
	}
	metadata, err := json.Marshal(archiveAssetMetadata{ExpandedAssetIDs: ids, IgnoredEntries: ignored, EntryCount: len(children)})
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE assets SET metadata_json = ?, updated_at = ? WHERE asset_id = ?`, string(metadata), formatTime(s.now()), parent.Asset.AssetID); err != nil {
		return err
	}
	parent.Asset.Metadata = metadata
	return nil
}

func (s *Store) loadExpandedArchiveResult(ctx context.Context, parentAssetID string) (AssetResult, bool, error) {
	parent, err := s.assetResult(ctx, parentAssetID)
	if err != nil {
		return AssetResult{}, false, err
	}
	var metadata archiveAssetMetadata
	if len(parent.Asset.Metadata) == 0 || json.Unmarshal(parent.Asset.Metadata, &metadata) != nil || len(metadata.ExpandedAssetIDs) == 0 {
		return parent, false, nil
	}
	parent.ExtractedAssets = make([]AssetResult, 0, len(metadata.ExpandedAssetIDs))
	for _, assetID := range metadata.ExpandedAssetIDs {
		child, childErr := s.assetResult(ctx, assetID)
		if childErr != nil {
			return AssetResult{}, false, childErr
		}
		parent.ExtractedAssets = append(parent.ExtractedAssets, child)
	}
	parent.IgnoredEntries = metadata.IgnoredEntries
	return parent, true, nil
}

func safeArchiveEntryPath(name string) bool {
	cleaned := filepath.Clean(filepath.FromSlash(name))
	return cleaned != "." && cleaned != ".." && !filepath.IsAbs(cleaned) && !strings.HasPrefix(cleaned, ".."+string(filepath.Separator))
}

func archiveSystemEntry(name string) bool {
	base := filepath.Base(filepath.FromSlash(name))
	return strings.HasPrefix(name, "__MACOSX/") || base == ".DS_Store" || base == "Thumbs.db"
}

func archiveEntryLess(left, right string) bool {
	leftCandidate := DetectEpisodeFilenameCandidate(filepath.Base(filepath.FromSlash(left)))
	rightCandidate := DetectEpisodeFilenameCandidate(filepath.Base(filepath.FromSlash(right)))
	if leftCandidate.EpisodeNo != nil && rightCandidate.EpisodeNo != nil && *leftCandidate.EpisodeNo != *rightCandidate.EpisodeNo {
		return *leftCandidate.EpisodeNo < *rightCandidate.EpisodeNo
	}
	if leftCandidate.EpisodeNo != nil && rightCandidate.EpisodeNo == nil {
		return true
	}
	if leftCandidate.EpisodeNo == nil && rightCandidate.EpisodeNo != nil {
		return false
	}
	return strings.ToLower(left) < strings.ToLower(right)
}

func declaredMIMEForExtension(extension string) string {
	if value := mime.TypeByExtension(extension); value != "" {
		return value
	}
	switch extension {
	case ".md":
		return "text/markdown"
	case ".mov":
		return "video/quicktime"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	default:
		return "application/octet-stream"
	}
}

func archiveFailureCode(err error) string {
	var domain *DomainError
	if errors.As(err, &domain) {
		return domain.Code
	}
	return "ARCHIVE_EXPANSION_FAILED"
}
