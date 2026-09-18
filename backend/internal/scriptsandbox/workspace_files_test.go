package scriptsandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type workspaceFileRunner struct {
	*workspaceRunnerFixture
	request WorkspaceFileOperation
	cancel  context.CancelFunc
}

func (runner *workspaceFileRunner) RunInput(ctx context.Context, _ string, args []string, input io.Reader, stdoutLimit, stderrLimit int64) (commandResult, error) {
	runner.commands = append(runner.commands, slices.Clone(args))
	expected := []string{"exec", "--interactive", "--user", workspaceUser, "--workdir", workspaceRoot, runner.state.ID,
		"/usr/bin/env", "-i", "--", "PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "HOME=" + workspaceRoot,
		"python", "-I", "-B", "-c", workspaceTransferHelper, "file", strconv.FormatInt(DefaultLimits().DiskBytes, 10)}
	if !slices.Equal(args, expected) || stdoutLimit != WorkspaceFileWireBytes || stderrLimit != 4096 || !runner.state.State.Running {
		return commandResult{ExitCode: -1}, errors.New("unsafe file helper invocation")
	}
	body, err := io.ReadAll(input)
	if err != nil || len(body) > WorkspaceFileWireBytes || json.Unmarshal(body, &runner.request) != nil {
		return commandResult{ExitCode: -1}, errors.New("invalid request transport")
	}
	if runner.cancel != nil {
		runner.cancel()
		return commandResult{ExitCode: -1}, ctx.Err()
	}
	return runner.result, runner.runErr
}

func fileWorkspaceFixture(t *testing.T) (*OCI, WorkspaceHandle, *workspaceFileRunner) {
	t.Helper()
	sandbox, handle, base := workspaceFixture(t)
	base.state.State.Running = true
	runner := &workspaceFileRunner{workspaceRunnerFixture: base}
	sandbox.runner = runner
	return sandbox, handle, runner
}

