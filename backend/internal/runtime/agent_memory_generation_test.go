package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"content-agent/backend/internal/identity"
)

func queuedMemoryGenerationFixture(t *testing.T, mode string) (*Store, AgentMemoryGeneration, context.Context) {
	t.Helper()
	store, _, ctx, _ := nativeWorkspaceFixture(t, mode)
	snapshot, err := store.ResolveAgentMemorySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	source := memoryRolloutFixture(snapshot)
	if _, err := store.SaveAgentMemoryRollout(ctx, source); err != nil {
		t.Fatal(err)
	}
	job, err := store.QueueAgentMemoryGeneration(mcpOwnerContext(), source.ProjectID, source.ActivityKey, source.SegmentID, source.ContentHash)
	if err != nil || job.Status != "queued" || job.Phase != "extraction" || job.Revision != 1 {
		t.Fatalf("queue: %+v %v", job, err)
	}
	again, err := store.QueueAgentMemoryGeneration(mcpOwnerContext(), source.ProjectID, source.ActivityKey, source.SegmentID, source.ContentHash)
	if err != nil || again.GenerationID != job.GenerationID {
		t.Fatalf("duplicate generation: %+v %v", again, err)
	}
	return store, job, ctx
}

func TestMemoryPauseAtomicallySavesCheckpointAndFencesWorker(t *testing.T) {
	store, job, _ := queuedMemoryGenerationFixture(t, "conversation")
	worker := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
	claim, err := store.ClaimAgentMemoryGeneration(worker, "worker", "model", 60)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := store.StartAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt); err != nil {
		t.Fatal(err)
	}
	checkpoint := json.RawMessage(`{"sdk":"interrupted"}`)
	paused, err := store.PauseAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt, claim.Job.CheckpointHash, checkpoint)
	if err != nil || paused.Status != "paused" || paused.LeaseUntil != "" || paused.Checkpoint != string(checkpoint) {
		t.Fatalf("pause: %+v %v", paused, err)
	}
	again, err := store.PauseAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt, claim.Job.CheckpointHash, checkpoint)
	if err != nil || again.Revision != paused.Revision {
		t.Fatalf("lost pause receipt: %+v %v", again, err)
	}
	_, err = store.RenewAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt, 60)
	assertDomainCode(t, err, "AGENT_ACTIVITY_STALE")
	_, err = store.StartAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt)
	assertDomainCode(t, err, "AGENT_ACTIVITY_STALE")
	_, err = store.PauseAgentMemoryGeneration(worker, job.GenerationID, "worker", "wrong", claim.Job.Attempt, claim.Job.CheckpointHash, checkpoint)
	assertDomainCode(t, err, "AGENT_ACTIVITY_STALE")
	_, err = store.ControlAgentMemoryGeneration(mcpOwnerContext(), job.ProjectID, job.GenerationID, "resume", paused.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.PauseAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt, claim.Job.CheckpointHash, checkpoint)
	assertDomainCode(t, err, "AGENT_ACTIVITY_STALE")
	next, err := store.ClaimAgentMemoryGeneration(worker, "worker", "model", 60)
	if err != nil || next == nil || next.Job.Attempt != claim.Job.Attempt+1 || next.AttemptToken == claim.AttemptToken || string(next.Checkpoint) != string(checkpoint) {
		t.Fatalf("resume claim: %+v %v", next, err)
	}
}

