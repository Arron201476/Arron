package runtime

import (
	"strings"
	"testing"
)

func TestNativeMemoryManifestUsesPrivateOwnerVersionAndRevokesRead(t *testing.T) {
	for _, mode := range []string{"conversation", "background", "stateful"} {
		t.Run(mode, func(t *testing.T) {
			store, _, ctx, access := nativeWorkspaceFixture(t, mode)
			activity, _ := AgentActivityFromContext(ctx)
			doc, err := store.UpdateAgentMemory(mcpOwnerContext(), UpdateAgentMemoryCommand{
				ProjectID: activity.ProjectID, Enabled: true, RequestID: "save",
				Files: map[string]string{"memory_summary.md": "private", "notes/detail.md": "detail"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.ResolveAgentMemorySnapshot(ctx); err != nil {
				t.Fatal(err)
			}
			manager := nativeFileManager(t, store, &nativeFileEngineFixture{nativeEngineFixture: newNativeEngineFixture(t)})
			source := &NativeWorkspaceMemorySource{Version: doc.Version, ContentHash: doc.ContentHash}
			manifest, err := manager.PrepareManifest(ctx, access, NativeWorkspaceManifestSources{Memory: source})
			if err != nil || len(manifest.Files) != 2 {
				t.Fatalf("manifest: %+v %v", manifest, err)
			}
			for _, file := range manifest.Files {
				if file.Memory == nil || file.Skill != nil || file.ProjectFile != nil || file.Path != nativeMemoryDirectory+"/"+file.ResourcePath {
					t.Fatalf("source: %+v", file)
				}
				body, err := manager.ReadManifestFile(ctx, access, manifest.ManifestHash, file.Path)
				if err != nil || string(body) != doc.Files[file.ResourcePath] {
					t.Fatalf("read: %q %v", body, err)
				}
			}
			wrong := *source
			wrong.Version++
			_, err = manager.PrepareManifest(ctx, access, NativeWorkspaceManifestSources{Memory: &wrong})
			assertDomainCode(t, err, "AGENT_MEMORY_CONFLICT")
			if _, err := store.UpdateAgentMemory(mcpOwnerContext(), UpdateAgentMemoryCommand{ProjectID: activity.ProjectID, ExpectedVersion: 1, Forget: true, RequestID: "forget"}); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.ReadManifestFile(ctx, access, manifest.ManifestHash, manifest.Files[0].Path); err == nil {
				t.Fatal("forgotten memory was readable")
			}
		})
	}
}

func TestPrivateMemoryCannotBeDirectlyPublished(t *testing.T) {
	for _, private := range []string{".agent-memory/memory_summary.md", ".AGENT-MEMORY/notes/detail.md"} {
		for _, file := range []NativeWorkspacePublicationFile{{SourcePath: private, Path: "export.md"}, {SourcePath: "notes.md", Path: private}} {
			err := validateNativePublicationArguments(NativeWorkspacePublicationArguments{
				Snapshot: NativeWorkspaceSnapshotReference{Version: 1, SHA256: strings.Repeat("a", 64)},
				Files:    []NativeWorkspacePublicationFile{file},
			})
			assertDomainCode(t, err, "NATIVE_WORKSPACE_PUBLICATION_INVALID")
		}
	}
}