func fileWorkspaceReceipt(t *testing.T, request WorkspaceFileOperation, body []byte) []byte {
	t.Helper()
	result := WorkspaceFileResult{Operation: request.Operation, Path: request.Path, Data: []byte{}, SHA256: fileContentHash(body),
		Entries: []WorkspaceEntry{{Path: request.Path, SizeBytes: int64(len(body)), Mode: 0o600, SHA256: fileContentHash(body)}}}
	if request.Operation == "read" {
		result.Data = body
	}
	wire, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func TestNativeWorkspaceFileValidatesBeforeEngineAccess(t *testing.T) {
	invalidMode := int64(0o1600)
	for _, request := range []WorkspaceFileOperation{
		{Operation: "exec", Path: "file"}, {Operation: "write", Path: "../escape"},
		{Operation: "write", Path: "/host"}, {Operation: "write", Path: "C:/host"},
		{Operation: "write", Path: "a\\b"}, {Operation: "write", Path: strings.Repeat("x", 241)},
		{Operation: "write", Path: strings.Repeat("\u4e2d", 81)},
		{Operation: "write", Path: "."}, {Operation: "remove", Path: ".", Recursive: true},
		{Operation: "write", Path: "file", Parents: true}, {Operation: "read", Path: "file", Recursive: true},
		{Operation: "read", Path: "file", Data: []byte("data")},
		{Operation: "chmod", Path: "file"}, {Operation: "chmod", Path: "file", Mode: &invalidMode},
		{Operation: "write", Path: "file", Data: make([]byte, WorkspaceFileBytes+1)},
	} {
		sandbox, handle, runner := fileWorkspaceFixture(t)
		_, err := sandbox.FileWorkspace(context.Background(), handle, request)
		assertSandboxCode(t, err, "WORKSPACE_FILE_REQUEST_INVALID")
		if len(runner.commands) != 0 {
			t.Fatal("invalid request reached engine")
		}
	}
}

func TestNativeWorkspaceFileBinaryReadAndWriteUseFixedHelper(t *testing.T) {
	for _, operation := range []string{"read", "write"} {
		t.Run(operation, func(t *testing.T) {
			sandbox, handle, runner := fileWorkspaceFixture(t)
			body := []byte{0, 255, 128, 1, 10}
			request := WorkspaceFileOperation{Operation: operation, Path: "nested/payload.bin"}
			if operation == "write" {
				request.Data = body
			}
			runner.result.Stdout = fileWorkspaceReceipt(t, request, body)
			actual, err := sandbox.FileWorkspace(context.Background(), handle, request)
			if err != nil || actual.SHA256 != fileContentHash(body) || !reflect.DeepEqual(runner.request, request) || runner.deleted {
				t.Fatalf("file result = %+v, %v", actual, err)
			}
			if operation == "read" && !bytes.Equal(actual.Data, body) || operation == "write" && len(actual.Data) != 0 {
				t.Fatal("binary response changed")
			}
			if len(runner.commands) != 3 || runner.commands[0][0] != "inspect" || runner.commands[1][0] != "exec" || runner.commands[2][0] != "inspect" {
				t.Fatal("file operation was not surrounded by binding checks")
			}
		})
	}
}

func TestNativeWorkspaceFileKnownPreflightRejectionPreservesEnvironment(t *testing.T) {
	for _, code := range []string{"WORKSPACE_BUSY", "WORKSPACE_FILE_NOT_FOUND", "WORKSPACE_FILE_PATH_COLLISION", "WORKSPACE_FILE_LIMIT_EXCEEDED", "WORKSPACE_ARCHIVE_ENTRY_UNSAFE"} {
		sandbox, handle, runner := fileWorkspaceFixture(t)
		runner.result = commandResult{ExitCode: 1, Stderr: []byte(`{"code":"` + code + `"}`)}
		runner.runErr = errors.New("helper rejected before mutation")
		_, err := sandbox.FileWorkspace(context.Background(), handle, WorkspaceFileOperation{Operation: "write", Path: "file"})
		assertSandboxCode(t, err, code)
		if runner.deleted || len(runner.commands) != 3 || runner.commands[2][0] != "inspect" {
			t.Fatal("safe rejection did not preserve and recheck workspace")
		}
	}
}

func TestNativeWorkspaceFileUnconfirmedResultTerminatesOnlyVerifiedEnvironment(t *testing.T) {
	for _, failure := range []string{"lost", "cancel", "resume", "corrupt", "hash", "unknown_field", "extra_json", "stderr", "overflow", "cleanup"} {
		t.Run(failure, func(t *testing.T) {
			sandbox, handle, runner := fileWorkspaceFixture(t)
			request := WorkspaceFileOperation{Operation: "write", Path: "file", Data: []byte("a")}
			runner.result.Stdout = fileWorkspaceReceipt(t, request, request.Data)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch failure {
			case "lost", "cleanup":
				runner.runErr = errors.New("receipt lost")
				runner.deleteErr = failure == "cleanup"
			case "cancel":
				runner.cancel = cancel
			case "resume":
				runner.result = commandResult{ExitCode: 1, Stderr: []byte(`{"code":"WORKSPACE_TRANSFER_UNCONFIRMED"}`)}
			case "corrupt":
				runner.result.Stdout = []byte("broken")
			case "hash":
				runner.result.Stdout = fileWorkspaceReceipt(t, request, []byte("b"))
			case "unknown_field":
				runner.result.Stdout = bytes.Replace(runner.result.Stdout, []byte(`"operation":`), []byte(`"program":"ignored","operation":`), 1)
			case "extra_json":
				runner.result.Stdout = append(runner.result.Stdout, []byte(`{}`)...)
			case "stderr":
				runner.result.Stderr = []byte("warning")
			case "overflow":
				runner.result = commandResult{ExitCode: 1, Stderr: []byte(`{"code":"WORKSPACE_BUSY"}`)}
				runner.runErr = &Error{Code: "SKILL_SCRIPT_OUTPUT_LIMIT_EXCEEDED"}
			}
			_, err := sandbox.FileWorkspace(ctx, handle, request)
			code := "WORKSPACE_TRANSFER_UNCONFIRMED"
			if runner.deleteErr {
				code = "WORKSPACE_CLEANUP_UNCONFIRMED"
			}
			assertSandboxCode(t, err, code)
			if runner.deleted == runner.deleteErr {
				t.Fatal("cleanup confirmation mismatch")
			}
			executions := 0
			for _, command := range runner.commands {
				if command[0] == "exec" {
					executions++
				}
				if command[0] == "rm" && command[len(command)-1] != handle.ContainerID {
					t.Fatal("cleanup targeted an unverified environment")
				}
			}
			if executions != 1 {
				t.Fatal("unconfirmed operation was replayed")
			}
		})
	}
}

func TestNativeWorkspaceFileReceiptMetadataIsStrict(t *testing.T) {
	mode := int64(0o640)
	for _, kind := range []string{"stat", "list", "mkdir", "remove", "chmod"} {
		request := WorkspaceFileOperation{Operation: kind, Path: "file"}
		result := WorkspaceFileResult{Operation: kind, Path: "file", Entries: []WorkspaceEntry{}}
		switch kind {
		case "stat", "mkdir":
			result.Entries = []WorkspaceEntry{{Path: "file", Directory: true, Mode: 0o700}}
		case "list":
			result.Entries = []WorkspaceEntry{{Path: "file/child", Mode: 0o600, SizeBytes: 2}}
		case "chmod":
			request.Mode = &mode
			result.Entries = []WorkspaceEntry{{Path: "file", Mode: mode, SizeBytes: 2}}
		}
		if err := validateWorkspaceFileResult(request, result, DefaultLimits()); err != nil {
			t.Fatalf("valid %s receipt rejected: %v", kind, err)
		}
		result.Data = []byte("unexpected")
		assertSandboxCode(t, validateWorkspaceFileResult(request, result, DefaultLimits()), "WORKSPACE_FILE_OPERATION_UNCONFIRMED")
	}
	for _, entry := range []WorkspaceEntry{
		{Path: "other"}, {Path: "file/child/grandchild"}, {Path: "file/../escape"},
		{Path: "file/child", Mode: 0o1600}, {Path: "file/child", SizeBytes: -1},
		{Path: "file/child", Directory: true, SizeBytes: 1}, {Path: "file/child", SHA256: fileContentHash(nil)},
	} {
		request := WorkspaceFileOperation{Operation: "list", Path: "file"}
		result := WorkspaceFileResult{Operation: "list", Path: "file", Entries: []WorkspaceEntry{entry}}
		assertSandboxCode(t, validateWorkspaceFileResult(request, result, DefaultLimits()), "WORKSPACE_FILE_OPERATION_UNCONFIRMED")
	}
	request := WorkspaceFileOperation{Operation: "stat", Path: "."}
	result := WorkspaceFileResult{Operation: "stat", Path: ".", Entries: []WorkspaceEntry{{Path: "."}}}
	assertSandboxCode(t, validateWorkspaceFileResult(request, result, DefaultLimits()), "WORKSPACE_FILE_OPERATION_UNCONFIRMED")
}
