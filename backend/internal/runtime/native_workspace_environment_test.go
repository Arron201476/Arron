package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"content-agent/backend/internal/identity"
	"content-agent/backend/internal/scriptsandbox"
)

type nativeEngineFixture struct {
	mu                                    sync.Mutex
	handles                               map[string]scriptsandbox.WorkspaceHandle
	creates, reconnects, deletes, exports int
	archive                               scriptsandbox.WorkspaceSnapshot
	createError, deleteError              error
	exportError                           error
	hydrateError                          error
	hydrates                              int
	hydrated                              map[string]scriptsandbox.WorkspaceSnapshot
	loseHandle, corruptHandle             bool
	createStarted, createRelease          chan struct{}
	exportStarted, exportRelease          chan struct{}
	deleteStarted, deleteRelease          chan struct{}
}

func newNativeEngineFixture(t *testing.T) *nativeEngineFixture {
	t.Helper()
	snapshot, err := scriptsandbox.ParseWorkspaceSnapshot(nativeTestArchive(t, []byte{0, 255, 128}), scriptsandbox.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return &nativeEngineFixture{handles: make(map[string]scriptsandbox.WorkspaceHandle), hydrated: make(map[string]scriptsandbox.WorkspaceSnapshot), archive: snapshot}
}

func (engine *nativeEngineFixture) HydrateWorkspace(ctx context.Context, handle scriptsandbox.WorkspaceHandle, data []byte, hash string) (scriptsandbox.WorkspaceSnapshot, error) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.hydrates++
	if engine.handles[handle.SessionID] != handle {
		return scriptsandbox.WorkspaceSnapshot{}, errors.New("restore used another environment")
	}
	if engine.hydrateError != nil {
		return scriptsandbox.WorkspaceSnapshot{}, engine.hydrateError
	}
	if err := ctx.Err(); err != nil {
		return scriptsandbox.WorkspaceSnapshot{}, err
	}
	snapshot, err := scriptsandbox.ParseWorkspaceSnapshot(data, handle.Limits)
	if err != nil {
		return snapshot, err
	}
	if snapshot.SHA256 != hash {
		return snapshot, errors.New("restore hash mismatch")
	}
	if previous, exists := engine.hydrated[handle.SessionID]; exists && previous.SHA256 != snapshot.SHA256 {
		return scriptsandbox.WorkspaceSnapshot{}, &scriptsandbox.Error{Code: "WORKSPACE_HYDRATE_CONFLICT", Message: "existing files differ"}
	}
	engine.hydrated[handle.SessionID] = snapshot
	return snapshot, nil
}

