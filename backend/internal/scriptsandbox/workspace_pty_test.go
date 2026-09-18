package scriptsandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

type ptyStreamFixture struct {
	requests    []map[string]any
	responses   [][]byte
	exchangeErr error
	closeErr    error
	closed      bool
}

func (stream *ptyStreamFixture) Exchange(_ context.Context, frame []byte) ([]byte, error) {
	var request map[string]any
	if err := json.Unmarshal(frame, &request); err != nil {
		return nil, err
	}
	stream.requests = append(stream.requests, request)
	if stream.exchangeErr != nil {
		return nil, stream.exchangeErr
	}
	if len(stream.responses) == 0 {
		return nil, io.ErrUnexpectedEOF
	}
	response := stream.responses[0]
	stream.responses = stream.responses[1:]
	return response, nil
}

func (stream *ptyStreamFixture) Close() error {
	stream.closed = true
	return stream.closeErr
}

type ptyRunnerFixture struct {
	*workspaceRunnerFixture
	stream    *ptyStreamFixture
	starts    int
	startArgs []string
	startErr  error
}

func (runner *ptyRunnerFixture) StartWorkspacePTYStream(_ context.Context, _ string, args []string) (workspacePTYStream, error) {
	runner.starts++
	runner.startArgs = slices.Clone(args)
	return runner.stream, runner.startErr
}

func ptyResponse(sequence int, output []byte, exitCode *int, reason string, inputBytes int) []byte {
	if output == nil {
		output = []byte{}
	}
	encoded, _ := json.Marshal(map[string]any{"sequence": sequence, "output": output, "exit_code": exitCode, "reason": reason, "input_bytes": inputBytes})
	return append(encoded, '\n')
}

func ptyFixture(t *testing.T) (*OCI, WorkspaceHandle, *ptyRunnerFixture) {
	t.Helper()
	sandbox, handle, runner := workspaceFixture(t)
	runner.state.State.Running = true
	pty := &ptyRunnerFixture{workspaceRunnerFixture: runner, stream: &ptyStreamFixture{responses: [][]byte{ptyResponse(1, []byte("ready"), nil, "running", 0)}}}
	sandbox.runner = pty
	return sandbox, handle, pty
}

func ptyStartRequest() WorkspacePTYStart {
	return WorkspacePTYStart{Command: WorkspaceCommand{Argv: []string{"python", "-c", "print(input())"}, Cwd: "/workspace"}, TTY: true, YieldMillis: 1000}
}

func TestWorkspacePTYStartInputPollExitAndTransportIsolation(t *testing.T) {
	sandbox, handle, runner := ptyFixture(t)
	id := strings.Repeat("a", 32)
	first, err := sandbox.StartWorkspacePTY(context.Background(), handle, id, ptyStartRequest())
	if err != nil || first.ProcessID != id || string(first.Output) != "ready" || first.ExitCode != nil {
		t.Fatalf("startup = %+v, %v", first, err)
	}
	args := runner.startArgs
	for _, forbidden := range []string{"--tty", "-t", "--privileged", "--env", "--mount", "--volume"} {
		if slices.Contains(args, forbidden) {
			t.Fatalf("interactive transport escapes fixed protocol: %s", forbidden)
		}
	}
	for flag, expected := range map[string]string{"--user": workspaceUser, "--workdir": workspaceRoot} {
		index := slices.Index(args, flag)
		if index < 0 || args[index+1] != expected {
			t.Fatalf("missing bound %s", flag)
		}
	}
	if !slices.Contains(args, handle.ContainerID) || args[len(args)-1] != workspacePTYHelper || !slices.Contains(args, "--interactive") {
		t.Fatal("startup does not target the exact verified container and helper")
	}
	if runner.stream.requests[0]["timeout_ms"] != float64(1000) || runner.stream.requests[0]["tty"] != true {
		t.Fatal("request did not retain administrator lifetime and explicit interactive mode")
	}
	exit := 23
	runner.stream.responses = append(runner.stream.responses,
		ptyResponse(2, []byte{0, 255, 'a'}, nil, "running", 3),
		ptyResponse(3, []byte("done"), &exit, "exited", 0))
	second, err := sandbox.WriteWorkspacePTY(context.Background(), handle, WorkspacePTYInput{ProcessID: id, Input: []byte("ok\n"), YieldMillis: 1000})
	if err != nil || !slices.Equal(second.Output, []byte{0, 255, 'a'}) || second.InputBytes != 3 || runner.stream.requests[1]["input"] != "b2sK" {
		t.Fatalf("input = %+v, %v", second, err)
	}
	final, err := sandbox.WriteWorkspacePTY(context.Background(), handle, WorkspacePTYInput{ProcessID: id, YieldMillis: 1000})
	if err != nil || final.ExitCode == nil || *final.ExitCode != 23 || !runner.stream.closed || runner.deleted {
		t.Fatalf("exit = %+v, %v", final, err)
	}
	_, err = sandbox.WriteWorkspacePTY(context.Background(), handle, WorkspacePTYInput{ProcessID: id, YieldMillis: 1000})
	if ErrorCode(err) != "WORKSPACE_PTY_SESSION_LOST" || len(runner.stream.requests) != 3 || runner.starts != 1 {
		t.Fatalf("ended process was reused: %v", err)
	}
	_, err = sandbox.StartWorkspacePTY(context.Background(), handle, id, ptyStartRequest())
	if ErrorCode(err) != "WORKSPACE_PTY_REPLAY_FORBIDDEN" || runner.starts != 1 {
		t.Fatalf("process identity was replayed: %v", err)
	}
}