func TestMemoryUserPauseRetainsReasonAcrossReceiptRetry(t *testing.T) {
	store, job, _ := queuedMemoryGenerationFixture(t, "conversation")
	worker := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
	claim, err := store.ClaimAgentMemoryGeneration(worker, "worker", "model", 60)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := store.StartAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt); err != nil {
		t.Fatal(err)
	}
	_, err = store.PauseAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt, claim.Job.CheckpointHash, json.RawMessage(`{"pause_kind":"unexpected"}`))
	assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
	checkpoint := json.RawMessage(`{"sdk":"turn-boundary","pause_kind":"user"}`)
	paused, err := store.PauseAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt, claim.Job.CheckpointHash, checkpoint)
	if err != nil || paused.ErrorCode != "MEMORY_GENERATION_USER_PAUSED" || paused.Status != "paused" || paused.LeaseUntil != "" {
		t.Fatalf("user pause: %+v %v", paused, err)
	}
	again, err := store.PauseAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt, claim.Job.CheckpointHash, checkpoint)
	if err != nil || again.Revision != paused.Revision || again.ErrorCode != paused.ErrorCode {
		t.Fatalf("retry: %+v %v", again, err)
	}
	next, err := store.ClaimAgentMemoryGeneration(worker, "worker", "model", 60)
	if err != nil || next != nil {
		t.Fatalf("user pause resumed automatically: %+v %v", next, err)
	}
	_, err = store.ControlAgentMemoryGeneration(mcpOwnerContext(), job.ProjectID, job.GenerationID, "resume", paused.Revision)
	if err != nil {
		t.Fatal(err)
	}
	next, err = store.ClaimAgentMemoryGeneration(worker, "worker", "model", 60)
	if err != nil || next == nil || string(next.Checkpoint) != string(checkpoint) {
		t.Fatalf("explicit resume: %+v %v", next, err)
	}
}

func TestMemoryGenerationCannotAdoptUserPublication(t *testing.T) {
	store, job, _ := queuedMemoryGenerationFixture(t, "conversation")
	job.Phase, job.Attempt = "consolidation", 1
	current, err := store.UpdateAgentMemory(mcpOwnerContext(), UpdateAgentMemoryCommand{
		ProjectID: job.ProjectID, ExpectedVersion: job.BaseVersion,
		Files: map[string]string{"memory_summary.md": "user edit"}, Enabled: true, RequestID: "not-generation-publication",
	})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	accepted, err := memoryGenerationOwnPublicationQuery(context.Background(), tx, job, current)
	if err != nil || accepted {
		t.Fatalf("user publication must not authorize generation: %v %v", accepted, err)
	}
	_, err = store.memoryGenerationSourceTx(context.Background(), tx, job)
	assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
}

func TestMemoryGenerationRequiresIndependentWorkerAndCurrentOwner(t *testing.T) {
	for _, mode := range []string{"conversation", "background", "stateful"} {
		t.Run(mode, func(t *testing.T) {
			store, job, original := queuedMemoryGenerationFixture(t, mode)
			worker := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
			_, err := store.ClaimAgentMemoryGeneration(original, "worker", "explicit-model", 60)
			assertDomainCode(t, err, "AGENT_ACTIVITY_FORBIDDEN")
			_, err = store.ClaimAgentMemoryGeneration(mcpOwnerContext(), "worker", "explicit-model", 60)
			assertDomainCode(t, err, "AGENT_ACTIVITY_FORBIDDEN")
			claimed, err := store.ClaimAgentMemoryGeneration(worker, "worker", "explicit-model", 60)
			if err != nil || claimed == nil || claimed.ConversationID == "" || claimed.Job.GenerationID != job.GenerationID || claimed.Job.Started || claimed.Source.ContentHash != job.SourceHash {
				t.Fatalf("claim: %+v %v", claimed, err)
			}
			public, _ := json.Marshal(claimed.Job)
			if strings.Contains(string(public), claimed.AttemptToken) || strings.Contains(string(public), claimed.Job.TokenHash) || strings.Contains(string(public), "PRIVATE_SOURCE_BODY") {
				t.Fatal("private worker credentials or input leaked to job metadata")
			}
			_, err = store.StartAgentMemoryGeneration(worker, job.GenerationID, "worker", "wrong-token", claimed.Job.Attempt)
			assertDomainCode(t, err, "AGENT_ACTIVITY_STALE")
			started, err := store.StartAgentMemoryGeneration(worker, job.GenerationID, "worker", claimed.AttemptToken, claimed.Job.Attempt)
			if err != nil || !started.Started {
				t.Fatalf("start: %+v %v", started, err)
			}
			again, err := store.StartAgentMemoryGeneration(worker, job.GenerationID, "worker", claimed.AttemptToken, claimed.Job.Attempt)
			if err != nil || again.Revision != started.Revision {
				t.Fatalf("start retry: %+v %v", again, err)
			}
			if _, err := store.UpdateAgentMemory(mcpOwnerContext(), UpdateAgentMemoryCommand{ProjectID: job.ProjectID, ExpectedVersion: job.BaseVersion, Forget: true, RequestID: "forget-generation"}); err != nil {
				t.Fatal(err)
			}
			_, err = store.StartAgentMemoryGeneration(worker, job.GenerationID, "worker", claimed.AttemptToken, claimed.Job.Attempt)
			assertDomainCode(t, err, "AGENT_ACTIVITY_STALE")
			var status, checkpoint, tokenHash string
			if err := store.db.QueryRow(`SELECT status,checkpoint_json,token_hash FROM agent_memory_generations WHERE generation_id=?`, job.GenerationID).Scan(&status, &checkpoint, &tokenHash); err != nil || status != "cancelled" || checkpoint != "" || tokenHash != "" {
				t.Fatalf("forget job: %s %v", status, err)
			}
		})
	}
}

