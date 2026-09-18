package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/agentcontract"
)

func TestAgentTurnAcceptanceIsDurableIdempotentAndDoesNotPrematurelyCommitMessages(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "Agent Turn")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	meta := CommandMeta{
		Scope: project.ProjectID, CommandType: "create_message",
		IdempotencyKey: "11111111-1111-4111-8111-111111111111", RequestHash: "turn-v1",
	}
	request := agentcontract.MessageRequest{Content: "生成故事大纲"}
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, request, meta)
	if err != nil {
		t.Fatalf("AcceptAgentTurn() error = %v", err)
	}
	repeated, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID, request, meta)
	if err != nil || repeated.AgentTurnID != turn.AgentTurnID {
		t.Fatalf("idempotent turn = %+v, error = %v", repeated, err)
	}
	if turn.SubmissionID == "" || turn.SubmissionID == meta.IdempotencyKey || repeated.SubmissionID != turn.SubmissionID {
		t.Fatalf("submission identity was omitted, exposed the key, or changed on retry: %+v", repeated)
	}
	encoded, err := json.Marshal(turn)
	if err != nil {
		t.Fatal(err)
	}
	var public map[string]any
	if err := json.Unmarshal(encoded, &public); err != nil {
		t.Fatal(err)
	}
	if public["submission_id"] != turn.SubmissionID || public["idempotency_key"] != nil {
		t.Fatalf("invalid public submission contract: %s", encoded)
	}
	messages, err := store.ListMessages(ctx, project.PrimaryConversationID)
	if err != nil {
		t.Fatalf("ListMessages() error = %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("accepted turn prematurely committed %d messages", len(messages))
	}
	listed, err := store.ListProjectAgentTurns(ctx, project.ProjectID, 10)
	if err != nil || len(listed) != 1 || listed[0].Request.Content != request.Content {
		t.Fatalf("ListProjectAgentTurns() = %+v, error = %v", listed, err)
	}
	if listed[0].SubmissionID != turn.SubmissionID {
		t.Fatal("projection lost the original submission identity")
	}
}

func TestAgentTurnSubmissionIdentityIncludesAuthorConversationAndRequest(t *testing.T) {
	const expected = "3b296e84998e503bc56fd2a11a453f42bd498512e9a47cdcb2e4225e5d3478f3"
	if actual := agentTurnSubmissionID("u", "conversation", "request"); actual != expected {
		t.Fatalf("submission identity protocol changed: %s", actual)
	}
	for _, identity := range [][3]string{{"other", "conversation", "request"}, {"u", "other", "request"}, {"u", "conversation", "other"}} {
		if agentTurnSubmissionID(identity[0], identity[1], identity[2]) == expected {
			t.Fatal("different submitter, conversation, or request reused the identity")
		}
	}
	if agentTurnSubmissionID("u", "conversation", "") != "" {
		t.Fatal("legacy requests without a key acquired a shared identity")
	}
}

func TestAgentTurnClaimsOldestWritePerConversationAndAllowsCrossConversationParallelism(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	firstProject, _ := store.CreateProject(ctx, "First")
	secondProject, _ := store.CreateProject(ctx, "Second")
	accept := func(project Project, key, hash, content string) AgentTurn {
		turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID,
			agentcontract.MessageRequest{Content: content}, CommandMeta{
				Scope: project.ProjectID, CommandType: "create_message",
				IdempotencyKey: key, RequestHash: hash,
			})
		if err != nil {
			t.Fatalf("AcceptAgentTurn(%s) error = %v", content, err)
		}
		return turn
	}
	first := accept(firstProject, "11111111-1111-4111-8111-111111111111", "a", "first")
	second := accept(firstProject, "22222222-2222-4222-8222-222222222222", "b", "second")
	parallel := accept(secondProject, "33333333-3333-4333-8333-333333333333", "c", "parallel")
	claimed, err := store.ClaimRunnableAgentTurns(ctx, 8)
	if err != nil {
		t.Fatalf("ClaimRunnableAgentTurns() error = %v", err)
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed = %+v, want two conversations", claimed)
	}
	ids := map[string]bool{claimed[0].AgentTurnID: true, claimed[1].AgentTurnID: true}
	if !ids[first.AgentTurnID] || !ids[parallel.AgentTurnID] || ids[second.AgentTurnID] {
		t.Fatalf("claimed IDs = %+v", ids)
	}
	again, err := store.ClaimRunnableAgentTurns(ctx, 8)
	if err != nil || len(again) != 0 {
		t.Fatalf("second claim = %+v, error = %v", again, err)
	}
}

