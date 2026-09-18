package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"content-agent/backend/internal/agenttool"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/scriptsandbox"
)

type nativeHTTPPTYEngine struct {
	*nativeHTTPEngine
	starts, inputs int
}

func (e *nativeHTTPPTYEngine) StartWorkspacePTY(ctx context.Context, handle scriptsandbox.WorkspaceHandle, id string, request scriptsandbox.WorkspacePTYStart) (scriptsandbox.WorkspacePTYResult, error) {
	e.starts++
	if err := e.ReconnectWorkspace(ctx, handle); err != nil {
		return scriptsandbox.WorkspacePTYResult{}, err
	}
	return scriptsandbox.WorkspacePTYResult{ProcessID: id, Output: []byte("ready"), Reason: "running"}, nil
}

func (e *nativeHTTPPTYEngine) WriteWorkspacePTY(ctx context.Context, handle scriptsandbox.WorkspaceHandle, input scriptsandbox.WorkspacePTYInput) (scriptsandbox.WorkspacePTYResult, error) {
	e.inputs++
	if err := e.ReconnectWorkspace(ctx, handle); err != nil {
		return scriptsandbox.WorkspacePTYResult{}, err
	}
	exit := 7
	return scriptsandbox.WorkspacePTYResult{ProcessID: input.ProcessID, Output: []byte{0, 255, 128}, ExitCode: &exit, Reason: "exited", InputBytes: len(input.Input)}, nil
}

func TestNativePTYHTTPRequiresConsumedApprovalAndReturnsExactDurableReceipt(t *testing.T) {
	f := newNativeHTTPFixture(t)
	f.policy(t, true, nil)
	engine := &nativeHTTPPTYEngine{nativeHTTPEngine: f.engine}
	f.server.ConfigureNativeWorkspaceEngine(engine)
	registry, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	f.store.SetAgentToolRegistry(registry)
	f.request(t, http.MethodPost, "ensure", strings.NewReader(`{}`), f.headers, http.StatusOK)
	activity := businessruntime.AgentActivityIdentity{ProjectID: f.headers["X-Agent-Project-ID"], AgentTurnID: f.headers["X-Agent-Turn-ID"]}
	ctx := businessruntime.WithAgentActivity(identity.WithPrincipal(f.ctx, identity.ServicePrincipal()), activity)
	project, err := f.store.GetProject(f.ctx, activity.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	start := scriptsandbox.WorkspacePTYStart{Command: scriptsandbox.WorkspaceCommand{Argv: []string{"python", "-c", "input()"}, Cwd: "/workspace"}, TTY: true, YieldMillis: 1000}
	for _, input := range []bool{false, true} {
		tool, sdk, arguments := "runtime:exec_command", "sdk-pty-start", json.RawMessage(`{"cmd":"read input","tty":true}`)
		request := businessruntime.NativeWorkspacePTYRequest{Start: &start}
		if input {
			tool, sdk, arguments = "runtime:write_stdin", "sdk-pty-input", json.RawMessage(`{"session_id":1000,"chars":"hello\n"}`)
			request = businessruntime.NativeWorkspacePTYRequest{Input: &businessruntime.NativeWorkspacePTYInput{SessionID: 1000, ExpectedSequence: 1, Chars: "hello\n", YieldMillis: 1000}}
		}
		call, err := f.store.BeginAgentToolCall(ctx, businessruntime.BeginAgentToolCallCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
			AgentTurnID: activity.AgentTurnID, SDKToolCallID: sdk, ToolID: tool, Arguments: arguments})
		if err != nil {
			t.Fatal(err)
		}
		request.AgentToolCallID, request.SDKToolCallID, request.Arguments = call.AgentToolCallID, sdk, arguments
		raw, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		f.request(t, http.MethodPost, "pty", bytes.NewReader(raw), f.headers, http.StatusForbidden)
		if _, err := f.store.ResolveAgentToolApproval(f.ctx, businessruntime.ResolveAgentToolApprovalCommand{AgentToolApprovalID: call.Approval.AgentToolApprovalID,
			ExpectedVersion: call.Approval.Version, SubjectSnapshotHash: call.Approval.SubjectSnapshotHash, Action: "approve"}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.StartAgentToolCall(ctx, businessruntime.StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: sdk}); err != nil {
			t.Fatal(err)
		}
		result := f.request(t, http.MethodPost, "pty", bytes.NewReader(raw), f.headers, http.StatusOK)
		var receipt struct {
			Data businessruntime.NativeWorkspacePTYReceipt `json:"data"`
		}
		if err := json.Unmarshal(result.Body.Bytes(), &receipt); err != nil || receipt.Data.PTYSessionID != 1000 || receipt.Data.SessionID != f.session || receipt.Data.AgentToolCallID != call.AgentToolCallID {
			t.Fatalf("unbound terminal receipt: %+v, %v", receipt, err)
		}
		if input && (receipt.Data.Sequence != 2 || receipt.Data.Result.InputBytes != 6 || receipt.Data.Result.ExitCode == nil || *receipt.Data.Result.ExitCode != 7 || !bytes.Equal(receipt.Data.Result.Output, []byte{0, 255, 128})) {
			t.Fatalf("input receipt lost its process state or bytes: %+v", receipt)
		}
		if _, err := f.store.CompleteAgentToolCall(ctx, businessruntime.CompleteAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, Result: json.RawMessage(`"SDK output"`)}); err != nil {
			t.Fatal(err)
		}
		again := f.request(t, http.MethodPost, "pty", bytes.NewReader(raw), f.headers, http.StatusOK)
		if !bytes.Equal(again.Body.Bytes(), result.Body.Bytes()) {
			t.Fatal("receipt retry changed its historical result")
		}
	}
	if engine.starts != 1 || engine.inputs != 1 {
		t.Fatal("approval rejection or receipt retry executed terminal I/O")
	}
	for _, body := range []string{`null`, `{"unexpected":true}`, `{"arguments":"` + strings.Repeat("a", 256<<10) + `"}`} {
		f.request(t, http.MethodPost, "pty", strings.NewReader(body), f.headers, http.StatusBadRequest)
	}
}

