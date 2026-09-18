package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"content-agent/backend/internal/agentcontract"
)

func assertToolExecutionProjection(t *testing.T, store *Store, call AgentToolCall, want AgentToolExecution) {
	t.Helper()
	ctx := context.Background()
	loaded, err := store.GetAgentToolCall(ctx, call.AgentToolCallID)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := store.ListAgentToolCalls(ctx, call.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	var match *AgentToolCall
	for index := range listed {
		if listed[index].AgentToolCallID == call.AgentToolCallID {
			match = &listed[index]
		}
	}
	if match == nil {
		t.Fatal("tool missing from public history")
	}
	for _, actual := range []AgentToolCall{call, loaded, *match} {
		if actual.Execution == nil || !reflect.DeepEqual(*actual.Execution, want) {
			t.Fatalf("execution origin = %+v, want %+v", actual.Execution, want)
		}
		payload, err := json.Marshal(actual.Execution)
		if err != nil {
			t.Fatal(err)
		}
		for _, private := range []string{"token", "run_state", "request_json", "checkpoint", "lease"} {
			if strings.Contains(string(payload), private) {
				t.Fatalf("private execution data in projection: %s", payload)
			}
		}
	}
}

func TestAgentToolExecutionProjectionConversation(t *testing.T) {
	ctx := context.Background()
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "origin.db"))
	defer store.Close()
	project, err := store.CreateProject(ctx, "Conversation origin")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Read files"},
		CommandMeta{Scope: project.ProjectID, CommandType: "create_message", IdempotencyKey: "origin", RequestHash: "origin"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil {
		t.Fatal(err)
	}
	command := BeginAgentToolCallCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		AgentTurnID: turn.AgentTurnID, SDKToolCallID: "origin-turn", ToolID: "runtime:list_workspace_files"}
	call, err := store.BeginAgentToolCall(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	assertToolExecutionProjection(t, store, call, AgentToolExecution{Mode: "conversation", AgentTurnID: turn.AgentTurnID})
	command.AgentTurnID, command.SDKToolCallID = "", "unbound"
	unbound, err := store.BeginAgentToolCall(ctx, command)
	if err != nil || unbound.Execution != nil {
		t.Fatalf("invented origin for unbound tool: %+v %v", unbound, err)
	}
}

func TestAgentToolExecutionProjectionBackground(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "origin.db"), loadBackgroundTaskRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	task := createBackgroundTaskForTest(t, store)
	claim, err := store.ClaimAgentTask(ctx, ClaimAgentTaskCommand{WorkerID: "origin", ProviderID: "sdk", ModelID: "fixture", LeaseSeconds: 60})
	if err != nil || claim == nil {
		t.Fatalf("claim: %+v %v", claim, err)
	}
	call := backgroundApprovalForTest(t, store, *claim, "origin-background")
	assertToolExecutionProjection(t, store, call, AgentToolExecution{Mode: "background_task", AgentTaskID: task.AgentTaskID,
		AttemptID: claim.Attempt.AgentTaskAttemptID, AttemptNo: 1, CapabilityID: task.CapabilityID})
}

func TestAgentToolExecutionProjectionStatefulAndScopeMismatch(t *testing.T) {
	ctx := context.Background()
	store := openProjectFilesTestStore(t, filepath.Join(t.TempDir(), "origin.db"))
	defer store.Close()
	project, claim := statefulToolClaimForTest(t, store)
	call, err := store.BeginAgentToolCall(ctx, statefulToolCommand(project, claim, "runtime:list_workspace_files", "origin-workflow", json.RawMessage(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	assertToolExecutionProjection(t, store, call, AgentToolExecution{Mode: "stateful_workflow", AttemptID: claim.Attempt.AttemptID, AttemptNo: 1,
		RunID: claim.Attempt.RunID, StepRunID: claim.Attempt.StepRunID, StepID: claim.ContextPack.Step.StepID,
		TaskItemID: claim.Attempt.TaskItemID, ItemKey: claim.Task.ItemKey, CapabilityID: claim.CapabilityID})
	foreign, err := store.CreateProject(ctx, "Unrelated project")
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []struct{ project, conversation, workspace string }{
		{foreign.ProjectID, project.PrimaryConversationID, project.WorkspaceID},
		{project.ProjectID, foreign.PrimaryConversationID, project.WorkspaceID},
		{project.ProjectID, project.PrimaryConversationID, "foreign-workspace"},
	} {
		if _, err := store.db.Exec(`UPDATE agent_tool_calls SET project_id = ?, conversation_id = ?, workspace_id = ? WHERE agent_tool_call_id = ?`,
			scope.project, scope.conversation, scope.workspace, call.AgentToolCallID); err != nil {
			t.Fatal(err)
		}
		loaded, err := store.GetAgentToolCall(ctx, call.AgentToolCallID)
		if err != nil || loaded.Execution != nil {
			t.Fatalf("foreign execution leaked: %+v %v", loaded.Execution, err)
		}
	}
}