func TestAgentTurnCommitProducesOneTerminalEventAndCancellationCannotWinAfterCommitStarts(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, _ := store.CreateProject(ctx, "Commit race")
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID,
		agentcontract.MessageRequest{Content: "hello"}, CommandMeta{
			Scope: project.ProjectID, CommandType: "create_message",
			IdempotencyKey: "44444444-4444-4444-8444-444444444444", RequestHash: "commit",
		})
	if err != nil {
		t.Fatalf("AcceptAgentTurn() error = %v", err)
	}
	if _, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil {
		t.Fatalf("ClaimRunnableAgentTurns() error = %v", err)
	}
	if _, err := store.BeginAgentTurnCommit(ctx, turn.AgentTurnID); err != nil {
		t.Fatalf("BeginAgentTurnCommit() error = %v", err)
	}
	cancel, err := store.CancelAgentTurn(ctx, turn.AgentTurnID)
	if err != nil || cancel.Accepted {
		t.Fatalf("CancelAgentTurn() = %+v, error = %v", cancel, err)
	}
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "commit_sdk_agent_turn",
			IdempotencyKey: "44444444-4444-4444-8444-444444444444", RequestHash: "exchange",
		},
		ConversationID: project.PrimaryConversationID,
		Request:        agentcontract.MessageRequest{Content: "hello"},
		Decision: agentcontract.AgentDecision{
			Intent: "chat", Reply: "done", Confidence: 1,
		},
	})
	if err != nil {
		t.Fatalf("CreateMessageExchange() error = %v", err)
	}
	committed, err := store.CompleteAgentTurnCommit(ctx, turn.AgentTurnID, exchange)
	if err != nil || committed.Status != "committed" {
		t.Fatalf("CompleteAgentTurnCommit() = %+v, error = %v", committed, err)
	}
	if _, err := store.CompleteAgentTurnCommit(ctx, turn.AgentTurnID, exchange); err != nil {
		t.Fatalf("idempotent CompleteAgentTurnCommit() error = %v", err)
	}
	assertAgentTurnTerminalCount(t, store, turn.AgentTurnID, 1)
	events, err := store.ListProjectEvents(ctx, project.ProjectID, 0, 100)
	if err != nil {
		t.Fatalf("ListProjectEvents() error = %v", err)
	}
	seenDelta := false
	for _, event := range events.Items {
		if event.EventType == "agent.output.delta" && event.SubjectID == turn.AgentTurnID {
			seenDelta = true
		}
	}
	if !seenDelta {
		t.Fatal("commit emitted no safe output delta")
	}
}

