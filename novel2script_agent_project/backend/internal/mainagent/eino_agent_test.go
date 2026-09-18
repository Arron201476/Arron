package mainagent

import (
	"context"
	"errors"
	"testing"

	"novel2script-agent/backend/internal/agent"
)

func TestEinoAgentMatchesNativeGuardedDecision(t *testing.T) {
	response := `{
		"intent":"generate_from_material",
		"confidence":0.93,
		"next_action":"start_run",
		"source_mode":"non_novel",
		"agent_reply":"开始生成。",
		"requires_approval":false,
		"reason":"explicit_generation_request"
	}`
	input := Context{Request: MessageRequest{
		ProjectID: "project_eino", Message: "根据素材生成短剧", SourceMode: agent.SourceModeNonNovel,
	}}

	native := New(&fakeChatClient{configured: true, response: response}, "control-model").Decide(context.Background(), input)
	eino, err := NewEinoAgent(New(&fakeChatClient{configured: true, response: response}, "control-model"))
	if err != nil {
		t.Fatal(err)
	}
	actual := eino.Decide(context.Background(), input)

	if actual.Intent != native.Intent || actual.NextAction != native.NextAction || actual.Reason != native.Reason || actual.RequiresGenerationConfig != native.RequiresGenerationConfig {
		t.Fatalf("Eino decision diverged from native: native=%s eino=%s", formatDecisionForDebug(native), formatDecisionForDebug(actual))
	}
	if actual.Orchestrator != "eino" || native.Orchestrator != "native" {
		t.Fatalf("unexpected orchestrators: native=%q eino=%q", native.Orchestrator, actual.Orchestrator)
	}
	if actual.Trace == nil || native.Trace == nil || actual.Trace.GuardApplied != native.Trace.GuardApplied {
		t.Fatalf("guard traces diverged: native=%#v eino=%#v", native.Trace, actual.Trace)
	}
}

func TestEinoAgentKeepsControlFailureSafe(t *testing.T) {
	eino, err := NewEinoAgent(New(&fakeChatClient{configured: true, err: errors.New("timeout")}, "control-model"))
	if err != nil {
		t.Fatal(err)
	}
	decision := eino.Decide(context.Background(), Context{Request: MessageRequest{
		Message: "帮我生成短剧", SourceMode: agent.SourceModeNonNovel,
	}})
	if decision.NextAction != ActionReply || decision.Runtime != "fallback" || decision.Orchestrator != "eino" {
		t.Fatalf("expected safe Eino fallback, got %s runtime=%s orchestrator=%s", formatDecisionForDebug(decision), decision.Runtime, decision.Orchestrator)
	}
	if decision.Reason == "generation_config_required" {
		t.Fatalf("safe fallback was incorrectly routed into generation: %#v", decision)
	}
}
