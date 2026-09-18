package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"content-agent/backend/internal/identity"
	businessruntime "content-agent/backend/internal/runtime"
)

func activityHeaders(request *http.Request) (businessruntime.AgentActivityIdentity, bool) {
	activity := businessruntime.AgentActivityIdentity{
		ProjectID:          request.Header.Get("X-Agent-Project-ID"),
		AgentTurnID:        request.Header.Get("X-Agent-Turn-ID"),
		AgentTaskAttemptID: request.Header.Get("X-Agent-Task-Attempt-ID"),
		ExecutionAttemptID: request.Header.Get("X-Agent-Execution-Attempt-ID"),
		AttemptToken:       request.Header.Get("X-Agent-Attempt-Token"),
	}
	memoryHeaders := request.Header.Values("X-Agent-Memory-Generation-ID") != nil || request.Header.Values("X-Agent-Memory-Generation-Attempt") != nil
	if memoryHeaders {
		activity.MemoryGenerationID = request.Header.Get("X-Agent-Memory-Generation-ID")
		attempt, err := strconv.Atoi(request.Header.Get("X-Agent-Memory-Generation-Attempt"))
		valid := err == nil && attempt > 0 && strconv.Itoa(attempt) == request.Header.Get("X-Agent-Memory-Generation-Attempt")
		for _, name := range []string{"X-Agent-Project-ID", "X-Agent-Memory-Generation-ID", "X-Agent-Memory-Generation-Attempt", "X-Agent-Attempt-Token"} {
			valid = valid && len(request.Header.Values(name)) == 1 && request.Header.Get(name) != ""
		}
		for _, name := range []string{"X-Agent-Turn-ID", "X-Agent-Task-Attempt-ID", "X-Agent-Execution-Attempt-ID"} {
			valid = valid && len(request.Header.Values(name)) == 0
		}
		activity.MemoryGenerationAttempt = attempt
		if !valid {
			activity.MemoryGenerationAttempt = -1 // Preserve malformed presence for fail-closed admission.
		}
	}
	return activity, memoryHeaders || activity.ProjectID != "" || activity.AgentTurnID != "" || activity.AgentTaskAttemptID != "" || activity.ExecutionAttemptID != "" || activity.AttemptToken != ""
}

func (s *Server) bindAgentActivity(writer http.ResponseWriter, request *http.Request, transport identity.Principal) (*http.Request, identity.Principal, bool) {
	activity, present := activityHeaders(request)
	if !present {
		return request, transport, true
	}
	if transport.Kind != identity.KindService || s.runtime == nil {
		writeError(writer, http.StatusForbidden, "AGENT_ACTIVITY_FORBIDDEN", "只有内部服务可以引用 Agent 执行身份。")
		return request, identity.Principal{}, false
	}
	if activity.MemoryGenerationAttempt < 0 || (activity.MemoryGenerationID != "" && !memoryActivityRouteAllowed(request)) {
		writeError(writer, http.StatusForbidden, "AGENT_ACTIVITY_FORBIDDEN", "Memory execution headers or endpoint are not admitted.")
		return request, identity.Principal{}, false
	}
	parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	toolCallID := ""
	if len(parts) == 5 && parts[0] == "internal" && parts[1] == "v1" && parts[2] == "agent-tool-calls" {
		toolCallID = parts[3]
		// Finishing a call is allowed after a turn commits or is cancelled. This
		// exception never grants permission to start another tool or read data.
		activity.AllowTerminal = request.Method == http.MethodPost && (parts[4] == "complete" || parts[4] == "failures" || parts[4] == "cancel")
	}
	if request.Method == http.MethodPost && request.URL.Path == "/internal/v1/agent/turn-commits" {
		activity.AllowTerminal = true // The commit handler enforces its durable idempotency/state gate.
	}
	if request.Method == http.MethodPost && (request.URL.Path == "/internal/v1/agent-memory/rollouts" || request.URL.Path == "/internal/v1/agent-memory/rollouts/read" || request.URL.Path == "/internal/v1/agent-memory/archive" || request.URL.Path == "/internal/v1/agent-memory/archive-receipt") {
		// A complete SDK result exists after execution. This only grants access
		// to its private source record; the store rechecks memory revocation.
		activity.AllowTerminal = true
	}
	if activity.MemoryGenerationID != "" {
		activity.AllowTerminal = false
		writer.Header().Set("Cache-Control", "private, no-store")
		if request.Method == http.MethodGet && len(parts) == 4 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "agent-tool-calls" {
			toolCallID = parts[3]
		}
	}
	principal, err := s.runtime.ResolveAgentActivityPrincipal(request.Context(), activity)
	if err != nil {
		writeError(writer, http.StatusForbidden, "AGENT_ACTIVITY_DENIED", "Agent 执行身份已失效或无权访问目标作品。")
		return request, identity.Principal{}, false
	}
	if toolCallID != "" {
		if err := s.runtime.ValidateAgentActivityToolCall(request.Context(), activity, toolCallID); err != nil {
			writeError(writer, http.StatusForbidden, "AGENT_ACTIVITY_SCOPE_MISMATCH", "工具调用不属于当前 Agent 执行。")
			return request, identity.Principal{}, false
		}
	}
	ctx := identity.WithDelegatedUser(request.Context(), principal)
	ctx = businessruntime.WithAgentActivity(ctx, activity)
	return request.WithContext(ctx), principal, true
}

func validateAgentActivityTarget(writer http.ResponseWriter, request *http.Request, projectID, turnID, attemptID, executionID, token string) bool {
	activity, present := businessruntime.AgentActivityFromContext(request.Context())
	if !present {
		return true
	}
	if activity.ProjectID != projectID || activity.AgentTurnID != turnID || activity.AgentTaskAttemptID != attemptID || activity.ExecutionAttemptID != executionID || activity.AttemptToken != token {
		writeError(writer, http.StatusForbidden, "AGENT_ACTIVITY_SCOPE_MISMATCH", "请求内容与当前 Agent 执行不一致。")
		return false
	}
	return true
}
