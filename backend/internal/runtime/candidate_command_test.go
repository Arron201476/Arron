package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"content-agent/backend/internal/identity"
)

func candidateCommandFixture(t *testing.T) (*Store, Project, ScriptCandidate) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "candidate-command.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	project, err := store.CreateProject(context.Background(), "Candidate commands")
	if err != nil {
		t.Fatal(err)
	}
	candidate := createCandidateFixture(t, store, project, "command")
	seedExportScriptUnit(t, store, candidate)
	return store, project, candidate
}

func candidatePreviewCommand(candidate ScriptCandidate) CreateFinalSelectionPreviewCommand {
	return CreateFinalSelectionPreviewCommand{CommandMeta: CommandMeta{Scope: candidate.ProjectID, CommandType: "create_final_selection_preview", IdempotencyKey: "preview", RequestHash: "preview-body"}, ProjectID: candidate.ProjectID, CandidateID: candidate.CandidateID, ExpectedArtifactVersionID: candidate.ScriptsArtifactVersionID}
}

func candidateCommandState(t *testing.T, store *Store) string {
	t.Helper()
	var value string
	err := store.db.QueryRow(`SELECT json_array(
		(SELECT json_group_array(json_array(final_selection_preview_id,status)) FROM final_selection_previews),
		(SELECT json_group_array(json_array(final_selection_id,status)) FROM final_selections),
		(SELECT json_group_array(json_array(approval_request_id,status,version)) FROM approvals),
		(SELECT COUNT(*) FROM exports), (SELECT COUNT(*) FROM events), (SELECT COUNT(*) FROM idempotency_records))`).Scan(&value)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func advanceCandidateVersion(t *testing.T, store *Store, candidate ScriptCandidate) string {
	t.Helper()
	newID := candidate.ScriptsArtifactVersionID + "-v2"
	_, err := store.db.Exec(`INSERT INTO artifact_versions(artifact_version_id,artifact_id,version,status,payload_json,schema_id,schema_version,created_by_kind,actor_ref,creation_reason,created_at,confirmed_at)
		SELECT ?,artifact_id,version+1,status,payload_json,schema_id,schema_version,created_by_kind,actor_ref,creation_reason,created_at,confirmed_at FROM artifact_versions WHERE artifact_version_id=?`, newID, candidate.ScriptsArtifactVersionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE script_candidates SET scripts_artifact_version_id=? WHERE candidate_id=?`, newID, candidate.CandidateID); err != nil {
		t.Fatal(err)
	}
	return newID
}

func TestFinalSelectionCannotConsumeChangedCandidateVersion(t *testing.T) {
	store, _, candidate := candidateCommandFixture(t)
	ctx := context.Background()
	command := candidatePreviewCommand(candidate)
	preview, err := store.CreateFinalSelectionPreview(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	newVersion := advanceCandidateVersion(t, store, candidate)
	before := candidateCommandState(t, store)
	_, err = store.ConfirmFinalSelection(ctx, ConfirmFinalSelectionCommand{ProjectID: candidate.ProjectID, CandidateID: candidate.CandidateID, PreviewHash: preview.Preview.PreviewHash, Confirmed: true})
	assertDomainCode(t, err, "FINAL_SELECTION_CONFLICT")
	_, err = store.CreateFinalSelectionPreview(ctx, command)
	assertDomainCode(t, err, "FINAL_SELECTION_PREVIEW_EXPIRED")
	command.IdempotencyKey = "new-preview"
	_, err = store.CreateFinalSelectionPreview(ctx, command)
	assertDomainCode(t, err, "FINAL_SELECTION_CONFLICT")
	if after := candidateCommandState(t, store); before != after {
		t.Fatal("stale version changed preview or final selection")
	}
	command.ExpectedArtifactVersionID = newVersion
	if _, err := store.CreateFinalSelectionPreview(ctx, command); err != nil {
		t.Fatal(err)
	}
}

func TestCandidateCommandsAuthorizeBeforeOriginalAndCachedWrites(t *testing.T) {
	for _, action := range []string{"preview", "confirm", "export"} {
		for _, cached := range []bool{false, true} {
			for _, scenario := range []string{"viewer", "membership", "project", "service"} {
				name := action + "/fresh/"
				if cached {
					name = action + "/cached/"
				}
				t.Run(name+scenario, func(t *testing.T) {
					store, project, candidate := candidateCommandFixture(t)
					principal := identity.DefaultLocalPrincipal()
					ctx := identity.WithPrincipal(context.Background(), principal)
					previewCommand := candidatePreviewCommand(candidate)
					confirmCommand := ConfirmFinalSelectionCommand{CommandMeta: CommandMeta{Scope: project.ProjectID, CommandType: "confirm_final_selection", IdempotencyKey: "confirm", RequestHash: "confirm-body"}, ProjectID: project.ProjectID, CandidateID: candidate.CandidateID, Confirmed: true}
					if action == "confirm" {
						preview, err := store.CreateFinalSelectionPreview(ctx, previewCommand)
						if err != nil {
							t.Fatal(err)
						}
						confirmCommand.PreviewHash = preview.Preview.PreviewHash
					}
					exportCommand := CreateScriptExportCommand{CommandMeta: CommandMeta{Scope: project.ProjectID, CommandType: "create_script_export", IdempotencyKey: "export", RequestHash: "export-body"}, CandidateID: candidate.CandidateID, ArtifactVersionID: candidate.ScriptsArtifactVersionID, Format: "txt"}
					invoke := func(scope string) error {
						switch action {
						case "preview":
							command := previewCommand
							command.Scope = scope
							_, err := store.CreateFinalSelectionPreview(ctx, command)
							return err
						case "confirm":
							command := confirmCommand
							command.Scope = scope
							_, err := store.ConfirmFinalSelection(ctx, command)
							return err
						default:
							command := exportCommand
							command.Scope = scope
							_, err := store.CreateScriptExport(ctx, command)
							return err
						}
					}
					if cached {
						if err := invoke(project.ProjectID); err != nil {
							t.Fatal(err)
						}
					}
					before := candidateCommandState(t, store)
					assertDomainCode(t, invoke("foreign"), "REQUEST_VALIDATION_FAILED")
					want := "ROLE_FORBIDDEN"
					switch scenario {
					case "viewer":
						_, err := store.db.Exec(`UPDATE workspace_memberships SET role='viewer' WHERE workspace_id=? AND user_id=?`, principal.WorkspaceID, principal.UserID)
						if err != nil {
							t.Fatal(err)
						}
					case "membership":
						_, err := store.db.Exec(`UPDATE workspace_memberships SET status='disabled' WHERE workspace_id=? AND user_id=?`, principal.WorkspaceID, principal.UserID)
						if err != nil {
							t.Fatal(err)
						}
						want = "WORKSPACE_ACCESS_DENIED"
					case "project":
						_, err := store.db.Exec(`UPDATE projects SET deleted_at=? WHERE project_id=?`, formatTime(store.now()), project.ProjectID)
						if err != nil {
							t.Fatal(err)
						}
						want = "PROJECT_NOT_FOUND"
					case "service":
						ctx = identity.WithPrincipal(context.Background(), identity.ServicePrincipal())
					}
					assertDomainCode(t, invoke(project.ProjectID), want)
					if after := candidateCommandState(t, store); before != after {
						t.Fatal("unauthorized candidate command changed durable state")
					}
				})
			}
		}
	}
}

func TestFinalSelectionReceiptsRemainBoundAfterReplacement(t *testing.T) {
	store, project, candidate := candidateCommandFixture(t)
	ctx := context.Background()
	previewCommand := candidatePreviewCommand(candidate)
	preview, err := store.CreateFinalSelectionPreview(ctx, previewCommand)
	if err != nil {
		t.Fatal(err)
	}
	command := ConfirmFinalSelectionCommand{CommandMeta: CommandMeta{Scope: project.ProjectID, CommandType: "confirm_final_selection", IdempotencyKey: "confirm", RequestHash: "confirm-body"}, ProjectID: project.ProjectID, CandidateID: candidate.CandidateID, PreviewHash: preview.Preview.PreviewHash, Confirmed: true}
	first, err := store.ConfirmFinalSelection(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	other := createCandidateFixture(t, store, project, "second")
	secondPreview, err := store.CreateFinalSelectionPreview(ctx, CreateFinalSelectionPreviewCommand{ProjectID: project.ProjectID, CandidateID: other.CandidateID, ExpectedCurrentSelectionID: &first.Selection.FinalSelectionID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmFinalSelection(ctx, ConfirmFinalSelectionCommand{ProjectID: project.ProjectID, CandidateID: other.CandidateID, PreviewHash: secondPreview.Preview.PreviewHash, ExpectedCurrentSelectionID: &first.Selection.FinalSelectionID, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	before := candidateCommandState(t, store)
	replayed, err := store.ConfirmFinalSelection(ctx, command)
	if err != nil || replayed.Selection.FinalSelectionID != first.Selection.FinalSelectionID {
		t.Fatalf("old confirmation: %+v %v", replayed, err)
	}
	consumed, err := store.CreateFinalSelectionPreview(ctx, previewCommand)
	if err != nil || consumed.Preview.Status != "consumed" || consumed.Approval.Status != "resolved" {
		t.Fatalf("consumed preview: %+v %v", consumed, err)
	}
	command.CandidateID = other.CandidateID
	_, err = store.ConfirmFinalSelection(ctx, command)
	assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
	previewCommand.CandidateID = other.CandidateID
	_, err = store.CreateFinalSelectionPreview(ctx, previewCommand)
	assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
	if after := candidateCommandState(t, store); before != after {
		t.Fatal("replay restored historical selection")
	}
}

func TestExportReceiptRetainsExactBytesAndRejectsWrongFormatOrExpiry(t *testing.T) {
	store, project, candidate := candidateCommandFixture(t)
	ctx := context.Background()
	command := CreateScriptExportCommand{CommandMeta: CommandMeta{Scope: project.ProjectID, CommandType: "create_script_export", IdempotencyKey: "export", RequestHash: "body"}, CandidateID: candidate.CandidateID, ArtifactVersionID: candidate.ScriptsArtifactVersionID, Format: "txt"}
	first, err := store.CreateScriptExport(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	advanceCandidateVersion(t, store, candidate)
	before := candidateCommandState(t, store)
	replayed, err := store.CreateScriptExport(ctx, command)
	if err != nil || replayed.ExportID != first.ExportID || replayed.Checksum != first.Checksum {
		t.Fatalf("exact historical export: %+v %v", replayed, err)
	}
	other := command
	other.Format = "docx"
	_, err = store.CreateScriptExport(ctx, other)
	assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
	store.now = func() time.Time { return first.ExpiresAt.Add(time.Second) }
	_, err = store.CreateScriptExport(ctx, command)
	assertDomainCode(t, err, "EXPORT_EXPIRED")
	if after := candidateCommandState(t, store); before != after {
		t.Fatal("cached export changed records")
	}
	principal := identity.DefaultLocalPrincipal()
	principal.WorkspaceID = "foreign"
	_, err = store.GetScriptExport(identity.WithPrincipal(ctx, principal), first.ExportID)
	assertDomainCode(t, err, "PROJECT_NOT_FOUND")
}

func TestScriptExportRejectsAnUnconfirmedOrMisnumberedUnit(t *testing.T) {
	for _, change := range []string{`UPDATE artifact_versions SET status='draft' WHERE artifact_version_id='av_export_unit'`, `UPDATE artifact_versions SET payload_json=json_set(payload_json,'$.episode_no',2) WHERE artifact_version_id='av_export_unit'`} {
		store, _, candidate := candidateCommandFixture(t)
		if _, err := store.db.Exec(change); err != nil {
			t.Fatal(err)
		}
		before := candidateCommandState(t, store)
		_, err := store.CreateScriptExport(context.Background(), CreateScriptExportCommand{CandidateID: candidate.CandidateID, ArtifactVersionID: candidate.ScriptsArtifactVersionID, Format: "txt"})
		assertDomainCode(t, err, "EXPORT_SOURCE_INCOMPLETE")
		if after := candidateCommandState(t, store); before != after {
			t.Fatal("invalid export changed state")
		}
	}
}
