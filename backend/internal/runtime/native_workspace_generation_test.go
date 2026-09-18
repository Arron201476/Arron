package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"content-agent/backend/internal/identity"
)

func TestNativeGenerationInputsRequireExactActiveConsolidation(t *testing.T) {
	store, job, original := queuedMemoryGenerationFixture(t, "conversation")
	worker := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
	claim, err := store.ClaimAgentMemoryGeneration(worker, "worker", "model", 60)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if _, err := store.StartAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt); err != nil {
		t.Fatal(err)
	}
	extractionContext := WithAgentActivity(worker, AgentActivityIdentity{ProjectID: job.ProjectID, MemoryGenerationID: job.GenerationID, MemoryGenerationAttempt: claim.Job.Attempt, AttemptToken: claim.AttemptToken})
	firstWorkspace, err := store.ReserveNativeWorkspace(extractionContext, ReserveNativeWorkspaceCommand{HolderKey: sha256Hex([]byte("extract-holder"))})
	if err != nil {
		t.Fatal(err)
	}
	output := json.RawMessage(`{"rollout_slug":"fixture","rollout_summary":"summary","raw_memory":"private"}`)
	if _, err := store.CompleteAgentMemoryExtraction(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt, output); err != nil {
		t.Fatal(err)
	}
	next, err := store.ClaimAgentMemoryGeneration(worker, "worker", "model", 60)
	if err != nil || next == nil {
		t.Fatal(err)
	}
	if _, err := store.StartAgentMemoryGeneration(worker, job.GenerationID, "worker", next.AttemptToken, next.Job.Attempt); err != nil {
		t.Fatal(err)
	}
	ctx := WithAgentActivity(worker, AgentActivityIdentity{ProjectID: job.ProjectID, MemoryGenerationID: job.GenerationID, MemoryGenerationAttempt: next.Job.Attempt, AttemptToken: next.AttemptToken})
	if current, err := store.CurrentNativeWorkspaceLease(ctx, 0); err != nil || current != nil {
		t.Fatal("consolidation inherited extraction workspace")
	}
	secondWorkspace, err := store.ReserveNativeWorkspace(ctx, ReserveNativeWorkspaceCommand{HolderKey: sha256Hex([]byte("consolidate-holder"))})
	if err != nil || secondWorkspace.SessionID == firstWorkspace.SessionID {
		t.Fatalf("phase workspace isolation: %v", err)
	}
	if current, err := store.CurrentNativeWorkspaceLease(ctx, 0); err != nil || current == nil || current.SessionID != secondWorkspace.SessionID {
		t.Fatal("same-phase workspace was not retained")
	}
	source := NativeWorkspaceGenerationSource{GenerationID: job.GenerationID, SourceHash: job.SourceHash, ExtractionHash: sha256Hex(output)}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	inputs, err := store.nativeGenerationInputsTx(ctx, tx, job.ProjectID, source)
	if err != nil || len(inputs) != 2 || inputs["extraction.json"] != string(output) || inputs["rollout.jsonl"] != next.Source.RolloutJSONL {
		t.Fatalf("inputs: %v", err)
	}
	for _, other := range []context.Context{worker, original, WithAgentActivity(worker, AgentActivityIdentity{ProjectID: job.ProjectID, MemoryGenerationID: job.GenerationID, MemoryGenerationAttempt: claim.Job.Attempt, AttemptToken: claim.AttemptToken})} {
		if _, err := store.nativeGenerationInputsTx(other, tx, job.ProjectID, source); err == nil {
			t.Fatal("foreign or stale identity read generation inputs")
		}
	}
	wrong := source
	wrong.ExtractionHash = sha256Hex(nil)
	if _, err := store.nativeGenerationInputsTx(ctx, tx, job.ProjectID, wrong); err == nil {
		t.Fatal("changed extraction accepted")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE native_workspace_leases SET activity_key=? WHERE session_id=?`, "memory:"+job.GenerationID, firstWorkspace.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CurrentNativeWorkspaceLease(ctx, 0); err == nil {
		t.Fatal("legacy phase-less workspace was silently replaced")
	}
}
