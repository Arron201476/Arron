package runtime

import (
	"strings"
	"testing"
	"time"
)

func TestNativeWorkspaceRenewalKeepsIdentityAndRejectsExpiredOrForeignHolder(t *testing.T) {
	for _, mode := range []string{"conversation", "background", "stateful"} {
		t.Run(mode, func(t *testing.T) {
			store, _, ctx, access := nativeWorkspaceFixture(t, mode)
			original, err := store.ValidateNativeWorkspaceLease(ctx, access)
			if err != nil {
				t.Fatal(err)
			}
			short := store.now().Add(10 * time.Second)
			if _, err := store.db.Exec(`UPDATE native_workspace_leases SET lease_until=? WHERE session_id=?`, formatTime(short), access.SessionID); err != nil {
				t.Fatal(err)
			}
			renewed, err := store.RenewNativeWorkspaceLease(ctx, access)
			if err != nil || renewed.SessionID != original.SessionID || renewed.Generation != original.Generation || renewed.SnapshotVersion != original.SnapshotVersion || !renewed.LeaseUntil.After(short) {
				t.Fatalf("renewal changed ownership or did not extend the lease: %+v %v", renewed, err)
			}
			wrong := access
			wrong.HolderKey = strings.Repeat("b", 64)
			_, err = store.RenewNativeWorkspaceLease(ctx, wrong)
			assertDomainCode(t, err, "NATIVE_WORKSPACE_LEASE_INVALID")
			if _, err := store.db.Exec(`UPDATE native_workspace_leases SET lease_until=? WHERE session_id=?`, formatTime(store.now().Add(-time.Second)), access.SessionID); err != nil {
				t.Fatal(err)
			}
			_, err = store.RenewNativeWorkspaceLease(ctx, access)
			assertDomainCode(t, err, "NATIVE_WORKSPACE_LEASE_EXPIRED")
			var count int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM native_workspace_environments`).Scan(&count); err != nil || count != 0 {
				t.Fatal("lease-only renewal created an environment")
			}
		})
	}
}

func TestNativeWorkspacePausingCanRenewOnlyExistingHolder(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	activity, _ := AgentActivityFromContext(ctx)
	if _, err := store.db.Exec(`UPDATE agent_turns SET status='pausing' WHERE agent_turn_id=?`, activity.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RenewNativeWorkspaceLease(ctx, access); err != nil {
		t.Fatal(err)
	}
	_, err := store.ReserveNativeWorkspace(ctx, ReserveNativeWorkspaceCommand{HolderKey: access.HolderKey, ExpectedGeneration: access.Generation, DispatchGeneration: access.DispatchGeneration})
	assertDomainCode(t, err, "NATIVE_WORKSPACE_EXECUTION_PAUSING")
	stale := access
	stale.DispatchGeneration++
	_, err = store.RenewNativeWorkspaceLease(ctx, stale)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_EXECUTION_STALE")
}
