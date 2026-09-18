package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestStatefulWorkerFailureCodesPersistWithoutRetry(t *testing.T) {
	for code, stage := range map[string]string{
		"PROVIDER_CONTENT_POLICY_BLOCKED":  "provider_call",
		"SDK_CHECKPOINT_FAILED":            "run_state_checkpoint",
		"SDK_EXECUTION_STATE_INVALID":      "run_state_restore",
		"SDK_TOOL_REPLAY_RISK":             "tool_execution",
		"AGENT_TOOL_CONFIGURATION_INVALID": "tool_execution",
		"AGENT_INPUT_GUARDRAIL_REJECTED":   "input_validation",
		"AGENT_OUTPUT_GUARDRAIL_REJECTED":  "output_validation",
	} {
		t.Run(code, func(t *testing.T) {
			ctx := context.Background()
			database := filepath.Join(t.TempDir(), "failure.db")
			store := openProjectFilesTestStore(t, database)
			defer func() { store.Close() }()
			_, claim := statefulToolClaimForTest(t, store)
			command := FailExecutionAttemptCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken,
				InputSnapshotHash: claim.Attempt.InputSnapshotHash, ErrorCode: code,
				FailureDetail: &ExecutionFailureDetail{Stage: stage, Summary: "Worker stopped before replay", Retryable: true}}
			failed, err := store.FailExecutionAttempt(ctx, command)
			if err != nil || failed.Retryable || failed.Attempt.Status != "failed" || failed.Task.Status != "failed" {
				t.Fatalf("Worker failure was not accepted as terminal: %+v %v", failed, err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store = openProjectFilesTestStore(t, database)
			if _, err := store.FailExecutionAttempt(ctx, command); err != nil {
				t.Fatal(err)
			}
			var storedStage string
			var retryable bool
			if err := store.db.QueryRow(`SELECT stage, retryable FROM execution_failure_details WHERE attempt_id = ?`, claim.Attempt.AttemptID).Scan(&storedStage, &retryable); err != nil || storedStage != stage || retryable {
				t.Fatalf("failure detail lost: %q %v %v", storedStage, retryable, err)
			}
			if next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim)); err != nil || next != nil {
				t.Fatalf("nonretryable Worker failure replayed: %+v %v", next, err)
			}
		})
	}
}

func TestStatefulManualRetryRespectsDurableToolSideEffects(t *testing.T) {
	for _, mode := range []string{"no-tools", "read", "write-pending", "write-approved", "write-rejected", "write-running", "write-completed", "write-failed", "checkpoint-approved", "checkpoint-rejected"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			database := filepath.Join(t.TempDir(), "retry.db")
			store := openProjectFilesTestStore(t, database)
			defer func() { store.Close() }()
			store.SetAgentToolRegistry(agentToolRegistryForTest(t))
			project, claim := statefulToolClaimForTest(t, store)
			blocked := mode == "write-running" || mode == "write-completed" || mode == "write-failed" || strings.HasPrefix(mode, "checkpoint-")
			if mode == "read" {
				call, err := store.BeginAgentToolCall(ctx, statefulToolCommand(project, claim, "runtime:inspect_project", "read", json.RawMessage(`{}`)))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.CompleteAgentToolCall(ctx, CompleteAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, Result: json.RawMessage(`{}`)}); err != nil {
					t.Fatal(err)
				}
			} else if mode != "no-tools" {
				call := statefulApprovalForTest(t, store, project, claim, "write")
				if strings.HasPrefix(mode, "checkpoint-") {
					if _, err := store.PauseExecutionForApproval(ctx, statefulPauseForTest(claim, call.SDKToolCallID)); err != nil {
						t.Fatal(err)
					}
				}
				if mode != "write-pending" {
					action := "approve"
					if strings.HasSuffix(mode, "rejected") {
						action = "reject"
					}
					resolveBackgroundApprovalForTest(t, store, call, action)
				}
				if strings.HasPrefix(mode, "checkpoint-") {
					resumed, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim))
					if err != nil || resumed == nil {
						t.Fatalf("resume: %+v %v", resumed, err)
					}
					claim = resumed
				} else if blocked {
					if _, err := store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID,
						ExpectedSDKToolCallID: call.SDKToolCallID, ConfigurationHash: toolConfigurationHashForTest(t, store, call.ToolID)}); err != nil {
						t.Fatal(err)
					}
					if mode == "write-completed" {
						command := CompleteAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, Result: json.RawMessage(`{"saved":true}`)}
						if _, err := store.CompleteAgentToolCall(ctx, command); err != nil {
							t.Fatal(err)
						}
					} else if mode == "write-failed" {
						if _, err := store.FailAgentToolCall(ctx, FailAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID,
							ErrorCode: "EXTERNAL_ACK_LOST", ErrorMessage: "External acknowledgement lost"}); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			failed, err := store.FailExecutionAttempt(ctx, FailExecutionAttemptCommand{AttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken,
				InputSnapshotHash: claim.Attempt.InputSnapshotHash, ErrorCode: "PROVIDER_RESPONSE_INVALID"})
			if err != nil || failed.Task.Status != "failed" {
				t.Fatalf("failure: %+v %v", failed, err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store = openProjectFilesTestStore(t, database)
			store.SetAgentToolRegistry(agentToolRegistryForTest(t))
			snapshot, err := store.GetRunSnapshot(ctx, claim.Attempt.RunID)
			if err != nil {
				t.Fatal(err)
			}
			offered := slices.ContainsFunc(snapshot.AvailableActions, func(action AvailableAction) bool { return action.ActionID == "retry_failed_step" && action.Enabled })
			if offered == blocked {
				t.Errorf("retry action disagrees with durable write/checkpoint risk: offered=%v blocked=%v", offered, blocked)
			}
			retried, err := store.RetryFailedStep(ctx, RetryFailedStepCommand{StepRunID: claim.Attempt.StepRunID})
			if blocked {
				assertDomainCode(t, err, "SDK_TOOL_REPLAY_RISK")
				if next, err := store.ClaimExecutionTask(ctx, statefulResumeCommand(claim)); err != nil || next != nil {
					t.Fatalf("manual retry replayed a tool: %+v %v", next, err)
				}
			} else if err != nil || retried.Run.Status != "running" {
				t.Fatalf("safe explicit retry blocked: %+v %v", retried, err)
			}
		})
	}
}
