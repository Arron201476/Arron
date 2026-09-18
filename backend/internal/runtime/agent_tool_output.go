package runtime

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"mime"
	"path/filepath"
	"strings"
)

// StoreAgentToolOutput reuses project asset quotas, storage, download authorization
// and deletion propagation instead of maintaining an independent output store.
func (s *Store) StoreAgentToolOutput(ctx context.Context, callID, sdkCallID, filename string, content []byte) (AssetResult, error) {
	if len(content) == 0 || len(content) > maxTextBytes || len(filename) == 0 || len(filename) > 200 ||
		strings.ContainsAny(filename, "/\\:\x00") || filename != filepath.Base(filename) || filename == "." || filename == ".." {
		return AssetResult{}, domainError("REQUEST_VALIDATION_FAILED", "工具产物文件名或大小无效。")
	}
	call, err := s.GetAgentToolCall(ctx, callID)
	if err != nil {
		return AssetResult{}, err
	}
	if (call.ToolKind != "hosted" && call.ToolKind != "mcp") || call.SDKToolCallID != sdkCallID || call.Status != "running" {
		return AssetResult{}, domainError("AGENT_TOOL_CALL_STATE_CONFLICT", "工具调用未运行或身份不匹配，不能写入产物。")
	}
	digest := sha256.Sum256(append([]byte(filename+"\x00"), content...))
	key := "tool-output:" + callID + ":" + hex.EncodeToString(digest[:])
	meta := func(command string) CommandMeta {
		return CommandMeta{Scope: call.ProjectID, CommandType: command, IdempotencyKey: key + ":" + command, RequestHash: hex.EncodeToString(digest[:])}
	}
	extension := strings.ToLower(filepath.Ext(filename))
	kind := allowedExtensions[extension]
	if kind == "" {
		// Keep arbitrary generated files downloadable without making them executable
		// or teaching the document parser every Code Interpreter output format.
		var archive bytes.Buffer
		writer := zip.NewWriter(&archive)
		file, err := writer.Create(filename)
		if err != nil {
			return AssetResult{}, err
		}
		if _, err := file.Write(content); err != nil {
			return AssetResult{}, err
		}
		if err := writer.Close(); err != nil {
			return AssetResult{}, err
		}
		content, filename, extension, kind = archive.Bytes(), filename+".zip", ".zip", "archive"
	}
	contentType := mime.TypeByExtension(extension)
	if kind == "text" {
		contentType = "text/plain"
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	session, err := s.CreateUploadSessionCommand(ctx, call.ProjectID, []UploadItemSpec{{
		ClientItemKey: key, Kind: kind, OriginalFilename: filename,
		DeclaredMIMEType: contentType, DeclaredSizeBytes: int64(len(content)),
	}}, meta("create_tool_output"))
	if err != nil {
		return AssetResult{}, err
	}
	if len(session.Items) != 1 {
		return AssetResult{}, fmt.Errorf("tool output upload contains %d items", len(session.Items))
	}
	itemID := session.Items[0].UploadItemID
	if _, err := s.WriteUploadContentCommand(ctx, itemID, bytes.NewReader(content), meta("write_tool_output")); err != nil {
		return AssetResult{}, err
	}
	call, err = s.GetAgentToolCall(ctx, callID)
	if err != nil {
		return AssetResult{}, err
	}
	if call.Status != "running" {
		return AssetResult{}, domainError("AGENT_TOOL_CALL_STATE_CONFLICT", "工具调用已结束，不能提交产物。")
	}
	// Generated archives are outputs, not material bundles to auto-extract.
	result, err := s.completeUploadItemAsset(ctx, itemID, meta("complete_tool_output"), &call)
	if err != nil {
		return AssetResult{}, err
	}
	return result, nil
}
