package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

const agentTurnConcurrency = 8

type agentTurnManager struct {
	store  *businessruntime.Store
	core   *shell.Core
	logger *slog.Logger
	wake   chan struct{}
	sem    chan struct{}

	mu             sync.Mutex
	dispatchMu     sync.Mutex
	running        bool
	directSequence uint64
	active         map[string]context.CancelFunc
	workers        sync.WaitGroup
	done           chan struct{}
}

func newAgentTurnManager(
	store *businessruntime.Store, core *shell.Core, logger *slog.Logger,
) *agentTurnManager {
	return &agentTurnManager{
		store: store, core: core, logger: logger,
		wake: make(chan struct{}, 1), sem: make(chan struct{}, agentTurnConcurrency),
		active: map[string]context.CancelFunc{},
		done:   make(chan struct{}),
	}
}

func (m *agentTurnManager) run(ctx context.Context) {
	m.mu.Lock()
	m.running = true
	m.mu.Unlock()
	defer close(m.done)
	defer func() {
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
		m.cancelAll()
		m.workers.Wait()
	}()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	m.notify()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
			m.dispatch(ctx)
		case <-ticker.C:
			m.dispatch(ctx)
		}
	}
}

func (m *agentTurnManager) notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *agentTurnManager) dispatch(ctx context.Context) {
	m.dispatchMu.Lock()
	defer m.dispatchMu.Unlock()
	available := cap(m.sem) - len(m.sem)
	if available <= 0 {
		return
	}
	m.mu.Lock()
	activeIDs := make([]string, 0, len(m.active))
	for id := range m.active {
		activeIDs = append(activeIDs, id)
	}
	m.mu.Unlock()
	turns, err := m.store.ClaimRunnableAgentTurns(ctx, available, activeIDs...)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			m.logger.Error("claim Agent turns", "error", err)
		}
		return
	}
	for _, turn := range turns {
		m.sem <- struct{}{}
		turnContext, cancel := context.WithCancel(ctx)
		m.mu.Lock()
		m.active[turn.AgentTurnID] = cancel
		m.mu.Unlock()
		m.workers.Add(1)
		go func() {
			defer m.workers.Done()
			m.execute(turnContext, turn, cancel)
		}()
	}
}

func (m *agentTurnManager) execute(
	ctx context.Context, turn businessruntime.AgentTurn, cancel context.CancelFunc,
) {
	defer func() {
		cancel()
		m.mu.Lock()
		delete(m.active, turn.AgentTurnID)
		m.mu.Unlock()
		<-m.sem
		m.notify()
	}()
	if m.finishIfCancelledOrTerminal(turn.AgentTurnID) {
		return
	}
	principal, err := m.store.ResolvePrincipal(ctx, identity.Principal{
		Kind: identity.KindUser, UserID: turn.UserID, WorkspaceID: turn.WorkspaceID,
		Role: identity.RoleViewer, AuthMethod: "agent_turn",
	})
	if err != nil {
		m.fail(turn, err, "identity_resolution")
		return
	}
	ctx = identity.WithPrincipal(ctx, principal)
	ctx = businessruntime.WithAgentActivity(ctx, businessruntime.AgentActivityIdentity{ProjectID: turn.ProjectID, AgentTurnID: turn.AgentTurnID})
	runtimeContext, err := m.store.GetAgentRuntimeContextForRequest(
		ctx, turn.ProjectID, turn.Request,
	)
	if err != nil {
		if m.finishIfCancelledOrTerminal(turn.AgentTurnID) {
			return
		}
		m.fail(turn, err, "runtime_context")
		return
	}
	resumeContext, err := m.store.GetAgentTurnResumeContext(ctx, turn.AgentTurnID)
	if err != nil {
		m.fail(turn, err, "run_state_restore")
		return
	}
	input := agentcontract.AgentInput{
		ProjectID: turn.ProjectID, ConversationID: turn.ConversationID,
		Request: turn.Request, RuntimeContext: runtimeContext,
		SDKRunState: resumeContext.RunState, ApprovalDecisions: resumeContext.ApprovalDecisions,
		DispatchGeneration: turn.DispatchGeneration,
	}
	for _, item := range turn.AdditionalInputs {
		input.AdditionalInputs = append(input.AdditionalInputs, agentcontract.AgentTurnAdditionalInput{
			InputID: item.InputID, AgentTurnID: item.AgentTurnID, Sequence: item.Sequence,
			Content: item.Content, Status: item.Status, Attachments: item.Attachments,
		})
	}
	pauseContext, stopPauseWatch := context.WithCancel(ctx)
	pauseDone := make(chan struct{})
	go func() { defer close(pauseDone); m.forwardPauseRequests(pauseContext, turn.AgentTurnID) }()
	defer func() { stopPauseWatch(); <-pauseDone }()
	handled, streamErr := m.core.StreamTurn(
		ctx, input, turn.IdempotencyKey, turn.AgentTurnID,
		func(event shell.AgentTurnEvent) error {
			return m.persistEvent(ctx, turn, event)
		},
	)
	m.finishExecution(turn, handled, streamErr)
}