func TestNativePTYHTTPRejectsAuthorityBeforeReadingInput(t *testing.T) {
	f := newNativeHTTPFixture(t)
	for _, endpoint := range []string{"pty", "pty/terminate"} {
		for _, name := range []string{"Authorization", "X-Agent-Turn-ID", "X-Workspace-Lease-Key", "X-Agent-Dispatch-Generation", "X-Workspace-Lease-Generation"} {
			t.Run(endpoint+"/"+name, func(t *testing.T) {
				headers := maps.Clone(f.headers)
				headers[name] = "invalid"
				body := &nativeUnreadBody{}
				request := httptest.NewRequest(http.MethodPost, nativeHTTPPrefix+f.session+"/"+endpoint, body)
				for key, value := range headers {
					request.Header.Set(key, value)
				}
				result := httptest.NewRecorder()
				f.server.Handler().ServeHTTP(result, request)
				if result.Code < 400 || body.reads != 0 {
					t.Fatalf("invalid terminal authority consumed input: status=%d reads=%d", result.Code, body.reads)
				}
			})
		}
	}
}

func TestNativePTYLifecycleHTTPAcceptsOnlyEmptyOwnerCleanup(t *testing.T) {
	f := newNativeHTTPFixture(t)
	f.policy(t, true, nil)
	for _, body := range []string{`null`, `{"process_id":"host-pid"}`, `{"signal":"KILL"}`, `{"command":"anything"}`, `{"data":"` + strings.Repeat("x", 4096) + `"}`} {
		f.request(t, http.MethodPost, "pty/terminate", strings.NewReader(body), f.headers, http.StatusBadRequest)
	}
	result := f.request(t, http.MethodPost, "pty/terminate", strings.NewReader(`{}`), f.headers, http.StatusOK)
	var receipt struct {
		Data businessruntime.NativeWorkspaceEnvironment `json:"data"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &receipt); err != nil || receipt.Data.SessionID != f.session || receipt.Data.State != "stopped" {
		t.Fatalf("cleanup = %+v, %v", receipt, err)
	}
	if f.engine.creates != 0 {
		t.Fatal("empty cleanup created an environment")
	}
}

func TestNativePTYStateHTTPRequiresExactExecutionAndReturnsNoHostProcess(t *testing.T) {
	f := newNativeHTTPFixture(t)
	f.policy(t, true, nil)
	result := f.request(t, http.MethodGet, "pty/state", nil, f.headers, http.StatusOK)
	var receipt struct {
		Data businessruntime.NativeWorkspacePTYState `json:"data"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &receipt); err != nil || receipt.Data.SessionID != f.session || receipt.Data.State != "absent" || receipt.Data.Processes == nil {
		t.Fatalf("inventory = %+v, %v", receipt, err)
	}
	for _, name := range []string{"Authorization", "X-Agent-Turn-ID", "X-Workspace-Lease-Key", "X-Agent-Dispatch-Generation", "X-Workspace-Lease-Generation"} {
		headers := maps.Clone(f.headers)
		headers[name] = "invalid"
		request := httptest.NewRequest(http.MethodGet, nativeHTTPPrefix+f.session+"/pty/state", nil)
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		result := httptest.NewRecorder()
		f.server.Handler().ServeHTTP(result, request)
		if result.Code < 400 {
			t.Fatal("foreign execution could read terminal inventory")
		}
	}
	if f.engine.creates != 0 {
		t.Fatal("reading inventory created an environment")
	}
}
