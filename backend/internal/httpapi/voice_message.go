package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
)

// The transport must authenticate before constructing this factory. Preparation
// rechecks current authorization; acceptance and quota enforcement remain in Store.
func (s *Server) newVoiceMessageFactory(request *http.Request, projectID, conversationID string,
	body agentcontract.MessageRequest,
) (voiceMessageFactory, error) {
	if request == nil || request.URL == nil || s.runtime == nil || projectID == "" || conversationID == "" {
		return nil, errors.New("invalid voice message context")
	}
	principal, present := identity.FromContext(request.Context())
	if !present || !principal.ValidUser() || !principal.Allows(identity.RoleEditor) {
		return nil, errors.New("voice requires an authenticated editor")
	}
	key := request.Header.Get("Idempotency-Key")
	if !uuidPattern.MatchString(key) || body.Content != "" {
		return nil, errors.New("voice requires an idempotency key and server-produced transcription")
	}
	resolved, _, err := s.runtime.LookupAgentTurnSubmission(request.Context(), conversationID, key)
	if err != nil {
		return nil, err
	}
	if resolved != projectID {
		return nil, errors.New("voice conversation belongs to a different project")
	}
	normalizeViewedContext(&body.ClientContext)
	snapshot, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	owned := request.Clone(request.Context())
	return func(ctx context.Context, text string) (agentcontract.MessageRequest, businessruntime.CommandMeta, error) {
		var prepared agentcontract.MessageRequest
		var meta businessruntime.CommandMeta
		current, ok := identity.FromContext(ctx)
		if !ok || current != principal || owned.Context().Err() != nil {
			return prepared, meta, errors.New("voice message authorization context changed")
		}
		if err := ctx.Err(); err != nil {
			return prepared, meta, err
		}
		if text == "" || text != strings.TrimSpace(text) || !utf8.ValidString(text) || utf8.RuneCountInString(text) > 65536 {
			return prepared, meta, errors.New("invalid normalized voice transcription")
		}
		if err := json.Unmarshal(snapshot, &prepared); err != nil {
			return prepared, meta, err
		}
		prepared.Content = text
		resolved, prior, err := s.runtime.LookupAgentTurnSubmission(ctx, conversationID, key)
		if err != nil {
			return prepared, meta, err
		}
		if resolved != projectID {
			return prepared, meta, errors.New("voice conversation belongs to a different project")
		}
		hash, err := commandRequestHash(owned, projectID, prepared)
		if err != nil {
			return prepared, meta, err
		}
		meta = businessruntime.CommandMeta{Scope: projectID, CommandType: "create_message",
			IdempotencyKey: key, RequestHash: messageRequestHashPrefix + hash}
		if prior != nil {
			matches, err := messageSubmissionMatches(owned, projectID, prepared, hash, *prior)
			if err != nil || !matches {
				return prepared, meta, errors.New("voice idempotency key was used for a different request")
			}
			meta.RequestHash = prior.RequestHash
			return prior.Request, meta, nil
		}
		if _, err := s.runtime.PreflightMessage(ctx, conversationID, prepared); err != nil {
			return prepared, meta, err
		}
		if !s.agentRollout.Allows(principal.WorkspaceID) {
			return prepared, meta, errors.New("voice Agent rollout is disabled")
		}
		if s.agentTurns == nil {
			return prepared, meta, errors.New("voice Agent manager is unavailable")
		}
		if prepared.CapabilityRef != nil && len(prepared.AttachmentRefs) == 0 {
			prepared.AttachmentRefs, err = s.resolveSingleDurableCapabilityMaterial(ctx, projectID, prepared.CapabilityRef.CapabilityID)
		}
		return prepared, meta, err
	}, nil
}

func (s *Server) newConversationVoiceControl(request *http.Request, sessionID string, generation int64,
	projectID, conversationID string, body agentcontract.MessageRequest,
) (*voiceControl, *voiceExecution, error) {
	factory, err := s.newVoiceMessageFactory(request, projectID, conversationID, body)
	if err != nil {
		return nil, nil, err
	}
	if !s.agentRollout.Allows(identity.WorkspaceIDFromContext(request.Context())) {
		return nil, nil, errors.New("voice Agent rollout is disabled")
	}
	if s.agentTurns == nil {
		return nil, nil, errors.New("voice Agent manager is unavailable")
	}
	s.agentTurns.mu.Lock()
	running := s.agentTurns.running
	s.agentTurns.mu.Unlock()
	if !running {
		return nil, nil, errors.New("voice Agent manager is not running")
	}
	return newManagedVoiceControl(request.Context(), s.agentTurns, sessionID, generation, projectID, conversationID, factory)
}
