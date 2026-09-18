package scriptsandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxWorkspaceFiles = 256
const maxWorkspaceEntries = 1024
const workspaceArchiveOverhead = 2 << 20

//go:embed workspace_transfer.py
var workspaceTransferHelper string

type WorkspaceEntry struct {
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
	Mode      int64  `json:"mode"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256,omitempty"`
}

type WorkspaceSnapshot struct {
	Archive   []byte           `json:"-"`
	SHA256    string           `json:"sha256"`
	SizeBytes int64            `json:"size_bytes"`
	Entries   []WorkspaceEntry `json:"entries"`
}

type workspaceArchiveEntry struct {
	WorkspaceEntry
	content []byte
}

func validWorkspaceEntryPath(name string) bool {
	if !utf8.ValidString(name) || !safeArtifactPath(name) || strings.ContainsAny(name, "\\<>\"|?*") {
		return false
	}
	for _, char := range name {
		if unicode.IsControl(char) {
			return false
		}
	}
	for _, segment := range strings.Split(name, "/") {
		if windowsDeviceName(strings.SplitN(segment, ".", 2)[0]) {
			return false
		}
	}
	return true
}

// ParseWorkspaceSnapshot validates an untrusted portable tar stream and produces
// a canonical archive without links, device files, owners, xattrs or host paths.
func ParseWorkspaceSnapshot(data []byte, limits Limits) (WorkspaceSnapshot, error) {
	if limits.Validate() != nil || len(data) < 1024 || int64(len(data)) > limits.DiskBytes+workspaceArchiveOverhead {
		return WorkspaceSnapshot{}, workspaceError("WORKSPACE_ARCHIVE_LIMIT_EXCEEDED", "Workspace archive size or limits are invalid")
	}
	if len(data)%512 != 0 || !allZero(data[len(data)-1024:]) {
		return WorkspaceSnapshot{}, workspaceError("WORKSPACE_ARCHIVE_INVALID", "Workspace archive has no complete tar terminator")
	}
	source := bytes.NewReader(data)
	reader := tar.NewReader(source)
	entries := make(map[string]workspaceArchiveEntry)
	var total int64
	files, rootHeaders := 0, 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return WorkspaceSnapshot{}, workspaceError("WORKSPACE_ARCHIVE_INVALID", "Workspace archive is malformed or truncated")
		}
		if header.Typeflag != tar.TypeDir && header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return WorkspaceSnapshot{}, workspaceError("WORKSPACE_ARCHIVE_ENTRY_UNSAFE", "Workspace archive must contain only regular files and directories")
		}
		name := strings.TrimPrefix(header.Name, "./")
		directory := header.Typeflag == tar.TypeDir
		if directory {
			name = strings.TrimSuffix(name, "/")
		}
		if directory && (header.Name == "." || header.Name == "./") {
			rootHeaders++
			if rootHeaders > 1 || header.Size != 0 {
				return WorkspaceSnapshot{}, workspaceError("WORKSPACE_ARCHIVE_INVALID", "Workspace archive root is invalid")
			}
			continue
		}
		if !validWorkspaceEntryPath(name) {
			return WorkspaceSnapshot{}, workspaceError("WORKSPACE_ARCHIVE_PATH_UNSAFE", "Workspace archive path is not portable or escapes its root")
		}
		key := strings.ToLower(name)
		if _, exists := entries[key]; exists {
			return WorkspaceSnapshot{}, workspaceError("WORKSPACE_ARCHIVE_PATH_COLLISION", "Workspace archive contains ambiguous paths")
		}
		if len(entries) >= maxWorkspaceEntries || header.Size < 0 || header.Size > limits.DiskBytes-total || (directory && header.Size != 0) {
			return WorkspaceSnapshot{}, workspaceError("WORKSPACE_ARCHIVE_LIMIT_EXCEEDED", "Workspace archive exceeds the file, entry or byte quota")
		}
		entry := workspaceArchiveEntry{WorkspaceEntry: WorkspaceEntry{Path: name, Directory: directory, Mode: header.Mode & 0o777, SizeBytes: header.Size}}
		if !directory {
			files++
			if files > maxWorkspaceFiles {
				return WorkspaceSnapshot{}, workspaceError("WORKSPACE_ARCHIVE_LIMIT_EXCEEDED", "Workspace archive exceeds the file quota")
			}
			entry.content = make([]byte, header.Size)
			if _, err := io.ReadFull(reader, entry.content); err != nil {
				return WorkspaceSnapshot{}, workspaceError("WORKSPACE_ARCHIVE_INVALID", "Workspace archive file is truncated")
			}
			total += header.Size
			digest := sha256.Sum256(entry.content)
			entry.SHA256 = hex.EncodeToString(digest[:])
		}
		entries[key] = entry
	}
	if !allZero(data[len(data)-source.Len():]) {
		return WorkspaceSnapshot{}, workspaceError("WORKSPACE_ARCHIVE_INVALID", "Workspace archive has trailing non-tar data")
	}
	// Materialize omitted parent directories and reject file/directory or case aliases.
	for _, entry := range entries {
		for parent := path.Dir(entry.Path); parent != "."; parent = path.Dir(parent) {
			key := strings.ToLower(parent)
			if previous, ok := entries[key]; ok {
				if !previous.Directory || previous.Path != parent {
					return WorkspaceSnapshot{}, workspaceError("WORKSPACE_ARCHIVE_PATH_COLLISION", "Workspace parent directory conflicts with another entry")
				}
			} else {
				if len(entries) >= maxWorkspaceEntries {
					return WorkspaceSnapshot{}, workspaceError("WORKSPACE_ARCHIVE_LIMIT_EXCEEDED", "Workspace archive exceeds the directory quota")
				}
				entries[key] = workspaceArchiveEntry{WorkspaceEntry: WorkspaceEntry{Path: parent, Directory: true, Mode: 0o755}}
			}
		}
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	metadata := make([]WorkspaceEntry, 0, len(entries))
	for _, name := range names {
		entry := entries[name]
		kind := byte(tar.TypeReg)
		if entry.Directory {
			kind = tar.TypeDir
		}
		header := &tar.Header{Name: entry.Path, Typeflag: kind, Size: entry.SizeBytes, Mode: entry.Mode, Uid: 65532, Gid: 65532, ModTime: time.Unix(0, 0)}
		if err := writer.WriteHeader(header); err != nil {
			return WorkspaceSnapshot{}, workspaceError("WORKSPACE_ARCHIVE_INVALID", "Workspace archive header cannot be represented")
		}
		if _, err := writer.Write(entry.content); err != nil {
			return WorkspaceSnapshot{}, err
		}
		metadata = append(metadata, entry.WorkspaceEntry)
	}
	if err := writer.Close(); err != nil {
		return WorkspaceSnapshot{}, err
	}
	if int64(buffer.Len()) > limits.DiskBytes+workspaceArchiveOverhead {
		return WorkspaceSnapshot{}, workspaceError("WORKSPACE_ARCHIVE_LIMIT_EXCEEDED", "Canonical workspace archive exceeds the quota")
	}
	digest := sha256.Sum256(buffer.Bytes())
	return WorkspaceSnapshot{Archive: buffer.Bytes(), SHA256: hex.EncodeToString(digest[:]), SizeBytes: total, Entries: metadata}, nil
}

