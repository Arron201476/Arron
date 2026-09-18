package shell

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"content-agent/backend/internal/agentcontract"
)

// RequiredSidecarAgentService is the post-migration Agent boundary. Every turn
// is executed by the OpenAI Agents SDK; there is deliberately no Eino fallback.
type RequiredSidecarAgentService struct {
	client *sidecarControlClient
}

func NewRequiredSidecarAgentService(
	baseURL string,
	token string,
	timeout time.Duration,
) (*RequiredSidecarAgentService, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("sidecar base URL must be an absolute HTTP URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("sidecar base URL must use HTTP or HTTPS")
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("sidecar internal token is required")
	}
	if timeout <= 0 {
		return nil, errors.New("sidecar timeout must be positive")
	}
	return &RequiredSidecarAgentService{client: &sidecarControlClient{
		baseURL: strings.TrimRight(parsed.String(), "/"),
		token:   token,
		http:    &http.Client{Timeout: timeout},
	}}, nil
}

func (s *RequiredSidecarAgentService) StreamTurn(
	ctx context.Context,
	input agentcontract.AgentInput,
	idempotencyKey string,
	agentTurnID string,
	handle AgentTurnStreamHandler,
) (bool, error) {
	return true, s.client.stream(ctx, input, idempotencyKey, agentTurnID, handle)
}

func (s *RequiredSidecarAgentService) GenericChatAvailable() bool { return true }

func (s *RequiredSidecarAgentService) PauseTurn(ctx context.Context, agentTurnID string) (bool, error) {
	return s.client.pause(ctx, agentTurnID)
}
