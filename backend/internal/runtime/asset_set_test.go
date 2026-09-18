package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestDetectEpisodeFilenameCandidate(t *testing.T) {
	for filename, expected := range map[string]int{
		"第12集.mov":         12,
		"第 12 集.mp4":       12,
		"第０１２集.mp4":        12,
		"show_EP03_v2.mp4": 3,
		"episode_7.mov":    7,
		"E9-final.mp4":     9,
	} {
		candidate := DetectEpisodeFilenameCandidate(filename)
		if candidate.Confidence != "high" || candidate.EpisodeNo == nil || *candidate.EpisodeNo != expected {
			t.Fatalf("candidate(%q) = %+v, want %d", filename, candidate, expected)
		}
	}
	if candidate := DetectEpisodeFilenameCandidate("2026-08-03_1080p_v2.mov"); candidate.Confidence != "none" {
		t.Fatalf("date/version candidate = %+v", candidate)
	}
	if candidate := DetectEpisodeFilenameCandidate("第2集_EP3.mov"); candidate.Confidence != "ambiguous" {
		t.Fatalf("ambiguous candidate = %+v", candidate)
	}
}

func TestAssetSetVersionSealAndReopen(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "Asset Set 测试")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	first := createVideoAsset(t, store, project.ProjectID, "第1集.mp4")
	third := createVideoAsset(t, store, project.ProjectID, "第3集.mp4")

	created, err := store.CreateAssetSet(ctx, CreateAssetSetCommand{
		ProjectID: project.ProjectID, Purpose: "video_reference_source", DisplayName: "参考剧视频",
	})
	if err != nil || created.AssetSet.CurrentVersion != 1 || len(created.Members) != 0 {
		t.Fatalf("CreateAssetSet() = %+v, error = %v", created, err)
	}
	updated, err := store.CreateAssetSetVersion(ctx, CreateAssetSetVersionCommand{
		AssetSetID: created.AssetSet.AssetSetID, ExpectedCurrentVersion: 1,
		Changes: []AssetSetChange{
			{Operation: "add_asset", AssetID: third.Asset.AssetID},
			{Operation: "add_asset", AssetID: first.Asset.AssetID},
		},
	})
	if err != nil || updated.AssetSet.CurrentVersion != 2 || len(updated.Members) != 2 ||
		updated.Version.Completeness.RecognizedEpisodeCount != 2 ||
		updated.Members[0].EpisodeNo == nil || *updated.Members[0].EpisodeNo != 1 ||
		updated.Members[1].EpisodeNo == nil || *updated.Members[1].EpisodeNo != 3 ||
		len(updated.Version.Completeness.MissingEpisodeNumbers) != 1 ||
		updated.Version.Completeness.MissingEpisodeNumbers[0] != 2 {
		t.Fatalf("CreateAssetSetVersion() = %+v, error = %v", updated, err)
	}
	_, err = store.SealAssetSet(ctx, SealAssetSetCommand{
		AssetSetID: updated.AssetSet.AssetSetID, ExpectedCurrentVersion: 2,
		UserConfirmedUploadComplete: true,
	})
	assertDomainCode(t, err, "ASSET_SET_INCOMPLETE")
	policy, _ := json.Marshal(map[string]any{
		"mode": "continue_incomplete", "confirmed_by": "user", "missing_episode_numbers": []int{2},
	})
	sealed, err := store.SealAssetSet(ctx, SealAssetSetCommand{
		AssetSetID: updated.AssetSet.AssetSetID, ExpectedCurrentVersion: 2,
		ContinuationPolicy: policy, UserConfirmedUploadComplete: true,
	})
	if err != nil || sealed.AssetSet.Status != "sealed" || sealed.AssetSet.CurrentVersion != 3 ||
		sealed.Version.Status != "sealed" {
		t.Fatalf("SealAssetSet() = %+v, error = %v", sealed, err)
	}
	historical, err := store.GetAssetSetVersion(ctx, updated.Version.AssetSetVersionID)
	if err != nil || historical.Version.Status != "superseded" || len(historical.Members) != 2 {
		t.Fatalf("historical version = %+v, error = %v", historical, err)
	}
	reopened, err := store.ReopenAssetSet(ctx, sealed.AssetSet.AssetSetID, 3)
	if err != nil || reopened.AssetSet.Status != "collecting" || reopened.AssetSet.CurrentVersion != 4 ||
		reopened.Version.Status != "draft" {
		t.Fatalf("ReopenAssetSet() = %+v, error = %v", reopened, err)
	}
}

