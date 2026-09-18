package shell

import (
	"context"
	"encoding/json"
	"errors"

	"content-agent/backend/internal/agentcontract"
)

type AgentService interface {
	StreamTurn(
		context.Context,
		agentcontract.AgentInput,
		string,
		string,
		AgentTurnStreamHandler,
	) (bool, error)
	GenericChatAvailable() bool
}

// AgentTurnEvent is the provider-independent event contract emitted by the
// SDK sidecar. Go validates identity and persists each accepted event before it
// reaches a browser.
type AgentTurnEvent struct {
	SchemaVersion  string          `json:"schema_version"`
	EventID        string          `json:"event_id"`
	EventType      string          `json:"event_type"`
	ProjectID      string          `json:"project_id"`
	ConversationID string          `json:"conversation_id"`
	TurnID         string          `json:"turn_id"`
	Terminal       bool            `json:"terminal"`
	Payload        json.RawMessage `json:"payload"`
	OccurredAt     string          `json:"occurred_at"`
}

type AgentTurnStreamHandler func(AgentTurnEvent) error

type unavailableAgent struct{}

func (unavailableAgent) GenericChatAvailable() bool { return false }

func (unavailableAgent) StreamTurn(
	context.Context,
	agentcontract.AgentInput,
	string,
	string,
	AgentTurnStreamHandler,
) (bool, error) {
	return true, errors.New("OpenAI Agents SDK service is not configured")
}
