package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"content-agent/backend/internal/identity"
)

func TestMemoryExplicitResumeRecoversOnlyProvenLegacyWorkspace(t *testing.T) {
	for _, fault := range []string{"none", "phase", "source", "snapshot", "live-lease", "target-exists", "snapshot-advanced", "write-ignored", "resume-write-fails", "resume-write-ignored"} {
		t.Run(fault, func(t *testing.T) {
			store, queued, _ := queuedMemoryGenerationFixture(t, "conversation")
			service := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
			claim, err := store.ClaimAgentMemoryGeneration(service, "worker", "model", 60)
			if err != nil || claim == nil {
				t.Fatal(err)
			}
			if _, err := store.StartAgentMemoryGeneration(service, queued.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt); err != nil {
				t.Fatal(err)
			}
			ctx := WithAgentActivity(service, AgentActivityIdentity{ProjectID: queued.ProjectID, MemoryGenerationID: queued.GenerationID, MemoryGenerationAttempt: claim.Job.Attempt, AttemptToken: claim.AttemptToken})
			holder := sha256Hex([]byte("legacy-holder"))
			lease, err := store.ReserveNativeWorkspace(ctx, ReserveNativeWorkspaceCommand{HolderKey: holder})
			if err != nil {
				t.Fatal(err)
			}
			access := NativeWorkspaceAccess{SessionID: lease.SessionID, Generation: lease.Generation, HolderKey: holder}
			snapshot, err := store.SaveNativeWorkspaceSnapshot(ctx, access, "legacy-snapshot", 0, nativeTestArchive(t, []byte("private snapshot")))
			if err != nil {
				t.Fatal(err)
			}
			binding := map[string]any{"generation_id": queued.GenerationID, "phase": "extraction", "project_id": queued.ProjectID, "user_id": queued.UserID, "source_hash": queued.SourceHash, "base_version": queued.BaseVersion, "base_hash": queued.BaseHash, "model_id": claim.Job.ModelID, "policy_hash": sha256Hex([]byte("policy"))}
			if fault == "phase" {
				binding["phase"] = "consolidation"
			}
			if fault == "source" {
				binding["source_hash"] = sha256Hex([]byte("other"))
			}
			hash := snapshot.Snapshot.SHA256
			if fault == "snapshot" {
				hash = sha256Hex([]byte("other"))
			}
			// Isolate persisted recovery references, not SDK RunState decoding.
			checkpoint, err := json.Marshal(map[string]any{"schema_version": "agent_memory_checkpoint.v1", "binding": binding, "state_json": "{}", "state_hash": sha256Hex([]byte("{}")), "stage_hash": sha256Hex([]byte("stage")), "pause_kind": "user", "native_workspace": map[string]any{"schema_version": "content_agent_native_workspace.v1", "session_id": lease.SessionID, "environment_id": lease.SessionID, "state": "closed", "snapshot": map[string]any{"version": snapshot.Version, "sha256": hash}}})
			if err != nil {
				t.Fatal(err)
			}
			paused, err := store.PauseAgentMemoryGeneration(service, queued.GenerationID, "worker", claim.AttemptToken, claim.Job.Attempt, claim.Job.CheckpointHash, checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			until := store.now().Add(-time.Second)
			if fault == "live-lease" {
				until = store.now().Add(time.Minute)
			}
			legacyKey := "memory:" + queued.GenerationID
			if _, err := store.db.Exec(`UPDATE native_workspace_leases SET activity_key=?,lease_until=? WHERE session_id=?`, legacyKey, formatTime(until), lease.SessionID); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "target-exists":
				id, err := newNativeWorkspaceUUID()
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.db.Exec(`INSERT INTO native_workspace_leases(session_id,activity_key,workspace_id,project_id,user_id,execution_epoch,holder_hash,generation,lease_until,status,snapshot_version,created_at,updated_at)
					SELECT ?,?,workspace_id,project_id,user_id,execution_epoch,holder_hash,generation,lease_until,status,0,created_at,updated_at FROM native_workspace_leases WHERE session_id=?`, id, legacyKey+":extraction", lease.SessionID); err != nil {
					t.Fatal(err)
				}
			case "snapshot-advanced":
				if _, err := store.db.Exec(`UPDATE native_workspace_leases SET snapshot_version=snapshot_version+1 WHERE session_id=?`, lease.SessionID); err != nil {
					t.Fatal(err)
				}
			case "write-ignored":
				if _, err := store.db.Exec(`CREATE TRIGGER ignore_legacy_phase BEFORE UPDATE OF activity_key ON native_workspace_leases BEGIN SELECT RAISE(IGNORE); END`); err != nil {
					t.Fatal(err)
				}
			case "resume-write-fails":
				if _, err := store.db.Exec(`CREATE TRIGGER reject_legacy_resume BEFORE UPDATE OF status ON agent_memory_generations WHEN NEW.status='queued' BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
					t.Fatal(err)
				}
			case "resume-write-ignored":
				if _, err := store.db.Exec(`CREATE TRIGGER ignore_legacy_resume BEFORE UPDATE OF status ON agent_memory_generations WHEN NEW.status='queued' BEGIN SELECT RAISE(IGNORE); END`); err != nil {
					t.Fatal(err)
				}
			}
			resumed, resumeErr := store.ControlAgentMemoryGeneration(mcpOwnerContext(), queued.ProjectID, queued.GenerationID, "resume", paused.Revision)
			var key, holderHash string
			var generation int64
			if err := store.db.QueryRow(`SELECT activity_key,holder_hash,generation FROM native_workspace_leases WHERE session_id=?`, lease.SessionID).Scan(&key, &holderHash, &generation); err != nil {
				t.Fatal(err)
			}
			if fault == "none" {
				if resumeErr != nil || resumed.Status != "queued" || resumed.Checkpoint != string(checkpoint) || key != legacyKey+":extraction" {
					t.Fatalf("verified resume: %s %s %v", resumed.Status, key, resumeErr)
				}
			} else if resumeErr == nil || key != legacyKey {
				t.Fatalf("unproven resume changed state: %s %v", key, resumeErr)
			}
			if fault != "none" {
				var status, checkpointHash string
				var revision int
				if err := store.db.QueryRow(`SELECT status,checkpoint_hash,revision FROM agent_memory_generations WHERE generation_id=?`, queued.GenerationID).Scan(&status, &checkpointHash, &revision); err != nil || status != "paused" || checkpointHash != paused.CheckpointHash || revision != paused.Revision {
					t.Fatalf("failed recovery did not roll back: %s %d %v", status, revision, err)
				}
			}
			if generation != lease.Generation || holderHash != sha256Hex([]byte(holder)) {
				t.Fatal("phase recovery bypassed normal fenced takeover")
			}
		})
	}
}
