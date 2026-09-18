package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"content-agent/backend/internal/identity"
)

func approvalRevisionFixture(t *testing.T, mode string) (*Store, Approval, []ApprovalRevisionTarget) {
	t.Helper()
	var store *Store
	var approval Approval
	if mode == "legacy" {
		var err error
		store, err = Open(filepath.Join(t.TempDir(), "revision-target.db"), loadTestRegistry(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		_, approval, _, _ = prepareEpisodeCheckpoint(t, store)
	} else {
		var output ArtifactCommitOutput
		store, output, _ = managedEditFixture(t, mode)
		snapshot, err := store.GetRunSnapshot(context.Background(), output.Artifact.RunID)
		if err != nil || snapshot.CurrentApproval == nil {
			t.Fatalf("approval: %+v, %v", snapshot, err)
		}
		approval = *snapshot.CurrentApproval
	}
	before := approvalCommandState(t, store, approval)
	result, err := store.GetApprovalRevisionTargets(context.Background(), approval.ApprovalRequestID)
	if err != nil || !reflect.DeepEqual(result.Approval, approval) {
		t.Fatalf("targets: %+v, %v", result, err)
	}
	if before != approvalCommandState(t, store, approval) {
		t.Fatal("reading revision targets wrote state")
	}
	return store, approval, result.Targets
}

func approvalRevisionCommand(approval Approval, target ApprovalRevisionTarget, key string) RequestApprovalRegenerationCommand {
	return RequestApprovalRegenerationCommand{
		CommandMeta:       CommandMeta{Scope: approval.ProjectID, CommandType: "request_approval_regeneration", IdempotencyKey: key, RequestHash: "exact-target-request"},
		ApprovalRequestID: approval.ApprovalRequestID, ExpectedApprovalVersion: approval.Version,
		SubjectSnapshotHash: approval.SubjectSnapshotHash, TargetArtifactVersionID: target.ArtifactVersionID,
		Action: "request_ai_revision", Instruction: "Improve the selected output.",
	}
}

func TestApprovalRevisionSelectsExactVersionAndPersistsSourceWithoutAdvancing(t *testing.T) {
	for _, mode := range []string{"single", "batch", "multi", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			store, approval, targets := approvalRevisionFixture(t, mode)
			wantCount := map[string]int{"single": 1, "batch": 3, "multi": 2, "legacy": 1}[mode]
			if len(targets) != wantCount {
				t.Fatalf("target count = %d, want %d", len(targets), wantCount)
			}
			if mode == "legacy" && targets[0].ArtifactType != "script_unit" {
				t.Fatal("derived handoff was offered for revision")
			}
			// The collection version is not its member count (first approval is v1).
			if mode == "multi" || mode == "batch" {
				if approval.SubjectVersion == len(targets) {
					t.Fatal("fixture does not distinguish version from count")
				}
			}
			target := targets[len(targets)-1]
			command := approvalRevisionCommand(approval, target, "selected-output")
			ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
			var versionsBefore int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM artifact_versions`).Scan(&versionsBefore); err != nil {
				t.Fatal(err)
			}
			request, err := store.RequestApprovalRevision(ctx, command)
			if err != nil {
				t.Fatal(err)
			}
			if request.Status != "queued" || request.ArtifactID == nil || *request.ArtifactID != target.ArtifactID ||
				request.BaseVersionID == nil || *request.BaseVersionID != target.ArtifactVersionID ||
				request.SourceApprovalRequestID == nil || *request.SourceApprovalRequestID != approval.ApprovalRequestID {
				t.Fatalf("queued target = %+v", request)
			}
			stored, err := store.GetRevisionRequest(ctx, request.RevisionRequestID)
			if err != nil || !reflect.DeepEqual(stored, request) {
				t.Fatalf("persisted request = %+v, %v", stored, err)
			}
			listed, err := store.ListRevisionRequests(ctx, approval.ProjectID)
			if err != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0], request) {
				t.Fatalf("listed source = %+v, %v", listed, err)
			}
			snapshot, err := store.GetRunSnapshot(ctx, approval.RunID)
			if err != nil || snapshot.Run.Status != "waiting_approval" || snapshot.CurrentApproval == nil || snapshot.CurrentApproval.Status != "pending" {
				t.Fatalf("revision advanced approval: %+v, %v", snapshot, err)
			}
			var versionsAfter, attempts int
			if err := store.db.QueryRow(`SELECT (SELECT COUNT(*) FROM artifact_versions),(SELECT COUNT(*) FROM revision_attempts)`).Scan(&versionsAfter, &attempts); err != nil {
				t.Fatal(err)
			}
			if versionsAfter != versionsBefore || attempts != 0 {
				t.Fatal("admission wrote an output or started execution")
			}
			before := approvalCommandState(t, store, approval)
			receipt, err := store.RequestApprovalRevision(ctx, command)
			if err != nil || !reflect.DeepEqual(receipt, request) {
				t.Fatalf("replay = %+v, %v", receipt, err)
			}
			competing := command
			competing.IdempotencyKey = "competing"
			_, err = store.RequestApprovalRevision(ctx, competing)
			assertDomainCode(t, err, "REVISION_IN_PROGRESS")
			if before != approvalCommandState(t, store, approval) {
				t.Fatal("replay or competing admission wrote state")
			}
		})
	}
}

func TestApprovalRevisionTargetsRejectChangedBindingOrUnboundSelection(t *testing.T) {
	for _, mode := range []string{"single", "batch", "multi"} {
		for _, fault := range []string{"target", "sibling_status", "sibling_owner", "snapshot_hash", "scope", "order"} {
			if mode == "single" && (fault == "scope" || fault == "order") {
				continue
			}
			t.Run(mode+"/"+fault, func(t *testing.T) {
				store, approval, targets := approvalRevisionFixture(t, mode)
				command := approvalRevisionCommand(approval, targets[len(targets)-1], "invalid-target")
				var query string
				var args []any
				switch fault {
				case "target":
					command.TargetArtifactVersionID = "unbound-version"
				case "sibling_status":
					query, args = `UPDATE artifact_versions SET status='stale' WHERE artifact_version_id=?`, []any{targets[0].ArtifactVersionID}
				case "sibling_owner":
					query, args = `UPDATE artifacts SET project_id='other-project' WHERE artifact_id=?`, []any{targets[0].ArtifactID}
				case "snapshot_hash":
					query, args = `UPDATE approvals SET subject_snapshot_hash='incorrect' WHERE approval_request_id=?`, []any{approval.ApprovalRequestID}
					command.SubjectSnapshotHash = "incorrect"
				case "scope":
					query, args = `UPDATE approval_subject_versions SET scope_key='other-scope' WHERE approval_request_id=? AND artifact_version_id=?`, []any{approval.ApprovalRequestID, targets[0].ArtifactVersionID}
				case "order":
					query, args = `UPDATE approval_subject_versions SET item_order=99 WHERE approval_request_id=? AND artifact_version_id=?`, []any{approval.ApprovalRequestID, targets[0].ArtifactVersionID}
				}
				if query != "" {
					if fault == "sibling_owner" {
						project, err := store.CreateProject(context.Background(), "Other project")
						if err != nil {
							t.Fatal(err)
						}
						query, args = `UPDATE artifacts SET project_id=? WHERE artifact_id=?`, []any{project.ProjectID, targets[0].ArtifactID}
					}
					if _, err := store.db.Exec(query, args...); err != nil {
						t.Fatal(err)
					}
				}
				before := approvalCommandState(t, store, approval)
				if fault != "target" {
					_, err := store.GetApprovalRevisionTargets(context.Background(), approval.ApprovalRequestID)
					assertDomainCode(t, err, "APPROVAL_SUBJECT_CHANGED")
				}
				_, err := store.RequestApprovalRevision(identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal()), command)
				code := "APPROVAL_SUBJECT_CHANGED"
				if fault == "target" {
					code = "TARGET_CANDIDATE_INVALID"
				}
				assertDomainCode(t, err, code)
				if before != approvalCommandState(t, store, approval) {
					t.Fatal("rejected target wrote state")
				}
			})
		}
	}
}

func TestApprovalRevisionOriginalReceiptSurvivesExpirationButCannotChangeTarget(t *testing.T) {
	store, approval, targets := approvalRevisionFixture(t, "multi")
	command := approvalRevisionCommand(approval, targets[0], "original")
	first, err := store.RequestApprovalRevision(identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal()), command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE approvals SET status='expired' WHERE approval_request_id=?`, approval.ApprovalRequestID); err != nil {
		t.Fatal(err)
	}
	before := approvalCommandState(t, store, approval)
	receipt, err := store.RequestApprovalRevision(identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal()), command)
	if err != nil || !reflect.DeepEqual(receipt, first) {
		t.Fatalf("expired receipt: %+v, %v", receipt, err)
	}
	command.TargetArtifactVersionID = targets[1].ArtifactVersionID
	_, err = store.RequestApprovalRevision(identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal()), command)
	assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
	if before != approvalCommandState(t, store, approval) {
		t.Fatal("old receipt recovery wrote state")
	}
}

