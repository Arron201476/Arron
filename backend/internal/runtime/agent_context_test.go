package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"content-agent/backend/internal/agentcontract"
)

func TestAgentRuntimeContextSeparatesActiveAndViewedRun(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, _, active := startNovelRunForLifecycle(t, store, "agent context")
	const historicalRunID = "run_historical_view"
	insertRawRun(t, store.db, project, historicalRunID, "completed")
	historicalView := historicalRunID

	runtimeContext, err := store.GetAgentRuntimeContextForView(ctx, project.ProjectID, &historicalView)
	if err != nil {
		t.Fatalf("GetAgentRuntimeContextForView() error = %v", err)
	}
	if runtimeContext.ActiveRun == nil || runtimeContext.ActiveRun.RunID != active.Run.RunID {
		t.Fatalf("active run = %+v, want %q", runtimeContext.ActiveRun, active.Run.RunID)
	}
	if runtimeContext.ViewedRun == nil || runtimeContext.ViewedRun.RunID != historicalRunID {
		t.Fatalf("viewed run = %+v, want %q", runtimeContext.ViewedRun, historicalRunID)
	}
	if len(runtimeContext.RunIndex) != 2 {
		t.Fatalf("run index = %+v", runtimeContext.RunIndex)
	}
}

func TestAgentRuntimeContextKeepsCancelledRunArtifactsAvailable(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, err := store.CreateProject(ctx, "cancelled artifact context")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	artifact, version := seedMessageContextArtifact(t, store, project)
	now := formatTime(time.Now().UTC())
	if _, err := store.db.Exec(`UPDATE runs SET status = 'cancelled', updated_at = ?, ended_at = ? WHERE run_id = ?`, now, now, artifact.RunID); err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE artifact_versions SET status = 'confirmed', confirmed_at = ? WHERE artifact_version_id = ?`, now, version.ArtifactVersionID); err != nil {
		t.Fatalf("confirm artifact: %v", err)
	}

	runtimeContext, err := store.GetAgentRuntimeContextForRequest(ctx, project.ProjectID, agentcontract.MessageRequest{
		Content: "刚才生成的剧本你觉得如何？",
	})
	if err != nil {
		t.Fatalf("GetAgentRuntimeContextForRequest() error = %v", err)
	}
	if runtimeContext.ActiveRun != nil {
		t.Fatalf("active run = %+v, want nil", runtimeContext.ActiveRun)
	}
	if len(runtimeContext.ArtifactSets) != 1 || runtimeContext.ArtifactSets[0].Count != 1 ||
		runtimeContext.ArtifactSets[0].Status != "confirmed" {
		t.Fatalf("artifact sets = %+v", runtimeContext.ArtifactSets)
	}
	if runtimeContext.ArtifactFocus != "semantic_reference" || len(runtimeContext.FocusedArtifacts) != 1 {
		t.Fatalf("focused artifacts = %+v, focus=%q", runtimeContext.FocusedArtifacts, runtimeContext.ArtifactFocus)
	}
	focused := runtimeContext.FocusedArtifacts[0]
	if focused.ArtifactVersionID != version.ArtifactVersionID || focused.RunID != artifact.RunID ||
		!json.Valid(focused.Payload) || string(focused.Payload) != `{"script_text":"测试目标台词"}` {
		t.Fatalf("focused artifact = %+v", focused)
	}
}

func TestAgentRuntimeContextUsesCurrentViewForGenericArtifactReferences(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, err := store.CreateProject(ctx, "current view artifact context")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	artifact, version := seedMessageContextArtifact(t, store, project)
	runtimeContext, err := store.GetAgentRuntimeContextForRequest(ctx, project.ProjectID, agentcontract.MessageRequest{
		Content: "这里写得怎么样？",
		ClientContext: agentcontract.ClientContext{
			CurrentArtifactID:        &artifact.ArtifactID,
			CurrentArtifactVersionID: &version.ArtifactVersionID,
		},
	})
	if err != nil {
		t.Fatalf("GetAgentRuntimeContextForRequest() error = %v", err)
	}
	if runtimeContext.ArtifactFocus != "current_view" || len(runtimeContext.FocusedArtifacts) != 1 ||
		runtimeContext.FocusedArtifacts[0].ArtifactID != artifact.ArtifactID {
		t.Fatalf("runtime context = %+v", runtimeContext)
	}
}

func TestAgentRuntimeContextVerifiesConfirmedAssetSetForMessage(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, err := store.CreateProject(ctx, "confirmed video input context")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	video := createVideoAsset(t, store, project.ProjectID, "第1集.mp4")
	created, err := store.CreateAssetSet(ctx, CreateAssetSetCommand{
		ProjectID: project.ProjectID, Purpose: "video_reference_source", DisplayName: "参考剧视频",
	})
	if err != nil {
		t.Fatalf("CreateAssetSet() error = %v", err)
	}
	updated, err := store.CreateAssetSetVersion(ctx, CreateAssetSetVersionCommand{
		AssetSetID: created.AssetSet.AssetSetID, ExpectedCurrentVersion: created.AssetSet.CurrentVersion,
		Changes: []AssetSetChange{{Operation: "add_asset", AssetID: video.Asset.AssetID}},
	})
	if err != nil {
		t.Fatalf("CreateAssetSetVersion() error = %v", err)
	}
	sealed, err := store.SealAssetSet(ctx, SealAssetSetCommand{
		AssetSetID: updated.AssetSet.AssetSetID, ExpectedCurrentVersion: updated.AssetSet.CurrentVersion,
		UserConfirmedUploadComplete: true,
	})
	if err != nil {
		t.Fatalf("SealAssetSet() error = %v", err)
	}
	versionID := sealed.Version.AssetSetVersionID
	runtimeContext, err := store.GetAgentRuntimeContextForRequest(ctx, project.ProjectID, agentcontract.MessageRequest{
		AttachmentRefs: []agentcontract.AttachmentRef{{AssetID: video.Asset.AssetID, AssetSnapshotID: video.Snapshot.AssetSnapshotID}},
		ClientContext:  agentcontract.ClientContext{CurrentAssetSetVersionID: &versionID},
	})
	if err != nil {
		t.Fatalf("GetAgentRuntimeContextForRequest() error = %v", err)
	}
	if runtimeContext.ConfirmedAssetSet == nil || runtimeContext.ConfirmedAssetSet.AssetSetVersionID != versionID ||
		runtimeContext.ConfirmedAssetSet.MemberCount != 1 {
		t.Fatalf("confirmed asset set = %+v", runtimeContext.ConfirmedAssetSet)
	}

	containerID := "archive-container"
	runtimeContext, err = store.GetAgentRuntimeContextForRequest(ctx, project.ProjectID, agentcontract.MessageRequest{
		AttachmentRefs: []agentcontract.AttachmentRef{
			{AssetID: containerID, AssetSnapshotID: "archive-snapshot"},
			{AssetID: video.Asset.AssetID, AssetSnapshotID: video.Snapshot.AssetSnapshotID, Hidden: true, ContainerAssetID: &containerID},
		},
		ClientContext: agentcontract.ClientContext{CurrentAssetSetVersionID: &versionID},
	})
	if err != nil {
		t.Fatalf("GetAgentRuntimeContextForRequest(archive container) error = %v", err)
	}
	if runtimeContext.ConfirmedAssetSet == nil || runtimeContext.ConfirmedAssetSet.AssetSetID != sealed.AssetSet.AssetSetID {
		t.Fatalf("archive-backed confirmed asset set = %+v", runtimeContext.ConfirmedAssetSet)
	}

	runtimeContext, err = store.GetAgentRuntimeContextForRequest(ctx, project.ProjectID, agentcontract.MessageRequest{
		ClientContext: agentcontract.ClientContext{CurrentAssetSetVersionID: &versionID},
	})
	if err != nil {
		t.Fatalf("GetAgentRuntimeContextForRequest(missing attachments) error = %v", err)
	}
	if runtimeContext.ConfirmedAssetSet != nil {
		t.Fatalf("unbound asset set was trusted: %+v", runtimeContext.ConfirmedAssetSet)
	}
}

func TestAgentRuntimeContextDoesNotMixArtifactsAcrossRuns(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	project, err := store.CreateProject(ctx, "isolated run artifact context")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	first, _ := seedMessageContextArtifact(t, store, project)
	second, _ := seedMessageContextArtifact(t, store, project)
	viewedRunID := first.RunID
	runtimeContext, err := store.GetAgentRuntimeContextForRequest(ctx, project.ProjectID, agentcontract.MessageRequest{
		Content: "评价这次生成的剧本",
		ClientContext: agentcontract.ClientContext{
			ViewedRunID: &viewedRunID,
		},
	})
	if err != nil {
		t.Fatalf("GetAgentRuntimeContextForRequest() error = %v", err)
	}
	if len(runtimeContext.FocusedArtifacts) != 1 || runtimeContext.FocusedArtifacts[0].RunID != first.RunID {
		t.Fatalf("focused artifacts = %+v", runtimeContext.FocusedArtifacts)
	}
	if runtimeContext.FocusedArtifacts[0].RunID == second.RunID {
		t.Fatalf("focused artifacts mixed viewed and unrelated runs: %+v", runtimeContext.FocusedArtifacts)
	}
}

func TestReferencedArtifactTypesPreferSpecificAliases(t *testing.T) {
	tests := []struct {
		content string
		want    string
		reject  string
	}{
		{content: "查看剧本分析", want: "script_analysis", reject: "script_unit"},
		{content: "评价完整剧本", want: "scripts", reject: "script_unit"},
		{content: "看看视频剧本", want: "video_script_unit", reject: "script_unit"},
		{content: "检查改编方案", want: "adaptation_options"},
		{content: "查看剧本交接", want: "script_handoff", reject: "script_unit"},
	}
	for _, test := range tests {
		t.Run(test.content, func(t *testing.T) {
			got := referencedArtifactTypes(test.content)
			if _, ok := got[test.want]; !ok {
				t.Fatalf("referencedArtifactTypes(%q) = %v, want %q", test.content, got, test.want)
			}
			if test.reject != "" {
				if _, ok := got[test.reject]; ok {
					t.Fatalf("referencedArtifactTypes(%q) = %v, must not include generic %q", test.content, got, test.reject)
				}
			}
		})
	}
}