func allZero(data []byte) bool {
	for _, value := range data {
		if value != 0 {
			return false
		}
	}
	return true
}

func (sandbox *OCI) transferWorkspace(ctx context.Context, handle WorkspaceHandle, operation string, expected *WorkspaceSnapshot) (WorkspaceSnapshot, error) {
	unlock, err := sandbox.lockWorkspace(ctx, handle)
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	defer unlock()
	if err := sandbox.inspectWorkspace(ctx, handle, true); err != nil {
		return WorkspaceSnapshot{}, err
	}
	runner, ok := sandbox.runner.(inputCommandRunner)
	if !ok {
		return WorkspaceSnapshot{}, workspaceError("WORKSPACE_ADAPTER_UNAVAILABLE", "Workspace archive transport is unavailable")
	}
	var input io.Reader
	if expected != nil {
		plan, err := json.Marshal(struct {
			Entries      []WorkspaceEntry `json:"entries"`
			ArchiveBytes int              `json:"archive_bytes"`
		}{expected.Entries, len(expected.Archive)})
		if err != nil {
			return WorkspaceSnapshot{}, err
		}
		input = io.MultiReader(bytes.NewReader(append(plan, '\n')), bytes.NewReader(expected.Archive))
	}
	// tmpfs cannot be transported with docker cp, and docker exec cannot run in
	// a paused container. The helper quiesces user processes inside its namespace.
	args := []string{"exec", "--interactive", "--user", workspaceUser, "--workdir", workspaceRoot, handle.ContainerID,
		"/usr/bin/env", "-i", "--", "PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "HOME=" + workspaceRoot,
		"python", "-I", "-B", "-c", workspaceTransferHelper, operation, strconv.FormatInt(handle.Limits.DiskBytes, 10)}
	runCtx, cancel := context.WithTimeout(ctx, engineOperationTimeout)
	defer cancel()
	result, runErr := runner.RunInput(runCtx, sandbox.enginePath, args, input, handle.Limits.DiskBytes+workspaceArchiveOverhead, 4096)
	if runCtx.Err() == nil && result.ExitCode == 1 && (runErr == nil || ErrorCode(runErr) != "SKILL_SCRIPT_OUTPUT_LIMIT_EXCEEDED") {
		var receipt struct {
			Code string `json:"code"`
		}
		if len(result.Stdout) == 0 && json.Unmarshal(result.Stderr, &receipt) == nil && safeWorkspaceTransferFailure(operation, receipt.Code) {
			if err := sandbox.inspectWorkspace(ctx, handle, true); err != nil {
				return WorkspaceSnapshot{}, err
			}
			return WorkspaceSnapshot{}, workspaceError(receipt.Code, "Workspace transfer was rejected without changing files; user processes were released")
		}
	}
	if runCtx.Err() != nil || runErr != nil || result.ExitCode != 0 || len(result.Stderr) != 0 {
		return WorkspaceSnapshot{}, sandbox.abortWorkspaceTransfer(handle)
	}
	snapshot, err := ParseWorkspaceSnapshot(result.Stdout, handle.Limits)
	if err != nil || (expected != nil && snapshot.SHA256 != expected.SHA256) {
		return WorkspaceSnapshot{}, sandbox.abortWorkspaceTransfer(handle)
	}
	if err := sandbox.inspectWorkspace(ctx, handle, true); err != nil {
		return WorkspaceSnapshot{}, err
	}
	return snapshot, nil
}

