package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

func TestVoiceMessageUsesBoundSubmissionAndRejectsChangedTranscript(t *testing.T) {
	m, project, meta := directManagerFixture(t)
	s := &Server{runtime: m.store, agentTurns: m, agentRollout: defaultAgentRolloutPolicy()}
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	r := httptest.NewRequest("GET", "/api/conversations/voice", nil).WithContext(ctx)
	r.Header.Set("Idempotency-Key", meta.IdempotencyKey)
	factory, err := s.newVoiceMessageFactory(r, project.ProjectID, project.PrimaryConversationID, agentcontract.MessageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Idempotency-Key", "changed")
	body, command, err := factory(ctx, "Transcript")
	if err != nil || command.IdempotencyKey != meta.IdempotencyKey || !strings.HasPrefix(command.RequestHash, messageRequestHashPrefix) {
		t.Fatalf("voice preparation lost frozen identity: %+v %v", command, err)
	}
	if _, err := m.store.AcceptAgentTurn(ctx, project.PrimaryConversationID, body, command); err != nil {
		t.Fatal(err)
	}
	s.agentRollout = NewAgentRolloutPolicy(false, nil, "disabled")
	replayed, replayMeta, err := factory(ctx, "Transcript")
	if err != nil || replayMeta != command || replayed.Content != body.Content {
		t.Fatalf("voice replay lost its durable receipt: %v", err)
	}
	if _, _, err := factory(ctx, "Different transcript"); err == nil {
		t.Fatal("voice accepted key reuse with changed transcription")
	}
}

func TestVoiceMessageRejectsAuthorizationChangeAndUnavailableAdmission(t *testing.T) {
	for _, mode := range []string{"identity", "rollout", "manager", "project", "transcript"} {
		t.Run(mode, func(t *testing.T) {
			m, project, meta := directManagerFixture(t)
			s := &Server{runtime: m.store, agentTurns: m, agentRollout: defaultAgentRolloutPolicy()}
			ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
			r := httptest.NewRequest("GET", "/api/conversations/voice", nil).WithContext(ctx)
			r.Header.Set("Idempotency-Key", meta.IdempotencyKey)
			projectID := project.ProjectID
			if mode == "project" {
				projectID = "other-project"
			}
			factory, err := s.newVoiceMessageFactory(r, projectID, project.PrimaryConversationID, agentcontract.MessageRequest{})
			if mode == "project" {
				if err == nil {
					t.Fatal("foreign project reached transcription preparation")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			text := "Transcript"
			switch mode {
			case "identity":
				ctx = context.Background()
			case "rollout":
				s.agentRollout = NewAgentRolloutPolicy(false, nil, "disabled")
			case "manager":
				s.agentTurns = nil
			case "transcript":
				text = " Transcript "
			}
			if _, _, err := factory(ctx, text); err == nil {
				t.Fatal("unsafe voice admission accepted")
			}
		})
	}
}

func TestVoiceMessageRequiresAuthenticatedEditorBeforePreparation(t *testing.T) {
	m, project, meta := directManagerFixture(t)
	s := &Server{runtime: m.store}
	for _, principal := range []identity.Principal{identity.ServicePrincipal(), {Kind: identity.KindUser,
		UserID: "viewer", WorkspaceID: "workspace", Role: identity.RoleViewer}} {
		r := httptest.NewRequest("GET", "/voice", nil).WithContext(identity.WithPrincipal(context.Background(), principal))
		r.Header.Set("Idempotency-Key", meta.IdempotencyKey)
		if _, err := s.newVoiceMessageFactory(r, project.ProjectID, project.PrimaryConversationID, agentcontract.MessageRequest{}); err == nil {
			t.Fatal("voice allowed an unauthenticated or read-only principal")
		}
	}
}

func TestConversationVoiceRejectsUnavailableAdmissionBeforeTranscription(t *testing.T) {
	for _, mode := range []string{"disabled", "other_canary", "missing_manager", "stopped_manager"} {
		t.Run(mode, func(t *testing.T) {
			m, project, meta := directManagerFixture(t)
			s := &Server{runtime: m.store, agentTurns: m, agentRollout: defaultAgentRolloutPolicy()}
			ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
			r := httptest.NewRequest("GET", "/voice", nil).WithContext(ctx)
			r.Header.Set("Idempotency-Key", meta.IdempotencyKey)
			switch mode {
			case "disabled":
				s.agentRollout = NewAgentRolloutPolicy(false, nil, "disabled")
			case "other_canary":
				s.agentRollout = NewAgentRolloutPolicy(true, []string{"other-workspace"}, "canary")
			case "missing_manager":
				s.agentTurns = nil
			case "stopped_manager":
				m.running = false
			}
			control, execution, err := s.newConversationVoiceControl(r, "s", 1, project.ProjectID,
				project.PrimaryConversationID, agentcontract.MessageRequest{})
			if err == nil || control != nil || execution != nil {
				t.Fatal("unavailable admission created a voice execution")
			}
			_, prior, err := m.store.LookupAgentTurnSubmission(ctx, project.PrimaryConversationID, meta.IdempotencyKey)
			if err != nil || prior != nil {
				t.Fatal("rejected voice admission changed durable submission")
			}
		})
	}
}
