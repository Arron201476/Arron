package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"content-agent/backend/internal/identity"
)

func nativeWorkspaceFixture(t *testing.T, mode string) (*Store, string, context.Context, NativeWorkspaceAccess) {
	t.Helper()
	database := filepath.Join(t.TempDir(), "native.db")
	registry := loadTestRegistry(t)
	if mode == "background" {
		registry = loadBackgroundTaskRegistry(t)
	}
	store, err := Open(database, registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	var activity AgentActivityIdentity
	var dispatch int64
	switch mode {
	case "conversation":
		activity, _ = AgentActivityFromContext(mcpExecutionContext(t, store, mcpOwnerContext()))
		turn, err := store.GetAgentTurn(context.Background(), activity.AgentTurnID)
		if err != nil {
			t.Fatal(err)
		}
		dispatch = turn.DispatchGeneration
	case "background":
		task := createBackgroundTaskForTest(t, store)
		claim, err := store.ClaimAgentTask(context.Background(), ClaimAgentTaskCommand{WorkerID: "native", ProviderID: "sdk", ModelID: "test", LeaseSeconds: 60})
		if err != nil || claim == nil {
			t.Fatalf("claim: %v", err)
		}
		activity = AgentActivityIdentity{ProjectID: task.ProjectID, AgentTaskAttemptID: claim.Attempt.AgentTaskAttemptID, AttemptToken: claim.AttemptToken}
	case "stateful":
		project, claim := statefulToolClaimForTest(t, store)
		activity = AgentActivityIdentity{ProjectID: project.ProjectID, ExecutionAttemptID: claim.Attempt.AttemptID, AttemptToken: claim.AttemptToken}
	}
	ctx := WithAgentActivity(identity.WithPrincipal(context.Background(), identity.ServicePrincipal()), activity)
	key := strings.Repeat("a", 64)
	lease, err := store.ReserveNativeWorkspace(ctx, ReserveNativeWorkspaceCommand{HolderKey: key, DispatchGeneration: dispatch})
	if err != nil {
		t.Fatal(err)
	}
	return store, database, ctx, NativeWorkspaceAccess{SessionID: lease.SessionID, Generation: lease.Generation, HolderKey: key, DispatchGeneration: dispatch}
}

func nativeTestArchive(t *testing.T, body []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Name: "nested/binary.bin", Typeflag: tar.TypeReg, Mode: 0o640, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestNativeWorkspaceThreeExecutionOwnersSnapshotReopenAndRevocation(t *testing.T) {
	for _, mode := range []string{"conversation", "background", "stateful"} {
		t.Run(mode, func(t *testing.T) {
			store, database, ctx, access := nativeWorkspaceFixture(t, mode)
			body := nativeTestArchive(t, []byte{0, 255, 128, 10})
			first, err := store.SaveNativeWorkspaceSnapshot(ctx, access, "snapshot-1", 0, body)
			if err != nil || first.Version != 1 || first.Snapshot.SizeBytes != 4 {
				t.Fatalf("save: %+v %v", first, err)
			}
			repeated, err := store.SaveNativeWorkspaceSnapshot(ctx, access, "snapshot-1", 0, body)
			if err != nil || repeated.Version != 1 {
				t.Fatalf("lost receipt repeat: %+v %v", repeated, err)
			}
			var storedKey string
			if err := store.db.QueryRow(`SELECT holder_hash FROM native_workspace_leases WHERE session_id=?`, access.SessionID).Scan(&storedKey); err != nil {
				t.Fatal(err)
			}
			if storedKey == access.HolderKey || storedKey != sha256Hex([]byte(access.HolderKey)) {
				t.Fatal("lease key was not hashed")
			}
			lease, err := store.ValidateNativeWorkspaceLease(ctx, access)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(lease)
			if bytes.Contains(encoded, []byte(access.HolderKey)) || bytes.Contains(encoded, []byte(storedKey)) {
				t.Fatal("lease response leaked secret binding")
			}
			registry := store.registry
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(database, registry)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			read, err := reopened.ReadNativeWorkspaceSnapshot(ctx, access, 1, first.Snapshot.SHA256)
			if err != nil || !bytes.Equal(read.Snapshot.Archive, first.Snapshot.Archive) {
				t.Fatalf("reopen: %v", err)
			}
			wrong := access
			wrong.HolderKey = strings.Repeat("b", 64)
			_, err = reopened.ReadNativeWorkspaceSnapshot(ctx, wrong, 1, first.Snapshot.SHA256)
			assertDomainCode(t, err, "NATIVE_WORKSPACE_LEASE_INVALID")
			activity, _ := AgentActivityFromContext(ctx)
			invalid := activity
			invalid.ProjectID = "other-project"
			if _, err := reopened.ValidateNativeWorkspaceLease(WithAgentActivity(ctx, invalid), access); err == nil {
				t.Fatal("cross-project activity accepted")
			}
			invalid = activity
			invalid.AllowTerminal = true
			_, err = reopened.ValidateNativeWorkspaceLease(WithAgentActivity(ctx, invalid), access)
			assertDomainCode(t, err, "AGENT_ACTIVITY_FORBIDDEN")
			if mode != "conversation" {
				invalid = activity
				invalid.AttemptToken = "expired-token"
				_, err = reopened.ValidateNativeWorkspaceLease(WithAgentActivity(ctx, invalid), access)
				assertDomainCode(t, err, "ATTEMPT_TOKEN_INVALID")
			}
			_, err = reopened.ValidateNativeWorkspaceLease(WithAgentActivity(mcpOwnerContext(), activity), access)
			assertDomainCode(t, err, "AGENT_ACTIVITY_FORBIDDEN")
			if _, err := reopened.db.Exec(`UPDATE workspace_memberships SET role='viewer' WHERE user_id=?`, identity.DefaultUserID); err != nil {
				t.Fatal(err)
			}
			_, err = reopened.SaveNativeWorkspaceSnapshot(ctx, access, "revoked", 1, body)
			assertDomainCode(t, err, "WORKSPACE_ACCESS_DENIED")
		})
	}
}

func TestNativeWorkspaceLeaseTakeoverAndConversationGenerationFence(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	command := ReserveNativeWorkspaceCommand{HolderKey: strings.Repeat("b", 64), ExpectedGeneration: 1, DispatchGeneration: access.DispatchGeneration}
	_, err := store.ReserveNativeWorkspace(ctx, command)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_BUSY")
	now := store.now().Add(61 * time.Second)
	store.now = func() time.Time { return now }
	_, err = store.ReserveNativeWorkspace(ctx, ReserveNativeWorkspaceCommand{HolderKey: access.HolderKey, ExpectedGeneration: 1, DispatchGeneration: access.DispatchGeneration})
	assertDomainCode(t, err, "NATIVE_WORKSPACE_LEASE_EXPIRED")
	command.ExpectedGeneration = 0
	_, err = store.ReserveNativeWorkspace(ctx, command)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_CONFLICT")
	command.ExpectedGeneration = 1
	lease, err := store.ReserveNativeWorkspace(ctx, command)
	if err != nil || lease.SessionID != access.SessionID || lease.Generation != 2 {
		t.Fatalf("takeover: %+v %v", lease, err)
	}
	_, err = store.ValidateNativeWorkspaceLease(ctx, access)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_LEASE_INVALID")
	access.HolderKey, access.Generation = command.HolderKey, 2
	activity, _ := AgentActivityFromContext(ctx)
	if _, err := store.db.Exec(`UPDATE agent_turns SET dispatch_generation=dispatch_generation+1 WHERE agent_turn_id=?`, activity.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	_, err = store.ValidateNativeWorkspaceLease(ctx, access)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_EXECUTION_STALE")
	access.DispatchGeneration++
	_, err = store.ValidateNativeWorkspaceLease(ctx, access)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_LEASE_INVALID")
	command.HolderKey, command.ExpectedGeneration, command.DispatchGeneration = strings.Repeat("c", 64), 2, access.DispatchGeneration
	next, err := store.ReserveNativeWorkspace(ctx, command)
	if err != nil || next.Generation != 3 {
		t.Fatalf("new dispatch lease: %+v %v", next, err)
	}
}

func TestNativeWorkspaceSnapshotExactVersionConflictsIntegrityAndQuota(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	firstBody, secondBody := nativeTestArchive(t, []byte("first")), nativeTestArchive(t, []byte("second"))
	first, err := store.SaveNativeWorkspaceSnapshot(ctx, access, "first", 0, firstBody)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.SaveNativeWorkspaceSnapshot(ctx, access, "first", 0, secondBody)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_REQUEST_CONFLICT")
	_, err = store.SaveNativeWorkspaceSnapshot(ctx, access, "stale", 0, secondBody)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_SNAPSHOT_CONFLICT")
	second, err := store.SaveNativeWorkspaceSnapshot(ctx, access, "second", 1, secondBody)
	if err != nil || second.Version != 2 {
		t.Fatalf("second save: %v", err)
	}
	old, err := store.ReadNativeWorkspaceSnapshot(ctx, access, 1, first.Snapshot.SHA256)
	if err != nil || old.Snapshot.SHA256 != first.Snapshot.SHA256 {
		t.Fatalf("old snapshot replaced: %v", err)
	}
	_, err = store.ReadNativeWorkspaceSnapshot(ctx, access, 2, first.Snapshot.SHA256)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_SNAPSHOT_INVALID")
	if _, err := store.db.Exec(`UPDATE native_workspace_snapshots SET entries_json='[]' WHERE version=1`); err != nil {
		t.Fatal(err)
	}
	_, err = store.ReadNativeWorkspaceSnapshot(ctx, access, 1, first.Snapshot.SHA256)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_SNAPSHOT_INVALID")
	_, err = store.SaveNativeWorkspaceSnapshot(ctx, access, "first", 0, firstBody)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_SNAPSHOT_INVALID")
	if _, err := store.db.Exec(`UPDATE workspace_quotas SET max_storage_bytes=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveNativeWorkspaceSnapshot(ctx, access, "over-quota", 2, firstBody); err == nil {
		t.Fatal("storage quota bypassed")
	}
	lease, err := store.ValidateNativeWorkspaceLease(ctx, access)
	if err != nil || lease.SnapshotVersion != 2 {
		t.Fatalf("failed snapshot changed head: %+v %v", lease, err)
	}
}

func TestNativeWorkspacePausedCancelledAndMixedActivityCannotAcquire(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	activity, _ := AgentActivityFromContext(ctx)
	command := ReserveNativeWorkspaceCommand{HolderKey: access.HolderKey, ExpectedGeneration: 1, DispatchGeneration: access.DispatchGeneration}
	if _, err := store.db.Exec(`UPDATE agent_turns SET status='pausing' WHERE agent_turn_id=?`, activity.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	_, err := store.ReserveNativeWorkspace(ctx, command)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_EXECUTION_PAUSING")
	if _, err := store.SaveNativeWorkspaceSnapshot(ctx, access, "pause-boundary", 0, nativeTestArchive(t, []byte("checkpoint"))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE agent_turns SET status='paused' WHERE agent_turn_id=?`, activity.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveNativeWorkspaceSnapshot(ctx, access, "after-pause", 1, nativeTestArchive(t, nil)); err == nil {
		t.Fatal("paused execution saved another snapshot")
	}
	if _, err := store.db.Exec(`UPDATE agent_turns SET status='cancelled' WHERE agent_turn_id=?`, activity.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ValidateNativeWorkspaceLease(ctx, access); err == nil {
		t.Fatal("cancelled execution retained access")
	}
	activity.ExecutionAttemptID = "mixed"
	_, err = store.ReserveNativeWorkspace(WithAgentActivity(ctx, activity), command)
	assertDomainCode(t, err, "AGENT_ACTIVITY_INVALID")
}

func TestNativeWorkspaceProjectDeleteCleansSnapshotsAndInvalidatesPreview(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	activity, _ := AgentActivityFromContext(ctx)
	first, err := store.SaveNativeWorkspaceSnapshot(ctx, access, "first", 0, nativeTestArchive(t, []byte("binary")))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := store.CreateProjectDeletePreview(mcpOwnerContext(), CreateProjectDeletePreviewCommand{ProjectID: activity.ProjectID})
	if err != nil || preview.Impact.NativeWorkspaceCount != 1 || preview.Impact.WorkspaceSnapshotCount != 1 {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	if _, err := store.SaveNativeWorkspaceSnapshot(ctx, access, "next", 1, nativeTestArchive(t, []byte("changed"))); err != nil {
		t.Fatal(err)
	}
	_, err = store.ConfirmProjectDelete(mcpOwnerContext(), ConfirmProjectDeleteCommand{ProjectID: activity.ProjectID, PreviewHash: preview.SnapshotHash, Confirmed: true})
	if err == nil {
		t.Fatal("snapshot change did not invalidate delete preview")
	}
	preview, err = store.CreateProjectDeletePreview(mcpOwnerContext(), CreateProjectDeletePreviewCommand{ProjectID: activity.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmProjectDelete(mcpOwnerContext(), ConfirmProjectDeleteCommand{ProjectID: activity.ProjectID, PreviewHash: preview.SnapshotHash, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM native_workspace_snapshots`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("snapshot content retained: %d %v", count, err)
	}
	var status, key string
	if err := store.db.QueryRow(`SELECT status,holder_hash FROM native_workspace_leases WHERE session_id=?`, access.SessionID).Scan(&status, &key); err != nil || status != "closing" || key != "" {
		t.Fatalf("lease not invalidated: %s %v", status, err)
	}
	if _, err := store.ReadNativeWorkspaceSnapshot(ctx, access, 1, first.Snapshot.SHA256); err == nil {
		t.Fatal("deleted project snapshot accessible")
	}
}

func TestNativeWorkspaceV55MigrationHasBackupAndNoSynthesizedEnvironments(t *testing.T) {
	store, database, ctx, access := nativeWorkspaceFixture(t, "conversation")
	activity, _ := AgentActivityFromContext(ctx)
	if _, err := store.db.Exec(`DROP TABLE native_workspace_snapshots; DROP TABLE native_workspace_leases; PRAGMA user_version=55;`); err != nil {
		t.Fatal(err)
	}
	registry := store.registry
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(database, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var backup string
	if err := reopened.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version=55 AND to_version=? AND status='completed'`, schemaVersion).Scan(&backup); err != nil || backup == "" {
		t.Fatalf("missing migration backup: %v", err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("backup absent: %v", err)
	}
	var count int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM native_workspace_leases`).Scan(&count); err != nil || count != 0 {
		t.Fatal("migration synthesized a workspace")
	}
	turn, err := reopened.GetAgentTurn(context.Background(), activity.AgentTurnID)
	if err != nil || turn.Status != "running" || turn.DispatchGeneration != access.DispatchGeneration {
		t.Fatalf("migration changed execution: %+v %v", turn, err)
	}
}
