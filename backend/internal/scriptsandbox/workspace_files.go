package scriptsandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"
)

const WorkspaceFileBytes = 16 << 20
const WorkspaceFileWireBytes = 24 << 20

type WorkspaceFileOperation struct {
	Operation string `json:"operation"`
	Path      string `json:"path"`
	Data      []byte `json:"data,omitempty"`
	Parents   bool   `json:"parents,omitempty"`
	Recursive bool   `json:"recursive,omitempty"`
	Mode      *int64 `json:"mode,omitempty"`
}

type WorkspaceFileResult struct {
	Operation string           `json:"operation"`
	Path      string           `json:"path"`
	Data      []byte           `json:"data"`
	Entries   []WorkspaceEntry `json:"entries"`
	SHA256    string           `json:"sha256"`
}

func (operation WorkspaceFileOperation) Mutates() bool {
	return operation.Operation == "write" || operation.Operation == "mkdir" || operation.Operation == "remove" || operation.Operation == "chmod"
}

func ValidateWorkspaceFileOperation(operation WorkspaceFileOperation, limits Limits) error {
	if err := limits.Validate(); err != nil {
		return err
	}
	if !slices.Contains([]string{"read", "stat", "list", "write", "mkdir", "remove", "chmod"}, operation.Operation) ||
		(operation.Path != "." && (!validWorkspaceEntryPath(operation.Path) || len(operation.Path) > 240 || path.Clean(operation.Path) != operation.Path)) ||
		(operation.Path == "." && operation.Operation != "stat" && operation.Operation != "list" && operation.Operation != "mkdir") ||
		operation.Parents && operation.Operation != "mkdir" || operation.Recursive && operation.Operation != "remove" ||
		operation.Mode != nil && (operation.Operation != "chmod" || *operation.Mode < 0 || *operation.Mode > 0o777) ||
		operation.Operation == "chmod" && operation.Mode == nil ||
		len(operation.Data) > WorkspaceFileBytes || int64(len(operation.Data)) > limits.DiskBytes || operation.Operation != "write" && len(operation.Data) != 0 {
		return workspaceError("WORKSPACE_FILE_REQUEST_INVALID", "Workspace file operation is outside the bounded filesystem contract")
	}
	return nil
}