func TestWorkspacePTYUnknownResponsesStopExactWorkspaceWithoutReplaying(t *testing.T) {
	for _, kind := range []string{"lost", "wrong_sequence", "bad_json", "oversize", "missing_output", "inconsistent_exit", "close_failed", "cleanup_failed"} {
		t.Run(kind, func(t *testing.T) {
			sandbox, handle, runner := ptyFixture(t)
			exit := 0
			switch kind {
			case "lost", "cleanup_failed":
				runner.stream.exchangeErr = io.ErrUnexpectedEOF
				runner.deleteErr = kind == "cleanup_failed"
			case "wrong_sequence":
				runner.stream.responses = [][]byte{ptyResponse(99, nil, nil, "running", 0)}
			case "bad_json":
				runner.stream.responses = [][]byte{[]byte("not a receipt")}
			case "oversize":
				runner.stream.responses = [][]byte{ptyResponse(1, make([]byte, workspacePTYOutputBytes+1), nil, "running", 0)}
			case "missing_output":
				runner.stream.responses = [][]byte{[]byte(`{"sequence":1,"reason":"running","exit_code":null,"input_bytes":0}`)}
			case "inconsistent_exit":
				runner.stream.responses = [][]byte{ptyResponse(1, nil, &exit, "running", 0)}
			case "close_failed":
				runner.stream.responses = [][]byte{ptyResponse(1, nil, &exit, "exited", 0)}
				runner.stream.closeErr = errors.New("close unconfirmed")
			}
			_, err := sandbox.StartWorkspacePTY(context.Background(), handle, strings.Repeat("a", 32), ptyStartRequest())
			want := "WORKSPACE_PTY_INTERRUPTED"
			if kind == "cleanup_failed" {
				want = "WORKSPACE_CLEANUP_UNCONFIRMED"
			}
			if ErrorCode(err) != want || runner.starts != 1 || len(runner.stream.requests) != 1 || !runner.stream.closed || runner.deleted != !runner.deleteErr {
				t.Fatalf("unconfirmed = %v; starts=%d deleted=%v", err, runner.starts, runner.deleted)
			}
		})
	}
}

