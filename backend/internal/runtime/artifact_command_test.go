package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"content-agent/backend/internal/identity"
)

func artifactCommandState(t *testing.T, store *Store, artifactID string) string {
	t.Helper()
	var value string
	err := store.db.QueryRow(`SELECT json_array(current_version_id,
		(SELECT COUNT(*) FROM artifact_versions), (SELECT COUNT(*) FROM approvals),
		(SELECT COUNT(*) FROM events), (SELECT COUNT(*) FROM idempotency_records),
		(SELECT version FROM projects WHERE project_id=artifacts.project_id)) FROM artifacts WHERE artifact_id=?`, artifactID).Scan(&value)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func assertScriptEditRejectsRevokedEditor(t *testing.T, store *Store, command CompleteScriptEditCommand, artifactID string) {
	t.Helper()
	principal := identity.DefaultLocalPrincipal()
	if _, err := store.db.Exec(`UPDATE workspace_memberships SET role='viewer' WHERE workspace_id=? AND user_id=?`, principal.WorkspaceID, principal.UserID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := store.db.Exec(`UPDATE workspace_memberships SET role=? WHERE workspace_id=? AND user_id=?`, principal.Role, principal.WorkspaceID, principal.UserID); err != nil {
			t.Fatal(err)
		}
	}()
	before := artifactCommandState(t, store, artifactID)
	_, err := store.CompleteScriptEdit(identity.WithPrincipal(context.Background(), principal), command)
	assertDomainCode(t, err, "ROLE_FORBIDDEN")
	if after := artifactCommandState(t, store, artifactID); before != after {
		t.Fatalf("revoked editor changed handoff state: %s -> %s", before, after)
	}
}

func TestArtifactVersionMutationsRecheckAuthorizationBeforeFreshAndCachedWrites(t *testing.T) {
	for _, generic := range []bool{false, true} {
		for _, cached := range []bool{false, true} {
			for _, scenario := range []struct{ name, query, code string }{
				{"role", `UPDATE workspace_memberships SET role='viewer' WHERE workspace_id=? AND user_id=?`, "ROLE_FORBIDDEN"},
				{"membership", `UPDATE workspace_memberships SET status='disabled' WHERE workspace_id=? AND user_id=?`, "WORKSPACE_ACCESS_DENIED"},
				{"user", `UPDATE users SET status='disabled' WHERE user_id=?`, "WORKSPACE_ACCESS_DENIED"},
				{"workspace", `UPDATE workspaces SET status='deleting' WHERE workspace_id=?`, "WORKSPACE_ACCESS_DENIED"},
				{"project", `UPDATE projects SET deleted_at='2026-09-09T00:00:00Z' WHERE project_id=?`, "PROJECT_NOT_FOUND"},
				{"foreign_workspace", "", "PROJECT_NOT_FOUND"},
			} {
				name := "workflow/fresh/" + scenario.name
				if generic {
					name = "generic/" + name
				}
				if cached {
					name = "cached/" + name
				}
				t.Run(name, func(t *testing.T) {
					store, approval := approvalCommandFixture(t)
					ctx := context.Background()
					version, err := store.GetArtifactVersion(ctx, approval.SubjectRefID)
					if err != nil {
						t.Fatal(err)
					}
					mutate := store.CreateArtifactVersion
					if generic {
						project, err := store.GetProject(ctx, approval.ProjectID)
						if err != nil {
							t.Fatal(err)
						}
						artifact := createDeliveryArtifact(t, store, project, "generic_document", `{"content":"Original"}`)
						version, err = store.GetArtifactVersion(ctx, artifact.CurrentVersionID)
						if err != nil {
							t.Fatal(err)
						}
						mutate = store.CreateConfirmedGenericArtifactVersion
					}
					command := CreateVersionCommand{CommandMeta: CommandMeta{Scope: approval.ProjectID, CommandType: "create_artifact_version", IdempotencyKey: "save-version", RequestHash: "original"},
						ArtifactID: version.ArtifactID, BaseVersionID: version.ArtifactVersionID, BaseVersion: version.Version, ChangeMode: "whole_artifact", NewPayload: version.Payload}
					principal := identity.DefaultLocalPrincipal()
					ctx = identity.WithPrincipal(ctx, principal)
					if cached {
						first, err := mutate(ctx, command)
						if err != nil {
							t.Fatal(err)
						}
						retry, err := mutate(ctx, command)
						if err != nil || first.ArtifactVersion.ArtifactVersionID != retry.ArtifactVersion.ArtifactVersionID {
							t.Fatalf("original receipt lost: %+v %v", retry, err)
						}
					}
					args := []any{principal.WorkspaceID, principal.UserID}
					switch scenario.name {
					case "user":
						args = []any{principal.UserID}
					case "workspace":
						args = []any{principal.WorkspaceID}
					case "project":
						args = []any{approval.ProjectID}
					case "foreign_workspace":
						principal.WorkspaceID = "foreign-workspace"
						ctx = identity.WithPrincipal(context.Background(), principal)
					}
					if scenario.query != "" {
						if _, err := store.db.Exec(scenario.query, args...); err != nil {
							t.Fatal(err)
						}
					}
					before := artifactCommandState(t, store, command.ArtifactID)
					_, err = mutate(ctx, command)
					assertDomainCode(t, err, scenario.code)
					if after := artifactCommandState(t, store, command.ArtifactID); before != after {
						t.Fatalf("denied mutation changed state: %s -> %s", before, after)
					}
				})
			}
		}
	}
}

func TestApplyArtifactVersionSavesGenericDocumentWithoutWorkflowApproval(t *testing.T) {
	store, approval := approvalCommandFixture(t)
	ctx := context.Background()
	project, err := store.GetProject(ctx, approval.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"generic_document", "generic_table"} {
		t.Run(kind, func(t *testing.T) {
			payload := json.RawMessage(`{"title":"Edited","content":"Saved body"}`)
			if kind == "generic_table" {
				payload = json.RawMessage(`{"columns":["name"],"rows":[["Saved"]]}`)
			}
			artifact := createDeliveryArtifact(t, store, project, kind, string(payload))
			command := CreateVersionCommand{CommandMeta: CommandMeta{Scope: project.ProjectID, CommandType: "create_artifact_version", IdempotencyKey: kind, RequestHash: "original"},
				ArtifactID: artifact.ArtifactID, BaseVersionID: artifact.CurrentVersionID, BaseVersion: 1, ChangeMode: "whole_artifact", NewPayload: payload}
			result, err := store.ApplyArtifactVersion(ctx, command)
			if err != nil || result.ArtifactVersion.Status != "confirmed" || result.ArtifactVersion.Version != 2 || result.Approval.ApprovalRequestID != "" {
				t.Fatalf("generic save still requires workflow approval: %+v %v", result, err)
			}
			before := artifactCommandState(t, store, artifact.ArtifactID)
			repeated, err := store.ApplyArtifactVersion(ctx, command)
			if err != nil || repeated.ArtifactVersion.ArtifactVersionID != result.ArtifactVersion.ArtifactVersionID || before != artifactCommandState(t, store, artifact.ArtifactID) {
				t.Fatalf("generic retry changed state: %+v %v", repeated, err)
			}
			other := createDeliveryArtifact(t, store, project, kind, string(payload))
			wrong := command
			wrong.ArtifactID, wrong.BaseVersionID = other.ArtifactID, other.CurrentVersionID
			_, err = store.ApplyArtifactVersion(ctx, wrong)
			assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
			wrong = command
			wrong.BaseVersion++
			_, err = store.ApplyArtifactVersion(ctx, wrong)
			assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
			wrong = command
			wrong.Scope = "another-project"
			_, err = store.ApplyArtifactVersion(ctx, wrong)
			assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
		})
	}
}
