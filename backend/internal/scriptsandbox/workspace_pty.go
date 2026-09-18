package scriptsandbox

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"regexp"
	"slices"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

//go:embed workspace_pty.py
var workspacePTYHelper string

var workspacePTYIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

const workspacePTYOutputBytes = 1 << 20
const workspacePTYFrameBytes = 2 << 20

type WorkspacePTYStart struct {
	Command     WorkspaceCommand `json:"command"`
	TTY         bool             `json:"tty"`
	YieldMillis int              `json:"yield_ms"`
}

type WorkspacePTYInput struct {
	ProcessID   string `json:"process_id"`
	Input       []byte `json:"input"`
	YieldMillis int    `json:"yield_ms"`
	Terminate   bool   `json:"terminate"`
}

type WorkspacePTYResult struct {
	ProcessID  string `json:"process_id"`
	Output     []byte `json:"output"`
	ExitCode   *int   `json:"exit_code"`
	Reason     string `json:"reason"`
	InputBytes int    `json:"input_bytes"`
}

type workspacePTYStream interface {
	Exchange(context.Context, []byte) ([]byte, error)
	Close() error
}

type workspacePTYRunner interface {
	StartWorkspacePTYStream(context.Context, string, []string) (workspacePTYStream, error)
}

type workspacePTYProcess struct {
	handle   WorkspaceHandle
	stream   workspacePTYStream
	sequence int
	tty      bool
	finished atomic.Bool
}

type workspacePTYRegistry struct {
	mu        sync.Mutex
	processes map[string]*workspacePTYProcess
}

func ValidateWorkspacePTYStart(request WorkspacePTYStart, limits Limits) error {
	if err := ValidateWorkspaceCommand(request.Command, limits); err != nil {
		return err
	}
	if len(request.Command.Stdin) != 0 || request.YieldMillis < 1 || request.YieldMillis > 30000 ||
		len(request.Command.Cwd) > 4096 || !utf8.ValidString(request.Command.Cwd) {
		return workspaceError("WORKSPACE_PTY_REQUEST_INVALID", "PTY startup requires a bounded yield and no implicit stdin")
	}
	for _, arg := range request.Command.Argv {
		if !utf8.ValidString(arg) {
			return workspaceError("WORKSPACE_PTY_REQUEST_INVALID", "PTY arguments must retain exact UTF-8 values")
		}
	}
	return nil
}

func ValidateWorkspacePTYInput(request WorkspacePTYInput) error {
	if !workspacePTYIDPattern.MatchString(request.ProcessID) || len(request.Input) > 64<<10 ||
		request.YieldMillis < 1 || request.YieldMillis > 30000 || request.Terminate && len(request.Input) != 0 {
		return workspaceError("WORKSPACE_PTY_REQUEST_INVALID", "PTY input must target an exact process with bounded bytes and yield")
	}
	return nil
}

// Runtime must persist the process identity and audited request before calling
// this interface. This transport never authorizes a tool or restarts a process.
func (sandbox *OCI) StartWorkspacePTY(ctx context.Context, handle WorkspaceHandle, processID string, request WorkspacePTYStart) (WorkspacePTYResult, error) {
	if !workspacePTYIDPattern.MatchString(processID) {
		return WorkspacePTYResult{}, workspaceError("WORKSPACE_PTY_REQUEST_INVALID", "PTY requires a unique process identity")
	}
	if err := ValidateWorkspacePTYStart(request, handle.Limits); err != nil {
		return WorkspacePTYResult{}, err
	}
	timeout := request.Command.TimeoutMillis
	if timeout == 0 {
		timeout = handle.Limits.TimeoutSeconds * 1000
	}
	frame, err := json.Marshal(struct {
		Sequence      int      `json:"sequence"`
		Operation     string   `json:"operation"`
		Argv          []string `json:"argv"`
		Cwd           string   `json:"cwd"`
		TTY           bool     `json:"tty"`
		TimeoutMillis int      `json:"timeout_ms"`
		YieldMillis   int      `json:"yield_ms"`
	}{1, "start", slices.Clone(request.Command.Argv), request.Command.Cwd, request.TTY, timeout, request.YieldMillis})
	if err != nil || len(frame)+1 > 128<<10 {
		return WorkspacePTYResult{}, workspaceError("WORKSPACE_PTY_REQUEST_INVALID", "PTY startup frame exceeds its bounded protocol")
	}
	runner, ok := sandbox.runner.(workspacePTYRunner)
	if !ok {
		return WorkspacePTYResult{}, workspaceError("WORKSPACE_ADAPTER_UNAVAILABLE", "Engine has no interactive workspace transport")
	}
	unlock, err := sandbox.lockWorkspace(ctx, handle)
	if err != nil {
		return WorkspacePTYResult{}, err
	}
	defer unlock()
	if err := sandbox.inspectWorkspace(ctx, handle, true); err != nil {
		return WorkspacePTYResult{}, err
	}
	process, err := sandbox.reserveWorkspacePTY(handle, processID, request.TTY)
	if err != nil {
		return WorkspacePTYResult{}, err
	}
	args := []string{"exec", "--interactive", "--user", workspaceUser, "--workdir", workspaceRoot, handle.ContainerID,
		"/usr/bin/env", "-i", "--", "PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "HOME=" + workspaceRoot,
		"python", "-I", "-B", "-u", "-c", workspacePTYHelper}
	process.stream, err = runner.StartWorkspacePTYStream(ctx, sandbox.enginePath, args)
	if err != nil || process.stream == nil {
		return WorkspacePTYResult{}, sandbox.abortWorkspacePTY(handle, process)
	}
	return sandbox.exchangeWorkspacePTY(ctx, processID, process, frame, request.YieldMillis, 0)
}

