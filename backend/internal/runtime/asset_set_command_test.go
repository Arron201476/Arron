package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"content-agent/backend/internal/identity"
)

func assetSetCommandFixture(t *testing.T) (*Store, Project, AssetSetSnapshot) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "asset-set-command.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	project, err := store.CreateProject(context.Background(), "Material batch")
	if err != nil {
		t.Fatal(err)
	}
	asset := createVideoAsset(t, store, project.ProjectID, "Episode 1.mp4")
	set, err := store.CreateAssetSet(context.Background(), CreateAssetSetCommand{ProjectID: project.ProjectID, Purpose: "video_reference_source", DisplayName: "Materials"})
	if err != nil {
		t.Fatal(err)
	}
	set, err = store.CreateAssetSetVersion(context.Background(), CreateAssetSetVersionCommand{AssetSetID: set.AssetSet.AssetSetID, ExpectedCurrentVersion: 1, Changes: []AssetSetChange{{Operation: "add_asset", AssetID: asset.Asset.AssetID}}})
	if err != nil {
		t.Fatal(err)
	}
	return store, project, set
}

func assetSetCommandState(t *testing.T, store *Store) string {
	t.Helper()
	var state string
	err := store.db.QueryRow(`SELECT json_array(
		(SELECT json_group_array(json_array(asset_set_id,current_version_id,current_version,status)) FROM asset_sets),
		(SELECT json_group_array(json_array(asset_set_version_id,status)) FROM asset_set_versions),
		(SELECT COUNT(*) FROM asset_set_members), (SELECT COUNT(*) FROM events), (SELECT COUNT(*) FROM idempotency_records))`).Scan(&state)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestAssetSetSnapshotRequiresMatchingSetAndVersion(t *testing.T) {
	store, project, base := assetSetCommandFixture(t)
	ctx := context.Background()
	sealed, err := store.SealAssetSet(ctx, SealAssetSetCommand{AssetSetID: base.AssetSet.AssetSetID, ExpectedCurrentVersion: base.Version.Version, UserConfirmedUploadComplete: true})
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateAssetSet(ctx, CreateAssetSetCommand{ProjectID: project.ProjectID, Purpose: "video_reference_source", DisplayName: "Other batch"})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := getAssetSetSnapshotTx(ctx, tx, other.AssetSet.AssetSetID, sealed.Version.AssetSetVersionID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("mismatched set and version accepted: %v", err)
	}
	for _, setID := range []string{"", sealed.AssetSet.AssetSetID} {
		got, err := getAssetSetSnapshotTx(ctx, tx, setID, sealed.Version.AssetSetVersionID)
		if err != nil || got.AssetSet.AssetSetID != sealed.AssetSet.AssetSetID {
			t.Fatalf("valid historical read failed: %v", err)
		}
	}
	entry, ok := store.registry.Get("video_reference_creation")
	if !ok || entry.Definition == nil {
		t.Fatal("video capability missing")
	}
	raw, err := json.Marshal(sourceInputRequest{ProjectID: project.ProjectID, SourceType: entry.Definition.InputBinding.SourceType, UserRequestMessageID: "request", AssetSetID: other.AssetSet.AssetSetID, AssetSetVersionID: sealed.Version.AssetSetVersionID, CollectionState: "sealed"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, _, err = store.prepareRunSourceInputTx(ctx, tx, project.ProjectID, entry.Definition, raw)
	assertDomainCode(t, err, "ASSET_SET_NOT_SEALED")
}

func TestAssetSetMutationChecksCurrentAuthorityBeforeCache(t *testing.T) {
	for _, action := range []string{"create", "change", "seal", "reopen"} {
		for _, cached := range []bool{false, true} {
			for _, scenario := range []string{"viewer", "membership", "project", "workspace", "service"} {
				name := action + "/fresh/" + scenario
				if cached {
					name = action + "/cached/" + scenario
				}
				t.Run(name, func(t *testing.T) {
					store, project, base := assetSetCommandFixture(t)
					principal := identity.DefaultLocalPrincipal()
					ctx := identity.WithPrincipal(context.Background(), principal)
					if action == "reopen" {
						var err error
						base, err = store.SealAssetSet(ctx, SealAssetSetCommand{AssetSetID: base.AssetSet.AssetSetID, ExpectedCurrentVersion: base.Version.Version, UserConfirmedUploadComplete: true})
						if err != nil {
							t.Fatal(err)
						}
					}
					invoke := func(scope string) error {
						meta := CommandMeta{Scope: scope, CommandType: action, IdempotencyKey: "command", RequestHash: "body"}
						var err error
						switch action {
						case "create":
							_, err = store.CreateAssetSet(ctx, CreateAssetSetCommand{CommandMeta: meta, ProjectID: project.ProjectID, Purpose: "video_reference_source", DisplayName: "Other batch"})
						case "change":
							no := 2
							_, err = store.CreateAssetSetVersion(ctx, CreateAssetSetVersionCommand{CommandMeta: meta, AssetSetID: base.AssetSet.AssetSetID, ExpectedCurrentVersion: base.Version.Version, Changes: []AssetSetChange{{Operation: "set_episode_no", AssetID: base.Members[0].AssetID, EpisodeNo: &no}}})
						case "seal":
							_, err = store.SealAssetSet(ctx, SealAssetSetCommand{CommandMeta: meta, AssetSetID: base.AssetSet.AssetSetID, ExpectedCurrentVersion: base.Version.Version, UserConfirmedUploadComplete: true})
						case "reopen":
							_, err = store.ReopenAssetSetCommand(ctx, ReopenAssetSetCommand{CommandMeta: meta, AssetSetID: base.AssetSet.AssetSetID, ExpectedCurrentVersion: base.Version.Version})
						}
						return err
					}
					if cached {
						if err := invoke(project.ProjectID); err != nil {
							t.Fatal(err)
						}
					}
					before := assetSetCommandState(t, store)
					assertDomainCode(t, invoke("foreign"), "REQUEST_VALIDATION_FAILED")
					want := "ROLE_FORBIDDEN"
					switch scenario {
					case "viewer":
						if _, err := store.db.Exec(`UPDATE workspace_memberships SET role='viewer' WHERE workspace_id=? AND user_id=?`, principal.WorkspaceID, principal.UserID); err != nil {
							t.Fatal(err)
						}
					case "membership":
						if _, err := store.db.Exec(`UPDATE workspace_memberships SET status='disabled' WHERE workspace_id=? AND user_id=?`, principal.WorkspaceID, principal.UserID); err != nil {
							t.Fatal(err)
						}
						want = "WORKSPACE_ACCESS_DENIED"
					case "project":
						if _, err := store.db.Exec(`UPDATE projects SET deleted_at=? WHERE project_id=?`, formatTime(store.now()), project.ProjectID); err != nil {
							t.Fatal(err)
						}
						want = "PROJECT_NOT_FOUND"
					case "workspace":
						principal.WorkspaceID = "foreign"
						ctx = identity.WithPrincipal(context.Background(), principal)
						want = "PROJECT_NOT_FOUND"
					case "service":
						ctx = identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
					}
					assertDomainCode(t, invoke(project.ProjectID), want)
					if after := assetSetCommandState(t, store); before != after {
						t.Fatal("rejected command changed material state")
					}
				})
			}
		}
	}
}

func TestAssetSetReceiptStaysBoundAfterReopen(t *testing.T) {
	store, project, base := assetSetCommandFixture(t)
	ctx := context.Background()
	command := SealAssetSetCommand{CommandMeta: CommandMeta{Scope: project.ProjectID, CommandType: "seal_asset_set", IdempotencyKey: "seal", RequestHash: "body"}, AssetSetID: base.AssetSet.AssetSetID, ExpectedCurrentVersion: base.Version.Version, UserConfirmedUploadComplete: true}
	sealed, err := store.SealAssetSet(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.ReopenAssetSet(ctx, sealed.AssetSet.AssetSetID, sealed.Version.Version)
	if err != nil {
		t.Fatal(err)
	}
	before := assetSetCommandState(t, store)
	receipt, err := store.SealAssetSet(ctx, command)
	if err != nil || receipt.Version.AssetSetVersionID != sealed.Version.AssetSetVersionID {
		t.Fatalf("original receipt = %+v, %v", receipt, err)
	}
	wrong := command
	wrong.ExpectedCurrentVersion++
	_, err = store.SealAssetSet(ctx, wrong)
	assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
	if after := assetSetCommandState(t, store); after != before {
		t.Fatal("old receipt resealed or changed current batch")
	}
	latest, err := store.GetAssetSet(ctx, base.AssetSet.AssetSetID)
	if err != nil || latest.Version.AssetSetVersionID != current.Version.AssetSetVersionID {
		t.Fatalf("latest = %+v, %v", latest, err)
	}
	principal := identity.DefaultLocalPrincipal()
	principal.WorkspaceID = "foreign"
	_, err = store.GetAssetSetVersion(identity.WithPrincipal(ctx, principal), sealed.Version.AssetSetVersionID)
	assertDomainCode(t, err, "PROJECT_NOT_FOUND")
}

func TestAssetSetExcludedUnnumberedMemberAndEmptySeal(t *testing.T) {
	store, project, base := assetSetCommandFixture(t)
	asset := createVideoAsset(t, store, project.ProjectID, "unrecognized.mp4")
	reason := "Not part of the episode sequence"
	updated, err := store.CreateAssetSetVersion(context.Background(), CreateAssetSetVersionCommand{AssetSetID: base.AssetSet.AssetSetID, ExpectedCurrentVersion: base.Version.Version, Changes: []AssetSetChange{{Operation: "add_asset", AssetID: asset.Asset.AssetID}, {Operation: "exclude_asset", AssetID: asset.Asset.AssetID, ExclusionReason: &reason}}})
	if err != nil || len(updated.Members) != 2 || updated.Members[1].EpisodeNo != nil || updated.Members[1].Included {
		t.Fatalf("excluded member = %+v, %v", updated, err)
	}
	updated, err = store.CreateAssetSetVersion(context.Background(), CreateAssetSetVersionCommand{AssetSetID: base.AssetSet.AssetSetID, ExpectedCurrentVersion: updated.Version.Version, Changes: []AssetSetChange{{Operation: "exclude_asset", AssetID: base.Members[0].AssetID, ExclusionReason: &reason}}})
	if err != nil {
		t.Fatal(err)
	}
	before := assetSetCommandState(t, store)
	_, err = store.SealAssetSet(context.Background(), SealAssetSetCommand{AssetSetID: base.AssetSet.AssetSetID, ExpectedCurrentVersion: updated.Version.Version, UserConfirmedUploadComplete: true})
	assertDomainCode(t, err, "ASSET_SET_INCOMPLETE")
	if before != assetSetCommandState(t, store) {
		t.Fatal("empty seal changed state")
	}
}

func TestAssetSetEpisodeNumbersAreBoundedBeforeEnumeration(t *testing.T) {
	for _, filename := range []string{"EP1000000000.mp4", "第99999999999999999999999999集.mp4"} {
		candidate := DetectEpisodeFilenameCandidate(filename)
		if candidate.EpisodeNo != nil || candidate.Confidence != "none" || candidate.Pattern != "out_of_range" {
			t.Fatalf("huge filename = %+v", candidate)
		}
	}
	store, _, base := assetSetCommandFixture(t)
	for _, no := range []int{0, -1, MaxAssetSetEpisodeNo + 1, 1000000000} {
		before := assetSetCommandState(t, store)
		_, err := store.CreateAssetSetVersion(context.Background(), CreateAssetSetVersionCommand{AssetSetID: base.AssetSet.AssetSetID, ExpectedCurrentVersion: base.Version.Version, Changes: []AssetSetChange{{Operation: "set_episode_no", AssetID: base.Members[0].AssetID, EpisodeNo: &no}}})
		assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
		if before != assetSetCommandState(t, store) {
			t.Fatal("invalid number changed state")
		}
	}
}

func TestAssetSetCASFailureRollsBackVersionAndReceipt(t *testing.T) {
	store, project, base := assetSetCommandFixture(t)
	if _, err := store.db.Exec(`CREATE TRIGGER ignore_batch_update BEFORE UPDATE ON asset_sets BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	before := assetSetCommandState(t, store)
	_, err := store.SealAssetSet(context.Background(), SealAssetSetCommand{CommandMeta: CommandMeta{Scope: project.ProjectID, CommandType: "seal_asset_set", IdempotencyKey: "seal", RequestHash: "body"}, AssetSetID: base.AssetSet.AssetSetID, ExpectedCurrentVersion: base.Version.Version, UserConfirmedUploadComplete: true})
	assertDomainCode(t, err, "RUN_STATE_CONFLICT")
	if before != assetSetCommandState(t, store) {
		t.Fatal("CAS failure leaked a version or receipt")
	}
}

func TestAssetSetReopenChecksStructuredVersionReference(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(map[bool]string{false: "unrelated_text", true: "source_reference"}[bound], func(t *testing.T) {
			store, project, base := assetSetCommandFixture(t)
			sealed, err := store.SealAssetSet(context.Background(), SealAssetSetCommand{AssetSetID: base.AssetSet.AssetSetID, ExpectedCurrentVersion: base.Version.Version, UserConfirmedUploadComplete: true})
			if err != nil {
				t.Fatal(err)
			}
			candidate := createCandidateFixture(t, store, project, "asset-reference")
			payload := map[string]any{"notes": "Example " + sealed.Version.AssetSetVersionID}
			if bound {
				payload["input"] = map[string]any{"asset_set_version_id": sealed.Version.AssetSetVersionID}
			}
			encoded, _ := json.Marshal(payload)
			if _, err := store.db.Exec(`INSERT INTO run_input_snapshot_versions(run_input_snapshot_version_id,run_id,version,status,payload_json,created_at) VALUES('asset-ref',?,99,'sealed',?,?)`, candidate.SourceRunID, string(encoded), formatTime(store.now())); err != nil {
				t.Fatal(err)
			}
			_, err = store.ReopenAssetSet(context.Background(), sealed.AssetSet.AssetSetID, sealed.Version.Version)
			if bound {
				assertDomainCode(t, err, "ASSET_SET_REOPEN_CONFLICT")
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