func (m *agentTurnManager) finishExecution(turn businessruntime.AgentTurn, handled bool, streamErr error) {
	current, lookupErr := m.store.GetAgentTurn(context.Background(), turn.AgentTurnID)
	if lookupErr != nil {
		m.logger.Error("load completed Agent turn", "agent_turn_id", turn.AgentTurnID, "error", lookupErr)
		return
	}
	if current.Status == "committed" || current.Status == "failed" || current.Status == "cancelled" {
		return
	}
	if current.Status == "waiting_approval" || current.Status == "accepted" || current.Status == "paused" {
		return
	}
	if current.Status == "cancel_requested" {
		if _, err := m.store.FinishAgentTurnCancelled(
			context.Background(), turn.AgentTurnID, "用户已取消本轮。",
		); err != nil {
			m.logger.Error("finish cancelled Agent turn", "agent_turn_id", turn.AgentTurnID, "error", err)
		}
		return
	}
	if streamErr != nil {
		m.fail(turn, streamErr, "sidecar_transport")
		return
	}
	if !handled {
		m.fail(turn, errors.New("configured Agent does not support streamed turns"), "agent_capability")
		return
	}
	m.fail(turn, errors.New("Agent stream ended without a durable terminal state"), "sidecar_protocol")
}

// runDirectTurn shares admission, cancellation and shutdown with queued turns.
// The transport callback must wait for its owned execution's cleanup before it
// returns; a replay never invokes it. This method does not expose a public API.
func (m *agentTurnManager) runDirectTurn(
	ctx context.Context, conversationID string, request agentcontract.MessageRequest,
	meta businessruntime.CommandMeta,
	run func(context.Context, businessruntime.AgentTurn, shell.AgentTurnStreamHandler) error,
) (businessruntime.AgentTurn, bool, error) {
	if run == nil {
		return businessruntime.AgentTurn{}, false, errors.New("direct turn transport is not configured")
	}
	if err := ctx.Err(); err != nil {
		return businessruntime.AgentTurn{}, false, err
	}
	m.dispatchMu.Lock()
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		m.dispatchMu.Unlock()
		return businessruntime.AgentTurn{}, false, errors.New("Agent turn manager is not running")
	}
	select {
	case m.sem <- struct{}{}:
	default:
		m.mu.Unlock()
		m.dispatchMu.Unlock()
		return businessruntime.AgentTurn{}, false, errors.New("Agent turn concurrency is exhausted")
	}
	ownedCtx, cancel := context.WithCancel(ctx)
	m.directSequence++
	registration := fmt.Sprintf("direct-pending-%d", m.directSequence)
	m.active[registration] = cancel
	m.workers.Add(1)
	m.mu.Unlock()
	defer func() {
		cancel()
		m.mu.Lock()
		delete(m.active, registration)
		m.mu.Unlock()
		<-m.sem
		m.workers.Done()
		m.notify()
	}()
	turn, claimed, err := m.store.AcceptAndClaimAgentTurn(ownedCtx, conversationID, request, meta)
	if err == nil && claimed {
		m.mu.Lock()
		delete(m.active, registration)
		registration = turn.AgentTurnID
		m.active[registration] = cancel
		m.mu.Unlock()
	}
	m.dispatchMu.Unlock()
	if err != nil || !claimed {
		return turn, false, err
	}
	if ownedCtx.Err() != nil || m.finishIfCancelledOrTerminal(turn.AgentTurnID) {
		m.finishExecution(turn, true, context.Canceled)
		return turn, true, context.Canceled
	}
	ownedCtx = businessruntime.WithAgentActivity(ownedCtx, businessruntime.AgentActivityIdentity{
		ProjectID: turn.ProjectID, AgentTurnID: turn.AgentTurnID,
	})
	var eventMu sync.Mutex
	var eventErr error
	finalSeen := false
	closed := false
	err = run(ownedCtx, turn, func(event shell.AgentTurnEvent) error {
		eventMu.Lock()
		defer eventMu.Unlock()
		if closed {
			return errors.New("direct Agent event transport is closed")
		}
		if eventErr != nil {
			return eventErr
		}
		if finalSeen {
			eventErr = errors.New("direct Agent stream emitted data after its final event")
			return eventErr
		}
		var final bool
		final, eventErr = shell.ValidateAgentTurnEvent(event, turn.ProjectID, turn.ConversationID, turn.AgentTurnID)
		if eventErr == nil {
			eventErr = m.persistEvent(ownedCtx, turn, event)
		}
		if eventErr == nil {
			finalSeen = final
		}
		return eventErr
	})
	eventMu.Lock()
	closed = true
	err = errors.Join(err, eventErr)
	if err == nil && !finalSeen {
		err = errors.New("direct Agent stream ended without a final event")
	}
	eventMu.Unlock()
	m.finishExecution(turn, true, err)
	current, lookupErr := m.store.GetAgentTurn(context.Background(), turn.AgentTurnID)
	if lookupErr != nil {
		return turn, true, lookupErr
	}
	if err == nil && (current.Status == "running" || current.Status == "committing" || current.Status == "failed" || current.Status == "cancelled") {
		err = errors.New("direct Agent execution did not complete successfully")
	}
	return current, true, err
}

