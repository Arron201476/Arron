package scriptsandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const testPythonImage = "registry.example/python@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type recordedCommand struct {
	args []string
}

type fakeRunner struct {
	commands   []recordedCommand
	archive    []byte
	start      commandResult
	startErr   error
	cleanupErr error
	block      bool
	running    bool
	createID   string
	failure    string
}

func (runner *fakeRunner) Run(
	ctx context.Context,
	_ string,
	args []string,
	_ int64,
	_ int64,
) (commandResult, error) {
	runner.commands = append(runner.commands, recordedCommand{args: slices.Clone(args)})
	if len(args) == 0 {
		return commandResult{}, errors.New("missing command")
	}
	switch args[0] {
	case "create":
		id := runner.createID
		if id == "" {
			id = strings.Repeat("a", 64)
		}
		return commandResult{Stdout: []byte(id)}, nil
	case "rm":
		runner.running = false
		return commandResult{Stderr: []byte("cleanup denied")}, runner.cleanupErr
	case "start":
		if len(args) != 2 {
			panic("script container must start detached")
		}
		if runner.failure == "start" {
			return commandResult{ExitCode: 1}, nil
		}
		runner.running = true
		return commandResult{}, nil
	case "exec":
		if !runner.running {
			panic("export/command attempted against stopped tmpfs")
		}
		if slices.Contains(args, workspaceTransferHelper) {
			if runner.failure == "export" {
				return commandResult{ExitCode: 1}, nil
			}
			if runner.failure == "unsafe" {
				return commandResult{ExitCode: 1, Stderr: []byte(`{"code":"WORKSPACE_ARCHIVE_ENTRY_UNSAFE"}`)}, errors.New("linked output")
			}
			return commandResult{Stdout: runner.archive}, nil
		}
		if runner.block {
			<-ctx.Done()
			return commandResult{ExitCode: -1}, ctx.Err()
		}
		return runner.start, runner.startErr
	case "cp", "pause":
		panic("tmpfs cannot be exported with cp or container pause")
	default:
		return commandResult{}, errors.New("unexpected command")
	}
}

func TestOCIArgumentsEnforceIsolationOnWindowsAndLinux(t *testing.T) {
	for _, adapter := range []string{"windows", "linux"} {
		t.Run(adapter, func(t *testing.T) {
			runner := &fakeRunner{archive: buildOutputTar(t, map[string]string{"result/report.txt": "ok"})}
			sandbox, err := newOCI(Config{
				EnginePath: enginePathForTest(t, adapter), Adapter: adapter, PythonImage: testPythonImage,
			}, runner)
			if err != nil {
				t.Fatal(err)
			}
			skillRoot, workRoot, outputRoot := scriptFixture(t)
			result, err := sandbox.Execute(context.Background(), Request{
				ExecutionID: "exec_fixture", SkillRoot: skillRoot, ScriptPath: "scripts/run.py",
				Runtime: "python", Input: json.RawMessage(`{"value":1}`), WorkRoot: workRoot,
				OutputRoot: outputRoot, Limits: DefaultLimits(),
			})
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if result.ExitCode != 0 || len(result.Artifacts) != 1 || result.Artifacts[0].Path != "result/report.txt" {
				t.Fatalf("result = %+v", result)
			}
			create := runner.commands[0].args
			for _, required := range []string{
				"--pull", "never",
				"--network", "none", "--ipc", "none", "--read-only", "--cap-drop", "ALL",
				"--security-opt", "no-new-privileges:true", "--pids-limit", "32",
				"--memory", "268435456", "--memory-swap", "268435456", "--cpus", "0.5",
				"--user", "65532:65532", "--entrypoint", "/usr/bin/env", "-i",
				"python", "-I", "-B", workspaceSupervisor, "--log-driver", "none",
			} {
				if !slices.Contains(create, required) {
					t.Fatalf("create args do not contain %q: %v", required, create)
				}
			}
			if len(runner.commands) != 5 || runner.commands[1].args[0] != "start" ||
				!slices.Equal(runner.commands[2].args, scriptExecArguments(strings.Repeat("a", 64), "scripts/run.py")) ||
				!slices.Contains(runner.commands[3].args, workspaceTransferHelper) || runner.commands[4].args[0] != "rm" {
				t.Fatal("script lifecycle must create/start, execute, export live tmpfs, then remove its exact container ID")
			}
			joined := strings.Join(create, " ")
			if strings.Contains(joined, "AWS_SECRET") || strings.Contains(joined, "OPENAI_API_KEY") ||
				strings.Contains(joined, "--env ") || !strings.Contains(joined, "target=/skill,readonly") ||
				!strings.Contains(joined, "target=/input,readonly") ||
				!strings.Contains(joined, "/output:rw,noexec,nosuid,nodev,size=16777216") {
				t.Fatalf("isolation arguments are incomplete: %s", joined)
			}
		})
	}
}

