package runtime

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"content-agent/backend/internal/identity"
	"content-agent/backend/internal/scriptsandbox"
)

func nativeRestoreFixture(t *testing.T, mode string) (*Store, string, context.Context, NativeWorkspaceAccess, *nativeEngineFixture, RestoreNativeWorkspaceCommand) {
	t.Helper()
	store, database, ctx, access := nativeWorkspaceFixture(t, mode)
	engine := newNativeEngineFixture(t)
	saved, err := store.SaveNativeWorkspaceSnapshot(ctx, access, "saved-source", 0, engine.archive.Archive)
	if err != nil {
		t.Fatal(err)
	}
	return store, database, ctx, access, engine, RestoreNativeWorkspaceCommand{RequestID: "restore-1", Version: saved.Version, SHA256: saved.Snapshot.SHA256}
}

func TestNativeRestoreThreeOwnersExactHistoricalVersionAndReopenReceipt(t *testing.T) {
	for _, mode := range []string{"conversation", "background", "stateful"} {
		t.Run(mode, func(t *testing.T) {
			store, database, ctx, access, engine, command := nativeRestoreFixture(t, mode)
			later, err := store.SaveNativeWorkspaceSnapshot(ctx, access, "later", 1, nativeTestArchive(t, []byte("later contents")))
			if err != nil {
				t.Fatal(err)
			}
			manager := nativeManagerForTest(t, store, engine)
			if _, err := manager.Restore(ctx, access, command); err != nil {
				t.Fatal(err)
			}
			state := nativeEnvironmentForTest(t, store, access)
			if engine.hydrated[state.environmentID].SHA256 != command.SHA256 || engine.hydrated[state.environmentID].SHA256 == later.Snapshot.SHA256 {
				t.Fatal("restore silently selected latest")
			}
			// Model edits after recovery must survive a lost response and restart.
			engine.hydrated[state.environmentID] = later.Snapshot
			registry := store.registry
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(database, registry)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			manager = nativeManagerForTest(t, reopened, engine)
			if _, err := manager.Restore(ctx, access, command); err != nil {
				t.Fatal(err)
			}
			if engine.creates != 1 || engine.hydrates != 1 || engine.reconnects != 1 || engine.hydrated[state.environmentID].SHA256 != later.Snapshot.SHA256 {
				t.Fatal("restore replay overwrote later changes")
			}
			var status string
			if err := reopened.db.QueryRow(`SELECT status FROM native_workspace_restores WHERE request_id=?`, command.RequestID).Scan(&status); err != nil || status != "completed" {
				t.Fatalf("restore receipt: %s %v", status, err)
			}
		})
	}
}