func (m *agentTurnManager) finishIfCancelledOrTerminal(agentTurnID string) bool {
	current, err := m.store.GetAgentTurn(context.Background(), agentTurnID)
	if err != nil {
		m.logger.Error("load Agent turn state", "agent_turn_id", agentTurnID, "error", err)
		return false
	}
	if current.Status == "committed" || current.Status == "failed" || current.Status == "cancelled" {
		return true
	}
	if current.Status != "cancel_requested" {
		return false
	}
	if _, err := m.store.FinishAgentTurnCancelled(
		context.Background(), agentTurnID, "用户已取消本轮。",
	); err != nil {
		m.logger.Error("finish cancelled Agent turn", "agent_turn_id", agentTurnID, "error", err)
		return false
	}
	return true
}

func (m *agentTurnManager) persistEvent(
	ctx context.Context, turn businessruntime.AgentTurn, event shell.AgentTurnEvent,
) error {
	var payload map[string]any
	if len(event.Payload) > 0 && string(event.Payload) != "null" {
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
	}
	if payload == nil {
		payload = map[string]any{}
	}
	switch event.EventType {
	case "agent.turn.inputs_included":
		var receipt struct {
			Included []string `json:"included_input_ids"`
		}
		if err := json.Unmarshal(event.Payload, &receipt); err != nil {
			return err
		}
		return m.store.RecordAgentTurnInputsIncluded(ctx, turn.AgentTurnID, turn.DispatchGeneration, receipt.Included)
	case "agent.turn.waiting_approval", "agent.turn.paused":
		m.recordObservation(ctx, turn.AgentTurnID, payload, "", "")
		var checkpoint struct {
			Recovery string          `json:"recovery_reason"`
			RunState json.RawMessage `json:"run_state"`
			Schema   string          `json:"run_state_schema"`
			Pending  []string        `json:"pending_sdk_tool_call_ids"`
			Included []string        `json:"included_input_ids"`
		}
		if err := json.Unmarshal(event.Payload, &checkpoint); err != nil {
			return err
		}
		if len(checkpoint.RunState) == 0 {
			return errors.New("Agent checkpoint event omitted SDK RunState")
		}
		if checkpoint.Pending == nil || (event.EventType == "agent.turn.waiting_approval" && len(checkpoint.Pending) == 0) {
			return errors.New("Agent approval event omitted pending SDK tool calls")
		}
		command := businessruntime.PauseAgentTurnForApprovalCommand{
			RecoveryReason: checkpoint.Recovery,
			SchemaVersion:  checkpoint.Schema,
			RunState:       checkpoint.RunState, PendingSDKToolCallIDs: checkpoint.Pending,
			AgentTurnDispatchGeneration: turn.DispatchGeneration,
			IncludedAgentTurnInputIDs:   checkpoint.Included,
		}
		var err error
		if event.EventType == "agent.turn.paused" {
			_, err = m.store.CompleteAgentTurnPause(ctx, turn.AgentTurnID, command)
		} else {
			_, err = m.store.PauseAgentTurnForApproval(ctx, turn.AgentTurnID, command)
		}
		return err
	case "agent.turn.committed":
		m.recordObservation(ctx, turn.AgentTurnID, payload, "", "")
		var receipt struct {
			Included []string `json:"included_input_ids"`
		}
		if err := json.Unmarshal(event.Payload, &receipt); err != nil {
			return err
		}
		includeInputs := func() error {
			if len(receipt.Included) == 0 {
				return nil
			}
			return m.store.RecordAgentTurnInputsIncluded(ctx, turn.AgentTurnID, turn.DispatchGeneration, receipt.Included)
		}
		current, err := m.store.GetAgentTurn(ctx, turn.AgentTurnID)
		if err != nil {
			return err
		}
		if current.Status == "committed" {
			return includeInputs()
		}
		rawExchange, ok := payload["exchange"]
		if !ok {
			return errors.New("Agent committed event omitted exchange")
		}
		encoded, err := json.Marshal(rawExchange)
		if err != nil {
			return err
		}
		var wrapped struct {
			Data businessruntime.MessageExchange `json:"data"`
		}
		if err := json.Unmarshal(encoded, &wrapped); err != nil {
			return err
		}
		if wrapped.Data.AgentMessage.MessageID == "" {
			return errors.New("Agent committed event contained an empty exchange")
		}
		_, err = m.store.CompleteAgentTurnCommit(ctx, turn.AgentTurnID, wrapped.Data)
		if err != nil {
			return err
		}
		return includeInputs()
	case "agent.turn.failed":
		m.recordObservation(ctx, turn.AgentTurnID, payload, stringPayload(payload, "failure_stage"), "")
		code, message := "AGENT_EXECUTION_FAILED", "Agent 暂时无法完成本次请求，请重试。"
		if stringPayload(payload, "failure_stage") == "session_compaction" {
			code = "NATIVE_COMPACTION_UNAVAILABLE"
			message = "模型网关未返回有效的原生上下文压缩结果。原会话已保留，请修复网关的 Responses compact 协议后重试。"
		}
		_, err := m.store.FailAgentTurn(
			ctx, turn.AgentTurnID, code, message,
		)
		return err
	case "agent.turn.cancelled":
		m.recordObservation(ctx, turn.AgentTurnID, payload, "", stringPayload(payload, "reason"))
		_, err := m.store.FinishAgentTurnCancelled(ctx, turn.AgentTurnID, "本轮已取消。")
		return err
	default:
		return m.store.AppendAgentTurnProgress(ctx, turn.AgentTurnID, event.EventType, payload)
	}
}

