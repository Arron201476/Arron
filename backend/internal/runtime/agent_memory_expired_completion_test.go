package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"content-agent/backend/internal/identity"
)

func TestExpiredMemoryCompletionRequiresSettledOriginalPublication(t *testing.T) {
	for _, outcome := range []string{"completed", "inflight", "cancelled", "edited"} {
		t.Run(outcome, func(t *testing.T) {
			store, job, _ := queuedMemoryGenerationFixture(t, "conversation")
			worker := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
			claim, err := store.ClaimAgentMemoryGeneration(worker, "worker", "model", 60)
			if err != nil || claim == nil {
				t.Fatalf("claim: %v", err)
			}
			if _, err := store.StartAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt); err != nil {
				t.Fatal(err)
			}
			// This fixture isolates terminal recovery; SDK input preparation is
			// covered separately. Publication and tool approval use production APIs.
			selection := `{"version":1,"updated_at":"2026-09-14T00:00:00Z","selected":[]}`
			plan, err := json.Marshal(AgentMemoryInputPlan{Files: map[string]string{nativeGenerationDirectory + "/phase_two_selection.json": selection}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`UPDATE agent_memory_generations SET phase='consolidation',input_plan_json=?,input_plan_hash=? WHERE generation_id=?`, string(plan), sha256Hex(plan), job.GenerationID); err != nil {
				t.Fatal(err)
			}
			ctx := WithAgentActivity(worker, AgentActivityIdentity{ProjectID: job.ProjectID, MemoryGenerationID: job.GenerationID,
				MemoryGenerationAttempt: claim.Job.Attempt, AttemptToken: claim.AttemptToken})
			if _, err := store.ResolveAgentMemorySnapshot(ctx); err != nil {
				t.Fatal(err)
			}
			holder := sha256Hex([]byte("completion-holder"))
			lease, err := store.ReserveNativeWorkspace(ctx, ReserveNativeWorkspaceCommand{HolderKey: holder})
			if err != nil {
				t.Fatal(err)
			}
			access := NativeWorkspaceAccess{SessionID: lease.SessionID, Generation: lease.Generation, HolderKey: holder}
			files := map[string]string{nativeMemoryDirectory + "/memory_summary.md": "confirmed private memory", nativeMemoryDirectory + "/phase_two_selection.json": selection}
			var archive bytes.Buffer
			writer := tar.NewWriter(&archive)
			names := []string{nativeMemoryDirectory + "/memory_summary.md", nativeMemoryDirectory + "/phase_two_selection.json"}
			for _, name := range names {
				if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(files[name]))}); err != nil {
					t.Fatal(err)
				}
				if _, err := writer.Write([]byte(files[name])); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			saved, err := store.SaveNativeWorkspaceSnapshot(ctx, access, "published-memory", 0, archive.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			args := AgentMemoryPublicationArguments{Snapshot: NativeWorkspaceSnapshotReference{Version: saved.Version, SHA256: saved.Snapshot.SHA256}, ExpectedVersion: job.BaseVersion, Files: names}
			raw, err := json.Marshal(args)
			if err != nil {
				t.Fatal(err)
			}
			call, err := store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{ProjectID: job.ProjectID, ConversationID: claim.ConversationID,
				MemoryGenerationID: job.GenerationID, MemoryGenerationAttempt: claim.Job.Attempt, AttemptToken: claim.AttemptToken,
				SDKToolCallID: "publish-before-loss", ToolID: publishAgentMemoryTool, Arguments: raw})
			if err != nil {
				t.Fatal(err)
			}
			proposal, err := store.GetAgentMemoryProposal(mcpOwnerContext(), call.AgentToolCallID)
			if err != nil || !proposal.Available || !proposal.CanApprove || !proposal.CanReject || len(proposal.Files) == 0 {
				t.Fatalf("consolidation candidate unavailable: %+v %v", proposal, err)
			}
			if _, err := store.db.Exec(`UPDATE native_workspace_leases SET activity_key=? WHERE session_id=?`, "memory:"+job.GenerationID+":extraction", lease.SessionID); err != nil {
				t.Fatal(err)
			}
			wrongPhase, err := store.GetAgentMemoryProposal(mcpOwnerContext(), call.AgentToolCallID)
			if err != nil || wrongPhase.Available || wrongPhase.CanApprove || !wrongPhase.CanReject || len(wrongPhase.Files) != 0 {
				t.Fatalf("foreign phase candidate exposed: %+v %v", wrongPhase, err)
			}
			_, err = store.ResolveAgentToolApproval(mcpOwnerContext(), ResolveAgentToolApprovalCommand{AgentToolApprovalID: call.Approval.AgentToolApprovalID,
				ExpectedVersion: call.Approval.Version, SubjectSnapshotHash: call.Approval.SubjectSnapshotHash, Action: "approve"})
			assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
			if _, err := store.db.Exec(`UPDATE native_workspace_leases SET activity_key=? WHERE session_id=?`, "memory:"+job.GenerationID+":consolidation", lease.SessionID); err != nil {
				t.Fatal(err)
			}
			// Cancelled generations cannot expose or approve an otherwise intact
			// candidate. Restore this fixture to exercise its original happy path.
			if _, err := store.db.Exec(`UPDATE agent_memory_generations SET status='cancelled' WHERE generation_id=?`, job.GenerationID); err != nil {
				t.Fatal(err)
			}
			cancelledProposal, err := store.GetAgentMemoryProposal(mcpOwnerContext(), call.AgentToolCallID)
			if err != nil || cancelledProposal.Available || cancelledProposal.CanApprove || !cancelledProposal.CanReject || len(cancelledProposal.Files) != 0 {
				t.Fatalf("cancelled generation exposed candidate: %+v %v", cancelledProposal, err)
			}
			_, err = store.ResolveAgentToolApproval(mcpOwnerContext(), ResolveAgentToolApprovalCommand{AgentToolApprovalID: call.Approval.AgentToolApprovalID,
				ExpectedVersion: call.Approval.Version, SubjectSnapshotHash: call.Approval.SubjectSnapshotHash, Action: "approve"})
			assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
			if _, err := store.db.Exec(`UPDATE agent_memory_generations SET status='running' WHERE generation_id=?`, job.GenerationID); err != nil {
				t.Fatal(err)
			}
			restoredProposal, err := store.GetAgentMemoryProposal(mcpOwnerContext(), call.AgentToolCallID)
			if err != nil || !restoredProposal.CanApprove || restoredProposal.ApprovalVersion != proposal.ApprovalVersion || restoredProposal.SubjectSnapshotHash != proposal.SubjectSnapshotHash {
				t.Fatalf("rejected approval changed original subject: %+v %v", restoredProposal, err)
			}
			if _, err := store.ResolveAgentToolApproval(mcpOwnerContext(), ResolveAgentToolApprovalCommand{AgentToolApprovalID: call.Approval.AgentToolApprovalID,
				ExpectedVersion: call.Approval.Version, SubjectSnapshotHash: call.Approval.SubjectSnapshotHash, Action: "approve"}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID}); err != nil {
				t.Fatal(err)
			}
			manager := nativeFileManager(t, store, &nativeFileEngineFixture{nativeEngineFixture: newNativeEngineFixture(t)})
			publication, err := manager.PublishMemory(ctx, access, AgentMemoryPublicationRequest{AgentToolCallID: call.AgentToolCallID, SDKToolCallID: call.SDKToolCallID, Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			if outcome != "inflight" {
				result, err := json.Marshal(publication)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.CompleteAgentToolCall(ctx, CompleteAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, Result: result}); err != nil {
					t.Fatal(err)
				}
			}
			if outcome == "edited" {
				if _, err := store.UpdateAgentMemory(mcpOwnerContext(), UpdateAgentMemoryCommand{ProjectID: job.ProjectID, ExpectedVersion: publication.Version,
					Files: map[string]string{"memory_summary.md": "later user edit"}, Enabled: true, RequestID: "after-publication"}); err != nil {
					t.Fatal(err)
				}
			}
			status := "running"
			if outcome == "cancelled" {
				status = "cancelled"
			}
			if _, err := store.db.Exec(`UPDATE agent_memory_generations SET status=?,lease_until='2000-01-01T00:00:00.000000000Z' WHERE generation_id=?`, status, job.GenerationID); err != nil {
				t.Fatal(err)
			}
			next, err := store.ClaimAgentMemoryGeneration(worker, "replacement", "model", 60)
			if err != nil || next != nil {
				t.Fatalf("must not replay published work: %+v %v", next, err)
			}
			actual, err := store.GetAgentMemoryGeneration(mcpOwnerContext(), job.ProjectID, job.GenerationID)
			if err != nil {
				t.Fatal(err)
			}
			expected := outcome
			if outcome == "inflight" || outcome == "edited" {
				expected = "paused"
			}
			if actual.Status != expected || actual.Attempt != claim.Job.Attempt {
				t.Fatalf("unexpected terminal recovery: %+v", actual)
			}
			if outcome == "completed" {
				again, err := store.CompleteAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt)
				if err != nil || again.Status != "completed" || again.Revision != actual.Revision || again.LeaseUntil != "" {
					t.Fatalf("exact completion retry: %+v %v", again, err)
				}
			}
		})
	}
}

