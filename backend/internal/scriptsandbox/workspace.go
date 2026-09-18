package scriptsandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const workspaceRoot = "/workspace"
const workspaceUser = "65532:65532"
const workspaceLabel = "content-agent.workspace"
const workspaceSupervisor = "import signal,time; signal.signal(signal.SIGCHLD, signal.SIG_IGN); time.sleep(2147483647)"

func workspaceSupervisorArguments() []string {
	return []string{"-i", "--", "PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "HOME=" + workspaceRoot,
		"python", "-I", "-B", "-c", workspaceSupervisor}
}

var workspaceIDPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
var containerIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// WorkspaceHandle is private execution state, not an API authorization token.
// The Runtime must check execution identity and lease before every operation.
type WorkspaceHandle struct {
	SessionID   string `json:"session_id"`
	ContainerID string `json:"container_id"`
	OwnerHash   string `json:"owner_hash"`
	PolicyHash  string `json:"policy_hash"`
	Limits      Limits `json:"limits"`
}

type WorkspaceCommand struct {
	Argv          []string `json:"argv"`
	Cwd           string   `json:"cwd"`
	Stdin         []byte   `json:"stdin,omitempty"`
	TimeoutMillis int      `json:"timeout_ms,omitempty"`
}

type WorkspaceCommandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

type inputCommandRunner interface {
	commandRunner
	RunInput(context.Context, string, []string, io.Reader, int64, int64) (commandResult, error)
}

type workspaceOperationLock struct {
	gate  chan struct{}
	users int
}

type workspaceOperationLocks struct {
	mu     sync.Mutex
	active map[string]*workspaceOperationLock
}

func (sandbox *OCI) lockWorkspace(ctx context.Context, handle WorkspaceHandle) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := sandbox.validateWorkspaceHandle(handle); err != nil {
		return nil, err
	}
	locks := &sandbox.workspaceLocks
	locks.mu.Lock()
	if locks.active == nil {
		locks.active = make(map[string]*workspaceOperationLock)
	}
	lock := locks.active[handle.ContainerID]
	if lock == nil {
		lock = &workspaceOperationLock{gate: make(chan struct{}, 1)}
		locks.active[handle.ContainerID] = lock
	}
	lock.users++
	locks.mu.Unlock()
	drop := func() {
		locks.mu.Lock()
		defer locks.mu.Unlock()
		lock.users--
		if lock.users == 0 {
			delete(locks.active, handle.ContainerID)
		}
	}
	select {
	case lock.gate <- struct{}{}:
		return func() { <-lock.gate; drop() }, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}

func workspaceError(code, message string) error { return &Error{Code: code, Message: message} }

