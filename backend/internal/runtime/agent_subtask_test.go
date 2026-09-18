package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/capability"
)

func TestSubtaskRequestValidation(t *testing.T) {
	for _, raw := range []string{`{}`, `[]`, `{"title":"x","task":"x"}`, `{"title":"x","task":"x","materials":null}`,
		`{"title":" ","task":"x","materials":""}`, `{"title":"x","task":2,"materials":""}`,
		`{"title":"x","task":"x","materials":"","extra":true}`, `{"title":"x","task":"x","materials":"\u0000"}`} {
		assertDomainCode(t, validateSubtaskRequest(json.RawMessage(raw)), "REQUEST_VALIDATION_FAILED")
	}
	for key, limit := range map[string]int{"title": 120, "task": 16000, "materials": 64000} {
		values := map[string]string{"title": "Review", "task": "Analyze", "materials": ""}
		values[key] = strings.Repeat("x", limit)
		raw, _ := json.Marshal(values)
		if err := validateSubtaskRequest(raw); err != nil {
			t.Fatal(err)
		}
		values[key] += "x"
		raw, _ = json.Marshal(values)
		assertDomainCode(t, validateSubtaskRequest(raw), "REQUEST_VALIDATION_FAILED")
	}
}

func TestSubtaskBudgetIsAtomicDurableAndIdempotentAcrossModes(t *testing.T) {
	for _, mode := range []string{"conversation", "background_task", "stateful_workflow"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			var store *Store
			var path string
			var registry *capability.Registry
			command := BeginAgentToolCallCommand{ToolID: delegateSubtaskTool, Arguments: json.RawMessage(`{"title":"Review","task":"Analyze supplied material","materials":"fixture"}`)}
			switch mode {
			case "conversation":
				var project Project
				var turn AgentTurn
				store, project, turn, path = pauseTurnFixture(t)
				claimInputTurn(t, store, turn.AgentTurnID)
				command.ProjectID, command.ConversationID, command.AgentTurnID = project.ProjectID, project.PrimaryConversationID, turn.AgentTurnID
				registry = loadTestRegistry(t)
			case "background_task":
				var task AgentTask
				var request ClaimAgentTaskCommand
				store, task, request = pausedBackgroundStore(t)
				claim, err := store.ClaimAgentTask(ctx, request)
				if err != nil || claim == nil {
					t.Fatal(err)
				}
				command.ProjectID, command.ConversationID, command.SkillInvocationID = task.ProjectID, task.ConversationID, task.SkillInvocationID
				command.AgentTaskAttemptID, command.AttemptToken = claim.Attempt.AgentTaskAttemptID, claim.AttemptToken
				path, registry = filepath.Join(store.dataRoot, "pause.db"), loadBackgroundTaskRegistry(t)
			case "stateful_workflow":
				path = filepath.Join(t.TempDir(), "subtasks.db")
				store = openProjectFilesTestStore(t, path)
				store.SetAgentToolRegistry(agentToolRegistryForTest(t))
				project, claim := statefulToolClaimForTest(t, store)
				command = statefulToolCommand(project, claim, delegateSubtaskTool, "", command.Arguments)
				registry = loadTestRegistry(t)
			}
			defer func() { store.Close() }()
			var first AgentToolCall
			for index := range maxExecutionSubtasks - 1 {
				command.SDKToolCallID = fmt.Sprintf("subtask-%d", index)
				call, err := store.BeginAgentToolCall(ctx, command)
				if err != nil {
					t.Fatal(err)
				}
				if index == 0 {
					first = call
				}
			}
			type outcome struct {
				call AgentToolCall
				err  error
			}
			results := make(chan outcome, 2)
			for index := range 2 {
				candidate := command
				candidate.SDKToolCallID = fmt.Sprintf("last-slot-%d", index)
				go func() { call, err := store.BeginAgentToolCall(ctx, candidate); results <- outcome{call, err} }()
			}
			accepted, rejected := 0, 0
			for range 2 {
				result := <-results
				if result.err == nil {
					accepted++
				} else {
					assertDomainCode(t, result.err, "AGENT_SUBTASK_LIMIT")
					rejected++
				}
			}
			if accepted != 1 || rejected != 1 {
				t.Fatalf("last slot accepted=%d rejected=%d", accepted, rejected)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			var err error
			store, err = Open(path, registry)
			if err != nil {
				t.Fatal(err)
			}
			store.SetAgentToolRegistry(agentToolRegistryForTest(t))
			command.SDKToolCallID = "subtask-0"
			replayed, err := store.BeginAgentToolCall(ctx, command)
			if err != nil || replayed.AgentToolCallID != first.AgentToolCallID {
				t.Fatal("lost acknowledgement must keep the original subtask", err)
			}
			command.SDKToolCallID = "after-rebuild"
			_, err = store.BeginAgentToolCall(ctx, command)
			assertDomainCode(t, err, "AGENT_SUBTASK_LIMIT")
		})
	}
}