func TestExpiredMemoryCompletionCannotAdoptMissingOrUserPublication(t *testing.T) {
	for _, state := range []string{"unpublished", "user-edit", "cancelled", "paused", "live"} {
		t.Run(state, func(t *testing.T) {
			store, job, _ := queuedMemoryGenerationFixture(t, "conversation")
			worker := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
			claim, err := store.ClaimAgentMemoryGeneration(worker, "worker", "model", 60)
			if err != nil || claim == nil {
				t.Fatalf("claim: %v", err)
			}
			if _, err := store.StartAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt); err != nil {
				t.Fatal(err)
			}
			status, lease := "running", "2000-01-01T00:00:00.000000000Z"
			if state == "paused" || state == "cancelled" {
				status = state
			}
			if state == "live" {
				lease = "2099-01-01T00:00:00.000000000Z"
			}
			if _, err := store.db.Exec(`UPDATE agent_memory_generations SET phase='consolidation',status=?,lease_until=? WHERE generation_id=?`, status, lease, job.GenerationID); err != nil {
				t.Fatal(err)
			}
			if state == "user-edit" {
				if _, err := store.UpdateAgentMemory(mcpOwnerContext(), UpdateAgentMemoryCommand{ProjectID: job.ProjectID, ExpectedVersion: job.BaseVersion,
					Files: map[string]string{"memory_summary.md": "user-authored update"}, Enabled: true, RequestID: "user-not-generation"}); err != nil {
					t.Fatal(err)
				}
			}
			next, err := store.ClaimAgentMemoryGeneration(worker, "replacement", "model", 60)
			if err != nil || next != nil {
				t.Fatalf("must not replay: %+v %v", next, err)
			}
			actual, err := store.GetAgentMemoryGeneration(mcpOwnerContext(), job.ProjectID, job.GenerationID)
			if err != nil {
				t.Fatal(err)
			}
			expected := status
			if state == "unpublished" || state == "user-edit" {
				expected = "paused"
				if actual.ErrorCode != "MEMORY_GENERATION_LEASE_LOST" {
					t.Fatalf("lost lease not preserved: %+v", actual)
				}
			}
			if actual.Status != expected || actual.Attempt != claim.Job.Attempt {
				t.Fatalf("unexpected recovery: %+v", actual)
			}
		})
	}
}
