package runtime

import (
	"errors"
	"strings"
	"testing"
)

func TestNativeWorkspaceProbeShutdownAndRestoreLifecycle(t *testing.T) {
	for _, mode := range []string{"conversation", "background", "stateful"} {
		t.Run(mode, func(t *testing.T) {
			store, _, ctx, access := nativeWorkspaceFixture(t, mode)
			engine := newNativeEngineFixture(t)
			manager, err := NewNativeWorkspaceManager(store, engine)
			if err != nil {
				t.Fatal(err)
			}
			probe, err := manager.Probe(ctx, access)
			if err != nil || probe.State != "absent" || engine.creates != 0 {
				t.Fatalf("probe created a workspace: %+v %v", probe, err)
			}
			if _, err := manager.Shutdown(ctx, access); err != nil || engine.deletes != 0 {
				t.Fatalf("absent shutdown: %v", err)
			}
			if _, err := manager.Ensure(ctx, access); err != nil {
				t.Fatal(err)
			}
			probe, err = manager.Probe(ctx, access)
			if err != nil || probe.State != "ready" || engine.creates != 1 || engine.reconnects != 1 {
				t.Fatalf("ready probe did not reconnect exact handle: %+v %v", probe, err)
			}
			snapshot, err := manager.Checkpoint(ctx, access, "before-shutdown", 0)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				closed, err := manager.Shutdown(ctx, access)
				if err != nil || closed.State != "closed" || engine.deletes != 1 {
					t.Fatalf("shutdown: %+v %v", closed, err)
				}
			}
			probe, err = manager.Probe(ctx, access)
			if err != nil || probe.State != "closed" || engine.creates != 1 {
				t.Fatalf("closed probe recreated files: %+v %v", probe, err)
			}
			if _, err := manager.Ensure(ctx, access); err == nil {
				t.Fatal("closed workspace recreated empty")
			}
			if _, err := manager.Restore(ctx, access, RestoreNativeWorkspaceCommand{RequestID: strings.Repeat("d", 32), Version: snapshot.Version, SHA256: snapshot.Snapshot.SHA256}); err != nil {
				t.Fatal(err)
			}
			if engine.creates != 2 || engine.hydrates != 1 {
				t.Fatal("explicit restore did not hydrate replacement")
			}
		})
	}
}

func TestNativeWorkspaceShutdownUnknownIsNotClosedOrReplayed(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	engine := newNativeEngineFixture(t)
	manager, err := NewNativeWorkspaceManager(store, engine)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Ensure(ctx, access); err != nil {
		t.Fatal(err)
	}
	engine.deleteError = errors.New("provider outcome unknown")
	if _, err := manager.Shutdown(ctx, access); err == nil {
		t.Fatal("unknown deletion was accepted")
	}
	if _, err := manager.Shutdown(ctx, access); err == nil || engine.deletes != 1 {
		t.Fatal("unknown deletion was replayed")
	}
	if _, err := manager.Probe(ctx, access); err == nil {
		t.Fatal("blocked workspace reported alive or absent")
	}
	var status string
	if err := store.db.QueryRow(`SELECT status FROM native_workspace_environments WHERE session_id=?`, access.SessionID).Scan(&status); err != nil || status != "blocked" {
		t.Fatalf("unknown shutdown lost state: %s %v", status, err)
	}
}

func TestNativeWorkspaceLifecycleRequiresExactLeaseAndRejectsBusyOperation(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	engine := newNativeEngineFixture(t)
	manager, err := NewNativeWorkspaceManager(store, engine)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Ensure(ctx, access); err != nil {
		t.Fatal(err)
	}
	wrong := access
	wrong.HolderKey = strings.Repeat("f", 64)
	if _, err := manager.Probe(ctx, wrong); err == nil {
		t.Fatal("wrong holder probed environment")
	}
	if _, err := manager.Shutdown(ctx, wrong); err == nil {
		t.Fatal("wrong holder closed environment")
	}
	state, err := manager.begin(ctx, access, "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Shutdown(ctx, access); err == nil {
		t.Fatal("shutdown raced an outstanding operation")
	}
	if _, err := manager.Probe(ctx, access); err == nil {
		t.Fatal("probe raced an outstanding operation")
	}
	if err := manager.finish(ctx, access, state, nil); err != nil {
		t.Fatal(err)
	}
	if engine.deletes != 0 || engine.reconnects != 0 {
		t.Fatal("invalid lifecycle request reached engine")
	}
}
