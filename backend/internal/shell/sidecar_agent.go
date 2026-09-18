package shell

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"content-agent/backend/internal/agentcontract"
)

const maxSidecarResponseBytes = agentcontract.MaxSDKRunStateEnvelopeBytes

type sidecarControlClient struct {
	baseURL string
	token   string
	http    *http.Client
}

var allowedSidecarAgentEvents = map[string]struct{}{
	"agent.updated":               {},
	"agent.tool.started":          {},
	"agent.tool.completed":        {},
	"agent.output.delta":          {},
	"agent.approval.requested":    {},
	"agent.artifact.created":      {},
	"agent.turn.waiting_approval": {},
	"agent.turn.paused":           {},
	"agent.turn.inputs_included":  {},
	"agent.turn.committed":        {},
	"agent.turn.failed":           {},
	"agent.turn.cancelled":        {},
}

// ValidateAgentTurnEvent is shared by streamed and direct execution transports.
// The returned flag marks the final event of this dispatch, including pauses.
func ValidateAgentTurnEvent(event AgentTurnEvent, projectID, conversationID, turnID string) (bool, error) {
	if event.SchemaVersion != "1.0.0" {
		return false, errors.New("sidecar stream event schema is unsupported")
	}
	if event.ProjectID != projectID || event.ConversationID != conversationID || event.TurnID != turnID {
		return false, errors.New("sidecar stream event identity mismatch")
	}
	if _, ok := allowedSidecarAgentEvents[event.EventType]; !ok {
		return false, errors.New("sidecar stream event type is unsupported")
	}
	if len(event.Payload) > maxSidecarResponseBytes {
		return false, errors.New("sidecar stream event payload exceeds limit")
	}
	terminal := event.EventType == "agent.turn.committed" || event.EventType == "agent.turn.failed" || event.EventType == "agent.turn.cancelled"
	if event.Terminal != terminal {
		return false, errors.New("sidecar stream terminal marker is invalid")
	}
	return terminal || event.EventType == "agent.turn.waiting_approval" || event.EventType == "agent.turn.paused", nil
}

func (c *sidecarControlClient) stream(
	ctx context.Context,
	input agentcontract.AgentInput,
	idempotencyKey string,
	agentTurnID string,
	handle AgentTurnStreamHandler,
) error {
	payload := struct {
		ProjectID          string                                    `json:"project_id"`
		ConversationID     string                                    `json:"conversation_id"`
		Request            agentcontract.MessageRequest              `json:"request"`
		IdempotencyKey     string                                    `json:"idempotency_key"`
		AgentTurnID        string                                    `json:"agent_turn_id"`
		DispatchGeneration int64                                     `json:"dispatch_generation,omitempty"`
		RunState           json.RawMessage                           `json:"run_state,omitempty"`
		ApprovalDecisions  []agentcontract.AgentToolApprovalDecision `json:"approval_decisions,omitempty"`
		AdditionalInputs   []agentcontract.AgentTurnAdditionalInput  `json:"additional_inputs,omitempty"`
	}{
		ProjectID: input.ProjectID, ConversationID: input.ConversationID,
		Request: input.Request, IdempotencyKey: idempotencyKey, AgentTurnID: agentTurnID,
		RunState: input.SDKRunState, ApprovalDecisions: input.ApprovalDecisions,
		AdditionalInputs:   input.AdditionalInputs,
		DispatchGeneration: input.DispatchGeneration,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode sidecar stream input: %w", err)
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.baseURL+"/internal/v1/agent/execute-stream", bytes.NewReader(encoded),
	)
	if err != nil {
		return fmt.Errorf("create sidecar stream request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("call sidecar stream: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return sidecarHTTPError("stream execution", response)
	}
	if runID := strings.TrimSpace(response.Header.Get("X-Sidecar-Run-ID")); runID != "" && runID != agentTurnID {
		return errors.New("sidecar stream returned a mismatched turn ID")
	}

	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64*1024), maxSidecarResponseBytes)
	streamFinalCount := 0
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		if streamFinalCount > 0 {
			return errors.New("sidecar stream emitted data after its terminal event")
		}
		var event AgentTurnEvent
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event); err != nil {
			return fmt.Errorf("decode sidecar stream event: %w", err)
		}
		final, err := ValidateAgentTurnEvent(event, input.ProjectID, input.ConversationID, agentTurnID)
		if err != nil {
			return err
		}
		if final {
			streamFinalCount++
		}
		if handle != nil {
			if err := handle(event); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read sidecar stream: %w", err)
	}
	if streamFinalCount != 1 {
		return errors.New("sidecar stream ended without a final event")
	}
	return nil
}

func (c *sidecarControlClient) pause(ctx context.Context, agentTurnID string) (bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/internal/v1/agent/runs/"+url.PathEscape(agentTurnID)+"/pause", nil)
	if err != nil {
		return false, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, sidecarHTTPError("pause execution", response)
	}
	var result struct {
		RunID    string `json:"run_id"`
		Accepted bool   `json:"accepted"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&result); err != nil {
		return false, err
	}
	if result.RunID != agentTurnID {
		return false, errors.New("sidecar pause returned a mismatched turn ID")
	}
	return result.Accepted, nil
}

func sidecarHTTPError(operation string, response *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(response.Body, maxSidecarResponseBytes))
	if err != nil {
		return fmt.Errorf("sidecar %s returned HTTP %d", operation, response.StatusCode)
	}
	var envelope struct {
		Detail string `json:"detail"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		detail := strings.TrimSpace(envelope.Detail)
		if detail != "" {
			if len(detail) > 500 {
				detail = detail[:500]
			}
			return fmt.Errorf(
				"sidecar %s returned HTTP %d: %s",
				operation,
				response.StatusCode,
				detail,
			)
		}
	}
	return fmt.Errorf("sidecar %s returned HTTP %d", operation, response.StatusCode)
}