func TestNativeRestoreConflictLeavesLiveFilesAndFailedRequestCannotReplay(t *testing.T) {
	store, _, ctx, access, engine, command := nativeRestoreFixture(t, "conversation")
	manager := nativeManagerForTest(t, store, engine)
	if _, err := manager.Restore(ctx, access, command); err != nil {
		t.Fatal(err)
	}
	state := nativeEnvironmentForTest(t, store, access)
	later, err := scriptsandbox.ParseWorkspaceSnapshot(nativeTestArchive(t, []byte("unsaved edits")), scriptsandbox.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	engine.hydrated[state.environmentID] = later
	command.RequestID = "different-request"
	_, err = manager.Restore(ctx, access, command)
	if scriptsandbox.ErrorCode(err) != "WORKSPACE_HYDRATE_CONFLICT" {
		t.Fatalf("wanted conflict: %v", err)
	}
	if nativeEnvironmentForTest(t, store, access).State != "ready" || !bytes.Equal(engine.hydrated[state.environmentID].Archive, later.Archive) {
		t.Fatal("conflict destroyed existing files")
	}
	_, err = manager.Restore(ctx, access, command)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_RECOVERY_REQUIRED")
	if engine.hydrates != 2 {
		t.Fatal("failed hydrate was replayed")
	}
}

func TestNativeRestoreUnconfirmedReplacementForbiddenUntilExactCleanup(t *testing.T) {
	store, _, ctx, access, engine, command := nativeRestoreFixture(t, "conversation")
	manager := nativeManagerForTest(t, store, engine)
	engine.hydrateError = errors.New("lost restore receipt")
	if _, err := manager.Restore(ctx, access, command); err == nil {
		t.Fatal("unknown restore reported success")
	}
	old := nativeEnvironmentForTest(t, store, access)
	command.RequestID = "new-explicit-recovery"
	_, err := manager.Restore(ctx, access, command)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_RECOVERY_REQUIRED")
	if err := manager.Cleanup(identity.WithPrincipal(context.Background(), identity.ServicePrincipal()), access.SessionID); err != nil {
		t.Fatal(err)
	}
	engine.hydrateError = nil
	if _, err := manager.Restore(ctx, access, command); err != nil {
		t.Fatal(err)
	}
	next := nativeEnvironmentForTest(t, store, access)
	if next.environmentID == old.environmentID || next.handle.ContainerID == old.handle.ContainerID || engine.creates != 2 || engine.deletes != 1 {
		t.Fatal("closed environment identity was reused")
	}
	if engine.hydrated[next.environmentID].SHA256 != command.SHA256 {
		t.Fatal("replacement is not selected snapshot")
	}
	// Late old operation receipts must not replace the new live binding.
	if err := manager.finish(ctx, access, old, nil); err == nil {
		t.Fatal("late restore replaced new environment")
	}
	if nativeEnvironmentForTest(t, store, access).environmentID != next.environmentID {
		t.Fatal("late restore redirected workspace")
	}
}

func TestNativeRestoreInvalidSelectionAndRevokedIdentityCauseNoEngineEffects(t *testing.T) {
	store, _, ctx, access, engine, command := nativeRestoreFixture(t, "conversation")
	manager := nativeManagerForTest(t, store, engine)
	wrong := command
	wrong.SHA256 = strings.Repeat("e", 64)
	_, err := manager.Restore(ctx, access, wrong)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_SNAPSHOT_INVALID")
	wrong = command
	wrong.Version = 22
	_, err = manager.Restore(ctx, access, wrong)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_SNAPSHOT_NOT_FOUND")
	wrong = command
	wrong.RequestID = "../../restore"
	_, err = manager.Restore(ctx, access, wrong)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_REQUEST_INVALID")
	if _, err := store.db.Exec(`UPDATE workspace_memberships SET role='viewer'`); err != nil {
		t.Fatal(err)
	}
	_, err = manager.Restore(ctx, access, command)
	assertDomainCode(t, err, "WORKSPACE_ACCESS_DENIED")
	if engine.creates != 0 || engine.hydrates != 0 {
		t.Fatal("invalid selection or revoked access reached engine")
	}
}

func TestNativeRestoreAbandonedOperationSurvivesRestartAndCannotRecreate(t *testing.T) {
	store, database, ctx, access, engine, command := nativeRestoreFixture(t, "conversation")
	manager := nativeManagerForTest(t, store, engine)
	if _, _, _, err := manager.beginRestore(ctx, access, command); err != nil {
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
	manager = nativeManagerForTest(t, reopened, engine)
	_, err = manager.Restore(ctx, access, command)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_OPERATION_IN_PROGRESS")
	command.RequestID = "another-request"
	_, err = manager.Restore(ctx, access, command)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_OPERATION_IN_PROGRESS")
	if engine.creates != 0 || engine.hydrates != 0 {
		t.Fatal("abandoned operation silently replayed")
	}
}

func TestNativeRestoreV57MigrationPreservesEnvironmentAndSnapshot(t *testing.T) {
	store, database, ctx, access, engine, command := nativeRestoreFixture(t, "conversation")
	manager := nativeManagerForTest(t, store, engine)
	if _, err := manager.Restore(ctx, access, command); err != nil {
		t.Fatal(err)
	}
	before := nativeEnvironmentForTest(t, store, access)
	if _, err := store.db.Exec(`DROP TABLE native_workspace_restores; PRAGMA user_version=57;`); err != nil {
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
	if err := reopened.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version=57 AND to_version=? AND status='completed'`, schemaVersion).Scan(&backup); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatal(err)
	}
	after := nativeEnvironmentForTest(t, reopened, access)
	if after.environmentID != before.environmentID || after.handle != before.handle || after.State != "ready" {
		t.Fatal("migration changed a live binding")
	}
	var count int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM native_workspace_restores`).Scan(&count); err != nil || count != 0 {
		t.Fatal("migration invented a restore receipt")
	}
	if _, err := reopened.ReadNativeWorkspaceSnapshot(ctx, access, command.Version, command.SHA256); err != nil {
		t.Fatal(err)
	}
}

func TestNativeExportDoesNotAdvanceSnapshotAndSaveReceiptIsExact(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	engine := newNativeEngineFixture(t)
	manager := nativeManagerForTest(t, store, engine)
	if _, err := manager.Ensure(ctx, access); err != nil {
		t.Fatal(err)
	}
	exported, err := manager.Export(ctx, access)
	if err != nil || exported.SHA256 != engine.archive.SHA256 {
		t.Fatalf("export: %v", err)
	}
	lease, err := store.ValidateNativeWorkspaceLease(ctx, access)
	if err != nil || lease.SnapshotVersion != 0 {
		t.Fatal("SDK archive export advanced the persistent version")
	}
	_, found, err := store.ReadNativeWorkspaceSnapshotReceipt(ctx, access, "sdk-persist", 0, exported.SHA256)
	if err != nil || found {
		t.Fatalf("missing receipt: %t %v", found, err)
	}
	saved, err := store.SaveNativeWorkspaceSnapshot(ctx, access, "sdk-persist", 0, exported.Archive)
	if err != nil {
		t.Fatal(err)
	}
	read, found, err := store.ReadNativeWorkspaceSnapshotReceipt(ctx, access, "sdk-persist", 0, exported.SHA256)
	if err != nil || !found || read.Version != saved.Version || !bytes.Equal(read.Snapshot.Archive, saved.Snapshot.Archive) {
		t.Fatalf("exact receipt: %t %v", found, err)
	}
	_, _, err = store.ReadNativeWorkspaceSnapshotReceipt(ctx, access, "sdk-persist", 0, strings.Repeat("e", 64))
	assertDomainCode(t, err, "NATIVE_WORKSPACE_REQUEST_CONFLICT")
	if _, err := store.db.Exec(`UPDATE native_workspace_snapshots SET entries_json='[]'`); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.ReadNativeWorkspaceSnapshotReceipt(ctx, access, "sdk-persist", 0, exported.SHA256)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_SNAPSHOT_INVALID")
}
