package shell

import (
	"context"
	"errors"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
)

// Core is the capability-independent host boundary. Domain workflows can be
// absent while project, conversation and generic model services remain valid.
type Core struct {
	registry *capability.Registry
	agent    AgentService
}

func (c *Core) PauseTurn(ctx context.Context, agentTurnID string) (bool, error) {
	controller, ok := c.agent.(interface {
		PauseTurn(context.Context, string) (bool, error)
	})
	if !ok {
		return false, errors.New("configured Agent does not support native pause")
	}
	return controller.PauseTurn(ctx, agentTurnID)
}

func (c *Core) StreamTurn(
	ctx context.Context,
	input agentcontract.AgentInput,
	idempotencyKey string,
	agentTurnID string,
	handle AgentTurnStreamHandler,
) (bool, error) {
	return c.agent.StreamTurn(ctx, input, idempotencyKey, agentTurnID, handle)
}

func New(registry *capability.Registry) *Core {
	return NewWithAgentService(registry, nil)
}

func NewWithAgentService(registry *capability.Registry, agent AgentService) *Core {
	if registry == nil {
		registry = capability.NewEmptyRegistry()
	}
	if agent == nil {
		agent = unavailableAgent{}
	}
	return &Core{registry: registry, agent: agent}
}

type Readiness struct {
	AgentCoreAvailable       bool `json:"agent_core_available"`
	GenericChatAvailable     bool `json:"generic_chat_available"`
	DomainCapabilityCount    int  `json:"domain_capability_count"`
	AvailableCapabilityCount int  `json:"available_capability_count"`
}

func (c *Core) Readiness() Readiness {
	return Readiness{
		AgentCoreAvailable:       true,
		GenericChatAvailable:     c.agent.GenericChatAvailable(),
		DomainCapabilityCount:    len(c.registry.Entries()),
		AvailableCapabilityCount: c.registry.CountByStatus(capability.Available),
	}
}

func (c *Core) Registry() *capability.Registry {
	return c.registry
}
