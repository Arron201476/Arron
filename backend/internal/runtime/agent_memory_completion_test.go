package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"content-agent/backend/internal/identity"
)

func TestMemoryPublicationRequiresExactPlannedSelection(t *testing.T) {
	store, job, _ := queuedMemoryGenerationFixture(t, "conversation")
	selection := `{"version":1,"updated_at":"2026-09-14T00:00:00Z","selected":[]}`
	plan, err := json.Marshal(AgentMemoryInputPlan{Files: map[string]string{nativeGenerationDirectory + "/phase_two_selection.json": selection}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE agent_memory_generations SET phase='consolidation',input_plan_json=?,input_plan_hash=? WHERE generation_id=?`, string(plan), sha256Hex(plan), job.GenerationID); err != nil {
		t.Fatal(err)
	}
	ctx := WithAgentActivity(context.Background(), AgentActivityIdentity{MemoryGenerationID: job.GenerationID})
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, candidate := range []string{"", "changed", selection} {
		err := validateMemoryGenerationSelectionTx(ctx, tx, map[string]string{"phase_two_selection.json": candidate})
		if candidate == selection {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
		}
	}
}

func TestMemoryCompletionCannotUseModelOutputOrDelegatedAuthority(t *testing.T) {
	store, job, original := queuedMemoryGenerationFixture(t, "conversation")
	worker := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
	claim, err := store.ClaimAgentMemoryGeneration(worker, "worker", "model", 60)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v", err)
	}
	started, err := store.StartAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CompleteAgentMemoryGeneration(original, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt)
	assertDomainCode(t, err, "AGENT_ACTIVITY_FORBIDDEN")
	_, err = store.CompleteAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt)
	assertDomainCode(t, err, "AGENT_ACTIVITY_STALE")
	if _, err := store.db.Exec(`UPDATE agent_memory_generations SET phase='consolidation' WHERE generation_id=?`, job.GenerationID); err != nil {
		t.Fatal(err)
	}
	_, err = store.CompleteAgentMemoryGeneration(worker, job.GenerationID, "worker", "wrong", claim.Job.Attempt)
	assertDomainCode(t, err, "AGENT_ACTIVITY_STALE")
	_, err = store.CompleteAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt)
	assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
	var status string
	var revision int
	if err := store.db.QueryRow(`SELECT status,revision FROM agent_memory_generations WHERE generation_id=?`, job.GenerationID).Scan(&status, &revision); err != nil {
		t.Fatal(err)
	}
	if status != "running" || revision != started.Revision {
		t.Fatal("unpublished completion changed generation state")
	}
}
