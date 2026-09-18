package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"content-agent/backend/internal/identity"
	"content-agent/backend/internal/scriptsandbox"
)

type runtimeScriptSandbox struct {
	status  scriptsandbox.Status
	result  scriptsandbox.Result
	err     error
	request scriptsandbox.Request
	calls   int
}

type waitingScriptSandbox struct {
	runtimeScriptSandbox
	started chan struct{}
}

func (sandbox *waitingScriptSandbox) Execute(ctx context.Context, _ scriptsandbox.Request) (scriptsandbox.Result, error) {
	close(sandbox.started)
	<-ctx.Done()
	return scriptsandbox.Result{}, ctx.Err()
}

func TestScriptExecutionStopsWhenToolCallIsCancelled(t *testing.T) {
	for _, cause := range []string{"tool-cancelled", "skill-disabled", "policy-disabled"} {
		t.Run(cause, func(t *testing.T) { testScriptExecutionRevocation(t, cause) })
	}
}

func testScriptExecutionRevocation(t *testing.T, cause string) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	sandbox := &waitingScriptSandbox{runtimeScriptSandbox: runtimeScriptSandbox{status: scriptsandbox.Status{Available: true, Adapter: "linux", Engine: "docker", Runtimes: []string{"python"}}}, started: make(chan struct{})}
	store.SetScriptSandbox(sandbox)
	project, err := store.CreateProject(ctx, "cancel running script")
	if err != nil {
		t.Fatal(err)
	}
	installed, err := store.InstallSkillZIP(ctx, "script.zip", bytesReader(buildScriptSkillZIP(t)), "admin")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := store.UpdateScriptSandboxPolicy(ctx, UpdateScriptSandboxPolicyCommand{Enabled: true, ActorRef: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	arguments := json.RawMessage(`{"skill_name":"sandbox-skill","script_id":"render","input_json":"{}"}`)
	call := approvedRunningScriptToolCall(t, store, project, "sdk-cancel-script", arguments)
	result := make(chan SkillScriptExecution, 1)
	failures := make(chan error, 1)
	executionCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	go func() {
		execution, err := store.ExecuteSkillScript(executionCtx, ExecuteSkillScriptCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID, Arguments: arguments})
		if err != nil {
			failures <- err
			return
		}
		result <- execution
	}()
	select {
	case <-sandbox.started:
	case err := <-failures:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("script did not start")
	}
	if !store.scriptToolCallActive(ctx, call.AgentToolCallID) {
		t.Fatal("script monitor rejected an authorized execution before revocation")
	}
	switch cause {
	case "skill-disabled":
		_, err = store.SetSkillInstallationEnabled(ctx, installed.SkillInstallationID, false, "admin")
	case "policy-disabled":
		_, err = store.UpdateScriptSandboxPolicy(ctx, UpdateScriptSandboxPolicyCommand{Enabled: false, ExpectedVersion: policy.Version, ActorRef: "admin"})
	default:
		_, err = store.CancelAgentToolCall(ctx, CancelAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, Reason: "cancelled"})
	}
	if err != nil {
		t.Fatal(err)
	}
	select {
	case execution := <-result:
		if execution.Status != "failed" {
			t.Fatalf("cancelled script succeeded: %+v", execution)
		}
	case err := <-failures:
		t.Fatal(err)
	case <-time.After(4 * time.Second):
		t.Fatal("cancelled tool did not stop the script")
	}
}

func (sandbox *runtimeScriptSandbox) Status() scriptsandbox.Status { return sandbox.status }

func (sandbox *runtimeScriptSandbox) Execute(
	_ context.Context,
	request scriptsandbox.Request,
) (scriptsandbox.Result, error) {
	sandbox.calls++
	sandbox.request = request
	if len(sandbox.result.Artifacts) > 0 {
		for _, artifact := range sandbox.result.Artifacts {
			path := filepath.Join(request.OutputRoot, filepath.FromSlash(artifact.Path))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return scriptsandbox.Result{}, err
			}
			if err := os.WriteFile(path, []byte("sandbox output"), 0o600); err != nil {
				return scriptsandbox.Result{}, err
			}
		}
	}
	return sandbox.result, sandbox.err
}

