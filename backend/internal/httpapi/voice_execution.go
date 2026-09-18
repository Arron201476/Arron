package httpapi

import (
	"context"
	"errors"
	"sync"

	"content-agent/backend/internal/agentcontract"
	businessruntime "content-agent/backend/internal/runtime"
	"content-agent/backend/internal/shell"
)

type voiceMessageFactory func(context.Context, string) (agentcontract.MessageRequest, businessruntime.CommandMeta, error)

type voiceExecutionGrant struct {
	turn    businessruntime.AgentTurn
	claimed bool
	err     error
}

// voiceExecution bridges the private request/reply protocol to the existing
// manager. The connection owner must finish it after stopping its transport.
type voiceExecution struct {
	mu             sync.Mutex
	ctx            context.Context
	cancel         context.CancelFunc
	manager        *agentTurnManager
	projectID      string
	conversationID string
	message        voiceMessageFactory
	started        bool
	closed         bool
	turn           businessruntime.AgentTurn
	handle         shell.AgentTurnStreamHandler
	ready          chan voiceExecutionGrant
	finished       chan error
	done           chan struct{}
	result         error
}

func newManagedVoiceControl(ctx context.Context, manager *agentTurnManager, sessionID string, generation int64,
	projectID, conversationID string, message voiceMessageFactory,
) (*voiceControl, *voiceExecution, error) {
	if manager == nil || message == nil {
		return nil, nil, errors.New("voice requires an active manager and authorized message preparation")
	}
	owned, cancel := context.WithCancel(ctx)
	execution := &voiceExecution{ctx: owned, cancel: cancel, manager: manager, projectID: projectID, conversationID: conversationID,
		message: message, ready: make(chan voiceExecutionGrant, 1), finished: make(chan error, 1), done: make(chan struct{})}
	control, err := newVoiceControl(sessionID, generation, projectID, conversationID, execution.prepare, execution.persist)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return control, execution, nil
}

func (v *voiceExecution) prepare(ctx context.Context, transcription string) (businessruntime.AgentTurn, bool, error) {
	v.mu.Lock()
	if v.started || v.closed || v.ctx.Err() != nil || ctx.Err() != nil {
		v.mu.Unlock()
		return businessruntime.AgentTurn{}, false, errors.New("voice execution preparation was already attempted or closed")
	}
	v.started = true
	v.mu.Unlock()
	go func() {
		defer close(v.done)
		request, meta, err := v.message(v.ctx, transcription)
		published := false
		var turn businessruntime.AgentTurn
		var claimed bool
		if err == nil && (request.Content != transcription || meta.Scope != v.projectID) {
			err = errors.New("prepared voice message changed the transcript")
		}
		if err == nil {
			turn, claimed, err = v.manager.runDirectTurn(v.ctx, v.conversationID, request, meta,
				func(ctx context.Context, turn businessruntime.AgentTurn, handle shell.AgentTurnStreamHandler) error {
					v.mu.Lock()
					if v.closed || ctx.Err() != nil {
						v.mu.Unlock()
						return context.Canceled
					}
					v.turn, v.handle = turn, handle
					v.mu.Unlock()
					v.ready <- voiceExecutionGrant{turn: turn, claimed: true}
					published = true
					select {
					case err := <-v.finished:
						return err
					case <-ctx.Done():
						// Wake the transport owner, but retain the manager slot until
						// it confirms that its socket and Sidecar task have drained.
						v.cancel()
						return errors.Join(ctx.Err(), <-v.finished)
					}
				})
		}
		v.mu.Lock()
		v.handle = nil
		v.result = err
		v.mu.Unlock()
		if !published {
			v.ready <- voiceExecutionGrant{turn: turn, claimed: claimed, err: err}
		}
	}()
	select {
	case grant := <-v.ready:
		v.mu.Lock()
		closed := v.closed
		v.mu.Unlock()
		if closed || v.ctx.Err() != nil || ctx.Err() != nil {
			v.cancel()
			return businessruntime.AgentTurn{}, false, context.Canceled
		}
		return grant.turn, grant.claimed, grant.err
	case <-ctx.Done():
		v.cancel()
		return businessruntime.AgentTurn{}, false, ctx.Err()
	case <-v.ctx.Done():
		return businessruntime.AgentTurn{}, false, v.ctx.Err()
	}
}

// transportContext is cancelled by both the connection owner and manager stop.
// Its owner must stop the transport and then call finish, including on errors.
func (v *voiceExecution) transportContext() context.Context {
	return v.ctx
}

func (v *voiceExecution) persist(ctx context.Context, turn businessruntime.AgentTurn, event shell.AgentTurnEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	v.mu.Lock()
	handle, owned := v.handle, v.turn
	closed := v.closed
	v.mu.Unlock()
	if closed || v.ctx.Err() != nil || handle == nil || turn.AgentTurnID != owned.AgentTurnID || turn.DispatchGeneration != owned.DispatchGeneration {
		return errors.New("voice event has no active managed execution")
	}
	return handle(event)
}

func (v *voiceExecution) finish(cause error) error {
	v.mu.Lock()
	started, alreadyClosed := v.started, v.closed
	v.closed = true
	v.mu.Unlock()
	if !alreadyClosed {
		v.finished <- cause
	}
	if cause != nil || !started {
		v.cancel()
	}
	if started {
		<-v.done
	}
	v.cancel()
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.result
}
