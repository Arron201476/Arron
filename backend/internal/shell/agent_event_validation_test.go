package shell

import "testing"

func TestSharedAgentEventValidationPreservesDispatchFinalSemantics(t *testing.T) {
	for _, test := range []struct {
		kind     string
		terminal bool
		final    bool
	}{
		{"agent.tool.started", false, false},
		{"agent.turn.committed", true, true},
		{"agent.turn.failed", true, true},
		{"agent.turn.cancelled", true, true},
		{"agent.turn.paused", false, true},
		{"agent.turn.waiting_approval", false, true},
	} {
		t.Run(test.kind, func(t *testing.T) {
			event := AgentTurnEvent{SchemaVersion: "1.0.0", ProjectID: "p", ConversationID: "c", TurnID: "t",
				EventType: test.kind, Terminal: test.terminal}
			final, err := ValidateAgentTurnEvent(event, "p", "c", "t")
			if err != nil || final != test.final {
				t.Fatalf("event validation: final=%v error=%v", final, err)
			}
			event.Terminal = !event.Terminal
			if _, err := ValidateAgentTurnEvent(event, "p", "c", "t"); err == nil {
				t.Fatal("invalid terminal marker accepted")
			}
		})
	}
}

func TestSharedAgentEventValidationRejectsForeignIdentityAndUnknownSchema(t *testing.T) {
	valid := AgentTurnEvent{SchemaVersion: "1.0.0", ProjectID: "p", ConversationID: "c", TurnID: "t", EventType: "agent.tool.started"}
	for _, mutate := range []func(*AgentTurnEvent){
		func(e *AgentTurnEvent) { e.ProjectID = "foreign" },
		func(e *AgentTurnEvent) { e.ConversationID = "foreign" },
		func(e *AgentTurnEvent) { e.TurnID = "foreign" },
		func(e *AgentTurnEvent) { e.SchemaVersion = "unknown" },
		func(e *AgentTurnEvent) { e.EventType = "unknown" },
	} {
		event := valid
		mutate(&event)
		if _, err := ValidateAgentTurnEvent(event, "p", "c", "t"); err == nil {
			t.Fatal("invalid execution event accepted")
		}
	}
}
