package runtime

import (
	"strings"
	"unicode"

	"content-agent/backend/internal/agenttool"
)

const agentProgramCallSchema = `
CREATE TABLE IF NOT EXISTS agent_program_tool_calls (
	agent_tool_call_id TEXT PRIMARY KEY REFERENCES agent_tool_calls(agent_tool_call_id) ON DELETE CASCADE,
	program_call_id TEXT NOT NULL CHECK(length(program_call_id) BETWEEN 1 AND 256)
);
CREATE TABLE IF NOT EXISTS execution_program_progress (
	attempt_id TEXT PRIMARY KEY REFERENCES execution_attempts(attempt_id) ON DELETE CASCADE,
	status TEXT NOT NULL CHECK(status IN ('running','completed','incomplete'))
);
`

func validateProgramCallID(id string) error {
	if len(id) > 256 || strings.TrimSpace(id) != id || strings.ContainsFunc(id, unicode.IsControl) {
		return domainError("REQUEST_VALIDATION_FAILED", "Invalid program call identity.")
	}
	return nil
}

func validateProgramToolGrant(registry *agenttool.Registry, command BeginAgentToolCallCommand) error {
	if command.ProgramCallID == "" {
		return nil
	}
	for _, id := range registry.PrivateCatalog().ProgrammaticToolIDs {
		if id == command.ToolID {
			return nil
		}
	}
	return domainError("AGENT_TOOL_PROGRAMMATIC_NOT_ALLOWED", "This tool has no programmatic grant in the current workspace.")
}
