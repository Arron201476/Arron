package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
)

type eventFlushRecorder struct {
	*httptest.ResponseRecorder
	afterFlush func()
}

func (writer *eventFlushRecorder) Flush() {
	writer.ResponseRecorder.Flush()
	if writer.afterFlush != nil {
		writer.afterFlush()
	}
}

func lifecycleEvent(seq int64, kind string) businessruntime.Event {
	return businessruntime.Event{EventID: "fixture-event", EventType: kind, SchemaVersion: 1, ProjectID: "fixture-project", ProjectEventSeq: seq, Payload: json.RawMessage(`{}`)}
}

func TestEventStreamRevalidatesSessionBetweenReplayBatches(t *testing.T) {
	for _, action := range []string{"logout", "expire", "replace_user"} {
		t.Run(action, func(t *testing.T) {
			principal := identity.DefaultLocalPrincipal()
			principal.AuthMethod = "session_cookie"
			server := &Server{auth: NewLocalAuthenticator(identity.DefaultLocalPrincipal()), sessions: map[string]browserSession{}, now: func() time.Time { return time.Now().UTC() }}
			server.sessions["fixture-cookie"] = browserSession{Principal: principal, ExpiresAt: server.now().Add(time.Minute)}
			ctx, cancel := context.WithTimeout(identity.WithPrincipal(context.Background(), principal), 2*time.Second)
			defer cancel()
			request := httptest.NewRequest(http.MethodGet, "/events?after_seq=0", nil).WithContext(ctx)
			request.AddCookie(&http.Cookie{Name: browserSessionCookie, Value: "fixture-cookie"})
			reads := 0
			writer := &eventFlushRecorder{ResponseRecorder: httptest.NewRecorder()}
			writer.afterFlush = func() {
				server.sessionMu.Lock()
				defer server.sessionMu.Unlock()
				session := server.sessions["fixture-cookie"]
				switch action {
				case "logout":
					delete(server.sessions, "fixture-cookie")
				case "expire":
					session.ExpiresAt = server.now().Add(-time.Minute)
					server.sessions["fixture-cookie"] = session
				case "replace_user":
					session.Principal.UserID = "different-user"
					server.sessions["fixture-cookie"] = session
				}
			}
			server.streamEvents(writer, request, eventStreamSource{
				scope: "project", currentSeq: func() (int64, error) { return 2, nil },
				list: func(cursor int64) (businessruntime.EventBatch, error) {
					reads++
					if reads > 1 {
						return businessruntime.EventBatch{}, &businessruntime.DomainError{Code: "WORKSPACE_ACCESS_DENIED", Message: "unexpected second replay batch"}
					}
					return businessruntime.EventBatch{Items: []businessruntime.Event{lifecycleEvent(cursor+1, "message.created")}, HasMore: true}, nil
				},
				eventSeq: func(event businessruntime.Event) int64 { return event.ProjectEventSeq },
			})
			body := writer.Body.String()
			if reads != 1 || !strings.Contains(body, "event: stream.access_revoked") || strings.Contains(body, "id: 2\n") {
				t.Fatalf("session %s continued replay: reads=%d body=%s", action, reads, body)
			}
		})
	}
}

func TestEventStreamSnapshotNotificationsPreserveReplayFrames(t *testing.T) {
	for _, scenario := range []string{"project", "run", "delta_only"} {
		t.Run(scenario, func(t *testing.T) {
			principal := identity.DefaultLocalPrincipal()
			server := &Server{auth: NewLocalAuthenticator(principal)}
			ctx, cancel := context.WithTimeout(identity.WithPrincipal(context.Background(), principal), 2*time.Second)
			defer cancel()
			request := httptest.NewRequest(http.MethodGet, "/events?after_seq=0", nil).WithContext(ctx)
			scope := "project"
			if scenario == "run" {
				scope = "run"
			}
			items := []businessruntime.Event{lifecycleEvent(1, "custom_skill.output_changed"), lifecycleEvent(2, "revision_request.no_change"), lifecycleEvent(3, "agent.output.delta")}
			if scenario == "delta_only" {
				items = []businessruntime.Event{lifecycleEvent(1, "agent.output.delta")}
			}
			writer := &eventFlushRecorder{ResponseRecorder: httptest.NewRecorder(), afterFlush: cancel}
			server.streamEvents(writer, request, eventStreamSource{
				scope: scope, currentSeq: func() (int64, error) { return int64(len(items)), nil },
				list:     func(int64) (businessruntime.EventBatch, error) { return businessruntime.EventBatch{Items: items}, nil },
				eventSeq: func(event businessruntime.Event) int64 { return event.ProjectEventSeq },
			})
			body := writer.Body.String()
			wantNotices := 0
			if scenario == "project" {
				wantNotices = 1
				if !strings.Contains(body, `"project_id":"fixture-project"`) || !strings.Contains(body, `"project_event_seq":2`) {
					t.Fatalf("missing snapshot identity: %s", body)
				}
			}
			if strings.Count(body, "event: stream.snapshot_invalidated\n") != wantNotices || strings.Count(body, "\nid: ") != len(items) {
				t.Fatalf("notification changed durable cursor frames: %s", body)
			}
		})
	}
}

func TestEventStreamClosesOnRevokedReplayWithoutEmittingPayload(t *testing.T) {
	principal := identity.DefaultLocalPrincipal()
	server := &Server{auth: NewLocalAuthenticator(principal)}
	request := httptest.NewRequest(http.MethodGet, "/events?after_seq=0", nil).WithContext(identity.WithPrincipal(context.Background(), principal))
	writer := httptest.NewRecorder()
	server.streamEvents(writer, request, eventStreamSource{
		scope: "project", currentSeq: func() (int64, error) { return 1, nil },
		list: func(int64) (businessruntime.EventBatch, error) {
			return businessruntime.EventBatch{}, &businessruntime.DomainError{Code: "WORKSPACE_ACCESS_DENIED", Message: "private detail"}
		},
		eventSeq: func(event businessruntime.Event) int64 { return event.ProjectEventSeq },
	})
	body := writer.Body.String()
	if !strings.Contains(body, "event: stream.access_revoked") || strings.Contains(body, "private detail") || strings.Contains(body, "\nid:") {
		t.Fatalf("revoked replay response: %s", body)
	}
}
