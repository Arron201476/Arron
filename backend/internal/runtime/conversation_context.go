package runtime

import "content-agent/backend/internal/agentcontract"

// RelevantConversationMessages keeps one project conversation while excluding
// unrelated Skill runs from the bounded model-visible context.
func RelevantConversationMessages(
	messages []Message,
	request agentcontract.MessageRequest,
	runtimeContext agentcontract.RuntimeContext,
	limit int,
) []agentcontract.ConversationMessage {
	if limit <= 0 {
		limit = 20
	}
	targetRunID := request.ClientContext.ViewedRunID
	targetCapabilityID := request.ClientContext.ViewedCapabilityID
	if request.CapabilityRef != nil {
		targetCapabilityID = &request.CapabilityRef.CapabilityID
		targetRunID = nil
	}

	result := make([]agentcontract.ConversationMessage, 0, limit)
	for _, message := range messages {
		routing := MessageRoutingContext{Scope: "project"}
		if message.Context != nil && message.Context.RoutingContext.Scope != "" {
			routing = message.Context.RoutingContext
		}
		include := routing.Scope == "project"
		if targetRunID != nil && routing.RunID != nil && *routing.RunID == *targetRunID {
			include = true
		}
		if targetRunID == nil && targetCapabilityID != nil && routing.Scope == "capability" &&
			routing.CapabilityID != nil && *routing.CapabilityID == *targetCapabilityID {
			include = true
		}
		if !include {
			continue
		}
		result = append(result, agentcontract.ConversationMessage{
			MessageID:    message.MessageID,
			Role:         message.Role,
			Content:      message.Content,
			Scope:        routing.Scope,
			InvocationID: routing.InvocationID,
			RunID:        routing.RunID,
			CapabilityID: routing.CapabilityID,
			ArtifactID:   routing.ArtifactID,
		})
	}
	if len(result) > limit {
		result = result[len(result)-limit:]
	}
	return result
}
