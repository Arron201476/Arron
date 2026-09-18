package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

func pauseTurnFixture(t *testing.T) (*Store, Project, AgentTurn, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "turn-pause.db")
	store, err := Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	store.SetAgentToolRegistry(agentToolRegistryForTest(t))
	project, err := store.CreateProject(context.Background(), "Main pause")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	turn, err := store.AcceptAgentTurn(context.Background(), project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Original"}, CommandMeta{})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, project, turn, path
}

func TestMainTurnPauseQueueCheckpointAndReopen(t *testing.T) {
	ctx := context.Background()
	store, project, turn, path := pauseTurnFixture(t)
	defer func() { store.Close() }()
	command := ControlAgentTurnCommand{AgentTurnID: turn.AgentTurnID}
	paused, err := store.RequestAgentTurnPause(ctx, command)
	if err != nil || paused.Status != "paused" || paused.StartedAt != nil {
		t.Fatalf("pause queued: %+v %v", paused, err)
	}
	if _, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Later"}, CommandMeta{CommandType: "create_message", RequestHash: "later", IdempotencyKey: "88888888-8888-4888-8888-888888888888"}); err != nil {
		t.Fatal(err)
	}
	if items, err := store.ClaimRunnableAgentTurns(ctx, 2); err != nil || len(items) != 0 {
		t.Fatalf("bypassed queue pause: %+v %v", items, err)
	}
	if _, err := store.ResumeAgentTurn(ctx, command); err != nil {
		t.Fatal(err)
	}
	if items, err := store.ClaimRunnableAgentTurns(ctx, 2, turn.AgentTurnID); err != nil || len(items) != 0 {
		t.Fatalf("claimed active handle: %+v %v", items, err)
	}
	items, err := store.ClaimRunnableAgentTurns(ctx, 2)
	if err != nil || len(items) != 1 || items[0].AgentTurnID != turn.AgentTurnID {
		t.Fatalf("claim: %+v %v", items, err)
	}
	paused, err = store.RequestAgentTurnPause(ctx, command)
	if err != nil || paused.Status != "pausing" {
		t.Fatalf("request: %+v %v", paused, err)
	}
	_, err = store.ResumeAgentTurn(ctx, command)
	assertDomainCode(t, err, "AGENT_TURN_STATE_CONFLICT")
	checkpoint := PauseAgentTurnForApprovalCommand{SchemaVersion: "1.16", RunState: json.RawMessage(`{"$schemaVersion":"1.16","private_marker":9007199254740993}`), PendingSDKToolCallIDs: []string{}}
	paused, err = store.CompleteAgentTurnPause(ctx, turn.AgentTurnID, checkpoint)
	if err != nil || paused.Status != "paused" {
		t.Fatalf("checkpoint: %+v %v", paused, err)
	}
	if _, err := store.CompleteAgentTurnPause(ctx, turn.AgentTurnID, checkpoint); err != nil {
		t.Fatal(err)
	}
	var version, leaks int
	if err := store.db.QueryRow(`SELECT checkpoint_version FROM agent_turn_run_states WHERE agent_turn_id=?`, turn.AgentTurnID).Scan(&version); err != nil || version != 1 {
		t.Fatalf("duplicate checkpoint: %d %v", version, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM events WHERE payload_json LIKE '%private_marker%'`).Scan(&leaks); err != nil || leaks != 0 {
		t.Fatalf("leaked state: %d %v", leaks, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverInterruptedAgentTurns(ctx); err != nil {
		t.Fatal(err)
	}
	paused, _ = store.GetAgentTurn(ctx, turn.AgentTurnID)
	if paused.Status != "paused" {
		t.Fatalf("restart lost pause: %+v", paused)
	}
	resumed, err := store.ResumeAgentTurn(ctx, command)
	if err != nil || resumed.Status != "accepted" {
		t.Fatalf("resume: %+v %v", resumed, err)
	}
	state, err := store.GetAgentTurnResumeContext(ctx, turn.AgentTurnID)
	if err != nil || string(state.RunState) != string(checkpoint.RunState) {
		t.Fatalf("checkpoint altered: %+v %v", state, err)
	}
	if _, err := store.db.Exec(`UPDATE agent_turn_run_states SET state_hash='corrupt' WHERE agent_turn_id=?`, turn.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	_, err = store.GetAgentTurnResumeContext(ctx, turn.AgentTurnID)
	assertDomainCode(t, err, "AGENT_RUN_STATE_CORRUPT")
}

func TestMainTurnPauseRemainsReachableBeyondRecentHistory(t *testing.T) {
	ctx := context.Background()
	store, project, original, _ := pauseTurnFixture(t)
	defer store.Close()
	if _, err := store.RequestAgentTurnPause(ctx, ControlAgentTurnCommand{AgentTurnID: original.AgentTurnID}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 52; i++ {
		turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, agentcontract.MessageRequest{Content: "Later"}, CommandMeta{CommandType: "create_message", RequestHash: "later", IdempotencyKey: fmt.Sprintf("b7777777-7777-4777-8777-%012d", i)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.CancelAgentTurn(ctx, turn.AgentTurnID); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.ListProjectAgentTurns(ctx, project.ProjectID, 50)
	if err != nil || len(items) != 51 || items[0].AgentTurnID != original.AgentTurnID || items[0].Status != "paused" {
		t.Fatalf("hidden paused turn: count=%d error=%v", len(items), err)
	}
	if _, err := store.ResumeAgentTurn(ctx, ControlAgentTurnCommand{AgentTurnID: original.AgentTurnID}); err != nil {
		t.Fatal(err)
	}
	items, err = store.ClaimRunnableAgentTurns(ctx, 1)
	if err != nil || len(items) != 1 || items[0].AgentTurnID != original.AgentTurnID {
		t.Fatalf("old turn cannot resume: %+v %v", items, err)
	}
}

func TestMainTurnPauseRetainsOldApprovalUntilExplicitResume(t *testing.T) {
	for _, decision := range []string{"approve", "reject"} {
		t.Run(decision, func(t *testing.T) {
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
			command := ControlAgentTurnCommand{AgentTurnID: turn.AgentTurnID}
			if _, err := store.RequestAgentTurnPause(ctx, command); err != nil {
				t.Fatal(err)
			}
			checkpoint := PauseAgentTurnForApprovalCommand{SchemaVersion: "1.16", RunState: json.RawMessage(`{"$schemaVersion":"1.16"}`), PendingSDKToolCallIDs: []string{"old-write"}}
			// Approval may reach Go before Sidecar observes the user pause.
			paused, err := store.PauseAgentTurnForApproval(ctx, turn.AgentTurnID, checkpoint)
			if err != nil || paused.Status != "paused" {
				t.Fatalf("approval race: %+v %v", paused, err)
			}
			if _, err := store.ResolveAgentToolApproval(ctx, ResolveAgentToolApprovalCommand{AgentToolApprovalID: call.Approval.AgentToolApprovalID, ExpectedVersion: call.Approval.Version, SubjectSnapshotHash: call.Approval.SubjectSnapshotHash, Action: decision}); err != nil {
				t.Fatal(err)
			}
			paused, _ = store.GetAgentTurn(ctx, turn.AgentTurnID)
			if paused.Status != "paused" {
				t.Fatalf("approval resumed without consent: %+v", paused)
			}
			_, err = store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID, ConfigurationHash: toolConfigurationHashForTest(t, store, "mcp:fixture/save_fact")})
			assertDomainCode(t, err, "AGENT_ACTIVITY_STALE")
			if next, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil || len(next) != 0 {
				t.Fatalf("claimed pause: %+v %v", next, err)
			}
			resumed, err := store.ResumeAgentTurn(ctx, command)
			if err != nil || resumed.Status != "accepted" {
				t.Fatalf("resume: %+v %v", resumed, err)
			}
			state, err := store.GetAgentTurnResumeContext(ctx, turn.AgentTurnID)
			if err != nil || len(state.ApprovalDecisions) != 1 || state.ApprovalDecisions[0].Action != decision {
				t.Fatalf("decision lost: %+v %v", state, err)
			}
		})
	}
}

func TestMainTurnPauseCancellationCommitAndCorruptResume(t *testing.T) {
	for _, action := range []string{"cancel", "commit", "restart", "missing-state"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			store, _, turn, _ := pauseTurnFixture(t)
			defer store.Close()
			if _, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil {
				t.Fatal(err)
			}
			command := ControlAgentTurnCommand{AgentTurnID: turn.AgentTurnID}
			if _, err := store.RequestAgentTurnPause(ctx, command); err != nil {
				t.Fatal(err)
			}
			switch action {
			case "cancel":
				result, err := store.CancelAgentTurn(ctx, turn.AgentTurnID)
				if err != nil || !result.Accepted || result.Turn.Status != "cancel_requested" {
					t.Fatalf("cancel: %+v %v", result, err)
				}
				_, err = store.CompleteAgentTurnPause(ctx, turn.AgentTurnID, PauseAgentTurnForApprovalCommand{SchemaVersion: "1.16", RunState: json.RawMessage(`{"$schemaVersion":"1.16"}`)})
				assertDomainCode(t, err, "AGENT_TURN_STATE_INVALID")
			case "commit":
				result, err := store.BeginAgentTurnCommit(ctx, turn.AgentTurnID)
				if err != nil || result.Status != "committing" {
					t.Fatalf("commit: %+v %v", result, err)
				}
			case "restart":
				if err := store.RecoverInterruptedAgentTurns(ctx); err != nil {
					t.Fatal(err)
				}
				result, _ := store.GetAgentTurn(ctx, turn.AgentTurnID)
				if result.Status != "failed" {
					t.Fatalf("restarted from unsafe pause: %+v", result)
				}
			case "missing-state":
				if _, err := store.db.Exec(`UPDATE agent_turns SET status='paused' WHERE agent_turn_id=?`, turn.AgentTurnID); err != nil {
					t.Fatal(err)
				}
				_, err := store.ResumeAgentTurn(ctx, command)
				assertDomainCode(t, err, "AGENT_RUN_STATE_CORRUPT")
			}
		})
	}
}

func TestMainTurnPausePermissionAndIdempotentReceipt(t *testing.T) {
	store, _, turn, _ := pauseTurnFixture(t)
	defer store.Close()
	ctx := context.Background()
	command := ControlAgentTurnCommand{AgentTurnID: turn.AgentTurnID, CommandMeta: CommandMeta{Scope: turn.AgentTurnID, CommandType: "pause_agent_turn", IdempotencyKey: "77777777-7777-4777-8777-777777777777", RequestHash: "pause"}}
	if _, err := store.RequestAgentTurnPause(ctx, command); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResumeAgentTurn(ctx, ControlAgentTurnCommand{AgentTurnID: turn.AgentTurnID}); err != nil {
		t.Fatal(err)
	}
	result, err := store.RequestAgentTurnPause(ctx, command)
	if err != nil || result.Status != "accepted" {
		t.Fatalf("late receipt paused again: %+v %v", result, err)
	}
	for _, user := range []identity.Principal{
		{Kind: identity.KindUser, UserID: turn.UserID, WorkspaceID: turn.WorkspaceID, Role: identity.RoleViewer},
		{Kind: identity.KindUser, UserID: "other", WorkspaceID: turn.WorkspaceID, Role: identity.RoleOwner},
	} {
		_, err := store.RequestAgentTurnPause(identity.WithPrincipal(ctx, user), command)
		assertDomainCode(t, err, "ROLE_FORBIDDEN")
	}
	_, err = store.RequestAgentTurnPause(WithAgentActivity(ctx, AgentActivityIdentity{ProjectID: turn.ProjectID, AgentTurnID: turn.AgentTurnID}), command)
	assertDomainCode(t, err, "ROLE_FORBIDDEN")
}

func TestMainTurnPauseV44MigrationPreservesCheckpointAndFrozenCatalog(t *testing.T) {
	store, _, turn, path := pauseTurnFixture(t)
	ctx := context.Background()
	if _, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RequestAgentTurnPause(ctx, ControlAgentTurnCommand{AgentTurnID: turn.AgentTurnID}); err != nil {
		t.Fatal(err)
	}
	checkpoint := PauseAgentTurnForApprovalCommand{SchemaVersion: "1.16", RunState: json.RawMessage(`{"$schemaVersion":"1.16","preserved":true}`)}
	if _, err := store.CompleteAgentTurnPause(ctx, turn.AgentTurnID, checkpoint); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE agent_turns SET status='waiting_approval' WHERE agent_turn_id=?`, turn.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	var ddl, catalog string
	if err := store.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='agent_turns'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT json_group_array(descriptor_json) FROM agent_turn_skills WHERE agent_turn_id=?`, turn.AgentTurnID).Scan(&catalog); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacyDDL := strings.Replace(strings.Replace(ddl, "CREATE TABLE agent_turns", "CREATE TABLE agent_turns_v44", 1), "'pausing','paused',", "", 1)
	if legacyDDL == ddl || strings.Contains(legacyDDL, "'pausing'") {
		t.Fatal("invalid legacy fixture DDL")
	}
	_, err = db.Exec(`PRAGMA foreign_keys=OFF;` + legacyDDL + `; INSERT INTO agent_turns_v44 SELECT * FROM agent_turns; DROP TABLE agent_turns; ALTER TABLE agent_turns_v44 RENAME TO agent_turns; PRAGMA user_version=44;`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err := store.GetAgentTurnResumeContext(ctx, turn.AgentTurnID)
	if err != nil || string(state.RunState) != string(checkpoint.RunState) {
		t.Fatalf("migration lost checkpoint: %+v %v", state, err)
	}
	var after string
	var ready bool
	if err := store.db.QueryRow(`SELECT json_group_array(descriptor_json) FROM agent_turn_skills WHERE agent_turn_id=?`, turn.AgentTurnID).Scan(&after); err != nil || after != catalog {
		t.Fatalf("catalog changed: %v", err)
	}
	if err := store.db.QueryRow(`SELECT skill_snapshot_ready FROM agent_turns WHERE agent_turn_id=?`, turn.AgentTurnID).Scan(&ready); err != nil || !ready {
		t.Fatalf("frozen marker lost: %v", err)
	}
	if _, err := store.RequestAgentTurnPause(ctx, ControlAgentTurnCommand{AgentTurnID: turn.AgentTurnID}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration broke foreign keys")
	}
}