func TestAgentTurnPersistsPostCommitSDKObservationAndCorrelations(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, _ := store.CreateProject(ctx, "Observed turn")
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID,
		agentcontract.MessageRequest{Content: "observe"}, CommandMeta{
			Scope: project.ProjectID, CommandType: "create_message",
			IdempotencyKey: "47777777-7777-4777-8777-777777777777", RequestHash: "observe",
		})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil {
		t.Fatal(err)
	}
	now := formatTime(store.now())
	if _, err := store.db.Exec(`INSERT INTO agent_tool_calls(
		agent_tool_call_id, workspace_id, project_id, conversation_id, agent_turn_id,
		sdk_tool_call_id, tool_id, tool_kind, tool_name, access_mode, approval_policy,
		max_result_bytes, approval_status, status, arguments_summary_json,
		arguments_hash, requested_at, updated_at
	) VALUES('tool_corr', ?, ?, ?, ?, 'sdk_corr', 'inspect_project', 'function',
		'inspect_project', 'read', 'never', 1024, 'not_required', 'completed', '{}',
		'hash', ?, ?)`, project.WorkspaceID, project.ProjectID,
		project.PrimaryConversationID, turn.AgentTurnID, now, now); err != nil {
		t.Fatal(err)
	}
	exchange, err := store.CreateMessageExchange(ctx, CreateMessageExchangeCommand{
		CommandMeta: CommandMeta{
			Scope: project.ProjectID, CommandType: "commit_sdk_agent_turn",
			IdempotencyKey: turn.IdempotencyKey, RequestHash: "observed-exchange",
		},
		ConversationID: project.PrimaryConversationID,
		Request:        agentcontract.MessageRequest{Content: "observe"},
		Decision:       agentcontract.AgentDecision{Intent: "chat", Reply: "done", Confidence: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	taskID := "task_corr"
	exchange.Invocation = &SkillInvocation{SkillInvocationID: "inv_corr", AgentTaskID: &taskID}
	exchange.RunRef = &Run{RunID: "run_corr"}
	if _, err := store.CompleteAgentTurnCommit(ctx, turn.AgentTurnID, exchange); err != nil {
		t.Fatal(err)
	}
	observed, err := store.RecordAgentTurnObservation(ctx, turn.AgentTurnID, AgentTurnObservation{
		SchemaVersion: agentTurnObservationSchema,
		ProviderID:    "openai-compatible-responses", ModelID: "gpt-5.6",
		ReleaseID: "release-local", TraceRefs: []string{"trace_1"},
		ResponseIDs: []string{"resp_1"}, RequestIDs: []string{"req_1"},
		Usage:     map[string]int64{"input_tokens": 12, "output_tokens": 7, "total_tokens": 19},
		LatencyMS: map[string]int64{"agent_loop": 25, "total": 40},
	})
	if err != nil {
		t.Fatal(err)
	}
	if observed.Status != "committed" || observed.Observation.ProviderID != "openai-compatible-responses" ||
		observed.Observation.Usage["total_tokens"] != 19 ||
		observed.Correlation.SkillInvocationID == nil || *observed.Correlation.SkillInvocationID != "inv_corr" ||
		observed.Correlation.AgentTaskID == nil || *observed.Correlation.AgentTaskID != taskID ||
		observed.Correlation.RunID == nil || *observed.Correlation.RunID != "run_corr" ||
		len(observed.Correlation.AgentToolCallIDs) != 1 || observed.Correlation.AgentToolCallIDs[0] != "tool_corr" {
		t.Fatalf("observed turn = %+v", observed)
	}
	reloaded, err := store.GetAgentTurn(ctx, turn.AgentTurnID)
	if err != nil || reloaded.Observation.TraceRefs[0] != "trace_1" ||
		reloaded.Correlation.AgentToolCallIDs[0] != "tool_corr" {
		t.Fatalf("reloaded turn = %+v, error = %v", reloaded, err)
	}
	if _, err := store.RecordAgentTurnObservation(ctx, turn.AgentTurnID, AgentTurnObservation{
		SchemaVersion: agentTurnObservationSchema, ProviderID: "unsafe provider/value",
	}); err == nil {
		t.Fatal("unsafe observation identifier was accepted")
	}
}

func TestAgentTurnCancelAndRestartRecoveryAlwaysCloseWithOneTerminalEvent(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, _ := store.CreateProject(ctx, "Recovery")
	turn, err := store.AcceptAgentTurn(ctx, project.PrimaryConversationID,
		agentcontract.MessageRequest{Content: "slow"}, CommandMeta{
			Scope: project.ProjectID, CommandType: "create_message",
			IdempotencyKey: "55555555-5555-4555-8555-555555555555", RequestHash: "cancel",
		})
	if err != nil {
		t.Fatalf("AcceptAgentTurn() error = %v", err)
	}
	if _, err := store.ClaimRunnableAgentTurns(ctx, 1); err != nil {
		t.Fatalf("ClaimRunnableAgentTurns() error = %v", err)
	}
	cancel, err := store.CancelAgentTurn(ctx, turn.AgentTurnID)
	if err != nil || !cancel.Accepted || cancel.Turn.Status != "cancel_requested" {
		t.Fatalf("CancelAgentTurn() = %+v, error = %v", cancel, err)
	}
	if cancel.Turn.Observation.CancelReason != "user_request" {
		t.Fatalf("cancel reason = %q", cancel.Turn.Observation.CancelReason)
	}
	if err := store.RecoverInterruptedAgentTurns(ctx); err != nil {
		t.Fatalf("RecoverInterruptedAgentTurns() error = %v", err)
	}
	recovered, err := store.GetAgentTurn(ctx, turn.AgentTurnID)
	if err != nil || recovered.Status != "cancelled" {
		t.Fatalf("recovered turn = %+v, error = %v", recovered, err)
	}
	repeated, err := store.CancelAgentTurn(ctx, turn.AgentTurnID)
	if err != nil || repeated.Accepted {
		t.Fatalf("repeated cancel = %+v, error = %v", repeated, err)
	}
	assertAgentTurnTerminalCount(t, store, turn.AgentTurnID, 1)
}

func assertAgentTurnTerminalCount(t *testing.T, store *Store, turnID string, want int) {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`
		SELECT COUNT(*) FROM events
		WHERE subject_type = 'agent_turn' AND subject_id = ?
		  AND event_type IN ('agent.turn.committed','agent.turn.failed','agent.turn.cancelled')`,
		turnID).Scan(&count); err != nil {
		t.Fatalf("count terminal events: %v", err)
	}
	if count != want {
		t.Fatalf("terminal event count = %d, want %d", count, want)
	}
}

func TestAgentTurnEventPayloadUsesProviderIndependentEnvelope(t *testing.T) {
	ctx := context.Background()
	store, _ := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	defer store.Close()
	project, _ := store.CreateProject(ctx, "Envelope")
	turn, _ := store.AcceptAgentTurn(ctx, project.PrimaryConversationID,
		agentcontract.MessageRequest{Content: "test"}, CommandMeta{
			Scope: project.ProjectID, CommandType: "create_message",
			IdempotencyKey: "66666666-6666-4666-8666-666666666666", RequestHash: "envelope",
		})
	_, _ = store.ClaimRunnableAgentTurns(ctx, 1)
	events, _ := store.ListProjectEvents(ctx, project.ProjectID, 0, 100)
	for _, event := range events.Items {
		if event.EventType != "agent.turn.started" {
			continue
		}
		var payload struct {
			AgentEvent struct {
				SchemaVersion  string          `json:"schema_version"`
				ConversationID string          `json:"conversation_id"`
				TurnID         string          `json:"turn_id"`
				Terminal       bool            `json:"terminal"`
				Payload        json.RawMessage `json:"payload"`
			} `json:"agent_event"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode event payload: %v", err)
		}
		if payload.AgentEvent.SchemaVersion != "1.0.0" ||
			payload.AgentEvent.ConversationID != project.PrimaryConversationID ||
			payload.AgentEvent.TurnID != turn.AgentTurnID || payload.AgentEvent.Terminal {
			t.Fatalf("agent event payload = %+v", payload.AgentEvent)
		}
		return
	}
	t.Fatal("agent.turn.started event not found")
}
