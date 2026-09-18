package httpapi

import "strings"

// AgentRolloutPolicy is a fail-closed gate for accepting new SDK-owned turns.
// It never selects a legacy Agent implementation.
type AgentRolloutPolicy struct {
	enabled          bool
	canaryWorkspaces map[string]struct{}
	releaseID        string
}

func NewAgentRolloutPolicy(enabled bool, canaryWorkspaceIDs []string, releaseID string) AgentRolloutPolicy {
	policy := AgentRolloutPolicy{
		enabled:          enabled,
		canaryWorkspaces: make(map[string]struct{}, len(canaryWorkspaceIDs)),
		releaseID:        strings.TrimSpace(releaseID),
	}
	if policy.releaseID == "" {
		policy.releaseID = "dev"
	}
	for _, workspaceID := range canaryWorkspaceIDs {
		if workspaceID = strings.TrimSpace(workspaceID); workspaceID != "" {
			policy.canaryWorkspaces[workspaceID] = struct{}{}
		}
	}
	return policy
}

func defaultAgentRolloutPolicy() AgentRolloutPolicy {
	return NewAgentRolloutPolicy(true, nil, "dev")
}

func (p AgentRolloutPolicy) Allows(workspaceID string) bool {
	if !p.enabled {
		return false
	}
	if len(p.canaryWorkspaces) == 0 {
		return true
	}
	_, allowed := p.canaryWorkspaces[strings.TrimSpace(workspaceID)]
	return allowed
}

func (p AgentRolloutPolicy) Status() map[string]any {
	mode := "all"
	if !p.enabled {
		mode = "disabled"
	} else if len(p.canaryWorkspaces) > 0 {
		mode = "canary"
	}
	return map[string]any{
		"enabled": p.enabled, "mode": mode,
		"canary_workspace_count": len(p.canaryWorkspaces),
		"release_id":             policySafeValue(p.releaseID, "dev"),
	}
}

func policySafeValue(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return fallback
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			!strings.ContainsRune("_.:-", character) {
			return fallback
		}
	}
	return value
}