func (m *agentTurnManager) fail(turn businessruntime.AgentTurn, cause error, failureStage string) {
	m.logger.Error(
		"Agent turn execution failed",
		"agent_turn_id", turn.AgentTurnID,
		"project_id", turn.ProjectID,
		"conversation_id", turn.ConversationID,
		"failure_stage", failureStage,
		"error", cause,
	)
	if _, err := m.store.RecordAgentTurnObservation(
		context.Background(), turn.AgentTurnID, businessruntime.AgentTurnObservation{
			SchemaVersion: "agent_turn_observation.v1",
			FailureStage:  failureStage,
		},
	); err != nil {
		m.logger.Error("persist Agent turn observation", "agent_turn_id", turn.AgentTurnID, "error", err)
	}
	if _, err := m.store.FailAgentTurn(
		context.Background(), turn.AgentTurnID, "AGENT_EXECUTION_FAILED",
		"Agent 暂时无法完成本次请求，请重试。",
	); err != nil {
		m.logger.Error("persist Agent turn failure", "agent_turn_id", turn.AgentTurnID, "error", err)
	}
}

func (m *agentTurnManager) recordObservation(
	ctx context.Context, agentTurnID string, payload map[string]any, failureStage, cancelReason string,
) {
	observation := businessruntime.AgentTurnObservation{
		SchemaVersion: "agent_turn_observation.v1",
	}
	if raw, exists := payload["observation"]; exists && raw != nil {
		encoded, err := json.Marshal(raw)
		if err != nil || json.Unmarshal(encoded, &observation) != nil {
			m.logger.Warn("ignore malformed Agent turn observation", "agent_turn_id", agentTurnID)
			observation = businessruntime.AgentTurnObservation{
				SchemaVersion: "agent_turn_observation.v1",
			}
		}
	}
	if observation.SchemaVersion == "" {
		observation.SchemaVersion = "agent_turn_observation.v1"
	}
	if observation.FailureStage == "" {
		observation.FailureStage = failureStage
	}
	if observation.CancelReason == "" {
		observation.CancelReason = cancelReason
	}
	if _, err := m.store.RecordAgentTurnObservation(ctx, agentTurnID, observation); err != nil {
		m.logger.Warn("ignore invalid Agent turn observation", "agent_turn_id", agentTurnID, "error", err)
	}
}

func stringPayload(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func (m *agentTurnManager) cancel(agentTurnID string) bool {
	m.mu.Lock()
	cancel := m.active[agentTurnID]
	m.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

func (m *agentTurnManager) forwardPauseRequests(ctx context.Context, agentTurnID string) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		turn, err := m.store.GetAgentTurn(ctx, agentTurnID)
		if err == nil && turn.Status == "pausing" {
			attempt, cancel := context.WithTimeout(ctx, 2*time.Second)
			accepted, _ := m.core.PauseTurn(attempt, agentTurnID)
			cancel()
			if accepted {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *agentTurnManager) cancelAll() {
	m.mu.Lock()
	cancellations := make([]context.CancelFunc, 0, len(m.active))
	for _, cancel := range m.active {
		cancellations = append(cancellations, cancel)
	}
	m.mu.Unlock()
	for _, cancel := range cancellations {
		cancel()
	}
}
