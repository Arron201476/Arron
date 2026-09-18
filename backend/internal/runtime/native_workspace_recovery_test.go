package runtime

import (
	"strings"
	"testing"
)

func TestNativeWorkspaceRecoveryThreeModesRequiresSealedManifestAndExactSnapshot(t *testing.T) {
	for _, mode := range []string{"conversation", "background", "stateful"} {
		t.Run(mode, func(t *testing.T) {
			store, _, ctx, access := nativeWorkspaceFixture(t, mode)
			engine := &nativeFileEngineFixture{nativeEngineFixture: newNativeEngineFixture(t)}
			manager := nativeFileManager(t, store, engine)
			_, err := manager.ReadRecovery(ctx, access)
			assertDomainCode(t, err, "NATIVE_WORKSPACE_MANIFEST_UNCONFIRMED")
			manifest, err := manager.PrepareManifest(ctx, access, NativeWorkspaceManifestSources{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Ensure(ctx, access); err != nil {
				t.Fatal(err)
			}
			_, err = manager.ReadRecovery(ctx, access)
			assertDomainCode(t, err, "NATIVE_WORKSPACE_MANIFEST_UNCONFIRMED")
			if err := manager.SealManifest(ctx, access, manifest.ManifestHash); err != nil {
				t.Fatal(err)
			}
			_, err = manager.ReadRecovery(ctx, access)
			assertDomainCode(t, err, "NATIVE_WORKSPACE_MANIFEST_UNCONFIRMED")
			for version := 0; version < 2; version++ {
				body := nativeTestArchive(t, []byte(strings.Repeat("body", version+1)))
				saved, err := store.SaveNativeWorkspaceSnapshot(ctx, access, strings.Repeat("s", version+1), version, body)
				if err != nil {
					t.Fatal(err)
				}
				recovery, err := manager.ReadRecovery(ctx, access)
				if err != nil || recovery.Manifest.ManifestHash != manifest.ManifestHash || recovery.Manifest.SessionID != access.SessionID || recovery.Snapshot.Version != saved.Version || recovery.Snapshot.SHA256 != saved.Snapshot.SHA256 {
					t.Fatalf("recovery references differ: %+v, %v", recovery, err)
				}
			}
			wrong := access
			wrong.HolderKey = strings.Repeat("f", 64)
			if _, err := manager.ReadRecovery(ctx, wrong); err == nil {
				t.Fatal("foreign holder read recovery")
			}
			if _, err := store.db.Exec(`UPDATE native_workspace_snapshots SET archive=? WHERE session_id=? AND version=2`, []byte("corrupt"), access.SessionID); err != nil {
				t.Fatal(err)
			}
			_, err = manager.ReadRecovery(ctx, access)
			assertDomainCode(t, err, "NATIVE_WORKSPACE_SNAPSHOT_INVALID")
			if len(engine.operations) != 0 {
				t.Fatal("reading recovery executed file operations")
			}
		})
	}
}