func TestApprovalRevisionTargetReadRechecksRoleAndCreationRollsBack(t *testing.T) {
	store, approval, targets := approvalRevisionFixture(t, "multi")
	principal := identity.DefaultLocalPrincipal()
	ctx := identity.WithPrincipal(context.Background(), principal)
	if _, err := store.db.Exec(`UPDATE workspace_memberships SET role='viewer' WHERE workspace_id=? AND user_id=?`, principal.WorkspaceID, principal.UserID); err != nil {
		t.Fatal(err)
	}
	before := approvalCommandState(t, store, approval)
	_, err := store.GetApprovalRevisionTargets(ctx, approval.ApprovalRequestID)
	assertDomainCode(t, err, "ROLE_FORBIDDEN")
	command := approvalRevisionCommand(approval, targets[0], "retry-after-rollback")
	_, err = store.RequestApprovalRevision(ctx, command)
	assertDomainCode(t, err, "ROLE_FORBIDDEN")
	if before != approvalCommandState(t, store, approval) {
		t.Fatal("revoked user wrote data")
	}
	if _, err := store.db.Exec(`UPDATE workspace_memberships SET role=? WHERE workspace_id=? AND user_id=?`, principal.Role, principal.WorkspaceID, principal.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_revision_created BEFORE INSERT ON events WHEN NEW.event_type='revision_request.created' BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	_, err = store.RequestApprovalRevision(ctx, command)
	if err == nil {
		t.Fatal("fault did not reject creation")
	}
	if before != approvalCommandState(t, store, approval) {
		t.Fatal("partial revision creation survived rollback")
	}
	if _, err := store.db.Exec(`DROP TRIGGER fail_revision_created`); err != nil {
		t.Fatal(err)
	}
	request, err := store.RequestApprovalRevision(ctx, command)
	if err != nil || request.Status != "queued" {
		t.Fatalf("retry after rollback: %+v, %v", request, err)
	}
}

func TestApprovalRevisionLegacyFallbackResolvesGenericEpisodeAndSingleArtifact(t *testing.T) {
	for _, mode := range []string{"single", "batch"} {
		t.Run(mode, func(t *testing.T) {
			store, approval, targets := approvalRevisionFixture(t, mode)
			command := approvalRevisionCommand(approval, targets[0], "legacy-without-target")
			command.TargetArtifactVersionID = ""
			want := targets[0]
			if mode == "batch" {
				command.Instruction = "请修改第2集的结尾。"
				for _, candidate := range targets {
					if candidate.ScopeKey == "episode:2" {
						want = candidate
					}
				}
			}
			result, err := store.RequestApprovalRevision(identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal()), command)
			if err != nil || result.BaseVersionID == nil || *result.BaseVersionID != want.ArtifactVersionID {
				t.Fatalf("legacy selection: %+v, %v", result, err)
			}
			encoded, err := json.Marshal(targets)
			if err != nil {
				t.Fatal(err)
			}
			var fields []map[string]any
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			for _, target := range fields {
				if len(target) != 6 {
					t.Fatalf("target exposes more than metadata: %+v", target)
				}
			}
		})
	}
}
