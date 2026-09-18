package shell

import (
	"context"
	"strings"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/capability"
)

func TestCoreIsReadyWithoutDomainCapabilities(t *testing.T) {
	core := New(capability.NewEmptyRegistry())
	readiness := core.Readiness()
	if !readiness.AgentCoreAvailable {
		t.Fatal("Agent Core must remain available with an empty Registry")
	}
	if readiness.DomainCapabilityCount != 0 || readiness.AvailableCapabilityCount != 0 {
		t.Fatalf("unexpected capability counts: %+v", readiness)
	}
}

func TestCoreFailsClosedWithoutSDKAgentService(t *testing.T) {
	core := New(capability.NewEmptyRegistry())
	handled, err := core.StreamTurn(context.Background(), agentcontract.AgentInput{
		ProjectID:      "project_1",
		ConversationID: "conversation_1",
		Request: agentcontract.MessageRequest{
			Content: "帮我处理这份内容",
		},
	}, "idempotency-key", "agt_test", nil)
	if !handled || err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("StreamTurn() handled = %v, error = %v", handled, err)
	}
	if core.Readiness().GenericChatAvailable {
		t.Fatal("generic chat must be unavailable without the SDK Agent service")
	}
}