func TestMemoryGenerationLeaseExpiryDoesNotReplayStartedModel(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "not-started", true: "started"}[started], func(t *testing.T) {
			store, job, _ := queuedMemoryGenerationFixture(t, "conversation")
			worker := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
			claim, err := store.ClaimAgentMemoryGeneration(worker, "first", "explicit-model", 30)
			if err != nil || claim == nil {
				t.Fatalf("claim: %v", err)
			}
			if started {
				if _, err := store.StartAgentMemoryGeneration(worker, job.GenerationID, "first", claim.AttemptToken, claim.Job.Attempt); err != nil {
					t.Fatal(err)
				}
			}
			lease, err := parseTime(claim.Job.LeaseUntil)
			if err != nil {
				t.Fatal(err)
			}
			store.now = func() time.Time { return lease.Add(-time.Nanosecond) }
			if early, err := store.ClaimAgentMemoryGeneration(worker, "early", "explicit-model", 30); err != nil || early != nil {
				t.Fatalf("lease reclaimed early: %+v %v", early, err)
			}
			store.now = func() time.Time { return lease.Add(time.Nanosecond) }
			next, err := store.ClaimAgentMemoryGeneration(worker, "second", "explicit-model", 30)
			if err != nil {
				t.Fatal(err)
			}
			if started {
				if next != nil {
					t.Fatal("started generation replayed after lease loss")
				}
				var status, code string
				if err := store.db.QueryRow(`SELECT status,error_code FROM agent_memory_generations WHERE generation_id=?`, job.GenerationID).Scan(&status, &code); err != nil || status != "paused" || code != "MEMORY_GENERATION_LEASE_LOST" {
					t.Fatalf("lost lease: %s %s %v", status, code, err)
				}
			} else if next == nil || next.Job.Attempt != claim.Job.Attempt+1 || next.AttemptToken == claim.AttemptToken {
				t.Fatalf("unstarted claim not replaced: %+v", next)
			}
			_, err = store.StartAgentMemoryGeneration(worker, job.GenerationID, "first", claim.AttemptToken, claim.Job.Attempt)
			assertDomainCode(t, err, "AGENT_ACTIVITY_STALE")
		})
	}
}

