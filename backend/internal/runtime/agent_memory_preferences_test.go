package runtime

import (
	"testing"
)

func TestAutomaticMemoryArchiveQueuesOnlyWithGenerationConsent(t *testing.T) {
	for _, mode := range []string{"conversation", "background", "stateful"} {
		for _, generate := range []bool{false, true} {
			name := mode + map[bool]string{false: "-archive-only", true: "-generate"}[generate]
			t.Run(name, func(t *testing.T) {
				store, _, ctx, _ := nativeWorkspaceFixture(t, mode)
				source, err := store.ResolveAgentMemorySnapshot(ctx)
				if err != nil {
					t.Fatal(err)
				}
				grant, err := store.UpdateAgentMemoryPreferences(mcpOwnerContext(), UpdateAgentMemoryPreferencesCommand{ProjectID: source.ProjectID, ArchiveEnabled: true, GenerateEnabled: generate, RequestID: "auto-generation"})
				if err != nil {
					t.Fatal(err)
				}
				command := memoryRolloutFixture(source)
				for i := 0; i < 2; i++ {
					if _, err := store.ArchiveAgentMemoryRollout(ctx, command, grant.Revision); err != nil {
						t.Fatalf("archive attempt %d: %v", i, err)
					}
				}
				var count int
				if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_memory_generations WHERE activity_key=? AND segment_id=?`, source.ActivityKey, command.SegmentID).Scan(&count); err != nil || count != map[bool]int{false: 0, true: 1}[generate] {
					t.Fatalf("generation count: %d %v", count, err)
				}
				var archiveRevision int
				var archiveGenerate bool
				var archiveGenerationID string
				if err := store.db.QueryRowContext(ctx, `SELECT archive_revision,archive_generate,archive_generation_id FROM agent_memory_rollouts WHERE activity_key=? AND segment_id=?`, source.ActivityKey, command.SegmentID).Scan(&archiveRevision, &archiveGenerate, &archiveGenerationID); err != nil || archiveRevision != grant.Revision || archiveGenerate != generate || (archiveGenerationID != "") != generate {
					t.Fatalf("archive receipt: %d %t %s %v", archiveRevision, archiveGenerate, archiveGenerationID, err)
				}
				confirmed, err := store.ReadAgentMemoryArchiveReceipt(ctx, command.SegmentID, command.ContentHash, grant.Revision)
				if err != nil || confirmed.GenerationID != archiveGenerationID || confirmed.GenerateEnabled != generate || confirmed.ConsentRevision != grant.Revision || confirmed.ContentHash != command.ContentHash {
					t.Fatalf("confirmed transaction: %+v %v", confirmed, err)
				}
				_, err = store.ReadAgentMemoryArchiveReceipt(ctx, command.SegmentID, command.ContentHash, grant.Revision+1)
				assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
				if generate {
					var id, user, project, hash, status, phase string
					if err := store.db.QueryRowContext(ctx, `SELECT generation_id,user_id,project_id,source_hash,status,phase FROM agent_memory_generations WHERE activity_key=? AND segment_id=?`, source.ActivityKey, command.SegmentID).Scan(&id, &user, &project, &hash, &status, &phase); err != nil || user != source.UserID || project != source.ProjectID || hash != command.ContentHash || status != "queued" || phase != "extraction" {
						t.Fatalf("queued identity/state: %s %s %s %s %s %v", user, project, hash, status, phase, err)
					}
					manual, err := store.QueueAgentMemoryGeneration(mcpOwnerContext(), source.ProjectID, source.ActivityKey, command.SegmentID, command.ContentHash)
					if err != nil || manual.GenerationID != id || manual.SourceHash != command.ContentHash {
						t.Fatalf("manual deduplication: %+v %v", manual, err)
					}
					if archiveGenerationID != id {
						t.Fatal("archive receipt refers to another generation")
					}
				}
				if _, err := store.UpdateAgentMemory(mcpOwnerContext(), UpdateAgentMemoryCommand{ProjectID: source.ProjectID, ExpectedVersion: source.Version, Forget: true, RequestID: "forget-archive-receipt"}); err != nil {
					t.Fatal(err)
				}
				_, err = store.ReadAgentMemoryArchiveReceipt(ctx, command.SegmentID, command.ContentHash, grant.Revision)
				assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
				if err := store.db.QueryRowContext(ctx, `SELECT archive_revision,archive_generate,archive_generation_id FROM agent_memory_rollouts WHERE activity_key=? AND segment_id=?`, source.ActivityKey, command.SegmentID).Scan(&archiveRevision, &archiveGenerate, &archiveGenerationID); err != nil || archiveRevision != 0 || archiveGenerate || archiveGenerationID != "" {
					t.Fatalf("forgotten receipt retained: %d %t %s %v", archiveRevision, archiveGenerate, archiveGenerationID, err)
				}
			})
		}
	}
}

func TestAutomaticMemoryQueueFailureRollsBackArchive(t *testing.T) {
	store, _, ctx, _ := nativeWorkspaceFixture(t, "conversation")
	source, err := store.ResolveAgentMemorySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := store.UpdateAgentMemoryPreferences(mcpOwnerContext(), UpdateAgentMemoryPreferencesCommand{ProjectID: source.ProjectID, ArchiveEnabled: true, GenerateEnabled: true, RequestID: "atomic-archive"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER fail_memory_generation BEFORE INSERT ON agent_memory_generations BEGIN SELECT RAISE(ABORT,'injected queue failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ArchiveAgentMemoryRollout(ctx, memoryRolloutFixture(source), grant.Revision); err == nil {
		t.Fatal("queue failure reported archival success")
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_memory_rollouts WHERE activity_key=?`, source.ActivityKey).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial archive retained: %d %v", count, err)
	}
}

func TestAutomaticMemoryArchiveRequiresCurrentConsent(t *testing.T) {
	for _, mode := range []string{"conversation", "background", "stateful"} {
		t.Run(mode, func(t *testing.T) {
			store, _, ctx, _ := nativeWorkspaceFixture(t, mode)
			source, err := store.ResolveAgentMemorySnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			command := memoryRolloutFixture(source)
			policy, err := store.ResolveAgentMemoryArchivePolicy(ctx)
			if err != nil || policy.ArchiveEnabled || policy.Revision != 0 || policy.UserID != source.UserID || policy.ProjectID != source.ProjectID {
				t.Fatalf("initial policy: %+v %v", policy, err)
			}
			_, err = store.ArchiveAgentMemoryRollout(ctx, command, 1)
			assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
			var count int
			if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_memory_rollouts WHERE activity_key=?`, source.ActivityKey).Scan(&count); err != nil || count != 0 {
				t.Fatalf("refused archive persisted: %d %v", count, err)
			}
			grant := UpdateAgentMemoryPreferencesCommand{ProjectID: source.ProjectID, ArchiveEnabled: true, RequestID: "archive-grant"}
			saved, err := store.UpdateAgentMemoryPreferences(mcpOwnerContext(), grant)
			if err != nil {
				t.Fatal(err)
			}
			policy, err = store.ResolveAgentMemoryArchivePolicy(ctx)
			if err != nil || policy != saved {
				t.Fatalf("execution policy: %+v %v", policy, err)
			}
			first, err := store.ArchiveAgentMemoryRollout(ctx, command, saved.Revision)
			if err != nil || first.ContentHash != command.ContentHash {
				t.Fatalf("archive: %+v %v", first, err)
			}
			again, err := store.ArchiveAgentMemoryRollout(ctx, command, saved.Revision)
			if err != nil || again != first {
				t.Fatalf("archive retry: %+v %v", again, err)
			}
			revoked, err := store.UpdateAgentMemoryPreferences(mcpOwnerContext(), UpdateAgentMemoryPreferencesCommand{ProjectID: source.ProjectID, ExpectedRevision: saved.Revision, RequestID: "archive-revoke"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.ArchiveAgentMemoryRollout(ctx, command, saved.Revision)
			assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
			grant.ExpectedRevision = revoked.Revision
			grant.RequestID = "archive-regrant"
			if _, err := store.UpdateAgentMemoryPreferences(mcpOwnerContext(), grant); err != nil {
				t.Fatal(err)
			}
			_, err = store.ArchiveAgentMemoryRollout(ctx, command, saved.Revision)
			assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
			_, err = store.ResolveAgentMemoryArchivePolicy(mcpOwnerContext())
			assertDomainCode(t, err, "AGENT_ACTIVITY_FORBIDDEN")
			activity, _ := AgentActivityFromContext(ctx)
			activity.MemoryGenerationID = "generation-cannot-archive-itself"
			generationCtx := WithAgentActivity(ctx, activity)
			_, err = store.ResolveAgentMemoryArchivePolicy(generationCtx)
			assertDomainCode(t, err, "AGENT_ACTIVITY_FORBIDDEN")
			_, err = store.ArchiveAgentMemoryRollout(generationCtx, command, saved.Revision)
			assertDomainCode(t, err, "AGENT_ACTIVITY_FORBIDDEN")
		})
	}
}

func TestMemoryConsentDefaultsAndForgetRevokesSavedGrant(t *testing.T) {
	store, job, delegated := queuedMemoryGenerationFixture(t, "conversation")
	ctx := mcpOwnerContext()
	initial, err := store.GetAgentMemoryPreferences(ctx, job.ProjectID)
	if err != nil || initial.Revision != 0 || initial.ArchiveEnabled || initial.GenerateEnabled {
		t.Fatalf("defaults: %+v %v", initial, err)
	}
	command := UpdateAgentMemoryPreferencesCommand{ProjectID: job.ProjectID, ArchiveEnabled: true, GenerateEnabled: true, RequestID: "consent"}
	saved, err := store.UpdateAgentMemoryPreferences(ctx, command)
	if err != nil || saved.Revision != 1 || !saved.GenerateEnabled {
		t.Fatalf("grant: %+v %v", saved, err)
	}
	again, err := store.UpdateAgentMemoryPreferences(ctx, command)
	if err != nil || again != saved {
		t.Fatalf("retry: %+v %v", again, err)
	}
	changed := command
	changed.GenerateEnabled = false
	_, err = store.UpdateAgentMemoryPreferences(ctx, changed)
	assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
	_, err = store.UpdateAgentMemoryPreferences(delegated, command)
	assertDomainCode(t, err, "AUTHENTICATION_REQUIRED")
	if _, err := store.UpdateAgentMemory(ctx, UpdateAgentMemoryCommand{ProjectID: job.ProjectID, ExpectedVersion: job.BaseVersion, Forget: true, RequestID: "forget-consent"}); err != nil {
		t.Fatal(err)
	}
	revoked, err := store.GetAgentMemoryPreferences(ctx, job.ProjectID)
	if err != nil || revoked.ArchiveEnabled || revoked.GenerateEnabled || revoked.Revision != 2 {
		t.Fatalf("revoke: %+v %v", revoked, err)
	}
	_, err = store.UpdateAgentMemoryPreferences(ctx, command)
	assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
	invalid := command
	invalid.ArchiveEnabled = false
	invalid.RequestID = "invalid"
	_, err = store.UpdateAgentMemoryPreferences(ctx, invalid)
	assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
}