func TestOCIScriptExportFailureAndUnconfirmedIdentityFailClosed(t *testing.T) {
	for _, failure := range []string{"start", "export", "unsafe", "malformed", "identity", "transport"} {
		t.Run(failure, func(t *testing.T) {
			runner := &fakeRunner{failure: failure, archive: buildOutputTar(t, map[string]string{"result.txt": "ok"})}
			if failure == "malformed" {
				runner.archive = []byte("not a tar archive")
			}
			if failure == "identity" {
				runner.createID = "unverified-container-name"
			}
			if failure == "transport" {
				runner.start = commandResult{ExitCode: -1}
				runner.startErr = errors.New("receipt lost")
			}
			sandbox, err := newOCI(Config{EnginePath: enginePathForTest(t, "linux"), Adapter: "linux", PythonImage: testPythonImage}, runner)
			if err != nil {
				t.Fatal(err)
			}
			skillRoot, workRoot, outputRoot := scriptFixture(t)
			result, err := sandbox.Execute(context.Background(), Request{ExecutionID: "exec_failure", SkillRoot: skillRoot,
				ScriptPath: "scripts/run.py", Runtime: "python", Input: json.RawMessage(`{}`), WorkRoot: workRoot,
				OutputRoot: outputRoot, Limits: DefaultLimits()})
			code := map[string]string{"start": "SCRIPT_SANDBOX_START_FAILED", "export": "SKILL_SCRIPT_OUTPUT_EXPORT_FAILED",
				"unsafe": "SKILL_SCRIPT_OUTPUT_ENTRY_UNSAFE", "malformed": "SKILL_SCRIPT_OUTPUT_ARCHIVE_INVALID",
				"identity": "SCRIPT_SANDBOX_CREATE_FAILED", "transport": "SKILL_SCRIPT_EXECUTION_FAILED"}[failure]
			assertSandboxCode(t, err, code)
			if len(result.Artifacts) != 0 {
				t.Fatal("unconfirmed export returned accepted artifacts")
			}
			files, err := os.ReadDir(outputRoot)
			if err != nil || len(files) != 0 {
				t.Fatalf("invalid output was extracted: %v", err)
			}
			if failure == "identity" {
				if len(runner.commands) != 1 {
					t.Fatal("unverified container was used or removed")
				}
			} else if runner.commands[len(runner.commands)-1].args[0] != "rm" {
				t.Fatal("failed script container was not removed")
			}
		})
	}
}

func TestOCIRejectsMutableImageAndHostScriptTraversal(t *testing.T) {
	_, err := newOCI(Config{EnginePath: enginePathForTest(t, "linux"), Adapter: "linux", PythonImage: "python:3.12"}, &fakeRunner{})
	if err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("mutable image error = %v", err)
	}
	sandbox, err := newOCI(Config{
		EnginePath: enginePathForTest(t, "linux"), Adapter: "linux", PythonImage: testPythonImage,
	}, &fakeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	skillRoot, workRoot, outputRoot := scriptFixture(t)
	_, err = sandbox.Execute(context.Background(), Request{
		ExecutionID: "exec_traversal", SkillRoot: skillRoot, ScriptPath: "scripts/../../escape.py",
		Runtime: "python", Input: json.RawMessage(`{}`), WorkRoot: workRoot,
		OutputRoot: outputRoot, Limits: DefaultLimits(),
	})
	assertSandboxCode(t, err, "SKILL_SCRIPT_PATH_UNSAFE")
}