func (sandbox *OCI) workspacePolicyHash(limits Limits) string {
	payload, _ := json.Marshal(struct {
		Version int
		Image   string
		Limits  Limits
	}{2, sandbox.pythonImage, limits})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func (sandbox *OCI) validateWorkspaceHandle(handle WorkspaceHandle) error {
	if !workspaceIDPattern.MatchString(handle.SessionID) || !containerIDPattern.MatchString(handle.ContainerID) ||
		!containerIDPattern.MatchString(handle.OwnerHash) || handle.Limits.Validate() != nil ||
		handle.PolicyHash != sandbox.workspacePolicyHash(handle.Limits) {
		return workspaceError("WORKSPACE_BINDING_INVALID", "Workspace execution binding is invalid")
	}
	return nil
}

func workspaceTmpfs(limits Limits) map[string]string {
	return map[string]string{
		workspaceRoot: "rw,noexec,nosuid,nodev,size=" + strconv.FormatInt(limits.DiskBytes, 10) + ",mode=700,uid=65532,gid=65532",
		"/tmp":        "rw,noexec,nosuid,nodev,size=" + strconv.FormatInt(limits.TempBytes, 10) + ",mode=1777",
	}
}

func (sandbox *OCI) workspaceCreateArguments(handle WorkspaceHandle) []string {
	limits := handle.Limits
	memory := strconv.FormatInt(limits.MemoryBytes, 10)
	args := []string{
		"create", "--pull", "never", "--name", "content-agent-workspace-" + handle.SessionID,
		"--label", workspaceLabel + ".session=" + handle.SessionID,
		"--label", workspaceLabel + ".owner=" + handle.OwnerHash,
		"--label", workspaceLabel + ".policy=" + handle.PolicyHash,
		"--hostname", "agent-workspace", "--network", "none", "--ipc", "none",
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
		"--pids-limit", strconv.Itoa(limits.ProcessCount), "--memory", memory, "--memory-swap", memory,
		"--cpus", strconv.FormatFloat(limits.CPUCount, 'f', -1, 64),
		"--ulimit", "nofile=64:64", "--ulimit", "core=0:0", "--user", workspaceUser,
		"--workdir", workspaceRoot, "--log-driver", "none", "--entrypoint", "/usr/bin/env",
	}
	for _, root := range []string{workspaceRoot, "/tmp"} {
		args = append(args, "--tmpfs", root+":"+workspaceTmpfs(limits)[root])
	}
	return append(append(args, sandbox.pythonImage), workspaceSupervisorArguments()...)
}

func (sandbox *OCI) CreateWorkspace(ctx context.Context, sessionID, ownerHash string, limits Limits) (WorkspaceHandle, error) {
	if !workspaceIDPattern.MatchString(sessionID) || !containerIDPattern.MatchString(ownerHash) || limits.Validate() != nil {
		return WorkspaceHandle{}, workspaceError("WORKSPACE_REQUEST_INVALID", "Workspace identity or resource limits are invalid")
	}
	handle := WorkspaceHandle{SessionID: sessionID, OwnerHash: ownerHash, Limits: limits, PolicyHash: sandbox.workspacePolicyHash(limits)}
	createCtx, cancel := context.WithTimeout(ctx, engineOperationTimeout)
	defer cancel()
	created, err := sandbox.runner.Run(createCtx, sandbox.enginePath, sandbox.workspaceCreateArguments(handle), 4096, 4096)
	if err != nil || created.ExitCode != 0 {
		// A lost create receipt must not cause deletion of an unverified existing container.
		return WorkspaceHandle{}, workspaceError("WORKSPACE_CREATE_UNCONFIRMED", "Workspace creation did not return a confirmed result")
	}
	handle.ContainerID = strings.TrimSpace(string(created.Stdout))
	if err := sandbox.validateWorkspaceHandle(handle); err != nil {
		return WorkspaceHandle{}, err
	}
	if err := sandbox.inspectWorkspace(ctx, handle, false); err != nil {
		return handle, err
	}
	startCtx, cancelStart := context.WithTimeout(ctx, engineOperationTimeout)
	defer cancelStart()
	started, err := sandbox.runner.Run(startCtx, sandbox.enginePath, []string{"start", handle.ContainerID}, 4096, 4096)
	if err != nil || started.ExitCode != 0 {
		return handle, workspaceError("WORKSPACE_START_UNCONFIRMED", "Workspace start did not return a confirmed result")
	}
	if err := sandbox.inspectWorkspace(ctx, handle, true); err != nil {
		return handle, err
	}
	return handle, nil
}

type workspaceInspection struct {
	ID     string `json:"Id"`
	Name   string
	State  struct{ Running, Paused bool }
	Config struct {
		Image        string
		User         string
		WorkingDir   string
		Entrypoint   []string
		Cmd          []string
		Labels       map[string]string
		ExposedPorts map[string]json.RawMessage
	}
	Mounts     []struct{ Type, Destination string }
	HostConfig struct {
		NetworkMode, IpcMode                       string
		PidMode, UTSMode, UsernsMode, CgroupnsMode string
		Privileged                                 bool
		ReadonlyRootfs                             bool
		CapAdd, CapDrop                            []string
		SecurityOpt                                []string
		Binds                                      []string
		VolumesFrom                                []string
		Mounts                                     []json.RawMessage
		Sysctls                                    map[string]string
		Devices                                    []json.RawMessage
		DeviceRequests                             []json.RawMessage
		PortBindings                               map[string]json.RawMessage
		Tmpfs                                      map[string]string
		PidsLimit                                  int64
		Memory, MemorySwap                         int64
		NanoCpus                                   int64
		LogConfig                                  struct{ Type string }
	}
}

func (sandbox *OCI) inspectWorkspace(ctx context.Context, handle WorkspaceHandle, requireRunning bool) error {
	if err := sandbox.validateWorkspaceHandle(handle); err != nil {
		return err
	}
	state, err := sandbox.readWorkspaceInspection(ctx, handle.ContainerID)
	if err != nil {
		return err
	}
	return sandbox.verifyWorkspaceInspection(handle, state, requireRunning)
}

func (sandbox *OCI) readWorkspaceInspection(ctx context.Context, selector string) (workspaceInspection, error) {
	inspectCtx, cancel := context.WithTimeout(ctx, engineOperationTimeout)
	defer cancel()
	result, err := sandbox.runner.Run(inspectCtx, sandbox.enginePath, []string{"inspect", "--type", "container", selector}, 64<<10, 4096)
	if err != nil || result.ExitCode != 0 {
		return workspaceInspection{}, workspaceError("WORKSPACE_UNAVAILABLE", "The bound workspace cannot be inspected; a replacement was not created")
	}
	var states []workspaceInspection
	if json.Unmarshal(result.Stdout, &states) != nil || len(states) != 1 {
		return workspaceInspection{}, workspaceError("WORKSPACE_BINDING_INVALID", "Workspace inspection is invalid")
	}
	return states[0], nil
}

func (sandbox *OCI) verifyWorkspaceInspection(handle WorkspaceHandle, state workspaceInspection, requireRunning bool) error {
	config := state.HostConfig
	limits := handle.Limits
	if state.ID != handle.ContainerID || state.Name != "/content-agent-workspace-"+handle.SessionID ||
		state.Config.Image != sandbox.pythonImage || state.Config.User != workspaceUser || state.Config.WorkingDir != workspaceRoot ||
		!slices.Equal(state.Config.Entrypoint, []string{"/usr/bin/env"}) || !slices.Equal(state.Config.Cmd, workspaceSupervisorArguments()) ||
		state.Config.Labels[workspaceLabel+".session"] != handle.SessionID || state.Config.Labels[workspaceLabel+".owner"] != handle.OwnerHash ||
		state.Config.Labels[workspaceLabel+".policy"] != handle.PolicyHash || len(state.Config.ExposedPorts) != 0 ||
		config.NetworkMode != "none" || config.IpcMode != "none" || config.Privileged || !config.ReadonlyRootfs ||
		config.PidMode != "" || config.UTSMode != "" || config.UsernsMode != "" || (config.CgroupnsMode != "" && config.CgroupnsMode != "private") ||
		len(config.CapAdd) != 0 || !slices.Equal(config.CapDrop, []string{"ALL"}) ||
		!slices.Equal(config.SecurityOpt, []string{"no-new-privileges:true"}) || len(config.Binds) != 0 ||
		len(config.VolumesFrom) != 0 || len(config.Mounts) != 0 || len(config.Sysctls) != 0 ||
		len(config.Devices) != 0 || len(config.DeviceRequests) != 0 || len(config.PortBindings) != 0 ||
		config.PidsLimit != int64(limits.ProcessCount) || config.Memory != limits.MemoryBytes || config.MemorySwap != limits.MemoryBytes ||
		config.NanoCpus != int64(math.Round(limits.CPUCount*1e9)) || config.LogConfig.Type != "none" ||
		!equalStringMap(config.Tmpfs, workspaceTmpfs(limits)) {
		return workspaceError("WORKSPACE_BINDING_INVALID", "Workspace identity or isolation settings changed")
	}
	for _, mount := range state.Mounts {
		if mount.Type != "tmpfs" || (mount.Destination != workspaceRoot && mount.Destination != "/tmp") {
			return workspaceError("WORKSPACE_BINDING_INVALID", "Workspace has an unauthorized mount")
		}
	}
	if requireRunning && !state.State.Running {
		return workspaceError("WORKSPACE_STOPPED", "Workspace stopped; restore requires the last verified snapshot")
	}
	if requireRunning && state.State.Paused {
		return workspaceError("WORKSPACE_FROZEN", "Workspace is frozen; execution cannot resume until recovery is confirmed")
	}
	return nil
}

// FindWorkspace resolves a lost create receipt using the Runtime's original
// identity and policy. It never creates, starts or deletes a container.
func (sandbox *OCI) FindWorkspace(ctx context.Context, sessionID, ownerHash string, limits Limits) (WorkspaceHandle, bool, error) {
	if !workspaceIDPattern.MatchString(sessionID) || !containerIDPattern.MatchString(ownerHash) || limits.Validate() != nil {
		return WorkspaceHandle{}, false, workspaceError("WORKSPACE_REQUEST_INVALID", "Workspace identity or resource limits are invalid")
	}
	state, err := sandbox.readWorkspaceInspection(ctx, "content-agent-workspace-"+sessionID)
	if err != nil {
		return WorkspaceHandle{}, false, err
	}
	handle := WorkspaceHandle{SessionID: sessionID, ContainerID: state.ID, OwnerHash: ownerHash, Limits: limits, PolicyHash: sandbox.workspacePolicyHash(limits)}
	if err := sandbox.validateWorkspaceHandle(handle); err != nil {
		return WorkspaceHandle{}, false, err
	}
	if err := sandbox.verifyWorkspaceInspection(handle, state, false); err != nil {
		return WorkspaceHandle{}, false, err
	}
	return handle, state.State.Running && !state.State.Paused, nil
}

func equalStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func (sandbox *OCI) ReconnectWorkspace(ctx context.Context, handle WorkspaceHandle) error {
	unlock, err := sandbox.lockWorkspace(ctx, handle)
	if err != nil {
		return err
	}
	defer unlock()
	return sandbox.inspectWorkspace(ctx, handle, true)
}

func (sandbox *OCI) DeleteWorkspace(ctx context.Context, handle WorkspaceHandle) error {
	unlock, err := sandbox.lockWorkspace(ctx, handle)
	if err != nil {
		return err
	}
	defer unlock()
	return sandbox.deleteWorkspace(ctx, handle)
}

func (sandbox *OCI) deleteWorkspace(ctx context.Context, handle WorkspaceHandle) error {
	if err := sandbox.inspectWorkspace(ctx, handle, false); err != nil {
		return err
	}
	cleanupCtx, cancel := context.WithTimeout(ctx, engineOperationTimeout)
	defer cancel()
	result, err := sandbox.runner.Run(cleanupCtx, sandbox.enginePath, []string{"rm", "--force", handle.ContainerID}, 4096, 4096)
	if err != nil || result.ExitCode != 0 {
		return workspaceError("WORKSPACE_CLEANUP_UNCONFIRMED", "Workspace cleanup failed; the environment may still exist")
	}
	sandbox.forgetWorkspacePTYs(handle.ContainerID)
	return nil
}

func ValidateWorkspaceCommand(command WorkspaceCommand, limits Limits) error {
	if len(command.Argv) == 0 || len(command.Argv) > 256 || len(command.Stdin) > 1<<20 ||
		command.TimeoutMillis < 0 || command.TimeoutMillis > limits.TimeoutSeconds*1000 ||
		path.Clean(command.Cwd) != command.Cwd || (command.Cwd != workspaceRoot && !strings.HasPrefix(command.Cwd, workspaceRoot+"/")) ||
		strings.ContainsAny(command.Cwd, "\\\x00\r\n") {
		return workspaceError("WORKSPACE_COMMAND_INVALID", "Command must target the bound workspace with bounded arguments and input")
	}
	size := 0
	for _, arg := range command.Argv {
		size += len(arg)
		if strings.ContainsRune(arg, '\x00') {
			return workspaceError("WORKSPACE_COMMAND_INVALID", "Command arguments contain an invalid null character")
		}
	}
	if size > 64<<10 || strings.TrimSpace(command.Argv[0]) == "" || strings.HasPrefix(command.Argv[0], "-") || strings.Contains(command.Argv[0], "=") || limits.Validate() != nil {
		return workspaceError("WORKSPACE_COMMAND_INVALID", "Command arguments exceed the platform limit")
	}
	return nil
}

func (sandbox *OCI) ExecuteWorkspace(ctx context.Context, handle WorkspaceHandle, command WorkspaceCommand) (WorkspaceCommandResult, error) {
	if err := ValidateWorkspaceCommand(command, handle.Limits); err != nil {
		return WorkspaceCommandResult{}, err
	}
	unlock, err := sandbox.lockWorkspace(ctx, handle)
	if err != nil {
		return WorkspaceCommandResult{}, err
	}
	defer unlock()
	if err := sandbox.inspectWorkspace(ctx, handle, true); err != nil {
		return WorkspaceCommandResult{}, err
	}
	runner, ok := sandbox.runner.(inputCommandRunner)
	if !ok {
		return WorkspaceCommandResult{}, workspaceError("WORKSPACE_ADAPTER_UNAVAILABLE", "Workspace command transport is unavailable")
	}
	args := []string{"exec", "--interactive", "--user", workspaceUser, "--workdir", command.Cwd, handle.ContainerID,
		"/usr/bin/env", "-i", "--", "PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "HOME=" + workspaceRoot}
	args = append(args, command.Argv...)
	timeout := time.Duration(handle.Limits.TimeoutSeconds) * time.Second
	if command.TimeoutMillis > 0 {
		timeout = time.Duration(command.TimeoutMillis) * time.Millisecond
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, runErr := runner.RunInput(runCtx, sandbox.enginePath, args, bytes.NewReader(command.Stdin), 1<<20, 1<<20)
	output := WorkspaceCommandResult{Stdout: result.Stdout, Stderr: result.Stderr, ExitCode: result.ExitCode}
	if runCtx.Err() != nil || (runErr != nil && (result.ExitCode <= 0 || ErrorCode(runErr) == "SKILL_SCRIPT_OUTPUT_LIMIT_EXCEEDED")) {
		// Cancelling the engine CLI alone does not guarantee its container child stopped.
		cleanupCtx, stop := context.WithTimeout(context.Background(), engineOperationTimeout)
		defer stop()
		if err := sandbox.deleteWorkspace(cleanupCtx, handle); err != nil {
			return output, workspaceError("WORKSPACE_CLEANUP_UNCONFIRMED", "Command outcome is unknown and workspace termination is unconfirmed")
		}
		return output, workspaceError("WORKSPACE_COMMAND_INTERRUPTED", "Workspace terminated after interrupted execution; uncheckpointed changes are not confirmed")
	}
	if err := sandbox.inspectWorkspace(ctx, handle, true); err != nil {
		return output, err
	}
	return output, nil
}