func fileContentHash(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func validateWorkspaceFileResult(request WorkspaceFileOperation, result WorkspaceFileResult, limits Limits) error {
	invalid := func() error {
		return workspaceError("WORKSPACE_FILE_OPERATION_UNCONFIRMED", "Workspace file response did not confirm the requested operation")
	}
	if result.Operation != request.Operation || result.Path != request.Path || result.Entries == nil || len(result.Entries) > maxWorkspaceEntries || len(result.Data) > WorkspaceFileBytes {
		return invalid()
	}
	seen, total, files := map[string]bool{}, int64(0), 0
	for _, entry := range result.Entries {
		if entry.Path != "." && (!validWorkspaceEntryPath(entry.Path) || path.Clean(entry.Path) != entry.Path || len(entry.Path) > 240) ||
			entry.Path == "." && !entry.Directory ||
			entry.Mode < 0 || entry.Mode > 0o777 || entry.SizeBytes < 0 || entry.SizeBytes > limits.DiskBytes ||
			entry.Directory && (entry.SizeBytes != 0 || entry.SHA256 != "") {
			return invalid()
		}
		key := strings.ToLower(entry.Path)
		if seen[key] {
			return invalid()
		}
		seen[key] = true
		total += entry.SizeBytes
		if !entry.Directory {
			files++
		}
		if request.Operation == "list" {
			if entry.Path == "." || path.Dir(entry.Path) != request.Path {
				return invalid()
			}
		} else if entry.Path != request.Path {
			return invalid()
		}
	}
	if total > limits.DiskBytes || files > maxWorkspaceFiles {
		return invalid()
	}
	if request.Operation == "read" || request.Operation == "write" {
		body := request.Data
		if request.Operation == "read" {
			body = result.Data
		}
		if len(result.Entries) != 1 || result.Entries[0].Directory || result.Entries[0].SizeBytes != int64(len(body)) ||
			result.SHA256 != fileContentHash(body) || result.Entries[0].SHA256 != result.SHA256 ||
			request.Operation == "write" && len(result.Data) != 0 {
			return invalid()
		}
		return nil
	}
	if len(result.Data) != 0 || result.SHA256 != "" {
		return invalid()
	}
	for _, entry := range result.Entries {
		if entry.SHA256 != "" {
			return invalid()
		}
	}
	switch request.Operation {
	case "stat":
		if len(result.Entries) != 1 {
			return invalid()
		}
	case "mkdir":
		if len(result.Entries) != 1 || !result.Entries[0].Directory {
			return invalid()
		}
	case "remove":
		if len(result.Entries) != 0 {
			return invalid()
		}
	case "chmod":
		if len(result.Entries) != 1 || request.Mode == nil || result.Entries[0].Mode != *request.Mode {
			return invalid()
		}
	}
	return nil
}

func safeWorkspaceFileFailure(code string) bool {
	return slices.Contains([]string{
		"WORKSPACE_BUSY", "WORKSPACE_ADAPTER_UNAVAILABLE", "WORKSPACE_FILE_REQUEST_INVALID", "WORKSPACE_FILE_PATH_UNSAFE",
		"WORKSPACE_FILE_NOT_FOUND", "WORKSPACE_FILE_EXISTS", "WORKSPACE_FILE_NOT_DIRECTORY", "WORKSPACE_FILE_IS_DIRECTORY",
		"WORKSPACE_FILE_PATH_COLLISION", "WORKSPACE_FILE_LIMIT_EXCEEDED", "WORKSPACE_FILE_ACCESS_DENIED",
		"WORKSPACE_ARCHIVE_ENTRY_UNSAFE", "WORKSPACE_ARCHIVE_LIMIT_EXCEEDED", "WORKSPACE_ARCHIVE_PATH_UNSAFE", "WORKSPACE_ARCHIVE_PATH_COLLISION",
	}, code)
}

func ValidateWorkspaceFileResult(request WorkspaceFileOperation, result WorkspaceFileResult, limits Limits) error {
	return validateWorkspaceFileResult(request, result, limits)
}

func WorkspaceFileFailurePreserved(err error) bool {
	var failure *Error
	return errors.As(err, &failure) && safeWorkspaceFileFailure(failure.Code)
}

// FileWorkspace exposes typed filesystem primitives, never a caller-selected
// program. Execution ownership and tool approval are enforced by Runtime.
func (sandbox *OCI) FileWorkspace(ctx context.Context, handle WorkspaceHandle, request WorkspaceFileOperation) (WorkspaceFileResult, error) {
	request.Data = bytes.Clone(request.Data)
	if request.Mode != nil {
		mode := *request.Mode
		request.Mode = &mode
	}
	if err := ValidateWorkspaceFileOperation(request, handle.Limits); err != nil {
		return WorkspaceFileResult{}, err
	}
	unlock, err := sandbox.lockWorkspace(ctx, handle)
	if err != nil {
		return WorkspaceFileResult{}, err
	}
	defer unlock()
	if err := sandbox.inspectWorkspace(ctx, handle, true); err != nil {
		return WorkspaceFileResult{}, err
	}
	runner, ok := sandbox.runner.(inputCommandRunner)
	if !ok {
		return WorkspaceFileResult{}, workspaceError("WORKSPACE_ADAPTER_UNAVAILABLE", "Workspace file transport is unavailable")
	}
	body, err := json.Marshal(request)
	if err != nil {
		return WorkspaceFileResult{}, err
	}
	args := []string{"exec", "--interactive", "--user", workspaceUser, "--workdir", workspaceRoot, handle.ContainerID,
		"/usr/bin/env", "-i", "--", "PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "HOME=" + workspaceRoot,
		"python", "-I", "-B", "-c", workspaceTransferHelper, "file", strconv.FormatInt(handle.Limits.DiskBytes, 10)}
	runCtx, cancel := context.WithTimeout(ctx, engineOperationTimeout)
	defer cancel()
	result, runErr := runner.RunInput(runCtx, sandbox.enginePath, args, bytes.NewReader(body), WorkspaceFileWireBytes, 4096)
	if runCtx.Err() == nil && result.ExitCode == 1 && (runErr == nil || ErrorCode(runErr) != "SKILL_SCRIPT_OUTPUT_LIMIT_EXCEEDED") {
		var failure struct {
			Code string `json:"code"`
		}
		if len(result.Stdout) == 0 && json.Unmarshal(result.Stderr, &failure) == nil && safeWorkspaceFileFailure(failure.Code) {
			if err := sandbox.inspectWorkspace(ctx, handle, true); err != nil {
				return WorkspaceFileResult{}, err
			}
			return WorkspaceFileResult{}, workspaceError(failure.Code, "Workspace file operation was rejected before mutation; processes were released")
		}
	}
	if runCtx.Err() != nil || runErr != nil || result.ExitCode != 0 || len(result.Stderr) != 0 || len(result.Stdout) > WorkspaceFileWireBytes {
		return WorkspaceFileResult{}, sandbox.abortWorkspaceTransfer(handle)
	}
	var output WorkspaceFileResult
	decoder := json.NewDecoder(bytes.NewReader(result.Stdout))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&output) != nil || decoder.Decode(new(any)) != io.EOF || validateWorkspaceFileResult(request, output, handle.Limits) != nil {
		return WorkspaceFileResult{}, sandbox.abortWorkspaceTransfer(handle)
	}
	if err := sandbox.inspectWorkspace(ctx, handle, true); err != nil {
		return WorkspaceFileResult{}, err
	}
	return output, nil
}