func TestNewFromEnvironmentIsDisabledByDefaultAndRejectsPartialConfiguration(t *testing.T) {
	t.Setenv("CONTENT_AGENT_SCRIPT_SANDBOX_ADAPTER", "")
	t.Setenv("CONTENT_AGENT_SCRIPT_OCI_COMMAND", "")
	t.Setenv("CONTENT_AGENT_SCRIPT_PYTHON_IMAGE", "")
	sandbox, err := NewFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if status := sandbox.Status(); status.Available || status.ReasonCode != "SCRIPT_SANDBOX_NOT_CONFIGURED" {
		t.Fatalf("default status = %+v", status)
	}

	t.Setenv("CONTENT_AGENT_SCRIPT_OCI_COMMAND", filepath.Join(t.TempDir(), "docker.exe"))
	if _, err := NewFromEnvironment(); err == nil || !strings.Contains(err.Error(), "configured together") {
		t.Fatalf("partial configuration error = %v", err)
	}
}

func TestNewFromEnvironmentAcceptsPinnedLocalEngineConfiguration(t *testing.T) {
	engine := filepath.Join(t.TempDir(), "docker.exe")
	if err := os.WriteFile(engine, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTENT_AGENT_SCRIPT_SANDBOX_ADAPTER", "windows")
	t.Setenv("CONTENT_AGENT_SCRIPT_OCI_COMMAND", engine)
	t.Setenv("CONTENT_AGENT_SCRIPT_PYTHON_IMAGE", testPythonImage)
	sandbox, err := NewFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	status := sandbox.Status()
	if !status.Available || status.Adapter != "windows" || status.Engine != "docker" {
		t.Fatalf("configured status = %+v", status)
	}
}

func TestNewOCIRejectsRelativeOrMissingEnginePath(t *testing.T) {
	if _, err := NewOCI(Config{EnginePath: "docker", Adapter: "linux", PythonImage: testPythonImage}); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative engine error = %v", err)
	}
	missing := filepath.Join(t.TempDir(), "docker.exe")
	if _, err := NewOCI(Config{EnginePath: missing, Adapter: "windows", PythonImage: testPythonImage}); err == nil || !strings.Contains(err.Error(), "existing") {
		t.Fatalf("missing engine error = %v", err)
	}
}

func TestEngineEnvironmentDoesNotInheritCredentials(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "must-not-leak")
	t.Setenv("TEMP", t.TempDir())
	environment := minimalEngineEnvironment()
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "must-not-leak") || strings.Contains(joined, "OPENAI_API_KEY") ||
		strings.Contains(joined, "AWS_SECRET_ACCESS_KEY") {
		t.Fatalf("engine inherited credentials: %v", environment)
	}
	if !strings.Contains(joined, "TEMP=") {
		t.Fatalf("engine environment omitted required temporary directory: %v", environment)
	}
}

func TestOCITimeoutFailsClosedAndRemovesContainer(t *testing.T) {
	runner := &fakeRunner{block: true}
	sandbox, err := newOCI(Config{
		EnginePath: enginePathForTest(t, "linux"), Adapter: "linux", PythonImage: testPythonImage,
	}, runner)
	if err != nil {
		t.Fatal(err)
	}
	skillRoot, workRoot, outputRoot := scriptFixture(t)
	limits := DefaultLimits()
	limits.TimeoutSeconds = 1
	started := time.Now()
	_, err = sandbox.Execute(context.Background(), Request{
		ExecutionID: "exec_timeout", SkillRoot: skillRoot, ScriptPath: "scripts/run.py",
		Runtime: "python", Input: json.RawMessage(`{}`), WorkRoot: workRoot,
		OutputRoot: outputRoot, Limits: limits,
	})
	assertSandboxCode(t, err, "SKILL_SCRIPT_TIMEOUT")
	if time.Since(started) > 3*time.Second {
		t.Fatalf("timeout took too long: %s", time.Since(started))
	}
	last := runner.commands[len(runner.commands)-1].args
	if !slices.Equal(last[:2], []string{"rm", "--force"}) {
		t.Fatalf("cleanup command = %v", last)
	}
}

