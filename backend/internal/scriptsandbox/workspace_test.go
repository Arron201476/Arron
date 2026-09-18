package scriptsandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"slices"
	"strings"
	"testing"
)

type workspaceRunnerFixture struct {
	commands   [][]string
	state      workspaceInspection
	input      []byte
	result     commandResult
	runErr     error
	block      bool
	deleteErr  bool
	inspectErr bool
	stopOnExec bool
	deleted    bool
	createErr  bool
}

func (runner *workspaceRunnerFixture) Run(_ context.Context, _ string, args []string, _, _ int64) (commandResult, error) {
	runner.commands = append(runner.commands, slices.Clone(args))
	switch args[0] {
	case "create":
		if runner.createErr {
			return commandResult{ExitCode: -1}, errors.New("create receipt lost")
		}
		return commandResult{Stdout: []byte(runner.state.ID + "\n")}, nil
	case "start":
		runner.state.State.Running = true
		return commandResult{}, nil
	case "inspect":
		if runner.inspectErr {
			return commandResult{ExitCode: 1}, errors.New("inspection unavailable")
		}
		body, err := json.Marshal([]workspaceInspection{runner.state})
		return commandResult{Stdout: body}, err
	case "rm":
		if runner.deleteErr {
			return commandResult{ExitCode: 1}, errors.New("cleanup unavailable")
		}
		runner.deleted = true
		runner.state.State.Running = false
		return commandResult{}, nil
	default:
		return commandResult{ExitCode: -1}, errors.New("unexpected engine operation")
	}
}

func (runner *workspaceRunnerFixture) RunInput(ctx context.Context, _ string, args []string, input io.Reader, stdoutLimit, stderrLimit int64) (commandResult, error) {
	runner.commands = append(runner.commands, slices.Clone(args))
	if stdoutLimit != 1<<20 || stderrLimit != 1<<20 {
		return commandResult{ExitCode: -1}, errors.New("unbounded output")
	}
	runner.input, _ = io.ReadAll(input)
	if runner.block {
		<-ctx.Done()
		return commandResult{ExitCode: -1}, ctx.Err()
	}
	if runner.stopOnExec {
		runner.state.State.Running = false
	}
	return runner.result, runner.runErr
}

func workspaceFixture(t *testing.T) (*OCI, WorkspaceHandle, *workspaceRunnerFixture) {
	t.Helper()
	runner := &workspaceRunnerFixture{}
	sandbox, err := newOCI(Config{EnginePath: enginePathForTest(t, "linux"), Adapter: "linux", PythonImage: testPythonImage}, runner)
	if err != nil {
		t.Fatal(err)
	}
	limits := DefaultLimits()
	limits.TimeoutSeconds = 1
	handle := WorkspaceHandle{SessionID: "12345678-1234-1234-1234-123456789abc", ContainerID: strings.Repeat("a", 64), OwnerHash: strings.Repeat("b", 64), Limits: limits, PolicyHash: sandbox.workspacePolicyHash(limits)}
	runner.state.ID = handle.ContainerID
	runner.state.Name = "/content-agent-workspace-" + handle.SessionID
	runner.state.Config.Image = testPythonImage
	runner.state.Config.User = workspaceUser
	runner.state.Config.WorkingDir = workspaceRoot
	runner.state.Config.Entrypoint = []string{"/usr/bin/env"}
	runner.state.Config.Cmd = workspaceSupervisorArguments()
	runner.state.Config.Labels = map[string]string{workspaceLabel + ".session": handle.SessionID, workspaceLabel + ".owner": handle.OwnerHash, workspaceLabel + ".policy": handle.PolicyHash}
	config := &runner.state.HostConfig
	config.NetworkMode, config.IpcMode = "none", "none"
	config.ReadonlyRootfs = true
	config.CapDrop = []string{"ALL"}
	config.SecurityOpt = []string{"no-new-privileges:true"}
	config.PidsLimit = int64(limits.ProcessCount)
	config.Memory, config.MemorySwap = limits.MemoryBytes, limits.MemoryBytes
	config.NanoCpus = int64(math.Round(limits.CPUCount * 1e9))
	config.Tmpfs = workspaceTmpfs(limits)
	config.LogConfig.Type = "none"
	return sandbox, handle, runner
}

