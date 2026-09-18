package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"content-agent/backend/internal/identity"
)

func TestMemoryExplicitPauseWinsApprovalCheckpointRace(t *testing.T) {
	for _, requestFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "checkpoint-first", true: "request-first"}[requestFirst], func(t *testing.T) {
			store, job, _ := queuedMemoryGenerationFixture(t, "conversation")
			store.SetAgentToolRegistry(agentToolRegistryForTest(t))
			worker := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
			claim, err := store.ClaimAgentMemoryGeneration(worker, "worker", "model", 60)
			if err != nil || claim == nil {
				t.Fatalf("claim: %v", err)
			}
			if _, err := store.StartAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt); err != nil {
				t.Fatal(err)
			}
			ctx := WithAgentActivity(worker, AgentActivityIdentity{ProjectID: job.ProjectID, MemoryGenerationID: job.GenerationID, MemoryGenerationAttempt: claim.Job.Attempt, AttemptToken: claim.AttemptToken})
			call, err := store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{ProjectID: job.ProjectID, ConversationID: claim.ConversationID,
				MemoryGenerationID: job.GenerationID, MemoryGenerationAttempt: claim.Job.Attempt, AttemptToken: claim.AttemptToken,
				SDKToolCallID: "paused-operation", ToolID: nativeWorkspaceExecTool, Arguments: json.RawMessage(`{"command":"echo test"}`)})
			if err != nil {
				t.Fatal(err)
			}
			requestPause := func() {
				current, err := store.GetAgentMemoryGeneration(mcpOwnerContext(), job.ProjectID, job.GenerationID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.ControlAgentMemoryGeneration(mcpOwnerContext(), job.ProjectID, job.GenerationID, "pause", current.Revision); err != nil {
					t.Fatal(err)
				}
			}
			if requestFirst {
				requestPause()
			}
			checkpoint := json.RawMessage(`{"pause_kind":"approval","sdk":"paused"}`)
			paused, err := store.PauseAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt, claim.Job.CheckpointHash, checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			if requestFirst {
				again, err := store.PauseAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt, claim.Job.CheckpointHash, checkpoint)
				if err != nil || again.Revision != paused.Revision || again.ErrorCode != "MEMORY_GENERATION_USER_PAUSED" {
					t.Fatalf("retry: %+v %v", again, err)
				}
			} else {
				requestPause()
			}
			if _, err := store.ResolveAgentToolApproval(mcpOwnerContext(), ResolveAgentToolApprovalCommand{AgentToolApprovalID: call.Approval.AgentToolApprovalID, ExpectedVersion: call.Approval.Version, SubjectSnapshotHash: call.Approval.SubjectSnapshotHash, Action: "reject"}); err != nil {
				t.Fatal(err)
			}
			next, err := store.ClaimAgentMemoryGeneration(worker, "next", "model", 60)
			if err != nil || next != nil {
				t.Fatalf("user pause automatically resumed: %+v %v", next, err)
			}
		})
	}
}

func TestMemoryApprovalDecisionsResumeOnlyAfterCheckpointAndAllDecisions(t *testing.T) {
	for _, early := range []bool{false, true} {
		t.Run(map[bool]string{false: "pause-first", true: "approval-first"}[early], func(t *testing.T) {
			store, job, _ := queuedMemoryGenerationFixture(t, "conversation")
			store.SetAgentToolRegistry(agentToolRegistryForTest(t))
			worker := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
			claim, err := store.ClaimAgentMemoryGeneration(worker, "worker", "model", 60)
			if err != nil || claim == nil {
				t.Fatalf("claim: %v", err)
			}
			if _, err := store.StartAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt); err != nil {
				t.Fatal(err)
			}
			ctx := WithAgentActivity(worker, AgentActivityIdentity{ProjectID: job.ProjectID, MemoryGenerationID: job.GenerationID, MemoryGenerationAttempt: claim.Job.Attempt, AttemptToken: claim.AttemptToken})
			calls := []AgentToolCall{}
			for _, sdkID := range []string{"first", "second"} {
				call, err := store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{ProjectID: job.ProjectID, ConversationID: claim.ConversationID,
					MemoryGenerationID: job.GenerationID, MemoryGenerationAttempt: claim.Job.Attempt, AttemptToken: claim.AttemptToken,
					SDKToolCallID: sdkID, ToolID: nativeWorkspaceExecTool, Arguments: json.RawMessage(`{"command":"echo test"}`)})
				if err != nil {
					t.Fatal(err)
				}
				calls = append(calls, call)
			}
			pause := func() {
				_, err := store.PauseAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt, claim.Job.CheckpointHash, json.RawMessage(`{"sdk":"paused"}`))
				if err != nil {
					t.Fatal(err)
				}
			}
			if !early {
				pause()
			}
			for _, call := range calls {
				_, err := store.ResolveAgentToolApproval(mcpOwnerContext(), ResolveAgentToolApprovalCommand{AgentToolApprovalID: call.Approval.AgentToolApprovalID, ExpectedVersion: call.Approval.Version, SubjectSnapshotHash: call.Approval.SubjectSnapshotHash, Action: "reject"})
				if err != nil {
					t.Fatal(err)
				}
				if call.SDKToolCallID == "first" {
					next, err := store.ClaimAgentMemoryGeneration(worker, "next", "model", 60)
					if err != nil || next != nil {
						t.Fatalf("premature resume: %+v %v", next, err)
					}
				}
			}
			if early {
				pause()
			}
			next, err := store.ClaimAgentMemoryGeneration(worker, "next", "model", 60)
			if err != nil || next == nil || next.Job.Attempt != claim.Job.Attempt+1 || next.AttemptToken == claim.AttemptToken {
				t.Fatalf("resume: %+v %v", next, err)
			}
		})
	}
}