func (sandbox *OCI) WriteWorkspacePTY(ctx context.Context, handle WorkspaceHandle, request WorkspacePTYInput) (WorkspacePTYResult, error) {
	if err := ValidateWorkspacePTYInput(request); err != nil {
		return WorkspacePTYResult{}, err
	}
	unlock, err := sandbox.lockWorkspace(ctx, handle)
	if err != nil {
		return WorkspacePTYResult{}, err
	}
	defer unlock()
	sandbox.workspacePTYs.mu.Lock()
	process := sandbox.workspacePTYs.processes[handle.ContainerID+":"+request.ProcessID]
	sandbox.workspacePTYs.mu.Unlock()
	if process == nil || process.handle != handle || process.finished.Load() || process.stream == nil {
		return WorkspacePTYResult{}, workspaceError("WORKSPACE_PTY_SESSION_LOST", "PTY is missing or already ended; snapshot recovery cannot restart its process")
	}
	if len(request.Input) != 0 && !process.tty {
		return WorkspacePTYResult{}, workspaceError("WORKSPACE_PTY_STDIN_UNAVAILABLE", "Only an explicitly interactive process accepts stdin")
	}
	if err := sandbox.inspectWorkspace(ctx, handle, true); err != nil {
		return WorkspacePTYResult{}, err
	}
	operation := "input"
	if request.Terminate {
		operation = "terminate"
	}
	input := bytes.Clone(request.Input)
	if input == nil {
		input = []byte{}
	}
	frame, err := json.Marshal(struct {
		Sequence    int    `json:"sequence"`
		Operation   string `json:"operation"`
		Input       []byte `json:"input"`
		YieldMillis int    `json:"yield_ms"`
	}{process.sequence + 1, operation, input, request.YieldMillis})
	if err != nil {
		return WorkspacePTYResult{}, err
	}
	return sandbox.exchangeWorkspacePTY(ctx, request.ProcessID, process, frame, request.YieldMillis, len(input))
}

func (sandbox *OCI) reserveWorkspacePTY(handle WorkspaceHandle, id string, tty bool) (*workspacePTYProcess, error) {
	registry := &sandbox.workspacePTYs
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.processes == nil {
		registry.processes = map[string]*workspacePTYProcess{}
	}
	key := handle.ContainerID + ":" + id
	if _, exists := registry.processes[key]; exists {
		return nil, workspaceError("WORKSPACE_PTY_REPLAY_FORBIDDEN", "An existing PTY process identity cannot launch another command")
	}
	total, active, allActive := 0, 0, 0
	for _, process := range registry.processes {
		if !process.finished.Load() {
			allActive++
		}
		if process.handle.ContainerID == handle.ContainerID {
			total++
			if !process.finished.Load() {
				active++
			}
		}
	}
	limit := min(16, (handle.Limits.ProcessCount-2)/2)
	if total >= 128 || active >= limit || allActive >= 128 || len(registry.processes) >= 4096 {
		return nil, workspaceError("WORKSPACE_PTY_LIMIT_EXCEEDED", "Interactive process quota reached")
	}
	process := &workspacePTYProcess{handle: handle, tty: tty}
	registry.processes[key] = process
	return process, nil
}