func TestMemoryGenerationRejectsRevokedSourceBeforeClaim(t *testing.T) {
	store, job, _ := queuedMemoryGenerationFixture(t, "conversation")
	worker := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
	if _, err := store.UpdateAgentMemory(mcpOwnerContext(), UpdateAgentMemoryCommand{ProjectID: job.ProjectID, ExpectedVersion: job.BaseVersion, Files: map[string]string{"memory_summary.md": "new user edit"}, Enabled: true, RequestID: "edit-before-generation"}); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimAgentMemoryGeneration(worker, "worker", "explicit-model", 60)
	if err != nil || claim != nil {
		t.Fatalf("revoked source claimed: %+v %v", claim, err)
	}
	var status string
	if err := store.db.QueryRow(`SELECT status FROM agent_memory_generations WHERE generation_id=?`, job.GenerationID).Scan(&status); err != nil || status != "cancelled" {
		t.Fatalf("revoked status: %s %v", status, err)
	}
}

func TestMemoryGenerationCheckpointCASAndAdmission(t *testing.T) {
	store, job, original := queuedMemoryGenerationFixture(t, "conversation")
	worker := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
	claim, err := store.ClaimAgentMemoryGeneration(worker, "worker", "explicit-model", 60)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v", err)
	}
	state := json.RawMessage(`{"sdk_state":"private checkpoint"}`)
	save := func(ctx context.Context, previous string, body json.RawMessage) (AgentMemoryGeneration, error) {
		return store.CheckpointAgentMemoryGeneration(ctx, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt, previous, body)
	}
	_, err = save(original, sha256Hex(nil), state)
	assertDomainCode(t, err, "AGENT_ACTIVITY_FORBIDDEN")
	_, err = save(worker, sha256Hex(nil), state)
	assertDomainCode(t, err, "AGENT_ACTIVITY_STALE")
	if _, err := store.StartAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt); err != nil {
		t.Fatal(err)
	}
	saved, err := save(worker, sha256Hex(nil), state)
	if err != nil || saved.Checkpoint != string(state) || saved.CheckpointHash != sha256Hex(state) || saved.Phase != "extraction" {
		t.Fatalf("checkpoint: %v", err)
	}
	again, err := save(worker, sha256Hex(nil), state)
	if err != nil || again.Revision != saved.Revision {
		t.Fatalf("checkpoint retry: %v", err)
	}
	_, err = save(worker, sha256Hex(nil), json.RawMessage(`{"sdk_state":"new state"}`))
	assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
	_, err = save(worker, saved.CheckpointHash, json.RawMessage(`{"broken":`))
	assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
	lease, err := parseTime(saved.LeaseUntil)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return lease }
	_, err = save(worker, saved.CheckpointHash, state)
	assertDomainCode(t, err, "AGENT_ACTIVITY_STALE")
}

