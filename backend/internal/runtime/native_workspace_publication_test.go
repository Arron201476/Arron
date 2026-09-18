package runtime

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"content-agent/backend/internal/capability"
)

func nativePublicationSnapshot(t *testing.T, store *Store, ctx context.Context, access NativeWorkspaceAccess) NativeWorkspacePublicationArguments {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	files := []struct{ name, body string }{
		{"SKILL.md", authoredSkillText}, {"references/checklist.md", "Check narrative causality."}, {"assets/sample.bin", string([]byte{0, 255, 128, 10})},
	}
	arguments := NativeWorkspacePublicationArguments{}
	for _, file := range files {
		if err := writer.WriteHeader(&tar.Header{Name: file.name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(file.body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(file.body)); err != nil {
			t.Fatal(err)
		}
		arguments.Files = append(arguments.Files, NativeWorkspacePublicationFile{SourcePath: file.name, Path: "draft/" + file.name})
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	saved, err := store.SaveNativeWorkspaceSnapshot(ctx, access, "publication-snapshot", 0, buffer.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	arguments.Snapshot = NativeWorkspaceSnapshotReference{Version: saved.Version, SHA256: saved.Snapshot.SHA256}
	return arguments
}

func nativePublicationCall(t *testing.T, store *Store, ctx context.Context, arguments NativeWorkspacePublicationArguments, approve bool) NativeWorkspacePublicationRequest {
	t.Helper()
	activity, _ := AgentActivityFromContext(ctx)
	project, err := store.GetProject(context.Background(), activity.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	call, err := store.BeginAgentToolCall(ctx, BeginAgentToolCallCommand{ProjectID: activity.ProjectID, ConversationID: project.PrimaryConversationID,
		AgentTurnID: activity.AgentTurnID, AgentTaskAttemptID: activity.AgentTaskAttemptID, ExecutionAttemptID: activity.ExecutionAttemptID,
		AttemptToken: activity.AttemptToken, SDKToolCallID: store.newID("sdk_pub"), ToolID: "runtime:publish_workspace_files", Arguments: raw})
	if err != nil {
		t.Fatal(err)
	}
	if approve {
		if _, err := store.ResolveAgentToolApproval(mcpOwnerContext(), ResolveAgentToolApprovalCommand{AgentToolApprovalID: call.Approval.AgentToolApprovalID,
			ExpectedVersion: call.Approval.Version, SubjectSnapshotHash: call.Approval.SubjectSnapshotHash, Action: "approve"}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.StartAgentToolCall(ctx, StartAgentToolCallCommand{AgentToolCallID: call.AgentToolCallID, ExpectedSDKToolCallID: call.SDKToolCallID}); err != nil {
			t.Fatal(err)
		}
	}
	return NativeWorkspacePublicationRequest{AgentToolCallID: call.AgentToolCallID, SDKToolCallID: call.SDKToolCallID, Arguments: arguments}
}

func TestNativePublicationThreeModesBinarySkillDraftAndDurableReceipt(t *testing.T) {
	for _, mode := range []string{"conversation", "background", "stateful"} {
		t.Run(mode, func(t *testing.T) {
			store, database, ctx, access := nativeWorkspaceFixture(t, mode)
			engine := &nativeFileEngineFixture{nativeEngineFixture: newNativeEngineFixture(t)}
			manager := nativeFileManager(t, store, engine)
			arguments := nativePublicationSnapshot(t, store, ctx, access)
			request := nativePublicationCall(t, store, ctx, arguments, true)
			first, err := manager.PublishFiles(ctx, access, request)
			if err != nil || len(first.Files) != 3 || !first.Files[2].Binary {
				t.Fatalf("publication: %+v %v", first, err)
			}
			projectID := first.Files[0].ProjectID
			page, err := store.ReadProjectFile(ctx, projectID, first.Files[2].Path, 1, 0, 16000)
			if err != nil || !page.File.Binary || page.Content != "" || page.Truncated || page.NextOffset != 0 {
				t.Fatalf("binary page: %+v %v", page, err)
			}
			file, body, err := store.ReadProjectFileBytes(ctx, projectID, page.File.Path, 1)
			if err != nil || !bytes.Equal(body, []byte{0, 255, 128, 10}) || file.ContentHash != sha256Hex(body) {
				t.Fatalf("binary download: %v", err)
			}
			preview, err := store.PreviewProjectSkillDraft(mcpOwnerContext(), projectID, "draft")
			if err != nil || preview.Status != "valid" || len(preview.Files) != 3 {
				t.Fatalf("existing Skill validation: %+v %v", preview, err)
			}
			_, archive, err := store.ExportProjectSkillDraft(mcpOwnerContext(), projectID, "draft", preview.SnapshotHash)
			if err != nil {
				t.Fatal(err)
			}
			reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
			if err != nil {
				t.Fatal(err)
			}
			stream, err := reader.Open("assets/sample.bin")
			if err != nil {
				t.Fatal(err)
			}
			resource, err := io.ReadAll(stream)
			stream.Close()
			if err != nil || !bytes.Equal(resource, body) {
				t.Fatal("Skill ZIP changed binary resource bytes", err)
			}
			installed, err := store.InstallProjectSkillDraft(mcpOwnerContext(), projectID,
				ProjectSkillInstallArguments{RootPath: "draft", SnapshotHash: preview.SnapshotHash, Scope: capability.SkillScopeProject}, "published-skill", true)
			if err != nil || !installed.Installation.Enabled || installed.Installation.RegistryStatus != "available" {
				t.Fatalf("existing Skill installation: %+v %v", installed, err)
			}
			resources, err := store.ListSkillResources(mcpOwnerContext(), projectID, preview.CapabilityID, preview.Version)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, item := range resources {
				if item.Path == "assets/sample.bin" && item.ContentHash == "sha256:"+file.ContentHash && item.SizeBytes == int64(len(body)) {
					found = true
				}
			}
			if !found {
				t.Fatal("installed Skill did not register the exact binary resource")
			}
			project, err := store.GetProject(mcpOwnerContext(), projectID)
			if err != nil {
				t.Fatal(err)
			}
			writeDraftFile(t, store, project, "draft/SKILL.md", authoredSkillText+"\nA later draft.", 1)
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
			again, err := manager.PublishFiles(ctx, access, request)
			if err != nil || !reflect.DeepEqual(again, first) {
				t.Fatalf("durable exact ordered receipt: %+v %v", again, err)
			}
			latest, err := reopened.ReadProjectFile(mcpOwnerContext(), projectID, "draft/SKILL.md", 0, 0, 16000)
			if err != nil || latest.File.Version != 2 || !strings.Contains(latest.Content, "A later draft.") {
				t.Fatal("publication replay overwrote a later version", err)
			}
			if len(engine.operations) != 0 {
				t.Fatal("publication accessed the live workspace engine")
			}
			if _, err := reopened.db.Exec(`UPDATE project_file_versions SET content=X'01' WHERE project_id=? AND path=?`, projectID, page.File.Path); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.PublishFiles(ctx, access, request); err == nil {
				t.Fatal("corrupt publication receipt was accepted")
			}
		})
	}
}

func TestNativePublicationApprovalVersionAndQuotaFailuresAreAtomic(t *testing.T) {
	for _, failure := range []string{"approval", "arguments", "missing-source", "stale-version", "destination-collision", "quota", "lease", "reserved"} {
		t.Run(failure, func(t *testing.T) {
			store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
			engine := &nativeFileEngineFixture{nativeEngineFixture: newNativeEngineFixture(t)}
			manager := nativeFileManager(t, store, engine)
			arguments := nativePublicationSnapshot(t, store, ctx, access)
			switch failure {
			case "missing-source":
				arguments.Files[2].SourcePath = "not-present.bin"
			case "stale-version":
				arguments.Files[2].ExpectedVersion = 8
			case "destination-collision":
				arguments.Files[2].Path = "draft/SKILL.md/child"
			case "reserved":
				arguments.Files[2].Path = ".skills/injected/SKILL.md"
			case "quota":
				activity, _ := AgentActivityFromContext(ctx)
				file := nativeManifestProjectFile(t, store, ctx, "existing", 0)
				if _, err := store.db.Exec(`UPDATE project_file_versions SET size_bytes=? WHERE project_id=? AND path=?`, maxProjectFileHistoryBytes, activity.ProjectID, file.Path); err != nil {
					t.Fatal(err)
				}
			}
			request := nativePublicationCall(t, store, ctx, arguments, failure != "approval")
			if failure == "arguments" {
				request.Arguments.Files[0].Path = "different.md"
			}
			if failure == "lease" {
				access.HolderKey = strings.Repeat("b", 64)
			}
			if _, err := manager.PublishFiles(ctx, access, request); err == nil {
				t.Fatal("invalid publication succeeded")
			}
			var count int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM project_file_versions WHERE agent_tool_call_id=?`, request.AgentToolCallID).Scan(&count); err != nil || count != 0 {
				t.Fatal("failed publication left partial files", count, err)
			}
			if len(engine.operations) != 0 {
				t.Fatal("invalid publication used the engine")
			}
		})
	}
}