func TestWorkspacePTYInvalidInputAndNonTTYDoNotSendBytes(t *testing.T) {
	sandbox, handle, runner := ptyFixture(t)
	id := strings.Repeat("a", 32)
	request := ptyStartRequest()
	request.TTY = false
	if _, err := sandbox.StartWorkspacePTY(context.Background(), handle, id, request); err != nil {
		t.Fatal(err)
	}
	for _, input := range []WorkspacePTYInput{
		{ProcessID: id, Input: []byte("not interactive"), YieldMillis: 1000},
		{ProcessID: id, Input: make([]byte, (64<<10)+1), YieldMillis: 1000},
		{ProcessID: id, YieldMillis: 30001},
		{ProcessID: "arbitrary", YieldMillis: 1000},
	} {
		if _, err := sandbox.WriteWorkspacePTY(context.Background(), handle, input); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	if len(runner.stream.requests) != 1 || runner.deleted {
		t.Fatal("invalid input touched the process")
	}
}

func TestWorkspacePTYRestartHasNoImplicitProcessRecovery(t *testing.T) {
	sandbox, handle, runner := ptyFixture(t)
	id := strings.Repeat("a", 32)
	if _, err := sandbox.StartWorkspacePTY(context.Background(), handle, id, ptyStartRequest()); err != nil {
		t.Fatal(err)
	}
	restarted, err := newOCI(Config{EnginePath: sandbox.enginePath, Adapter: "linux", PythonImage: testPythonImage}, runner)
	if err != nil {
		t.Fatal(err)
	}
	_, err = restarted.WriteWorkspacePTY(context.Background(), handle, WorkspacePTYInput{ProcessID: id, YieldMillis: 1000})
	if ErrorCode(err) != "WORKSPACE_PTY_SESSION_LOST" || runner.starts != 1 || len(runner.stream.requests) != 1 {
		t.Fatalf("lost process was re-created: %v", err)
	}
}

func TestWorkspacePTYExplicitStopAndWorkspaceDeleteCloseOwnedStreams(t *testing.T) {
	sandbox, handle, runner := ptyFixture(t)
	id := strings.Repeat("a", 32)
	if _, err := sandbox.StartWorkspacePTY(context.Background(), handle, id, ptyStartRequest()); err != nil {
		t.Fatal(err)
	}
	exit := 137
	runner.stream.responses = [][]byte{ptyResponse(2, []byte("last output"), &exit, "terminated", 0)}
	result, err := sandbox.WriteWorkspacePTY(context.Background(), handle, WorkspacePTYInput{ProcessID: id, Terminate: true, YieldMillis: 1000})
	if err != nil || result.Reason != "terminated" || runner.stream.requests[1]["operation"] != "terminate" || !runner.stream.closed {
		t.Fatalf("termination = %+v, %v", result, err)
	}
	if err := sandbox.DeleteWorkspace(context.Background(), handle); err != nil || len(sandbox.workspacePTYs.processes) != 0 {
		t.Fatalf("workspace deletion retained PTY handles: %v", err)
	}
}

func TestWorkspacePTYInvalidStartupHasNoEngineSideEffects(t *testing.T) {
	for _, kind := range []string{"id", "argv", "cwd", "stdin", "yield", "unicode", "wire_limit", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			sandbox, handle, runner := ptyFixture(t)
			request, id := ptyStartRequest(), strings.Repeat("a", 32)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "id":
				id = "invalid"
			case "argv":
				request.Command.Argv = []string{""}
			case "cwd":
				request.Command.Cwd = "/workspace/" + strings.Repeat("a", 4096)
			case "stdin":
				request.Command.Stdin = []byte("not explicitly supplied")
			case "yield":
				request.YieldMillis = 30001
			case "unicode":
				request.Command.Argv = []string{"python", "\xff"}
			case "wire_limit":
				request.Command.Argv = []string{"python", strings.Repeat("\x01", 30000)}
			case "cancelled":
				cancel()
			}
			if _, err := sandbox.StartWorkspacePTY(ctx, handle, id, request); err == nil {
				t.Fatal("invalid startup accepted")
			}
			if runner.starts != 0 || len(runner.commands) != 0 || len(sandbox.workspacePTYs.processes) != 0 {
				t.Fatal("invalid startup changed engine state")
			}
		})
	}
}

func TestWorkspacePTYFrameReaderBoundsChunkedAndMissingNewline(t *testing.T) {
	valid := append(bytes.Repeat([]byte{'a'}, 100000), '\n')
	actual, err := readWorkspacePTYFrame(bufio.NewReaderSize(bytes.NewReader(valid), 17))
	if err != nil || !bytes.Equal(actual, valid) {
		t.Fatalf("chunked receipt = %d bytes, %v", len(actual), err)
	}
	for _, value := range [][]byte{bytes.Repeat([]byte{'x'}, workspacePTYFrameBytes+1), []byte("missing newline")} {
		if _, err := readWorkspacePTYFrame(bufio.NewReaderSize(bytes.NewReader(value), 17)); err == nil {
			t.Fatal("unbounded or partial receipt accepted")
		}
	}
}