func (sandbox *OCI) exchangeWorkspacePTY(ctx context.Context, id string, process *workspacePTYProcess, frame []byte, yieldMillis, inputBytes int) (WorkspacePTYResult, error) {
	// The per-call yield is not the command lifetime. The bridge enforces the
	// administrator's total deadline even when the caller is not polling.
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(yieldMillis)*time.Millisecond+5*time.Second)
	defer cancel()
	process.sequence++
	raw, err := process.stream.Exchange(callCtx, append(frame, '\n'))
	var response struct {
		Sequence   int    `json:"sequence"`
		Output     []byte `json:"output"`
		ExitCode   *int   `json:"exit_code"`
		Reason     string `json:"reason"`
		InputBytes int    `json:"input_bytes"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err != nil || callCtx.Err() != nil || len(raw) > workspacePTYFrameBytes || decoder.Decode(&response) != nil ||
		decoder.Decode(new(any)) != io.EOF || response.Sequence != process.sequence || response.Output == nil ||
		len(response.Output) > workspacePTYOutputBytes || response.InputBytes < 0 || response.InputBytes > inputBytes ||
		(response.ExitCode == nil && (response.Reason != "running" || response.InputBytes != inputBytes)) ||
		(response.ExitCode != nil && (*response.ExitCode < 0 || *response.ExitCode > 255 ||
			!slices.Contains([]string{"exited", "terminated", "timeout", "output_limit"}, response.Reason))) {
		return WorkspacePTYResult{}, sandbox.abortWorkspacePTY(process.handle, process)
	}
	if response.ExitCode != nil {
		process.finished.Store(true)
		if err := process.stream.Close(); err != nil {
			return WorkspacePTYResult{}, sandbox.abortWorkspacePTY(process.handle, process)
		}
		process.stream = nil
	}
	return WorkspacePTYResult{ProcessID: id, Output: response.Output, ExitCode: response.ExitCode, Reason: response.Reason, InputBytes: response.InputBytes}, nil
}

func (sandbox *OCI) abortWorkspacePTY(handle WorkspaceHandle, process *workspacePTYProcess) error {
	process.finished.Store(true)
	if process.stream != nil {
		if process.stream.Close() == nil {
			process.stream = nil
		}
	}
	// Closing the host CLI is not evidence that the container process stopped.
	cleanupCtx, cancel := context.WithTimeout(context.Background(), engineOperationTimeout)
	defer cancel()
	if err := sandbox.deleteWorkspace(cleanupCtx, handle); err != nil {
		return workspaceError("WORKSPACE_CLEANUP_UNCONFIRMED", "PTY outcome and exact workspace termination are unconfirmed")
	}
	return workspaceError("WORKSPACE_PTY_INTERRUPTED", "Workspace terminated after unconfirmed PTY I/O; the request was not replayed")
}

func (sandbox *OCI) forgetWorkspacePTYs(containerID string) {
	registry := &sandbox.workspacePTYs
	registry.mu.Lock()
	var streams []workspacePTYStream
	for key, process := range registry.processes {
		if process.handle.ContainerID == containerID {
			if process.stream != nil {
				streams = append(streams, process.stream)
			}
			delete(registry.processes, key)
		}
	}
	registry.mu.Unlock()
	var closed sync.WaitGroup
	for _, stream := range streams {
		closed.Add(1)
		go func(stream workspacePTYStream) {
			defer closed.Done()
			_ = stream.Close()
		}(stream)
	}
	closed.Wait()
}

type execWorkspacePTYStream struct {
	command    *exec.Cmd
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	reader     *bufio.Reader
	once       sync.Once
	closeErr   error
	frameLimit int
}

func (execCommandRunner) StartWorkspacePTYStream(ctx context.Context, name string, args []string) (workspacePTYStream, error) {
	return startWorkspaceProcessStream(ctx, name, args, workspacePTYFrameBytes)
}

func startWorkspaceProcessStream(ctx context.Context, name string, args []string, frameLimit int) (workspacePTYStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	command := exec.Command(name, args...)
	command.Env = minimalEngineEnvironment()
	command.Stderr = io.Discard
	command.WaitDelay = 2 * time.Second
	in, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := command.StdoutPipe()
	if err != nil {
		_ = in.Close()
		return nil, err
	}
	if err := command.Start(); err != nil {
		_ = in.Close()
		_ = out.Close()
		return nil, err
	}
	return &execWorkspacePTYStream{command: command, stdin: in, stdout: out,
		reader: bufio.NewReaderSize(out, 8192), frameLimit: frameLimit}, nil
}

func readWorkspacePTYFrame(reader *bufio.Reader) ([]byte, error) {
	return readWorkspaceProcessFrame(reader, workspacePTYFrameBytes)
}

func readWorkspaceProcessFrame(reader *bufio.Reader, limit int) ([]byte, error) {
	var body []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(body)+len(part) > limit {
			return nil, errors.New("workspace process response exceeds its frame limit")
		}
		body = append(body, part...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return body, err
	}
}

func (stream *execWorkspacePTYStream) Exchange(ctx context.Context, frame []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type response struct {
		body []byte
		err  error
	}
	done := make(chan response, 1)
	go func() {
		if _, err := stream.stdin.Write(frame); err != nil {
			done <- response{err: err}
			return
		}
		limit := stream.frameLimit
		if limit == 0 {
			limit = workspacePTYFrameBytes
		}
		body, err := readWorkspaceProcessFrame(stream.reader, limit)
		done <- response{body: body, err: err}
	}()
	select {
	case result := <-done:
		return result.body, result.err
	case <-ctx.Done():
		_ = stream.Close()
		return nil, ctx.Err()
	}
}

func (stream *execWorkspacePTYStream) Close() error {
	stream.once.Do(func() {
		_ = stream.stdin.Close()
		_ = stream.stdout.Close()
		// The helper normally exits on its final receipt or EOF. Bound the host
		// transport wait; unknown termination is escalated to exact OCI cleanup.
		done := make(chan error, 1)
		go func() { done <- stream.command.Wait() }()
		select {
		case err := <-done:
			stream.closeErr = err
		case <-time.After(3 * time.Second):
			_ = stream.command.Process.Kill()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
			}
			stream.closeErr = errors.New("PTY bridge did not close cleanly")
		}
	})
	return stream.closeErr
}
