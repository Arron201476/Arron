package runtime

import (
	"context"
	"encoding/json"
	"testing"
)

func TestMainTurnPauseProjectDeletionClosesCheckpointAndApprovals(t *testing.T) {
	for _, item := range []struct{ scope, status string }{
		{"project", "waiting_approval"}, {"project", "paused"}, {"project", "pausing"},
		{"workspace", "waiting_approval"}, {"workspace", "paused"}, {"workspace", "pausing"},
	} {
		t.Run(item.scope+"/"+item.status, func(t *testing.T) {
			status := item.status
			ctx := context.Background()
			store, project, turn, _ := pauseTurnFixture(t)
			defer store.Close()
			if _, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil {
				t.Fatal(err)
			}
			call, err := store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID, AgentTurnID: turn.AgentTurnID, SDKToolCallID: "old-write", ToolID: "mcp:fixture/save_fact", Arguments: json.RawMessage(`{"value":"original"}`), ConfigurationHash: toolConfigurationHashForTest(t, store, "mcp:fixture/save_fact")})
			if err != nil {
				t.Fatal(err)
			}
			checkpoint := PauseAgentTurnForApprovalCommand{SchemaVersion: "1.16", RunState: json.RawMessage(`{"$schemaVersion":"1.16"}`), PendingSDKToolCallIDs: []string{"old-write"}}
			if status != "pausing" {
				if _, err := store.PauseAgentTurnForApproval(ctx, turn.AgentTurnID, checkpoint); err != nil {
					t.Fatal(err)
				}
			}
			if status != "waiting_approval" {
				if _, err := store.RequestAgentTurnPause(ctx, ControlAgentTurnCommand{AgentTurnID: turn.AgentTurnID}); err != nil {
					t.Fatal(err)
				}
			}
			if item.scope == "workspace" {
				if _, err := store.DeleteWorkspace(ctx, DeleteWorkspaceCommand{WorkspaceID: turn.WorkspaceID, UserID: turn.UserID, Confirmation: turn.WorkspaceID}); err != nil {
					t.Fatal(err)
				}
			} else {
				preview, err := store.CreateProjectDeletePreview(ctx, CreateProjectDeletePreviewCommand{ProjectID: project.ProjectID})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.ConfirmProjectDelete(ctx, ConfirmProjectDeleteCommand{ProjectID: project.ProjectID, PreviewHash: preview.SnapshotHash, Confirmed: true}); err != nil {
					t.Fatal(err)
				}
			}
			current, err := store.GetAgentTurn(ctx, turn.AgentTurnID)
			if err != nil || current.Status != "cancelled" {
				t.Fatalf("deleted project left active main turn: %+v %v", current, err)
			}
			var states, active int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM agent_turn_run_states WHERE agent_turn_id=?`, turn.AgentTurnID).Scan(&states); err != nil || states != 0 {
				t.Fatalf("checkpoint survived deletion: %d %v", states, err)
			}
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM agent_turns WHERE project_id=? AND status NOT IN ('committed','failed','cancelled')`, project.ProjectID).Scan(&active); err != nil || active != 0 {
				t.Fatalf("deleted project consumed active quota: %d %v", active, err)
			}
			var callState, approvalState string
			if err := store.db.QueryRow(`SELECT c.status,a.status FROM agent_tool_calls c JOIN agent_tool_approvals a ON a.agent_tool_call_id=c.agent_tool_call_id WHERE c.agent_tool_call_id=?`, call.AgentToolCallID).Scan(&callState, &approvalState); err != nil || callState != "cancelled" || approvalState != "cancelled" {
				t.Fatalf("old write still allowed: %s %s %v", callState, approvalState, err)
			}
			_, err = store.ResumeAgentTurn(ctx, ControlAgentTurnCommand{AgentTurnID: turn.AgentTurnID})
			assertDomainCode(t, err, "PROJECT_NOT_FOUND")
		})
	}
}