func TestNativeWorkspaceCreationHasNoHostMountOrAmbientEnvironment(t *testing.T) {
	sandbox, expected, runner := workspaceFixture(t)
	handle, err := sandbox.CreateWorkspace(context.Background(), expected.SessionID, expected.OwnerHash, expected.Limits)
	if err != nil || handle != expected {
		t.Fatalf("create = %+v, %v", handle, err)
	}
	if len(runner.commands) != 4 || runner.commands[0][0] != "create" || runner.commands[1][0] != "inspect" || runner.commands[2][0] != "start" || runner.commands[3][0] != "inspect" {
		t.Fatalf("creation order = %v", runner.commands)
	}
	create := runner.commands[0]
	for flag, value := range map[string]string{"--pull": "never", "--network": "none", "--ipc": "none", "--user": workspaceUser, "--pids-limit": "32", "--memory": "268435456", "--memory-swap": "268435456", "--cpus": "0.5", "--log-driver": "none", "--security-opt": "no-new-privileges:true"} {
		index := slices.Index(create, flag)
		if index < 0 || index+1 >= len(create) || create[index+1] != value {
			t.Fatalf("missing enforced %s=%s: %v", flag, value, create)
		}
	}
	for _, forbidden := range []string{"--mount", "--volume", "--env", "--privileged", "--publish"} {
		if slices.Contains(create, forbidden) {
			t.Fatalf("unexpected option %s", forbidden)
		}
	}
	if !slices.Contains(create, "--read-only") || !slices.Contains(create, "/workspace:rw,noexec,nosuid,nodev,size=16777216,mode=700,uid=65532,gid=65532") {
		t.Fatal("workspace restrictions are incomplete")
	}
	if err := sandbox.ReconnectWorkspace(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.DeleteWorkspace(context.Background(), handle); err != nil || !runner.deleted {
		t.Fatalf("delete = %v", err)
	}
}

func TestNativeWorkspaceReconnectRejectsIsolationOrOwnershipDrift(t *testing.T) {
	mutations := map[string]func(*workspaceInspection){
		"owner":      func(s *workspaceInspection) { s.Config.Labels[workspaceLabel+".owner"] = strings.Repeat("c", 64) },
		"image":      func(s *workspaceInspection) { s.Config.Image = "python:latest" },
		"identity":   func(s *workspaceInspection) { s.ID = strings.Repeat("c", 64) },
		"root_user":  func(s *workspaceInspection) { s.Config.User = "0:0" },
		"entrypoint": func(s *workspaceInspection) { s.Config.Entrypoint = []string{"/bin/sh"} },
		"supervisor": func(s *workspaceInspection) { s.Config.Cmd = []string{"python", "untrusted.py"} },
		"network":    func(s *workspaceInspection) { s.HostConfig.NetworkMode = "host" },
		"privileged": func(s *workspaceInspection) { s.HostConfig.Privileged = true },
		"host_pid":   func(s *workspaceInspection) { s.HostConfig.PidMode = "host" },
		"host_mount": func(s *workspaceInspection) { s.HostConfig.Binds = []string{"/host:/workspace"} },
		"volume": func(s *workspaceInspection) {
			s.Mounts = append(s.Mounts, struct{ Type, Destination string }{"volume", workspaceRoot})
		},
		"capability": func(s *workspaceInspection) { s.HostConfig.CapAdd = []string{"SYS_ADMIN"} },
		"security":   func(s *workspaceInspection) { s.HostConfig.SecurityOpt = []string{"seccomp=unconfined"} },
		"memory":     func(s *workspaceInspection) { s.HostConfig.Memory = 0 },
		"disk":       func(s *workspaceInspection) { s.HostConfig.Tmpfs[workspaceRoot] = "rw" },
		"log":        func(s *workspaceInspection) { s.HostConfig.LogConfig.Type = "json-file" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			sandbox, handle, runner := workspaceFixture(t)
			runner.state.State.Running = true
			mutate(&runner.state)
			assertSandboxCode(t, sandbox.ReconnectWorkspace(context.Background(), handle), "WORKSPACE_BINDING_INVALID")
			_, err := sandbox.ExecuteWorkspace(context.Background(), handle, WorkspaceCommand{Argv: []string{"pwd"}, Cwd: workspaceRoot})
			assertSandboxCode(t, err, "WORKSPACE_BINDING_INVALID")
			for _, command := range runner.commands {
				if command[0] != "inspect" {
					t.Fatalf("unverified workspace operation = %v", command)
				}
			}
		})
	}
}