func safeWorkspaceTransferFailure(operation, code string) bool {
	switch code {
	case "WORKSPACE_BUSY", "WORKSPACE_ADAPTER_UNAVAILABLE":
		return true
	case "WORKSPACE_HYDRATE_CONFLICT":
		return operation == "hydrate"
	case "WORKSPACE_ARCHIVE_ENTRY_UNSAFE", "WORKSPACE_ARCHIVE_LIMIT_EXCEEDED", "WORKSPACE_ARCHIVE_PATH_UNSAFE", "WORKSPACE_ARCHIVE_PATH_COLLISION":
		return operation == "export"
	}
	return false
}

func (sandbox *OCI) abortWorkspaceTransfer(handle WorkspaceHandle) error {
	// An interrupted helper may leave stopped processes or partially restored
	// files. Never repeat the write or claim that killing the CLI resumed them.
	cleanupCtx, cancel := context.WithTimeout(context.Background(), engineOperationTimeout)
	defer cancel()
	if err := sandbox.deleteWorkspace(cleanupCtx, handle); err != nil {
		return workspaceError("WORKSPACE_CLEANUP_UNCONFIRMED", "Workspace transfer outcome and environment termination are unconfirmed")
	}
	return workspaceError("WORKSPACE_TRANSFER_UNCONFIRMED", "Workspace terminated after an unconfirmed transfer; use the last verified snapshot")
}

func (sandbox *OCI) ExportWorkspace(ctx context.Context, handle WorkspaceHandle) (WorkspaceSnapshot, error) {
	return sandbox.transferWorkspace(ctx, handle, "export", nil)
}

func (sandbox *OCI) HydrateWorkspace(ctx context.Context, handle WorkspaceHandle, archive []byte, expectedHash string) (WorkspaceSnapshot, error) {
	expected, err := ParseWorkspaceSnapshot(archive, handle.Limits)
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	if expectedHash != expected.SHA256 {
		return WorkspaceSnapshot{}, workspaceError("WORKSPACE_SNAPSHOT_HASH_MISMATCH", "Workspace snapshot does not match the selected version")
	}
	return sandbox.transferWorkspace(ctx, handle, "hydrate", &expected)
}