func TestSkillScriptPolicyApprovalExecutionAndTenantIsolation(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	fake := &runtimeScriptSandbox{
		status: scriptsandbox.Status{
			Available: true, Adapter: "linux", Engine: "docker", Runtimes: []string{"python"},
		},
		result: scriptsandbox.Result{
			Adapter: "linux", Engine: "docker", Runtime: "python", Image: "python@sha256:test",
			ExitCode: 0, StdoutSummary: "completed",
			Artifacts: []scriptsandbox.Artifact{{
				Path: "result.txt", SizeBytes: int64(len("sandbox output")),
				SHA256: "sha256:" + sha256Hex([]byte("sandbox output")),
			}},
			StartedAt: time.Now().UTC(), CompletedAt: time.Now().UTC(),
		},
	}
	store.SetScriptSandbox(fake)
	project, err := store.CreateProject(context.Background(), "script sandbox")
	if err != nil {
		t.Fatal(err)
	}
	installed, err := store.InstallSkillZIP(
		context.Background(), "script-fixture.zip",
		bytesReader(buildScriptSkillZIP(t)), "script_admin",
	)
	if err != nil {
		t.Fatalf("InstallSkillZIP() error = %v", err)
	}
	active := activeSkillVersionForTest(t, installed)
	if len(active.Manifest) == 0 || !json.Valid(active.Manifest) {
		t.Fatalf("installed script manifest = %s", active.Manifest)
	}

	policy, err := store.GetScriptSandboxPolicy(context.Background())
	if err != nil || policy.Enabled || policy.Version != 0 || !policy.Sandbox.Available {
		t.Fatalf("default policy = %+v, error = %v", policy, err)
	}
	arguments := json.RawMessage(`{"skill_name":"sandbox-skill","script_id":"render","input_json":"{\"title\":\"demo\",\"api_key\":\"must-not-persist\"}"}`)
	call := approvedRunningScriptToolCall(t, store, project, "script_sdk_1", arguments)
	_, err = store.ExecuteSkillScript(context.Background(), ExecuteSkillScriptCommand{
		AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID,
		Arguments: arguments,
	})
	assertDomainCode(t, err, "SKILL_SCRIPT_POLICY_DISABLED")

	policy, err = store.UpdateScriptSandboxPolicy(context.Background(), UpdateScriptSandboxPolicyCommand{
		ExpectedVersion: 0, Enabled: true, ActorRef: "script_admin",
	})
	if err != nil || !policy.Enabled || policy.Version != 1 || policy.UpdatedBy != "script_admin" {
		t.Fatalf("enabled policy = %+v, error = %v", policy, err)
	}
	execution, err := store.ExecuteSkillScript(context.Background(), ExecuteSkillScriptCommand{
		AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID,
		Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("ExecuteSkillScript() error = %v", err)
	}
	if execution.Status != "completed" || execution.ExitCode == nil || *execution.ExitCode != 0 ||
		len(execution.Artifacts) != 1 || execution.RequestedBy != "script_user" || fake.calls != 1 {
		t.Fatalf("execution = %+v, sandbox calls = %d", execution, fake.calls)
	}
	if fake.request.ScriptPath != "scripts/render.py" || fake.request.Runtime != "python" ||
		string(fake.request.Input) != `{"title":"demo","api_key":"must-not-persist"}` {
		t.Fatalf("sandbox request = %+v", fake.request)
	}
	var argumentsSummary string
	if err := store.db.QueryRowContext(context.Background(), `
		SELECT arguments_summary_json FROM agent_tool_calls WHERE agent_tool_call_id = ?`,
		call.AgentToolCallID,
	).Scan(&argumentsSummary); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(argumentsSummary, "must-not-persist") || !strings.Contains(argumentsSummary, "[redacted]") ||
		!strings.Contains(argumentsSummary, "demo") {
		t.Fatalf("script arguments summary was not safely redacted: %s", argumentsSummary)
	}
	repeated, err := store.ExecuteSkillScript(context.Background(), ExecuteSkillScriptCommand{
		AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID,
		Arguments: arguments,
	})
	if err != nil || repeated.SkillScriptExecutionID != execution.SkillScriptExecutionID || fake.calls != 1 {
		t.Fatalf("idempotent execution = %+v, error = %v, calls = %d", repeated, err, fake.calls)
	}
	changed := json.RawMessage(`{"skill_name":"sandbox-skill","script_id":"render","input_json":"{\"title\":\"changed\"}"}`)
	_, err = store.ExecuteSkillScript(context.Background(), ExecuteSkillScriptCommand{
		AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID,
		Arguments: changed,
	})
	assertDomainCode(t, err, "SKILL_SCRIPT_TOOL_CALL_MISMATCH")

	items, err := store.ListSkillScriptExecutions(context.Background(), project.ProjectID)
	if err != nil || len(items) != 1 {
		t.Fatalf("ListSkillScriptExecutions() = %+v, error = %v", items, err)
	}
	file, size, err := store.OpenSkillScriptArtifact(context.Background(), execution.SkillScriptExecutionID, "result.txt")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	if size != int64(len("sandbox output")) {
		t.Fatalf("artifact size = %d", size)
	}
	artifactRoot, err := resolveDataPath(store.dataRoot, execution.outputStorageRef)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactRoot, "result.txt"), []byte("tampered data!"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.OpenSkillScriptArtifact(context.Background(), execution.SkillScriptExecutionID, "result.txt")
	assertDomainCode(t, err, "SKILL_SCRIPT_ARTIFACT_INTEGRITY_FAILED")
	otherContext := identity.WithPrincipal(context.Background(), identity.Principal{
		Kind: identity.KindUser, UserID: "other_user", WorkspaceID: "workspace_other",
		Role: identity.RoleOwner,
	})
	_, err = store.GetSkillScriptExecution(otherContext, execution.SkillScriptExecutionID)
	assertDomainCode(t, err, "SKILL_SCRIPT_EXECUTION_NOT_FOUND")
}

func TestScriptSandboxPolicyCannotEnableUnavailableRuntime(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	policy, err := store.GetScriptSandboxPolicy(context.Background())
	if err != nil || policy.Sandbox.Available {
		t.Fatalf("default sandbox policy = %+v, error = %v", policy, err)
	}
	_, err = store.UpdateScriptSandboxPolicy(context.Background(), UpdateScriptSandboxPolicyCommand{
		ExpectedVersion: 0, Enabled: true,
	})
	assertDomainCode(t, err, "SCRIPT_SANDBOX_UNAVAILABLE")
}

func TestFreshSchemaStoresApprovalConsumptionOnAgentToolCalls(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	present, err := tableHasColumn(store.db, "agent_tool_calls", "approval_consumed_at")
	if err != nil || !present {
		t.Fatalf("agent_tool_calls approval_consumed_at present = %v, error = %v", present, err)
	}
	present, err = tableHasColumn(store.db, "agent_turns", "approval_consumed_at")
	if err != nil || present {
		t.Fatalf("agent_turns approval_consumed_at present = %v, error = %v", present, err)
	}
}

func TestApprovedAgentToolCallResumesOnlyWithExactSDKCallIdentity(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	if _, err := store.InstallSkillZIP(context.Background(), "script.zip", bytesReader(buildScriptSkillZIP(t)), "admin"); err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(context.Background(), "approval resume")
	if err != nil {
		t.Fatal(err)
	}
	arguments := json.RawMessage(`{"skill_name":"sandbox-skill","script_id":"render","input_json":"{}"}`)
	original, err := store.BeginAgentToolCall(context.Background(), BeginAgentToolCallCommand{
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		SDKToolCallID: "script_pending_1", ToolID: "runtime:execute_skill_script", Arguments: arguments,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ResolveAgentToolApproval(context.Background(), ResolveAgentToolApprovalCommand{
		AgentToolApprovalID: original.Approval.AgentToolApprovalID,
		ExpectedVersion:     original.Approval.Version, SubjectSnapshotHash: original.Approval.SubjectSnapshotHash,
		Action: "approve", ActorRef: "script_user",
	})
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := store.BeginAgentToolCall(context.Background(), BeginAgentToolCallCommand{
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		SDKToolCallID: "script_pending_1", ToolID: "runtime:execute_skill_script", Arguments: arguments,
	})
	if err != nil || resumed.AgentToolCallID != original.AgentToolCallID ||
		resumed.SDKToolCallID != "script_pending_1" || resumed.Status != "approved" {
		t.Fatalf("resumed call = %+v, error = %v", resumed, err)
	}
	second, err := store.BeginAgentToolCall(context.Background(), BeginAgentToolCallCommand{
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		SDKToolCallID: "script_resumed_2", ToolID: "runtime:execute_skill_script", Arguments: arguments,
	})
	if err != nil || second.AgentToolCallID == original.AgentToolCallID || second.Status != "pending_approval" {
		t.Fatalf("second approval call = %+v, error = %v", second, err)
	}
}

func approvedRunningScriptToolCall(
	t *testing.T,
	store *Store,
	project Project,
	sdkToolCallID string,
	arguments json.RawMessage,
) AgentToolCall {
	t.Helper()
	call, err := store.BeginAgentToolCall(context.Background(), BeginAgentToolCallCommand{
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		SDKToolCallID: sdkToolCallID, ToolID: "runtime:execute_skill_script", Arguments: arguments,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ResolveAgentToolApproval(context.Background(), ResolveAgentToolApprovalCommand{
		AgentToolApprovalID: call.Approval.AgentToolApprovalID,
		ExpectedVersion:     call.Approval.Version, SubjectSnapshotHash: call.Approval.SubjectSnapshotHash,
		Action: "approve", ActorRef: "script_user",
	})
	if err != nil {
		t.Fatal(err)
	}
	call, err = store.StartAgentToolCall(context.Background(), StartAgentToolCallCommand{
		AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return call
}

func buildScriptSkillZIP(t *testing.T) []byte {
	return buildScriptSkillVersionZIP(t, "1.0.0")
}

func buildScriptSkillVersionZIP(t *testing.T, version string) []byte {
	t.Helper()
	skillMarkdown := "---\nname: sandbox-skill\ndescription: Script sandbox fixture.\n---\n\nUse the declared renderer.\n"
	manifest := `{"schema_version":"1.0.0","id":"sandbox_skill","version":"` + version + `","execution_mode":"inline","scripts":[{"id":"render","path":"scripts/render.py","runtime":"python","description":"Render one result."}],"ui":{}}`
	return buildSkillZIP(t, []skillZIPTestEntry{
		{name: "SKILL.md", data: []byte(skillMarkdown)},
		{name: "content-agent/manifest.json", data: []byte(manifest)},
		{name: "scripts/render.py", data: []byte("print('render " + version + "')")},
	})
}

func bytesReader(value []byte) *bytes.Reader { return bytes.NewReader(value) }