func TestNativeWorkspaceExecPreservesBinaryInputAndNonzeroProgramResult(t *testing.T) {
	sandbox, handle, runner := workspaceFixture(t)
	runner.state.State.Running = true
	runner.result = commandResult{Stdout: []byte{0, 255, 128, 10}, Stderr: []byte("program rejected input"), ExitCode: 2}
	runner.runErr = errors.New("program exited 2")
	input := []byte{0, 255, 128, 1}
	argv := []string{"/bin/sh", "-c", "printf '%s' '--privileged'"}
	result, err := sandbox.ExecuteWorkspace(context.Background(), handle, WorkspaceCommand{Argv: argv, Cwd: workspaceRoot, Stdin: input})
	if err != nil || result.ExitCode != 2 || !bytes.Equal(result.Stdout, runner.result.Stdout) || !bytes.Equal(input, runner.input) || runner.deleted {
		t.Fatalf("result = %+v, %v", result, err)
	}
	command := runner.commands[1]
	expectedPrefix := []string{"exec", "--interactive", "--user", workspaceUser, "--workdir", workspaceRoot, handle.ContainerID, "/usr/bin/env", "-i", "--", "PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "HOME=" + workspaceRoot}
	if !slices.Equal(command, append(expectedPrefix, argv...)) || len(runner.commands) != 3 {
		t.Fatalf("untrusted input crossed engine option boundary: %v", command)
	}
}

func TestNativeWorkspaceExecInterruptionTerminatesBoundEnvironment(t *testing.T) {
	for _, failure := range []string{"timeout", "transport", "overflow", "cleanup_failure"} {
		t.Run(failure, func(t *testing.T) {
			sandbox, handle, runner := workspaceFixture(t)
			runner.state.State.Running = true
			runner.result.ExitCode = -1
			runner.runErr = errors.New("transport failed")
			if failure == "timeout" {
				runner.block = true
			}
			if failure == "overflow" {
				runner.result.ExitCode = 0
				runner.runErr = &Error{Code: "SKILL_SCRIPT_OUTPUT_LIMIT_EXCEEDED", Message: "output overflow"}
			}
			runner.deleteErr = failure == "cleanup_failure"
			_, err := sandbox.ExecuteWorkspace(context.Background(), handle, WorkspaceCommand{Argv: []string{"sleep", "60"}, Cwd: workspaceRoot})
			code := "WORKSPACE_COMMAND_INTERRUPTED"
			if runner.deleteErr {
				code = "WORKSPACE_CLEANUP_UNCONFIRMED"
			} else if !runner.deleted {
				t.Fatal("interrupted engine command left an untracked container process")
			}
			assertSandboxCode(t, err, code)
		})
	}
}

func TestNativeWorkspaceDoesNotRecreateMissingOrStoppedEnvironment(t *testing.T) {
	for _, missing := range []bool{false, true} {
		sandbox, handle, runner := workspaceFixture(t)
		runner.inspectErr = missing
		code := "WORKSPACE_STOPPED"
		if missing {
			code = "WORKSPACE_UNAVAILABLE"
		}
		assertSandboxCode(t, sandbox.ReconnectWorkspace(context.Background(), handle), code)
		if len(runner.commands) != 1 || runner.commands[0][0] != "inspect" {
			t.Fatalf("missing workspace silently recreated: %v", runner.commands)
		}
	}
}

func TestNativeWorkspaceRejectsInvalidCommandBeforeEngine(t *testing.T) {
	for _, command := range []WorkspaceCommand{
		{Argv: []string{"pwd"}, Cwd: "/etc"}, {Argv: []string{"pwd"}, Cwd: "/workspace/../tmp"},
		{Argv: []string{"--help"}, Cwd: workspaceRoot}, {Argv: []string{"LD_PRELOAD=payload", "pwd"}, Cwd: workspaceRoot},
		{Argv: []string{"pwd", "a\x00b"}, Cwd: workspaceRoot}, {Argv: []string{"pwd"}, Cwd: workspaceRoot, Stdin: make([]byte, (1<<20)+1)},
	} {
		sandbox, handle, runner := workspaceFixture(t)
		_, err := sandbox.ExecuteWorkspace(context.Background(), handle, command)
		assertSandboxCode(t, err, "WORKSPACE_COMMAND_INVALID")
		if len(runner.commands) != 0 {
			t.Fatal("invalid command reached engine")
		}
	}
}

func TestResourceLimitsRejectNonFiniteCPU(t *testing.T) {
	for _, cpu := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		limits := DefaultLimits()
		limits.CPUCount = cpu
		if limits.Validate() == nil {
			t.Fatalf("accepted non-finite CPU limit: %v", cpu)
		}
	}
}

func TestNativeWorkspaceLostCreateReceiptCanBeLocatedWithoutReplay(t *testing.T) {
	sandbox, expected, runner := workspaceFixture(t)
	runner.createErr = true
	_, err := sandbox.CreateWorkspace(context.Background(), expected.SessionID, expected.OwnerHash, expected.Limits)
	assertSandboxCode(t, err, "WORKSPACE_CREATE_UNCONFIRMED")
	if len(runner.commands) != 1 || runner.deleted {
		t.Fatal("unconfirmed creation triggered another mutation")
	}
	handle, running, err := sandbox.FindWorkspace(context.Background(), expected.SessionID, expected.OwnerHash, expected.Limits)
	if err != nil || handle != expected || running || len(runner.commands) != 2 || runner.commands[1][0] != "inspect" {
		t.Fatalf("find = %+v, %v, %v, commands=%v", handle, running, err, runner.commands)
	}
	_, _, err = sandbox.FindWorkspace(context.Background(), expected.SessionID, strings.Repeat("c", 64), expected.Limits)
	assertSandboxCode(t, err, "WORKSPACE_BINDING_INVALID")
	if err := sandbox.DeleteWorkspace(context.Background(), handle); err != nil || !runner.deleted {
		t.Fatalf("verified orphan cleanup = %v", err)
	}
}

func TestNativeWorkspaceStoppedDuringCommandCannotReturnConfirmedOutput(t *testing.T) {
	sandbox, handle, runner := workspaceFixture(t)
	runner.state.State.Running = true
	runner.stopOnExec = true
	_, err := sandbox.ExecuteWorkspace(context.Background(), handle, WorkspaceCommand{Argv: []string{"pwd"}, Cwd: workspaceRoot})
	assertSandboxCode(t, err, "WORKSPACE_STOPPED")
}