func TestMemoryGenerationUserPauseResumeFencesWorker(t *testing.T) {
	store, job, original := queuedMemoryGenerationFixture(t, "conversation")
	worker := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
	claim, err := store.ClaimAgentMemoryGeneration(worker, "first", "explicit-model", 60)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v", err)
	}
	started, err := store.StartAgentMemoryGeneration(worker, job.GenerationID, "first", claim.AttemptToken, claim.Job.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ControlAgentMemoryGeneration(original, job.ProjectID, job.GenerationID, "pause", started.Revision)
	assertDomainCode(t, err, "AUTHENTICATION_REQUIRED")
	state := json.RawMessage(`{"sdk_state":"original state","pause_kind":"user"}`)
	saved, err := store.CheckpointAgentMemoryGeneration(worker, job.GenerationID, "first", claim.AttemptToken, claim.Job.Attempt, sha256Hex(nil), state)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ControlAgentMemoryGeneration(mcpOwnerContext(), job.ProjectID, job.GenerationID, "pause", started.Revision)
	assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
	requested, err := store.ControlAgentMemoryGeneration(mcpOwnerContext(), job.ProjectID, job.GenerationID, "pause", saved.Revision)
	if err != nil || requested.Status != "running" || requested.ErrorCode != "MEMORY_GENERATION_PAUSE_REQUESTED" || requested.TokenHash != saved.TokenHash || requested.LeaseUntil != saved.LeaseUntil {
		t.Fatalf("pause request: %+v %v", requested, err)
	}
	renewed, err := store.RenewAgentMemoryGeneration(worker, job.GenerationID, "first", claim.AttemptToken, claim.Job.Attempt, 60)
	if err != nil || renewed.ErrorCode != "MEMORY_GENERATION_PAUSE_REQUESTED" {
		t.Fatalf("pause notification: %+v %v", renewed, err)
	}
	paused, err := store.PauseAgentMemoryGeneration(worker, job.GenerationID, "first", claim.AttemptToken, claim.Job.Attempt, saved.CheckpointHash, state)
	if err != nil || paused.Status != "paused" || paused.ErrorCode != "MEMORY_GENERATION_USER_PAUSED" || paused.LeaseUntil != "" {
		t.Fatalf("pause completion: %+v %v", paused, err)
	}
	_, err = store.RenewAgentMemoryGeneration(worker, job.GenerationID, "first", claim.AttemptToken, claim.Job.Attempt, 60)
	assertDomainCode(t, err, "AGENT_ACTIVITY_STALE")
	_, err = store.CheckpointAgentMemoryGeneration(worker, job.GenerationID, "first", claim.AttemptToken, claim.Job.Attempt, saved.CheckpointHash, state)
	assertDomainCode(t, err, "AGENT_ACTIVITY_STALE")
	resumed, err := store.ControlAgentMemoryGeneration(mcpOwnerContext(), job.ProjectID, job.GenerationID, "resume", paused.Revision)
	if err != nil || resumed.Status != "queued" {
		t.Fatalf("resume: %+v %v", resumed, err)
	}
	next, err := store.ClaimAgentMemoryGeneration(worker, "second", "explicit-model", 60)
	if err != nil || next == nil || string(next.Checkpoint) != string(state) || next.AttemptToken == claim.AttemptToken || next.Job.Attempt != claim.Job.Attempt+1 {
		t.Fatalf("resume claim: %v", err)
	}
	current, err := store.GetAgentMemoryGeneration(mcpOwnerContext(), job.ProjectID, job.GenerationID)
	if err != nil || current.Revision != next.Job.Revision {
		t.Fatalf("read: %v", err)
	}
	public, _ := json.Marshal(current)
	if strings.Contains(string(public), "original state") || strings.Contains(string(public), next.AttemptToken) {
		t.Fatal("private generation state leaked")
	}
	cancelled, err := store.ControlAgentMemoryGeneration(mcpOwnerContext(), job.ProjectID, job.GenerationID, "cancel", current.Revision)
	if err != nil || cancelled.Status != "cancelled" {
		t.Fatalf("cancel: %v", err)
	}
	_, err = store.ControlAgentMemoryGeneration(mcpOwnerContext(), job.ProjectID, job.GenerationID, "resume", cancelled.Revision)
	assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
}

func TestMemoryGenerationRenewalAndMissingCheckpoint(t *testing.T) {
	store, job, _ := queuedMemoryGenerationFixture(t, "conversation")
	worker := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
	claim, err := store.ClaimAgentMemoryGeneration(worker, "worker", "explicit-model", 60)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v", err)
	}
	lease, err := parseTime(claim.Job.LeaseUntil)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return lease.Add(-30 * time.Second) }
	renewed, err := store.RenewAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt, 60)
	if err != nil || renewed.LeaseUntil <= claim.Job.LeaseUntil || renewed.Started {
		t.Fatalf("renew: %+v %v", renewed, err)
	}
	again, err := store.RenewAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt, 30)
	if err != nil || again.Revision != renewed.Revision || again.LeaseUntil != renewed.LeaseUntil {
		t.Fatalf("renew shortened lease: %v", err)
	}
	started, err := store.StartAgentMemoryGeneration(worker, job.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt)
	if err != nil {
		t.Fatal(err)
	}
	paused, err := store.ControlAgentMemoryGeneration(mcpOwnerContext(), job.ProjectID, job.GenerationID, "pause", started.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ControlAgentMemoryGeneration(mcpOwnerContext(), job.ProjectID, job.GenerationID, "resume", paused.Revision)
	assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
}
