package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

func claimInputTurn(t *testing.T, store *Store, id string) AgentTurn {
	t.Helper()
	turns, err := store.ClaimRunnableAgentTurns(context.Background(), 1)
	if err != nil || len(turns) != 1 || turns[0].AgentTurnID != id {
		t.Fatalf("claim input turn: %+v %v", turns, err)
	}
	return turns[0]
}

func appendTurnInput(t *testing.T, store *Store, turnID, content string) AgentTurnInput {
	t.Helper()
	input, err := store.AppendAgentTurnInput(context.Background(), AppendAgentTurnInputCommand{AgentTurnID: turnID, Content: content})
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func inputTurnCheckpoint(turn AgentTurn, pending ...string) PauseAgentTurnForApprovalCommand {
	return PauseAgentTurnForApprovalCommand{
		SchemaVersion: "1.16", RunState: json.RawMessage(`{"$schemaVersion":"1.16","private_input_state":9007199254740993}`),
		PendingSDKToolCallIDs: pending, AgentTurnDispatchGeneration: turn.DispatchGeneration,
	}
}

func TestMainTurnInputsQueuedSnapshotReceiptsAndLateCompletion(t *testing.T) {
	ctx := context.Background()
	store, project, original, _ := pauseTurnFixture(t)
	defer store.Close()
	first := appendTurnInput(t, store, original.AgentTurnID, "First requirement")
	claim := claimInputTurn(t, store, original.AgentTurnID)
	if claim.DispatchGeneration != 1 || len(claim.AdditionalInputs) != 1 || claim.AdditionalInputs[0].InputID != first.InputID || claim.Request.Content != "Original" {
		t.Fatalf("initial immutable claim: %+v", claim)
	}
	second := appendTurnInput(t, store, original.AgentTurnID, "Second requirement")
	turn, err := store.GetAgentTurn(ctx, original.AgentTurnID)
	if err != nil || turn.Status != "pausing" || !turn.InputPauseRequested || len(turn.AdditionalInputs) != 2 {
		t.Fatalf("append pause: %+v %v", turn, err)
	}
	checkpoint := inputTurnCheckpoint(claim)
	checkpoint.IncludedAgentTurnInputIDs = []string{first.InputID, second.InputID}
	_, err = store.CompleteAgentTurnPause(ctx, original.AgentTurnID, checkpoint)
	assertDomainCode(t, err, "AGENT_TURN_INPUT_CONFLICT")
	var saved int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM agent_turn_run_states WHERE agent_turn_id=?`, original.AgentTurnID).Scan(&saved); err != nil || saved != 0 {
		t.Fatalf("bad receipt wrote checkpoint: %d %v", saved, err)
	}
	checkpoint.IncludedAgentTurnInputIDs = []string{first.InputID}
	turn, err = store.CompleteAgentTurnPause(ctx, original.AgentTurnID, checkpoint)
	if err != nil || turn.Status != "accepted" || turn.InputPauseRequested {
		t.Fatalf("automatic continuation: %+v %v", turn, err)
	}
	resumed := claimInputTurn(t, store, original.AgentTurnID)
	if resumed.DispatchGeneration != 2 || len(resumed.AdditionalInputs) != 2 || resumed.AdditionalInputs[0].Status != "included" || resumed.AdditionalInputs[1].Status != "received" || !resumed.StartedAt.Equal(*claim.StartedAt) || resumed.Request.Content != "Original" {
		t.Fatalf("resumed immutable claim: %+v", resumed)
	}
	late := appendTurnInput(t, store, original.AgentTurnID, "Arrived after the last model request")
	if err := store.RecordAgentTurnInputsIncluded(ctx, original.AgentTurnID, claim.DispatchGeneration, []string{first.InputID, second.InputID}); err == nil {
		t.Fatal("accepted an old dispatch receipt")
	}
	_, err = store.CompleteAgentTurnPause(ctx, original.AgentTurnID, inputTurnCheckpoint(claim))
	assertDomainCode(t, err, "AGENT_TURN_STATE_CONFLICT")
	for _, ids := range [][]string{{second.InputID}, {first.InputID, first.InputID}, {first.InputID, second.InputID, late.InputID}, {"foreign-input"}} {
		err := store.RecordAgentTurnInputsIncluded(ctx, original.AgentTurnID, resumed.DispatchGeneration, ids)
		assertDomainCode(t, err, "AGENT_TURN_INPUT_CONFLICT")
	}
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		CommandMeta:    CommandMeta{Scope: project.ProjectID, CommandType: "commit_sdk_agent_turn", IdempotencyKey: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", RequestHash: "exchange"},
		ConversationID: project.PrimaryConversationID, Request: original.Request,
		Decision: agentcontract.AgentDecision{Intent: "chat", Reply: "Done", Confidence: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteAgentTurnCommit(ctx, original.AgentTurnID, exchange); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAgentTurnInputsIncluded(ctx, original.AgentTurnID, resumed.DispatchGeneration, []string{first.InputID, second.InputID}); err != nil {
		t.Fatal(err)
	}
	turn, err = store.GetAgentTurn(ctx, original.AgentTurnID)
	if err != nil || turn.Status != "committed" || turn.AdditionalInputs[1].Status != "included" || turn.AdditionalInputs[2].Status != "received" || turn.AdditionalInputs[2].IncludedAt != nil {
		t.Fatalf("late input falsely included: %+v %v", turn, err)
	}
	before := turn.AdditionalInputs[1].IncludedAt
	if err := store.RecordAgentTurnInputsIncluded(ctx, original.AgentTurnID, resumed.DispatchGeneration, []string{first.InputID, second.InputID}); err != nil {
		t.Fatal(err)
	}
	turn, _ = store.GetAgentTurn(ctx, original.AgentTurnID)
	if !turn.AdditionalInputs[1].IncludedAt.Equal(*before) {
		t.Fatal("duplicate receipt changed its timestamp")
	}
	items, err := store.ListProjectAgentTurns(ctx, project.ProjectID, 50)
	if err != nil || len(items) != 1 || len(items[0].AdditionalInputs) != 3 {
		t.Fatalf("projection lost receipts: %+v %v", items, err)
	}
	encoded, err := json.Marshal(turn)
	if err != nil || strings.Contains(string(encoded), "dispatch_generation") || strings.Contains(string(encoded), "input_pause_requested") {
		t.Fatalf("private dispatch fields leaked: %s %v", encoded, err)
	}
}

func TestMainTurnInputsManualPauseOverridesAutoResumeAndSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	store, _, original, path := pauseTurnFixture(t)
	defer func() { store.Close() }()
	claim := claimInputTurn(t, store, original.AgentTurnID)
	appendTurnInput(t, store, original.AgentTurnID, "Append before manual pause")
	control := ControlAgentTurnCommand{AgentTurnID: original.AgentTurnID}
	if _, err := store.RequestAgentTurnPause(ctx, control); err != nil {
		t.Fatal(err)
	}
	appendTurnInput(t, store, original.AgentTurnID, "Append after manual pause")
	paused, err := store.CompleteAgentTurnPause(ctx, original.AgentTurnID, inputTurnCheckpoint(claim))
	if err != nil || paused.Status != "paused" || paused.InputPauseRequested {
		t.Fatalf("manual pause lost: %+v %v", paused, err)
	}
	appendTurnInput(t, store, original.AgentTurnID, "Keep paused")
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
	if claimed, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil || len(claimed) != 0 {
		t.Fatalf("manual pause resumed on rebuild: %+v %v", claimed, err)
	}
	if _, err := store.ResumeAgentTurn(ctx, control); err != nil {
		t.Fatal(err)
	}
	resumed := claimInputTurn(t, store, original.AgentTurnID)
	if resumed.DispatchGeneration != 2 || len(resumed.AdditionalInputs) != 3 || resumed.AdditionalInputs[2].Status != "received" {
		t.Fatalf("rebuild lost inputs: %+v", resumed)
	}
}

func TestMainTurnInputsApprovalInterleavingsRequireExplicitResume(t *testing.T) {
	for _, stage := range []string{"running", "waiting_approval", "approved_accepted"} {
		for _, decision := range []string{"approve", "reject"} {
			t.Run(stage+"/"+decision, func(t *testing.T) {
				ctx := context.Background()
				store, project, original, _ := pauseTurnFixture(t)
				defer store.Close()
				claim := claimInputTurn(t, store, original.AgentTurnID)
				call, err := store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID, AgentTurnID: original.AgentTurnID, SDKToolCallID: "old-write", ToolID: "mcp:fixture/save_fact", Arguments: json.RawMessage(`{"value":"original"}`), ConfigurationHash: toolConfigurationHashForTest(t, store, "mcp:fixture/save_fact")})
				if err != nil {
					t.Fatal(err)
				}
				checkpoint := inputTurnCheckpoint(claim, "old-write")
				if stage != "running" {
					if _, err := store.PauseAgentTurnForApproval(ctx, original.AgentTurnID, checkpoint); err != nil {
						t.Fatal(err)
					}
				}
				resolve := func() {
					t.Helper()
					if _, err := store.ResolveAgentToolApproval(ctx, ResolveAgentToolApprovalCommand{AgentToolApprovalID: call.Approval.AgentToolApprovalID, ExpectedVersion: call.Approval.Version, SubjectSnapshotHash: call.Approval.SubjectSnapshotHash, Action: decision}); err != nil {
						t.Fatal(err)
					}
				}
				if stage == "approved_accepted" {
					resolve()
				}
				appendTurnInput(t, store, original.AgentTurnID, "New input does not revoke or approve the old tool")
				if stage == "running" {
					if _, err := store.PauseAgentTurnForApproval(ctx, original.AgentTurnID, checkpoint); err != nil {
						t.Fatal(err)
					}
				}
				if stage != "approved_accepted" {
					resolve()
				}
				turn, err := store.GetAgentTurn(ctx, original.AgentTurnID)
				if err != nil || turn.Status != "paused" {
					t.Fatalf("approval auto resumed append: %+v %v", turn, err)
				}
				if turns, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil || len(turns) != 0 {
					t.Fatalf("claimed without consent: %+v %v", turns, err)
				}
				if _, err := store.ResumeAgentTurn(ctx, ControlAgentTurnCommand{AgentTurnID: original.AgentTurnID}); err != nil {
					t.Fatal(err)
				}
				resumed := claimInputTurn(t, store, original.AgentTurnID)
				state, err := store.GetAgentTurnResumeContext(ctx, original.AgentTurnID)
				if err != nil || resumed.DispatchGeneration != 2 || len(resumed.AdditionalInputs) != 1 || len(state.ApprovalDecisions) != 1 || state.ApprovalDecisions[0].Action != decision {
					t.Fatalf("lost old approval or input: %+v %+v %v", resumed, state, err)
				}
			})
		}
	}
}

func TestMainTurnInputsIdempotencyAuthorizationAndTerminalStates(t *testing.T) {
	ctx := context.Background()
	store, project, original, _ := pauseTurnFixture(t)
	defer store.Close()
	command := AppendAgentTurnInputCommand{AgentTurnID: original.AgentTurnID, Content: "Keep this immutable", CommandMeta: CommandMeta{IdempotencyKey: "append-main-one", CommandType: "append_agent_turn_input", RequestHash: "original-hash"}}
	first, err := store.AppendAgentTurnInput(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	claim := claimInputTurn(t, store, original.AgentTurnID)
	if err := store.RecordAgentTurnInputsIncluded(ctx, original.AgentTurnID, claim.DispatchGeneration, []string{first.InputID}); err != nil {
		t.Fatal(err)
	}
	_, err = store.FailAgentTurn(ctx, original.AgentTurnID, "FIXTURE_FAILURE", "Failed after model response")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := store.AppendAgentTurnInput(ctx, command)
	if err != nil || retry.InputID != first.InputID || retry.Status != "included" || retry.IncludedAt == nil {
		t.Fatalf("late idempotent receipt: %+v %v", retry, err)
	}
	for _, user := range []identity.Principal{
		{Kind: identity.KindUser, WorkspaceID: original.WorkspaceID, UserID: "other-author", Role: identity.RoleOwner},
		{Kind: identity.KindUser, WorkspaceID: original.WorkspaceID, UserID: original.UserID, Role: identity.RoleViewer},
	} {
		_, err := store.AppendAgentTurnInput(identity.WithPrincipal(ctx, user), command)
		assertDomainCode(t, err, "ROLE_FORBIDDEN")
	}
	_, err = store.AppendAgentTurnInput(identity.WithPrincipal(ctx, identity.Principal{Kind: identity.KindUser, WorkspaceID: "other-workspace", UserID: original.UserID, Role: identity.RoleOwner}), command)
	assertDomainCode(t, err, "PROJECT_NOT_FOUND")
	_, err = store.AppendAgentTurnInput(WithAgentActivity(ctx, AgentActivityIdentity{ProjectID: project.ProjectID, AgentTurnID: original.AgentTurnID}), command)
	assertDomainCode(t, err, "ROLE_FORBIDDEN")
	command.RequestHash = "changed-hash"
	_, err = store.AppendAgentTurnInput(ctx, command)
	assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
	for _, status := range []string{"failed", "cancelled", "committed", "committing", "cancel_requested"} {
		if _, err := store.db.Exec(`UPDATE agent_turns SET status=? WHERE agent_turn_id=?`, status, original.AgentTurnID); err != nil {
			t.Fatal(err)
		}
		_, err := store.AppendAgentTurnInput(ctx, AppendAgentTurnInputCommand{AgentTurnID: original.AgentTurnID, Content: "Too late"})
		assertDomainCode(t, err, "AGENT_TURN_STATE_CONFLICT")
	}
}

func TestMainTurnInputsLimitsCountUTF8Bytes(t *testing.T) {
	store, _, turn, _ := pauseTurnFixture(t)
	defer store.Close()
	ctx := context.Background()
	for _, content := range []string{"   ", strings.Repeat("a", (32<<10)+1), strings.Repeat("字", 10923)} {
		_, err := store.AppendAgentTurnInput(ctx, AppendAgentTurnInputCommand{AgentTurnID: turn.AgentTurnID, Content: content})
		assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
	}
	for i := 0; i < 16; i++ {
		appendTurnInput(t, store, turn.AgentTurnID, strings.Repeat("a", 32<<10))
	}
	_, err := store.AppendAgentTurnInput(ctx, AppendAgentTurnInputCommand{AgentTurnID: turn.AgentTurnID, Content: "One byte too many"})
	assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
	// Separate fixture for the count boundary; production inputs are never mutated.
	other, _, otherTurn, _ := pauseTurnFixture(t)
	defer other.Close()
	for i := 0; i < 128; i++ {
		appendTurnInput(t, other, otherTurn.AgentTurnID, fmt.Sprintf("Input %d", i))
	}
	_, err = other.AppendAgentTurnInput(ctx, AppendAgentTurnInputCommand{AgentTurnID: otherTurn.AgentTurnID, Content: "Too many inputs"})
	assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
}

func TestMainTurnInputRejectsPrincipalRevokedAfterAuthentication(t *testing.T) {
	store, _, turn, _ := pauseTurnFixture(t)
	defer store.Close()
	stale := identity.WithPrincipal(context.Background(), identity.Principal{Kind: identity.KindUser, WorkspaceID: turn.WorkspaceID, UserID: turn.UserID, Role: identity.RoleOwner})
	if _, err := store.db.Exec(`UPDATE workspace_memberships SET role='viewer' WHERE workspace_id=? AND user_id=?`, turn.WorkspaceID, turn.UserID); err != nil {
		t.Fatal(err)
	}
	_, err := store.AppendAgentTurnInput(stale, AppendAgentTurnInputCommand{AgentTurnID: turn.AgentTurnID, Content: "Revoked request"})
	assertDomainCode(t, err, "ROLE_FORBIDDEN")
}

func TestMainTurnInputsV45MigrationPreservesCheckpointAndCatalog(t *testing.T) {
	store, _, turn, path := pauseTurnFixture(t)
	defer func() { store.Close() }()
	ctx := context.Background()
	claim := claimInputTurn(t, store, turn.AgentTurnID)
	if _, err := store.RequestAgentTurnPause(ctx, ControlAgentTurnCommand{AgentTurnID: turn.AgentTurnID}); err != nil {
		t.Fatal(err)
	}
	checkpoint := inputTurnCheckpoint(claim)
	if _, err := store.CompleteAgentTurnPause(ctx, turn.AgentTurnID, checkpoint); err != nil {
		t.Fatal(err)
	}
	var catalog string
	if err := store.db.QueryRow(`SELECT json_group_array(descriptor_json) FROM agent_turn_skills WHERE agent_turn_id=?`, turn.AgentTurnID).Scan(&catalog); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE agent_turn_inputs; ALTER TABLE agent_turns DROP COLUMN dispatch_generation; ALTER TABLE agent_turns DROP COLUMN input_pause_requested; PRAGMA user_version=45;`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	store, err = Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := store.GetAgentTurn(ctx, turn.AgentTurnID)
	if err != nil || fresh.Status != "paused" || fresh.DispatchGeneration != 0 || fresh.InputPauseRequested || len(fresh.AdditionalInputs) != 0 {
		t.Fatalf("migration changed turn: %+v %v", fresh, err)
	}
	state, err := store.GetAgentTurnResumeContext(ctx, turn.AgentTurnID)
	if err != nil || string(state.RunState) != string(checkpoint.RunState) {
		t.Fatalf("migration altered native checkpoint: %+v %v", state, err)
	}
	var after, backup string
	if err := store.db.QueryRow(`SELECT json_group_array(descriptor_json) FROM agent_turn_skills WHERE agent_turn_id=?`, turn.AgentTurnID).Scan(&after); err != nil || after != catalog {
		t.Fatalf("catalog changed: %s %v", after, err)
	}
	if err := store.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version=45 AND to_version=? AND status='completed'`, schemaVersion).Scan(&backup); err != nil || backup == "" {
		t.Fatalf("migration backup missing: %s %v", backup, err)
	}
	appendTurnInput(t, store, turn.AgentTurnID, "New schema input")
	if _, err := store.ResumeAgentTurn(ctx, ControlAgentTurnCommand{AgentTurnID: turn.AgentTurnID}); err != nil {
		t.Fatal(err)
	}
	resumed := claimInputTurn(t, store, turn.AgentTurnID)
	if resumed.DispatchGeneration != 1 || len(resumed.AdditionalInputs) != 1 || !resumed.StartedAt.Equal(*claim.StartedAt) {
		t.Fatalf("migrated resume: %+v", resumed)
	}
}

func TestMainTurnInputsAutoResumeRebuildAndInterruptedUnconfirmedReceipts(t *testing.T) {
	for _, checkpointSaved := range []bool{false, true} {
		t.Run(fmt.Sprint(checkpointSaved), func(t *testing.T) {
			ctx := context.Background()
			store, _, turn, path := pauseTurnFixture(t)
			defer func() { store.Close() }()
			claim := claimInputTurn(t, store, turn.AgentTurnID)
			input := appendTurnInput(t, store, turn.AgentTurnID, "Not yet admitted")
			if checkpointSaved {
				if _, err := store.CompleteAgentTurnPause(ctx, turn.AgentTurnID, inputTurnCheckpoint(claim)); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			var err error
			store, err = Open(path, loadTestRegistry(t))
			if err != nil {
				t.Fatal(err)
			}
			if err := store.RecoverInterruptedAgentTurns(ctx); err != nil {
				t.Fatal(err)
			}
			fresh, err := store.GetAgentTurn(ctx, turn.AgentTurnID)
			if err != nil || len(fresh.AdditionalInputs) != 1 || fresh.AdditionalInputs[0].Status != "received" || fresh.AdditionalInputs[0].IncludedAt != nil {
				t.Fatalf("rebuild invented admission: %+v %v", fresh, err)
			}
			if checkpointSaved {
				resumed := claimInputTurn(t, store, turn.AgentTurnID)
				if resumed.DispatchGeneration != 2 || resumed.AdditionalInputs[0].InputID != input.InputID {
					t.Fatalf("resume lost original input: %+v", resumed)
				}
			} else {
				if fresh.Status != "failed" {
					t.Fatalf("unsafe restart: %+v", fresh)
				}
				err := store.RecordAgentTurnInputsIncluded(ctx, turn.AgentTurnID, claim.DispatchGeneration, []string{input.InputID})
				assertDomainCode(t, err, "AGENT_TURN_INPUT_CONFLICT")
				if turns, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil || len(turns) != 0 {
					t.Fatalf("reran an interrupted turn: %+v %v", turns, err)
				}
			}
		})
	}
}

func TestMainTurnInputsConcurrentRequestsKeepUniqueSequenceAndReceipts(t *testing.T) {
	store, _, turn, _ := pauseTurnFixture(t)
	defer store.Close()
	ctx := context.Background()
	var workers sync.WaitGroup
	errors := make(chan error, 16)
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			key := fmt.Sprintf("concurrent-input-%d", index%8)
			_, err := store.AppendAgentTurnInput(ctx, AppendAgentTurnInputCommand{AgentTurnID: turn.AgentTurnID, Content: key, CommandMeta: CommandMeta{IdempotencyKey: key, CommandType: "append_agent_turn_input", RequestHash: key}})
			errors <- err
		}(i)
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	claim := claimInputTurn(t, store, turn.AgentTurnID)
	if len(claim.AdditionalInputs) != 8 {
		t.Fatalf("duplicate inputs: %+v", claim.AdditionalInputs)
	}
	ids := []string{}
	for index, input := range claim.AdditionalInputs {
		if input.Sequence != index+1 {
			t.Fatalf("unordered inputs: %+v", claim.AdditionalInputs)
		}
		ids = append(ids, input.InputID)
	}
	for i := 0; i < 2; i++ {
		if err := store.RecordAgentTurnInputsIncluded(ctx, turn.AgentTurnID, claim.DispatchGeneration, ids); err != nil {
			t.Fatal(err)
		}
	}
	var received, included, leaks int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM events WHERE event_type='agent.turn.input_received'`).Scan(&received); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM events WHERE event_type='agent.turn.inputs_included'`).Scan(&included); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM events WHERE payload_json LIKE '%concurrent-input-%'`).Scan(&leaks); err != nil {
		t.Fatal(err)
	}
	if received != 8 || included != 1 || leaks != 0 {
		t.Fatalf("incorrect events: received=%d included=%d contentLeaks=%d", received, included, leaks)
	}
}