func TestAssetSetCommandsAreIdempotent(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "Asset Set idempotency")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	asset := createVideoAsset(t, store, project.ProjectID, "episode_1.mp4")

	create := CreateAssetSetCommand{
		CommandMeta: CommandMeta{Scope: project.ProjectID, CommandType: "create_asset_set", IdempotencyKey: "asset-set-create", RequestHash: "asset-set-create-v1"},
		ProjectID:   project.ProjectID, Purpose: "video_reference_source", DisplayName: "reference videos",
	}
	created, err := store.CreateAssetSet(ctx, create)
	if err != nil {
		t.Fatalf("CreateAssetSet() error = %v", err)
	}
	replayedCreate, err := store.CreateAssetSet(ctx, create)
	if err != nil || replayedCreate.AssetSet.AssetSetID != created.AssetSet.AssetSetID {
		t.Fatalf("CreateAssetSet() replay = %+v, error = %v", replayedCreate, err)
	}

	version := CreateAssetSetVersionCommand{
		CommandMeta: CommandMeta{Scope: project.ProjectID, CommandType: "create_asset_set_version", IdempotencyKey: "asset-set-version", RequestHash: "asset-set-version-v1"},
		AssetSetID:  created.AssetSet.AssetSetID, ExpectedCurrentVersion: 1,
		Changes: []AssetSetChange{{Operation: "add_asset", AssetID: asset.Asset.AssetID}},
	}
	updated, err := store.CreateAssetSetVersion(ctx, version)
	if err != nil {
		t.Fatalf("CreateAssetSetVersion() error = %v", err)
	}
	replayedVersion, err := store.CreateAssetSetVersion(ctx, version)
	if err != nil || replayedVersion.Version.AssetSetVersionID != updated.Version.AssetSetVersionID {
		t.Fatalf("CreateAssetSetVersion() replay = %+v, error = %v", replayedVersion, err)
	}

	seal := SealAssetSetCommand{
		CommandMeta: CommandMeta{Scope: project.ProjectID, CommandType: "seal_asset_set", IdempotencyKey: "asset-set-seal", RequestHash: "asset-set-seal-v1"},
		AssetSetID:  updated.AssetSet.AssetSetID, ExpectedCurrentVersion: updated.AssetSet.CurrentVersion,
		UserConfirmedUploadComplete: true,
	}
	sealed, err := store.SealAssetSet(ctx, seal)
	if err != nil {
		t.Fatalf("SealAssetSet() error = %v", err)
	}
	replayedSeal, err := store.SealAssetSet(ctx, seal)
	if err != nil || replayedSeal.Version.AssetSetVersionID != sealed.Version.AssetSetVersionID {
		t.Fatalf("SealAssetSet() replay = %+v, error = %v", replayedSeal, err)
	}

	reopen := ReopenAssetSetCommand{
		CommandMeta: CommandMeta{Scope: project.ProjectID, CommandType: "reopen_asset_set", IdempotencyKey: "asset-set-reopen", RequestHash: "asset-set-reopen-v1"},
		AssetSetID:  sealed.AssetSet.AssetSetID, ExpectedCurrentVersion: sealed.AssetSet.CurrentVersion,
	}
	reopened, err := store.ReopenAssetSetCommand(ctx, reopen)
	if err != nil {
		t.Fatalf("ReopenAssetSetCommand() error = %v", err)
	}
	replayedReopen, err := store.ReopenAssetSetCommand(ctx, reopen)
	if err != nil || replayedReopen.Version.AssetSetVersionID != reopened.Version.AssetSetVersionID {
		t.Fatalf("ReopenAssetSetCommand() replay = %+v, error = %v", replayedReopen, err)
	}
}
