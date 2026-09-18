package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/agenttool"
)

func TestProgramCallIdentityValidation(t *testing.T) {
	for _, id := range []string{" leading", "trailing ", "program\n", "program\x00id", strings.Repeat("x", 257)} {
		if validateProgramCallID(id) == nil {
			t.Fatalf("accepted invalid program ID %q", id)
		}
	}
	for _, id := range []string{"", "program-1", strings.Repeat("x", 256)} {
		if err := validateProgramCallID(id); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProgramToolOriginPersistsAndCannotBeRebound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "program.db")
	store, err := Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	registry, err := agenttool.Compile(agenttool.Config{SchemaVersion: agenttool.SchemaVersion,
		Programmatic: agenttool.ProgrammaticConfig{Enabled: true, ToolIDs: []string{"runtime:read_workspace_file"}}})
	if err != nil {
		t.Fatal(err)
	}
	store.SetAgentToolRegistry(registry)
	ctx := context.Background()
	project, err := store.CreateProject(ctx, "Program origin")
	if err != nil {
		t.Fatal(err)
	}
	command := BeginAgentToolCallCommand{ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID,
		SDKToolCallID: "program-read-1", ToolID: "runtime:read_workspace_file", ProgramCallID: "program-1",
		ConfigurationHash: toolConfigurationHashForTest(t, store, "runtime:read_workspace_file")}
	call, err := store.BeginAgentToolCall(ctx, command)
	if err != nil || call.ProgramCallID != command.ProgramCallID {
		t.Fatalf("program registration: %+v %v", call, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	store.SetAgentToolRegistry(registry)
	duplicate, err := store.BeginAgentToolCall(ctx, command)
	if err != nil || duplicate.AgentToolCallID != call.AgentToolCallID || duplicate.ProgramCallID != "program-1" {
		t.Fatalf("restored origin: %+v %v", duplicate, err)
	}
	for _, id := range []string{"", "other-program"} {
		command.ProgramCallID = id
		_, err = store.BeginAgentToolCall(ctx, command)
		assertDomainCode(t, err, "AGENT_TOOL_CALL_ID_CONFLICT")
	}
	command.SDKToolCallID = "ungranted-program-read"
	command.ProgramCallID = "program-1"
	command.ToolID = "runtime:list_workspace_files"
	command.ConfigurationHash = toolConfigurationHashForTest(t, store, command.ToolID)
	_, err = store.BeginAgentToolCall(ctx, command)
	assertDomainCode(t, err, "AGENT_TOOL_PROGRAMMATIC_NOT_ALLOWED")
}
