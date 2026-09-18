package runtime

import (
	"testing"

	"content-agent/backend/internal/agentcontract"
)

func TestRelevantConversationMessagesIsolatesIndependentRuns(t *testing.T) {
	runA, runB := "run_a", "run_b"
	capA, capB := "novel_to_script", "video_reference_creation"
	messages := []Message{
		messageWithRouting("project", nil, nil, "作品级说明"),
		messageWithRouting("run", &runA, &capA, "小说运行消息"),
		messageWithRouting("artifact", &runA, &capA, "小说产物消息"),
		messageWithRouting("run", &runB, &capB, "视频运行消息"),
	}
	got := RelevantConversationMessages(messages, agentcontract.MessageRequest{
		ClientContext: agentcontract.ClientContext{ViewedRunID: &runA, ViewedCapabilityID: &capA},
	}, agentcontract.RuntimeContext{}, 20)
	if len(got) != 3 || got[0].Content != "作品级说明" || got[1].Content != "小说运行消息" || got[2].Content != "小说产物消息" {
		t.Fatalf("relevant messages = %+v", got)
	}
}

func TestExplicitSkillInvocationDoesNotInheritHistoricalRuns(t *testing.T) {
	runA, runB := "run_a", "run_b"
	capA, capB := "novel_to_script", "video_reference_creation"
	messages := []Message{
		messageWithRouting("project", nil, nil, "作品级说明"),
		messageWithRouting("run", &runA, &capA, "小说运行消息"),
		messageWithRouting("run", &runB, &capB, "视频历史消息"),
	}
	got := RelevantConversationMessages(messages, agentcontract.MessageRequest{
		CapabilityRef: &agentcontract.CapabilityRef{CapabilityID: capB, Version: "1.2.0"},
	}, agentcontract.RuntimeContext{}, 20)
	if len(got) != 1 || got[0].Content != "作品级说明" {
		t.Fatalf("explicit Skill context = %+v", got)
	}
}

func messageWithRouting(scope string, runID, capabilityID *string, content string) Message {
	return Message{Content: content, Role: "user", Context: &MessageContext{
		RoutingContext: MessageRoutingContext{Scope: scope, RunID: runID, CapabilityID: capabilityID},
	}}
}
