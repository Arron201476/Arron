package shell

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSidecarMainPauseSignalAuthenticationAndIdentity(t *testing.T) {
	for _, item := range []struct {
		name, body        string
		status            int
		accepted, invalid bool
	}{
		{"accepted", `{"run_id":"turn/id","accepted":true}`, 200, true, false},
		{"not-yet-active", `{"run_id":"turn/id","accepted":false}`, 200, false, false},
		{"wrong-turn", `{"run_id":"other","accepted":true}`, 200, false, true},
		{"malformed", `{`, 200, false, true},
		{"unavailable", `{"detail":"unavailable"}`, 503, false, true},
	} {
		t.Run(item.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.EscapedPath() != "/internal/v1/agent/runs/turn%2Fid/pause" || r.Header.Get("Authorization") != "Bearer pause-token" {
					t.Error("incorrect pause request identity or authentication")
				}
				w.WriteHeader(item.status)
				_, _ = fmt.Fprint(w, item.body)
			}))
			defer server.Close()
			service, err := NewRequiredSidecarAgentService(server.URL, "pause-token", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			accepted, err := service.PauseTurn(context.Background(), "turn/id")
			if (err != nil) != item.invalid || accepted != item.accepted {
				t.Fatalf("pause: %v %v", accepted, err)
			}
		})
	}
}

func TestSidecarMainPauseIsFinalOnlyForCurrentTransport(t *testing.T) {
	for _, item := range []struct {
		name                        string
		terminal, trailing, invalid bool
	}{
		{"nonterminal-checkpoint", false, false, false},
		{"wrong-terminal-marker", true, false, true},
		{"event-after-checkpoint", false, true, true},
	} {
		t.Run(item.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				payload := fmt.Sprintf(`data: {"schema_version":"1.0.0","event_id":"pause","event_type":"agent.turn.paused","project_id":"project","conversation_id":"conversation_1","turn_id":"turn","terminal":%t,"payload":{"run_state":{"$schemaVersion":"1.16"},"run_state_schema":"1.16","pending_sdk_tool_call_ids":[]}}`, item.terminal)
				_, _ = fmt.Fprint(w, payload+"\n\n")
				if item.trailing {
					_, _ = fmt.Fprint(w, payload+"\n\n")
				}
			}))
			defer server.Close()
			service, err := NewRequiredSidecarAgentService(server.URL, "token", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			var events []AgentTurnEvent
			_, err = service.StreamTurn(context.Background(), sidecarTestInput("project", "original"), "key", "turn", func(e AgentTurnEvent) error { events = append(events, e); return nil })
			if (err != nil) != item.invalid {
				t.Fatalf("stream: %v", err)
			}
			if !item.invalid && (len(events) != 1 || events[0].Terminal || !strings.Contains(string(events[0].Payload), "run_state")) {
				t.Fatalf("checkpoint: %+v", events)
			}
		})
	}
}
