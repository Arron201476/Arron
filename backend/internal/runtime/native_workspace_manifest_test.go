package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/identity"
	"content-agent/backend/internal/scriptsandbox"
)

func nativeManifestProjectFile(t *testing.T, store *Store, ctx context.Context, content string, version int) ProjectFile {
	t.Helper()
	activity, _ := AgentActivityFromContext(ctx)
	project, err := store.GetProject(context.Background(), activity.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	operation := "create_file"
	if version > 0 {
		operation = "update_file"
	}
	patch := ProjectFilePatch{Path: "docs/notes.txt", ExpectedVersion: version, Operation: operation, Diff: "+fixture"}
	call := beginProjectFileCall(t, store, project, patch)
	file, err := store.ApplyProjectFilePatch(context.Background(), call.AgentToolCallID, call.SDKToolCallID, patch, content)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func nativeManifestSource(file ProjectFile) NativeWorkspaceManifestSources {
	return NativeWorkspaceManifestSources{Files: []NativeWorkspaceProjectSource{{Path: file.Path, Version: file.Version, ContentHash: file.ContentHash}}}
}

func nativeMaterializationWrites(content []byte) []scriptsandbox.WorkspaceFileOperation {
	dirMode, fileMode := int64(0755), int64(0644)
	return []scriptsandbox.WorkspaceFileOperation{
		{Operation: "mkdir", Path: "docs", Parents: true},
		{Operation: "chmod", Path: "docs", Mode: &dirMode},
		{Operation: "write", Path: "docs/notes.txt", Data: content},
		{Operation: "chmod", Path: "docs/notes.txt", Mode: &fileMode},
	}
}

func TestNativeManifestThreeModesVersionFreezeInitializeSealAndNoReplay(t *testing.T) {
	for _, mode := range []string{"conversation", "background", "stateful"} {
		t.Run(mode, func(t *testing.T) {
			store, database, ctx, access := nativeWorkspaceFixture(t, mode)
			engine := &nativeFileEngineFixture{nativeEngineFixture: newNativeEngineFixture(t)}
			manager := nativeFileManager(t, store, engine)
			file := nativeManifestProjectFile(t, store, ctx, "historical", 0)
			nativeManifestProjectFile(t, store, ctx, "new current version", 1)
			manifest, err := manager.PrepareManifest(ctx, access, nativeManifestSource(file))
			if err != nil || len(manifest.Files) != 1 || manifest.Files[0].SHA256 != sha256Hex([]byte("historical")) {
				t.Fatalf("manifest: %+v, %v", manifest, err)
			}
			body, err := manager.ReadManifestFile(ctx, access, manifest.ManifestHash, file.Path)
			if err != nil || string(body) != "historical" {
				t.Fatalf("resource: %q, %v", body, err)
			}
			if _, err := manager.Ensure(ctx, access); err != nil {
				t.Fatal(err)
			}
			assertDomainCode(t, manager.SealManifest(ctx, access, manifest.ManifestHash), "NATIVE_WORKSPACE_MANIFEST_UNCONFIRMED")
			for index, operation := range nativeMaterializationWrites(body) {
				request := NativeWorkspaceFileRequest{RequestID: fmt.Sprintf("%032x", index+1), MaterializationHash: manifest.ManifestHash, File: operation}
				if _, err := manager.File(ctx, access, request); err != nil {
					t.Fatal(err)
				}
				if _, err := manager.File(ctx, access, request); err == nil {
					t.Fatal("initial operation was replayed")
				}
			}
			if len(engine.operations) != 4 {
				t.Fatal("initial mutation replay reached engine")
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
			manager = nativeFileManager(t, reopened, engine)
			if err := manager.SealManifest(ctx, access, manifest.ManifestHash); err != nil {
				t.Fatal(err)
			}
			if err := manager.SealManifest(ctx, access, manifest.ManifestHash); err != nil {
				t.Fatal("exact seal receipt not recoverable", err)
			}
			_, err = manager.ReadManifestFile(ctx, access, manifest.ManifestHash, file.Path)
			assertDomainCode(t, err, "NATIVE_WORKSPACE_MANIFEST_UNCONFIRMED")
			var count int
			if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM native_workspace_file_operations WHERE session_id=?`, access.SessionID).Scan(&count); err != nil || count != 0 {
				t.Fatal("resource setup impersonated model approval")
			}
			patch := nativeFileCall(t, reopened, ctx, "model-edit", true)
			if _, err := manager.File(ctx, access, patch); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeManifestRejectsWrongSelectionAndDoesNotOverwriteLiveWorkspace(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	engine := &nativeFileEngineFixture{nativeEngineFixture: newNativeEngineFixture(t)}
	manager := nativeFileManager(t, store, engine)
	file := nativeManifestProjectFile(t, store, ctx, "fixture", 0)
	for _, mutation := range []string{"hash", "version", "path", "collision", "skill"} {
		selection := nativeManifestSource(file)
		switch mutation {
		case "hash":
			selection.Files[0].ContentHash = strings.Repeat("f", 64)
		case "version":
			selection.Files[0].Version = 0
		case "path":
			selection.Files[0].Path = "../private"
		case "collision":
			selection.Files = append(selection.Files, selection.Files[0])
		case "skill":
			selection.Skills = []NativeWorkspaceSkillSource{{CapabilityID: "missing", Version: "1", ContentHash: "sha256:" + strings.Repeat("a", 64)}}
		}
		if _, err := manager.PrepareManifest(ctx, access, selection); err == nil {
			t.Fatalf("accepted %s", mutation)
		}
	}
	manifest, err := manager.PrepareManifest(ctx, access, nativeManifestSource(file))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Ensure(ctx, access); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []scriptsandbox.WorkspaceFileOperation{
		{Operation: "write", Path: "docs/notes.txt", Data: []byte("changed")},
		{Operation: "write", Path: "arbitrary", Data: []byte("fixture")},
		{Operation: "remove", Path: "docs", Recursive: true},
	} {
		if _, err := manager.File(ctx, access, NativeWorkspaceFileRequest{RequestID: strings.Repeat("e", 32), MaterializationHash: manifest.ManifestHash, File: operation}); err == nil {
			t.Fatal("manifest granted arbitrary mutation")
		}
	}
	patch := nativeFileCall(t, store, ctx, "too-early", true)
	_, err = manager.File(ctx, access, patch)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_MANIFEST_UNCONFIRMED")
	if _, err := manager.Export(ctx, access); err == nil {
		t.Fatal("partial manifest exported")
	}
	if len(engine.operations) != 0 {
		t.Fatal("invalid initial mutations reached the engine")
	}
}

func TestNativeManifestUnknownResultFailsWithoutClearingOrReplaying(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	engine := &nativeFileEngineFixture{nativeEngineFixture: newNativeEngineFixture(t)}
	manager := nativeFileManager(t, store, engine)
	file := nativeManifestProjectFile(t, store, ctx, "fixture", 0)
	manifest, err := manager.PrepareManifest(ctx, access, nativeManifestSource(file))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Ensure(ctx, access); err != nil {
		t.Fatal(err)
	}
	engine.operation = func(context.Context, scriptsandbox.WorkspaceFileOperation) (scriptsandbox.WorkspaceFileResult, error) {
		return scriptsandbox.WorkspaceFileResult{}, fmt.Errorf("lost engine response")
	}
	request := NativeWorkspaceFileRequest{RequestID: strings.Repeat("e", 32), MaterializationHash: manifest.ManifestHash, File: nativeMaterializationWrites([]byte("fixture"))[0]}
	if _, err := manager.File(ctx, access, request); err == nil {
		t.Fatal("lost initialization succeeded")
	}
	if _, err := manager.File(ctx, access, request); err == nil {
		t.Fatal("lost initialization replayed")
	}
	var status string
	if err := store.db.QueryRow(`SELECT status FROM native_workspace_manifests WHERE session_id=?`, access.SessionID).Scan(&status); err != nil || status != "failed" {
		t.Fatal("unknown initialization remained authorized", err)
	}
	if len(engine.operations) != 1 || engine.deletes != 0 {
		t.Fatal("unconfirmed initialization replayed or destroyed state")
	}
}

func TestNativeManifestInstalledFrozenSkillResourcesAndHashChecks(t *testing.T) {
	store, _, _, _ := nativeWorkspaceFixture(t, "conversation")
	root := writeSkillTestPackage(t, t.TempDir(), "manifest-skill", "manifest_skill", "1.0.0", "inline", "Read the binary asset.")
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "sample.bin"), []byte{0, 255, 128}, 0644); err != nil {
		t.Fatal(err)
	}
	installed, err := store.InstallSkillDirectory(context.Background(), root, "manifest-author")
	if err != nil {
		t.Fatal(err)
	}
	activity, _ := AgentActivityFromContext(mcpExecutionContext(t, store, mcpOwnerContext()))
	ctx := WithAgentActivity(identity.WithPrincipal(context.Background(), identity.ServicePrincipal()), activity)
	turn, err := store.GetAgentTurn(context.Background(), activity.AgentTurnID)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.ReserveNativeWorkspace(ctx, ReserveNativeWorkspaceCommand{HolderKey: strings.Repeat("f", 64), DispatchGeneration: turn.DispatchGeneration})
	if err != nil {
		t.Fatal(err)
	}
	access := NativeWorkspaceAccess{SessionID: lease.SessionID, Generation: lease.Generation, HolderKey: strings.Repeat("f", 64), DispatchGeneration: turn.DispatchGeneration}
	engine := &nativeFileEngineFixture{nativeEngineFixture: newNativeEngineFixture(t)}
	manager := nativeFileManager(t, store, engine)
	version := activeSkillVersionForTest(t, installed)
	manifest, err := manager.PrepareManifest(ctx, access, NativeWorkspaceManifestSources{Skills: []NativeWorkspaceSkillSource{{CapabilityID: installed.CapabilityID, Version: version.Version, ContentHash: version.ContentHash}}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := manager.ReadManifestFile(ctx, access, manifest.ManifestHash, ".skills/manifest-skill/assets/sample.bin")
	if err != nil || string(data) != string([]byte{0, 255, 128}) {
		t.Fatalf("binary Skill resource: %v, %v", data, err)
	}
	var body string
	if err := store.db.QueryRow(`SELECT manifest_json FROM native_workspace_manifests WHERE session_id=?`, access.SessionID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, root) {
		t.Fatal("host path leaked into SDK inventory")
	}
	var files []NativeWorkspaceManifestFile
	if json.Unmarshal([]byte(body), &files) != nil {
		t.Fatal("invalid inventory")
	}
	files[0].SHA256 = strings.Repeat("d", 64)
	corrupt, _ := json.Marshal(files)
	if _, err := store.db.Exec(`UPDATE native_workspace_manifests SET manifest_json=? WHERE session_id=?`, string(corrupt), access.SessionID); err != nil {
		t.Fatal(err)
	}
	_, err = manager.ReadManifestFile(ctx, access, manifest.ManifestHash, files[0].Path)
	assertDomainCode(t, err, "NATIVE_WORKSPACE_MANIFEST_INVALID")
}

func TestNativeManifestV60MigrationRetainsBackupAndDoesNotAuthorizeResources(t *testing.T) {
	store, database, ctx, access := nativeWorkspaceFixture(t, "conversation")
	registry := store.registry
	if _, err := store.db.Exec(`DROP TABLE native_workspace_manifests; PRAGMA user_version=60`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(database, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.ValidateNativeWorkspaceLease(ctx, access); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM native_workspace_manifests`).Scan(&count); err != nil || count != 0 {
		t.Fatal("migration invented resource authority", err)
	}
	var backup string
	if err := reopened.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version=60 AND to_version=? AND status='completed'`, schemaVersion).Scan(&backup); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatal(err)
	}
}

func TestNativeManifestProjectDeletionRejectsLateReceiptAndClearsSources(t *testing.T) {
	store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
	engine := &nativeFileEngineFixture{nativeEngineFixture: newNativeEngineFixture(t)}
	manager := nativeFileManager(t, store, engine)
	file := nativeManifestProjectFile(t, store, ctx, "fixture", 0)
	manifest, err := manager.PrepareManifest(ctx, access, nativeManifestSource(file))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Ensure(ctx, access); err != nil {
		t.Fatal(err)
	}
	engine.operation = func(_ context.Context, operation scriptsandbox.WorkspaceFileOperation) (scriptsandbox.WorkspaceFileResult, error) {
		activity, _ := AgentActivityFromContext(ctx)
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := closeNativeProjectWorkspacesTx(ctx, tx, activity.ProjectID, store.now()); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return nativeFileResult(operation), nil
	}
	request := NativeWorkspaceFileRequest{RequestID: strings.Repeat("e", 32), MaterializationHash: manifest.ManifestHash, File: nativeMaterializationWrites([]byte("fixture"))[0]}
	if _, err := manager.File(ctx, access, request); err == nil {
		t.Fatal("late initialization accepted after project closure")
	}
	var status, body, done string
	if err := store.db.QueryRow(`SELECT status,manifest_json,done_json FROM native_workspace_manifests WHERE session_id=?`, access.SessionID).Scan(&status, &body, &done); err != nil {
		t.Fatal(err)
	}
	if status != "deleted" || body != "" || done != "[]" {
		t.Fatal("late result restored deleted resource references")
	}
}