func TestOCICleanupFailureOverridesSuccess(t *testing.T) {
	runner := &fakeRunner{
		archive:    buildOutputTar(t, map[string]string{"result.txt": "ok"}),
		cleanupErr: errors.New("remove failed"),
	}
	sandbox, err := newOCI(Config{
		EnginePath: enginePathForTest(t, "linux"), Adapter: "linux", PythonImage: testPythonImage,
	}, runner)
	if err != nil {
		t.Fatal(err)
	}
	skillRoot, workRoot, outputRoot := scriptFixture(t)
	_, err = sandbox.Execute(context.Background(), Request{
		ExecutionID: "exec_cleanup", SkillRoot: skillRoot, ScriptPath: "scripts/run.py",
		Runtime: "python", Input: json.RawMessage(`{}`), WorkRoot: workRoot,
		OutputRoot: outputRoot, Limits: DefaultLimits(),
	})
	assertSandboxCode(t, err, "SCRIPT_SANDBOX_CLEANUP_FAILED")
}

func TestOCIRejectsOverlappingRootsAndOversizedInput(t *testing.T) {
	sandbox, err := newOCI(Config{
		EnginePath: enginePathForTest(t, "linux"), Adapter: "linux", PythonImage: testPythonImage,
	}, &fakeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	skillRoot, _, outputRoot := scriptFixture(t)
	_, err = sandbox.Execute(context.Background(), Request{
		ExecutionID: "exec_overlap", SkillRoot: skillRoot, ScriptPath: "scripts/run.py",
		Runtime: "python", Input: json.RawMessage(`{}`), WorkRoot: filepath.Join(skillRoot, "work"),
		OutputRoot: outputRoot, Limits: DefaultLimits(),
	})
	assertSandboxCode(t, err, "SKILL_SCRIPT_WORKSPACE_UNSAFE")
	oversized := append([]byte{'"'}, bytes.Repeat([]byte{'x'}, maxInputBytes)...)
	oversized = append(oversized, '"')
	_, err = sandbox.Execute(context.Background(), Request{
		ExecutionID: "exec_large_input", SkillRoot: skillRoot, ScriptPath: "scripts/run.py",
		Runtime: "python", Input: oversized, WorkRoot: filepath.Join(filepath.Dir(skillRoot), "work-large"),
		OutputRoot: outputRoot, Limits: DefaultLimits(),
	})
	assertSandboxCode(t, err, "SKILL_SCRIPT_INPUT_INVALID")
}

func TestOutputArchiveBlocksTraversalSymlinkAndDiskOverflow(t *testing.T) {
	root := t.TempDir()
	traversal := buildRawTar(t, tar.Header{Name: "../escape.txt", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}, []byte("x"))
	_, err := extractArtifactArchive(traversal, root, 1024)
	assertSandboxCode(t, err, "SKILL_SCRIPT_OUTPUT_PATH_UNSAFE")
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(root), "escape.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("archive escaped output root: %v", statErr)
	}

	symlink := buildRawTar(t, tar.Header{Name: "link", Linkname: "../escape", Typeflag: tar.TypeSymlink}, nil)
	_, err = extractArtifactArchive(symlink, root, 1024)
	assertSandboxCode(t, err, "SKILL_SCRIPT_OUTPUT_ENTRY_UNSAFE")

	overflow := buildRawTar(t, tar.Header{Name: "large.bin", Mode: 0o644, Size: 5, Typeflag: tar.TypeReg}, []byte("12345"))
	_, err = extractArtifactArchive(overflow, root, 4)
	assertSandboxCode(t, err, "SKILL_SCRIPT_DISK_LIMIT_EXCEEDED")
}

func scriptFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	skill := filepath.Join(root, "skill")
	if err := os.MkdirAll(filepath.Join(skill, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "scripts", "run.py"), []byte("print('ok')"), 0o644); err != nil {
		t.Fatal(err)
	}
	return skill, filepath.Join(root, "work"), filepath.Join(root, "output")
}

func enginePathForTest(t *testing.T, adapter string) string {
	t.Helper()
	name := "docker"
	if adapter == "windows" {
		name = "docker.exe"
	}
	return filepath.Join(t.TempDir(), name)
}

func buildOutputTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for name, content := range files {
		data := []byte(content)
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func buildRawTar(t *testing.T, header tar.Header, data []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&header); err != nil {
		t.Fatal(err)
	}
	if len(data) > 0 {
		if _, err := writer.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func assertSandboxCode(t *testing.T, err error, code string) {
	t.Helper()
	var sandboxError *Error
	if !errors.As(err, &sandboxError) || sandboxError.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
}
