package scriptsandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func workspaceTar(t *testing.T, headers []*tar.Header, contents [][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for index, source := range headers {
		header := *source
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if index < len(contents) {
			if _, err := writer.Write(contents[index]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func binaryWorkspaceTar(t *testing.T) []byte {
	return workspaceTar(t, []*tar.Header{{Name: "assets/image.bin", Mode: 0o640, Size: 5, Typeflag: tar.TypeReg, Uid: 0, Gid: 0, ModTime: time.Now(), PAXRecords: map[string]string{"SCHILY.xattr.user.private": "strip-me"}}, {Name: "empty", Mode: 0o700, Typeflag: tar.TypeDir}}, [][]byte{{0, 255, 128, 1, 10}, nil})
}

func TestWorkspaceSnapshotCanonicalBinaryRoundTrip(t *testing.T) {
	snapshot, err := ParseWorkspaceSnapshot(binaryWorkspaceTar(t), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SizeBytes != 5 || len(snapshot.Entries) != 3 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	roundTrip, err := ParseWorkspaceSnapshot(snapshot.Archive, DefaultLimits())
	if err != nil || !reflect.DeepEqual(snapshot, roundTrip) {
		t.Fatalf("noncanonical round trip: %v", err)
	}
	digest := sha256.Sum256(snapshot.Archive)
	if snapshot.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("archive hash mismatch")
	}
	reader := tar.NewReader(bytes.NewReader(snapshot.Archive))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Uid != 65532 || header.Gid != 65532 || header.Uname != "" || len(header.Xattrs) != 0 || header.Mode&0o7000 != 0 {
			t.Fatalf("unsafe metadata persisted: %+v", header)
		}
		if header.Name == "assets/image.bin" {
			content, err := io.ReadAll(reader)
			if err != nil || !bytes.Equal(content, []byte{0, 255, 128, 1, 10}) || header.Mode != 0o640 {
				t.Fatal("binary content or mode changed")
			}
		}
	}
}

func TestWorkspaceSnapshotRejectsUnsafeEntriesAndPortablePathAliases(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "C:/host", "a\\b", "a/../b", "a\tb", "CON.a.b", "folder./file", "a?/file"} {
		t.Run(name, func(t *testing.T) {
			data := workspaceTar(t, []*tar.Header{{Name: name, Typeflag: tar.TypeReg, Mode: 0o600}}, nil)
			_, err := ParseWorkspaceSnapshot(data, DefaultLimits())
			assertSandboxCode(t, err, "WORKSPACE_ARCHIVE_PATH_UNSAFE")
		})
	}
	for _, kind := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeFifo, tar.TypeChar, tar.TypeBlock} {
		data := workspaceTar(t, []*tar.Header{{Name: "unsafe", Typeflag: kind, Mode: 0o600, Linkname: "/etc/passwd"}}, nil)
		_, err := ParseWorkspaceSnapshot(data, DefaultLimits())
		assertSandboxCode(t, err, "WORKSPACE_ARCHIVE_ENTRY_UNSAFE")
	}
	for _, headers := range [][]*tar.Header{
		{{Name: "a", Typeflag: tar.TypeReg}, {Name: "a", Typeflag: tar.TypeReg}},
		{{Name: "A", Typeflag: tar.TypeDir}, {Name: "a/file", Typeflag: tar.TypeReg}},
		{{Name: "a", Typeflag: tar.TypeReg}, {Name: "a/file", Typeflag: tar.TypeReg}},
		{{Name: "A/file", Typeflag: tar.TypeReg}, {Name: "a/other", Typeflag: tar.TypeReg}},
	} {
		_, err := ParseWorkspaceSnapshot(workspaceTar(t, headers, nil), DefaultLimits())
		assertSandboxCode(t, err, "WORKSPACE_ARCHIVE_PATH_COLLISION")
	}
	_, err := ParseWorkspaceSnapshot(workspaceTar(t, []*tar.Header{{Name: "/", Typeflag: tar.TypeDir}}, nil), DefaultLimits())
	assertSandboxCode(t, err, "WORKSPACE_ARCHIVE_PATH_UNSAFE")
}

func TestWorkspaceSnapshotRejectsMalformedAndOverQuotaArchives(t *testing.T) {
	valid := binaryWorkspaceTar(t)
	trailing := append(bytes.Clone(valid), append([]byte("trailing"), make([]byte, 2048-8)...)...)
	for _, data := range [][]byte{valid[:len(valid)-512], valid[:len(valid)-1], trailing} {
		_, err := ParseWorkspaceSnapshot(data, DefaultLimits())
		assertSandboxCode(t, err, "WORKSPACE_ARCHIVE_INVALID")
	}
	limits := DefaultLimits()
	limits.DiskBytes = 1 << 20
	body := make([]byte, (1<<20)+1)
	data := workspaceTar(t, []*tar.Header{{Name: "large", Size: int64(len(body)), Typeflag: tar.TypeReg}}, [][]byte{body})
	_, err := ParseWorkspaceSnapshot(data, limits)
	assertSandboxCode(t, err, "WORKSPACE_ARCHIVE_LIMIT_EXCEEDED")
	headers := make([]*tar.Header, maxWorkspaceFiles+1)
	for index := range headers {
		headers[index] = &tar.Header{Name: strings.Repeat("a", index/200+1) + string(rune(0x4e00+index)), Typeflag: tar.TypeReg}
	}
	_, err = ParseWorkspaceSnapshot(workspaceTar(t, headers, nil), DefaultLimits())
	assertSandboxCode(t, err, "WORKSPACE_ARCHIVE_LIMIT_EXCEEDED")
}

type workspaceArchiveRunner struct {
	*workspaceRunnerFixture
	archive        []byte
	imports        int
	failure        string
	cancelTransfer context.CancelFunc
	exportStarted  chan struct{}
	finishExport   chan struct{}
}

func (runner *workspaceArchiveRunner) Run(ctx context.Context, name string, args []string, stdoutLimit, stderrLimit int64) (commandResult, error) {
	switch args[0] {
	case "pause", "unpause", "cp":
		panic("tmpfs snapshots must not use whole-container pause or docker cp")
	default:
		return runner.workspaceRunnerFixture.Run(ctx, name, args, stdoutLimit, stderrLimit)
	}
}

func (runner *workspaceArchiveRunner) RunInput(ctx context.Context, name string, args []string, input io.Reader, stdoutLimit, stderrLimit int64) (commandResult, error) {
	if !slices.Contains(args, workspaceTransferHelper) {
		return runner.workspaceRunnerFixture.RunInput(ctx, name, args, input, stdoutLimit, stderrLimit)
	}
	runner.commands = append(runner.commands, slices.Clone(args))
	prefix := []string{"exec", "--interactive", "--user", workspaceUser, "--workdir", workspaceRoot, runner.state.ID,
		"/usr/bin/env", "-i", "--", "PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "HOME=" + workspaceRoot,
		"python", "-I", "-B", "-c", workspaceTransferHelper}
	if runner.state.State.Paused || !runner.state.State.Running || len(args) != len(prefix)+2 ||
		!slices.Equal(args[:len(prefix)], prefix) || stdoutLimit != DefaultLimits().DiskBytes+workspaceArchiveOverhead || stderrLimit != 4096 {
		return commandResult{ExitCode: -1}, errors.New("unsafe transfer")
	}
	if runner.exportStarted != nil {
		close(runner.exportStarted)
		select {
		case <-runner.finishExport:
		case <-ctx.Done():
			return commandResult{ExitCode: -1}, ctx.Err()
		}
		runner.exportStarted = nil
	}
	if runner.cancelTransfer != nil {
		runner.cancelTransfer()
		return commandResult{ExitCode: -1}, ctx.Err()
	}
	if runner.failure == "busy" {
		return commandResult{ExitCode: 1, Stderr: []byte(`{"code":"WORKSPACE_BUSY"}`)}, errors.New("helper rejected")
	}
	if args[len(prefix)] == "hydrate" {
		body, err := io.ReadAll(input)
		if err != nil {
			return commandResult{ExitCode: -1}, err
		}
		planJSON, archive, ok := bytes.Cut(body, []byte{'\n'})
		var plan struct {
			Entries      []WorkspaceEntry `json:"entries"`
			ArchiveBytes int              `json:"archive_bytes"`
		}
		if !ok || json.Unmarshal(planJSON, &plan) != nil || plan.ArchiveBytes != len(archive) {
			return commandResult{ExitCode: -1}, errors.New("invalid transport plan")
		}
		expected, err := ParseWorkspaceSnapshot(archive, DefaultLimits())
		if err != nil || !reflect.DeepEqual(plan.Entries, expected.Entries) {
			return commandResult{ExitCode: -1}, errors.New("metadata mismatch")
		}
		current, err := ParseWorkspaceSnapshot(runner.archive, DefaultLimits())
		if err != nil {
			return commandResult{ExitCode: -1}, err
		}
		if current.SHA256 != expected.SHA256 {
			if len(current.Entries) != 0 {
				return commandResult{ExitCode: 1, Stderr: []byte(`{"code":"WORKSPACE_HYDRATE_CONFLICT"}`)}, errors.New("helper rejected")
			}
			runner.imports++
			runner.archive = bytes.Clone(archive)
		}
	}
	switch runner.failure {
	case "receipt_lost":
		return commandResult{ExitCode: -1}, errors.New("transfer receipt lost")
	case "resume", "partial":
		return commandResult{ExitCode: 1, Stderr: []byte(`{"code":"WORKSPACE_TRANSFER_UNCONFIRMED"}`)}, errors.New("unconfirmed")
	case "corrupt":
		return commandResult{Stdout: []byte("not an archive")}, nil
	case "hash":
		return commandResult{Stdout: make([]byte, 1024)}, nil
	case "overflow":
		return commandResult{Stdout: runner.archive}, &Error{Code: "SKILL_SCRIPT_OUTPUT_LIMIT_EXCEEDED"}
	case "stopped":
		runner.state.State.Running = false
	}
	return commandResult{Stdout: bytes.Clone(runner.archive)}, nil
}

func archiveFixture(t *testing.T) (*OCI, WorkspaceHandle, *workspaceArchiveRunner) {
	t.Helper()
	sandbox, handle, base := workspaceFixture(t)
	base.state.State.Running = true
	runner := &workspaceArchiveRunner{workspaceRunnerFixture: base, archive: workspaceTar(t, nil, nil)}
	sandbox.runner = runner
	return sandbox, handle, runner
}

func TestNativeWorkspaceSnapshotHydrationAndIdempotentRepeat(t *testing.T) {
	sandbox, handle, runner := archiveFixture(t)
	expected, err := ParseWorkspaceSnapshot(binaryWorkspaceTar(t), handle.Limits)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		actual, err := sandbox.HydrateWorkspace(context.Background(), handle, expected.Archive, expected.SHA256)
		if err != nil || !reflect.DeepEqual(expected, actual) || runner.state.State.Paused {
			t.Fatalf("hydrate = %+v %v", actual, err)
		}
	}
	if runner.imports != 1 {
		t.Fatal("hydration rewrote matching content")
	}
	exported, err := sandbox.ExportWorkspace(context.Background(), handle)
	if err != nil || !reflect.DeepEqual(expected, exported) {
		t.Fatalf("export = %+v %v", exported, err)
	}
}

func TestNativeWorkspaceSnapshotRejectsMismatchAndNonemptyOverwrite(t *testing.T) {
	sandbox, handle, runner := archiveFixture(t)
	expected, err := ParseWorkspaceSnapshot(binaryWorkspaceTar(t), handle.Limits)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sandbox.HydrateWorkspace(context.Background(), handle, expected.Archive, strings.Repeat("0", 64))
	assertSandboxCode(t, err, "WORKSPACE_SNAPSHOT_HASH_MISMATCH")
	if len(runner.commands) != 0 {
		t.Fatal("invalid snapshot reached engine")
	}
	runner.archive = workspaceTar(t, []*tar.Header{{Name: "existing", Typeflag: tar.TypeReg, Size: 4}}, [][]byte{[]byte("keep")})
	before := bytes.Clone(runner.archive)
	_, err = sandbox.HydrateWorkspace(context.Background(), handle, expected.Archive, expected.SHA256)
	assertSandboxCode(t, err, "WORKSPACE_HYDRATE_CONFLICT")
	if runner.imports != 0 || !bytes.Equal(before, runner.archive) || runner.state.State.Paused {
		t.Fatal("conflicting restore changed existing content")
	}
}

func TestNativeWorkspaceSnapshotFailureDoesNotClaimSuccess(t *testing.T) {
	for _, failure := range []string{"receipt_lost", "resume", "partial", "corrupt", "hash", "overflow", "cleanup", "cancel", "stopped"} {
		t.Run(failure, func(t *testing.T) {
			sandbox, handle, runner := archiveFixture(t)
			runner.failure = failure
			runner.deleteErr = failure == "cleanup"
			if runner.deleteErr {
				runner.failure = "receipt_lost"
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if failure == "cancel" {
				runner.cancelTransfer = cancel
			}
			expected, err := ParseWorkspaceSnapshot(binaryWorkspaceTar(t), handle.Limits)
			if err != nil {
				t.Fatal(err)
			}
			result, err := sandbox.HydrateWorkspace(ctx, handle, expected.Archive, expected.SHA256)
			code := "WORKSPACE_TRANSFER_UNCONFIRMED"
			if failure == "cleanup" {
				code = "WORKSPACE_CLEANUP_UNCONFIRMED"
			}
			if failure == "stopped" {
				code = "WORKSPACE_STOPPED"
			}
			assertSandboxCode(t, err, code)
			if result.SHA256 != "" {
				t.Fatal("failed restore returned a successful snapshot")
			}
			if failure != "cleanup" && failure != "stopped" && !runner.deleted {
				t.Fatal("unknown transfer left an untracked container")
			}
			if runner.imports > 1 {
				t.Fatal("failed restore was automatically replayed")
			}
		})
	}
}

func TestNativeWorkspaceSnapshotBusyLeavesExistingEnvironmentIntact(t *testing.T) {
	sandbox, handle, runner := archiveFixture(t)
	runner.failure = "busy"
	_, err := sandbox.ExportWorkspace(context.Background(), handle)
	assertSandboxCode(t, err, "WORKSPACE_BUSY")
	if runner.deleted || runner.imports != 0 {
		t.Fatal("busy snapshot mutated the environment")
	}
}

func TestNativeWorkspaceSnapshotSerializesCommandsAndCancelsWaiter(t *testing.T) {
	sandbox, handle, runner := archiveFixture(t)
	started, finish := make(chan struct{}), make(chan struct{})
	runner.exportStarted, runner.finishExport = started, finish
	done := make(chan error, 1)
	go func() { _, err := sandbox.ExportWorkspace(context.Background(), handle); done <- err }()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := sandbox.ExecuteWorkspace(ctx, handle, WorkspaceCommand{Argv: []string{"pwd"}, Cwd: workspaceRoot})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting command cancellation = %v", err)
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, command := range runner.commands {
		if command[0] == "exec" && !slices.Contains(command, workspaceTransferHelper) {
			t.Fatal("user command executed during snapshot transfer")
		}
	}
	if len(sandbox.workspaceLocks.active) != 0 {
		t.Fatal("workspace lock retained after operations ended")
	}
}