func waitNativeEngine(ctx context.Context, started, release chan struct{}) error {
	if started != nil {
		close(started)
	}
	if release == nil {
		return nil
	}
	select {
	case <-release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (engine *nativeEngineFixture) CreateWorkspace(ctx context.Context, id, owner string, limits scriptsandbox.Limits) (scriptsandbox.WorkspaceHandle, error) {
	engine.mu.Lock()
	engine.creates++
	handle := scriptsandbox.WorkspaceHandle{SessionID: id, OwnerHash: owner, Limits: limits, ContainerID: sha256Hex([]byte(id)), PolicyHash: strings.Repeat("b", 64)}
	engine.handles[id] = handle
	engine.mu.Unlock()
	if err := waitNativeEngine(ctx, engine.createStarted, engine.createRelease); err != nil {
		return handle, err
	}
	if engine.loseHandle {
		return scriptsandbox.WorkspaceHandle{}, engine.createError
	}
	if engine.corruptHandle {
		handle.OwnerHash = strings.Repeat("c", 64)
	}
	return handle, engine.createError
}

func (engine *nativeEngineFixture) FindWorkspace(ctx context.Context, id, owner string, limits scriptsandbox.Limits) (scriptsandbox.WorkspaceHandle, bool, error) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	handle, ok := engine.handles[id]
	if !ok {
		return handle, false, errors.New("inspection unavailable")
	}
	return handle, true, nil
}

func (engine *nativeEngineFixture) ReconnectWorkspace(ctx context.Context, handle scriptsandbox.WorkspaceHandle) error {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.reconnects++
	if engine.handles[handle.SessionID] != handle {
		return errors.New("bound environment missing")
	}
	return ctx.Err()
}

func (engine *nativeEngineFixture) DeleteWorkspace(ctx context.Context, handle scriptsandbox.WorkspaceHandle) error {
	if err := waitNativeEngine(ctx, engine.deleteStarted, engine.deleteRelease); err != nil {
		return err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.deletes++
	if engine.deleteError != nil {
		return engine.deleteError
	}
	if engine.handles[handle.SessionID] != handle {
		return errors.New("unverified deletion")
	}
	delete(engine.handles, handle.SessionID)
	return nil
}

func (engine *nativeEngineFixture) ExportWorkspace(ctx context.Context, handle scriptsandbox.WorkspaceHandle) (scriptsandbox.WorkspaceSnapshot, error) {
	engine.mu.Lock()
	engine.exports++
	valid := engine.handles[handle.SessionID] == handle
	engine.mu.Unlock()
	if !valid {
		return scriptsandbox.WorkspaceSnapshot{}, errors.New("wrong environment")
	}
	if err := waitNativeEngine(ctx, engine.exportStarted, engine.exportRelease); err != nil {
		return scriptsandbox.WorkspaceSnapshot{}, err
	}
	if engine.exportError != nil {
		if sandboxErr, ok := engine.exportError.(*scriptsandbox.Error); ok && sandboxErr.Code == "WORKSPACE_TRANSFER_UNCONFIRMED" {
			engine.mu.Lock()
			delete(engine.handles, handle.SessionID)
			engine.mu.Unlock()
		}
		return scriptsandbox.WorkspaceSnapshot{}, engine.exportError
	}
	return engine.archive, nil
}

func TestNativeEnvironmentSnapshotFailuresPreserveFilesOrConfirmTermination(t *testing.T) {
	for _, mode := range []string{"quota", "busy", "confirmed-termination", "unknown-termination"} {
		t.Run(mode, func(t *testing.T) {
			store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
			engine := newNativeEngineFixture(t)
			manager := nativeManagerForTest(t, store, engine)
			if _, err := manager.Ensure(ctx, access); err != nil {
				t.Fatal(err)
			}
			expected := "ready"
			switch mode {
			case "quota":
				if _, err := store.db.Exec(`UPDATE workspace_quotas SET max_storage_bytes=1`); err != nil {
					t.Fatal(err)
				}
			case "busy":
				engine.exportError = &scriptsandbox.Error{Code: "WORKSPACE_BUSY", Message: "quiescence refused without mutations"}
			case "confirmed-termination":
				engine.exportError = &scriptsandbox.Error{Code: "WORKSPACE_TRANSFER_UNCONFIRMED", Message: "transfer unknown, termination confirmed"}
				expected = "closed"
			case "unknown-termination":
				engine.exportError = &scriptsandbox.Error{Code: "WORKSPACE_CLEANUP_UNCONFIRMED", Message: "termination remains unknown"}
				expected = "blocked"
			}
			if _, err := manager.Checkpoint(ctx, access, "failed-save", 0); err == nil {
				t.Fatal("failed snapshot reported success")
			}
			if state := nativeEnvironmentForTest(t, store, access); state.State != expected {
				t.Fatalf("failure status = %s, want %s", state.State, expected)
			}
			if mode == "quota" || mode == "busy" {
				janitor := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
				if report, err := manager.Sweep(janitor, 8); err != nil || report.Attempted != 0 || engine.deletes != 0 {
					t.Fatalf("safe failure destroyed files: %+v %v", report, err)
				}
			}
		})
	}
}

func nativeManagerForTest(t *testing.T, store *Store, engine NativeWorkspaceEngine) *NativeWorkspaceManager {
	t.Helper()
	manager, err := NewNativeWorkspaceManager(store, engine)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func nativeEnvironmentForTest(t *testing.T, store *Store, access NativeWorkspaceAccess) nativeEnvironment {
	t.Helper()
	state, err := scanNativeEnvironment(store.db.QueryRow(nativeEnvironmentSelect+` WHERE session_id=?`, access.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func awaitNativeSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("engine did not reach synchronization point")
	}
}

func awaitNativeResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("engine operation did not finish")
		return nil
	}
}

func TestNativeEnvironmentThreeOwnersReopenAndExactSnapshot(t *testing.T) {
	for _, mode := range []string{"conversation", "background", "stateful"} {
		t.Run(mode, func(t *testing.T) {
			store, database, ctx, access := nativeWorkspaceFixture(t, mode)
			engine := newNativeEngineFixture(t)
			manager := nativeManagerForTest(t, store, engine)
			result, err := manager.Ensure(ctx, access)
			if err != nil || result.State != "ready" {
				t.Fatalf("ensure: %+v %v", result, err)
			}
			state := nativeEnvironmentForTest(t, store, access)
			encoded, _ := json.Marshal(result)
			if bytes.Contains(encoded, []byte(state.handle.ContainerID)) || bytes.Contains(encoded, []byte(state.ownerHash)) {
				t.Fatal("private binding leaked")
			}
			saved, err := manager.Checkpoint(ctx, access, "native-checkpoint", 0)
			if err != nil || saved.Version != 1 || saved.Snapshot.SHA256 != engine.archive.SHA256 {
				t.Fatalf("checkpoint: %+v %v", saved, err)
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
			if _, err := manager.Ensure(ctx, access); err != nil {
				t.Fatal(err)
			}
			if engine.creates != 1 || engine.reconnects != 1 {
				t.Fatal("reopen silently created another environment")
			}
			read, err := reopened.ReadNativeWorkspaceSnapshot(ctx, access, 1, saved.Snapshot.SHA256)
			if err != nil || !bytes.Equal(read.Snapshot.Archive, engine.archive.Archive) {
				t.Fatalf("persisted binary: %v", err)
			}
			repeated, err := manager.Checkpoint(ctx, access, "native-checkpoint", 0)
			if err != nil || repeated.Version != saved.Version || engine.exports != 1 {
				t.Fatalf("lost checkpoint receipt replayed the export: %+v %v", repeated, err)
			}
			_, err = manager.Checkpoint(ctx, access, "native-checkpoint", 1)
			assertDomainCode(t, err, "NATIVE_WORKSPACE_REQUEST_CONFLICT")
			if err := manager.Cleanup(ctx, access.SessionID); err == nil {
				t.Fatal("delegated Agent became janitor")
			}
			if err := manager.Cleanup(identity.WithPrincipal(context.Background(), identity.ServicePrincipal()), access.SessionID); err == nil {
				t.Fatal("janitor removed live healthy environment")
			}
		})
	}
}

func TestNativeEnvironmentWorkspaceDeletionSweepsWithoutRestoringSnapshots(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	engine := newNativeEngineFixture(t)
	manager := nativeManagerForTest(t, store, engine)
	if _, err := manager.Ensure(ctx, access); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Checkpoint(ctx, access, "before-workspace-delete", 0); err != nil {
		t.Fatal(err)
	}
	owner := mcpOwnerContext()
	principal, _ := identity.FromContext(owner)
	if _, err := store.DeleteWorkspace(owner, DeleteWorkspaceCommand{WorkspaceID: principal.WorkspaceID, UserID: principal.UserID, Confirmation: principal.WorkspaceID}); err != nil {
		t.Fatal(err)
	}
	janitor := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
	report, err := manager.Sweep(janitor, 8)
	if err != nil || report.Attempted != 1 || report.Confirmed != 1 || report.Pending != 0 {
		t.Fatalf("sweep: %+v %v", report, err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM native_workspace_snapshots`).Scan(&count); err != nil || count != 0 {
		t.Fatal("deleted workspace retained snapshot bytes")
	}
	if nativeEnvironmentForTest(t, store, access).State != "closed" {
		t.Fatal("workspace environment not cleaned")
	}
	if _, err := manager.Ensure(ctx, access); err == nil {
		t.Fatal("deleted workspace reopened environment")
	}
}

func TestNativeEnvironmentV56MigrationPreservesPrivateSnapshotAndCreatesNoContainer(t *testing.T) {
	store, database, ctx, access := nativeWorkspaceFixture(t, "conversation")
	saved, err := store.SaveNativeWorkspaceSnapshot(ctx, access, "before-migration", 0, nativeTestArchive(t, []byte("original")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE native_workspace_environments; PRAGMA user_version=56;`); err != nil {
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
	if err := reopened.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version=56 AND to_version=? AND status='completed'`, schemaVersion).Scan(&backup); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM native_workspace_environments`).Scan(&count); err != nil || count != 0 {
		t.Fatal("migration synthesized an environment")
	}
	read, err := reopened.ReadNativeWorkspaceSnapshot(ctx, access, 1, saved.Snapshot.SHA256)
	if err != nil || !bytes.Equal(read.Snapshot.Archive, saved.Snapshot.Archive) {
		t.Fatalf("migration changed snapshot: %v", err)
	}
	engine := newNativeEngineFixture(t)
	manager := nativeManagerForTest(t, reopened, engine)
	_, err = manager.Ensure(ctx, access)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_RECOVERY_REQUIRED")
	if engine.creates != 0 {
		t.Fatal("saved snapshot was replaced by an empty environment")
	}
}

func TestNativeEnvironmentPendingCreateSurvivesStoreRestartAndUnknownAbsence(t *testing.T) {
	store, database, ctx, access := nativeWorkspaceFixture(t, "conversation")
	engine := newNativeEngineFixture(t)
	manager := nativeManagerForTest(t, store, engine)
	if _, err := manager.begin(ctx, access, "ensure"); err != nil {
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
	_, err = manager.Ensure(ctx, access)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_OPERATION_IN_PROGRESS")
	if _, err := reopened.db.Exec(`UPDATE native_workspace_leases SET lease_until='2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	report, err := manager.Sweep(identity.WithPrincipal(context.Background(), identity.ServicePrincipal()), 8)
	if err != nil || report.Confirmed != 0 || report.Pending != 1 {
		t.Fatalf("absence falsely confirmed cleanup: %+v %v", report, err)
	}
	if engine.creates != 0 || engine.deletes != 0 || nativeEnvironmentForTest(t, reopened, access).State != "closing" {
		t.Fatal("unknown creation was replayed or treated as safely absent")
	}
}

func TestNativeEnvironmentDurableOperationExcludesOtherManagerAndDirectSave(t *testing.T) {
	store, database, ctx, access := nativeWorkspaceFixture(t, "conversation")
	engine := newNativeEngineFixture(t)
	manager := nativeManagerForTest(t, store, engine)
	if _, err := manager.Ensure(ctx, access); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(database, store.registry)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	other := nativeManagerForTest(t, reopened, engine)
	engine.exportStarted, engine.exportRelease = make(chan struct{}), make(chan struct{})
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := manager.Checkpoint(runCtx, access, "concurrent", 0); done <- err }()
	awaitNativeSignal(t, engine.exportStarted)
	if _, err := store.db.Exec(`UPDATE native_workspace_environments SET operation_until='2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	_, err = other.Ensure(ctx, access)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_OPERATION_IN_PROGRESS")
	_, err = other.Checkpoint(ctx, access, "duplicate", 0)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_OPERATION_IN_PROGRESS")
	_, err = reopened.SaveNativeWorkspaceSnapshot(ctx, access, "direct", 0, engine.archive.Archive)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_OPERATION_IN_PROGRESS")
	close(engine.exportRelease)
	if err := awaitNativeResult(t, done); err != nil {
		t.Fatal(err)
	}
	if engine.exports != 1 || engine.reconnects != 0 {
		t.Fatal("outstanding operation was replayed after deadline")
	}
}

func TestNativeEnvironmentLostCreateReceiptAndUnconfirmedCleanupNeverReplay(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	engine := newNativeEngineFixture(t)
	engine.loseHandle, engine.createError = true, errors.New("create receipt lost")
	manager := nativeManagerForTest(t, store, engine)
	if _, err := manager.Ensure(ctx, access); err == nil {
		t.Fatal("lost creation reported success")
	}
	if nativeEnvironmentForTest(t, store, access).State != "blocked" {
		t.Fatal("uncertain creation was not blocked")
	}
	_, err := manager.Ensure(ctx, access)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_RECOVERY_REQUIRED")
	janitor := identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
	engine.deleteError = errors.New("delete receipt lost")
	err = manager.Cleanup(janitor, access.SessionID)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_CLEANUP_UNCONFIRMED")
	state := nativeEnvironmentForTest(t, store, access)
	if state.State != "closing" {
		t.Fatal("failed cleanup released capacity")
	}
	assertDomainCode(t, manager.Cleanup(janitor, access.SessionID), "NATIVE_WORKSPACE_OPERATION_IN_PROGRESS")
	if _, err := store.db.Exec(`UPDATE native_workspace_environments SET operation_until='2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	engine.deleteError = nil
	if err := manager.Cleanup(janitor, access.SessionID); err != nil {
		t.Fatal(err)
	}
	if nativeEnvironmentForTest(t, store, access).State != "closed" || engine.creates != 1 || engine.deletes != 2 {
		t.Fatal("cleanup did not use original identity")
	}
	_, err = manager.Ensure(ctx, access)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_RECOVERY_REQUIRED")
}

func TestNativeEnvironmentLeaseTakeoverCancelsOldCreationWithoutNewEnvironment(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	engine := newNativeEngineFixture(t)
	engine.createStarted, engine.createRelease = make(chan struct{}), make(chan struct{})
	manager := nativeManagerForTest(t, store, engine)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := manager.Ensure(runCtx, access); done <- err }()
	awaitNativeSignal(t, engine.createStarted)
	if _, err := store.db.Exec(`UPDATE native_workspace_leases SET lease_until='2000-01-01T00:00:00Z' WHERE session_id=?`, access.SessionID); err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("d", 64)
	lease, err := store.ReserveNativeWorkspace(ctx, ReserveNativeWorkspaceCommand{HolderKey: key, ExpectedGeneration: access.Generation, DispatchGeneration: access.DispatchGeneration})
	if err != nil {
		t.Fatal(err)
	}
	next := access
	next.HolderKey, next.Generation = key, lease.Generation
	if err := awaitNativeResult(t, done); err == nil {
		t.Fatal("superseded creation returned success")
	}
	_, err = manager.Ensure(ctx, next)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_RECOVERY_REQUIRED")
	if engine.creates != 1 {
		t.Fatal("takeover created empty replacement")
	}
	if err := manager.Cleanup(identity.WithPrincipal(context.Background(), identity.ServicePrincipal()), access.SessionID); err != nil {
		t.Fatal(err)
	}
}

func TestNativeEnvironmentCorruptHandleAndMissingContainerFailClosed(t *testing.T) {
	for _, mode := range []string{"wrong-handle", "missing", "empty-receipt"} {
		t.Run(mode, func(t *testing.T) {
			store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
			engine := newNativeEngineFixture(t)
			manager := nativeManagerForTest(t, store, engine)
			engine.corruptHandle = mode == "wrong-handle"
			engine.loseHandle = mode == "empty-receipt"
			_, err := manager.Ensure(ctx, access)
			if mode == "missing" {
				if err != nil {
					t.Fatal(err)
				}
				engine.handles = make(map[string]scriptsandbox.WorkspaceHandle)
				_, err = manager.Ensure(ctx, access)
			}
			if err == nil || nativeEnvironmentForTest(t, store, access).State != "blocked" {
				t.Fatal("invalid environment reported ready")
			}
			if engine.creates != 1 {
				t.Fatal("missing container was replaced")
			}
		})
	}
}

func TestNativeEnvironmentDeleteDuringSnapshotRejectsLateSave(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	engine := newNativeEngineFixture(t)
	manager := nativeManagerForTest(t, store, engine)
	if _, err := manager.Ensure(ctx, access); err != nil {
		t.Fatal(err)
	}
	engine.exportStarted, engine.exportRelease = make(chan struct{}), make(chan struct{})
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := manager.Checkpoint(runCtx, access, "late", 0); done <- err }()
	awaitNativeSignal(t, engine.exportStarted)
	activity, _ := AgentActivityFromContext(ctx)
	preview, err := store.CreateProjectDeletePreview(mcpOwnerContext(), CreateProjectDeletePreviewCommand{ProjectID: activity.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmProjectDelete(mcpOwnerContext(), ConfirmProjectDeleteCommand{ProjectID: activity.ProjectID, PreviewHash: preview.SnapshotHash, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	if err := awaitNativeResult(t, done); err == nil {
		t.Fatal("deleted project received snapshot")
	}
	if err := manager.Cleanup(identity.WithPrincipal(context.Background(), identity.ServicePrincipal()), access.SessionID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM native_workspace_snapshots`).Scan(&count); err != nil || count != 0 {
		t.Fatal("late snapshot retained content")
	}
	if nativeEnvironmentForTest(t, store, access).State != "closed" {
		t.Fatal("deleted project environment not closed")
	}
}
