package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"content-agent/backend/internal/identity"
)

func approvalCommandFixture(t *testing.T) (*Store, Approval) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "approval.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	_, initial, _, _ := prepareStoryBibleForImpact(t, store, "Approval command boundaries")
	snapshot, err := store.GetRunSnapshot(context.Background(), initial.Run.RunID)
	if err != nil || snapshot.CurrentApproval == nil {
		t.Fatalf("pending approval: %+v %v", snapshot, err)
	}
	return store, *snapshot.CurrentApproval
}

func callApprovalCommand(t *testing.T, ctx context.Context, store *Store, approval Approval, meta CommandMeta) (json.RawMessage, error) {
	t.Helper()
	var result any
	var err error
	if meta.CommandType == "resolve_approval" {
		result, err = store.ResolveApproval(ctx, ResolveApprovalCommand{CommandMeta: meta, ApprovalRequestID: approval.ApprovalRequestID,
			ExpectedApprovalVersion: approval.Version, SubjectSnapshotHash: approval.SubjectSnapshotHash, Action: "approve"})
	} else {
		command := RequestApprovalRegenerationCommand{CommandMeta: meta, ApprovalRequestID: approval.ApprovalRequestID,
			ExpectedApprovalVersion: approval.Version, SubjectSnapshotHash: approval.SubjectSnapshotHash,
			Action: "request_ai_revision", Instruction: "Clarify the existing opening conflict."}
		if meta.CommandType == "request_approval_revision" {
			result, err = store.RequestApprovalRevision(ctx, command)
		} else {
			command.Action = "regenerate_artifact"
			result, err = store.RequestApprovalRegeneration(ctx, command)
		}
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func approvalCommandState(t *testing.T, store *Store, approval Approval) string {
	t.Helper()
	var state string
	err := store.db.QueryRow(`SELECT json_array(status, version, subject_ref_id, subject_snapshot_hash, resolution_json,
		(SELECT COUNT(*) FROM events), (SELECT COUNT(*) FROM messages), (SELECT COUNT(*) FROM revision_requests),
		(SELECT COUNT(*) FROM artifact_versions), (SELECT COUNT(*) FROM impact_reviews),
		(SELECT COUNT(*) FROM regeneration_plans), (SELECT COUNT(*) FROM idempotency_records))
		FROM approvals WHERE approval_request_id=?`, approval.ApprovalRequestID).Scan(&state)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestApprovalCommandsRecheckCurrentAuthorizationForFreshAndCachedRequests(t *testing.T) {
	for _, kind := range []string{"resolve_approval", "request_approval_regeneration", "request_approval_revision"} {
		for _, cached := range []bool{false, true} {
			for _, scenario := range []struct{ name, query, code string }{
				{"role", `UPDATE workspace_memberships SET role='viewer' WHERE workspace_id=? AND user_id=?`, "ROLE_FORBIDDEN"},
				{"membership", `UPDATE workspace_memberships SET status='disabled' WHERE workspace_id=? AND user_id=?`, "WORKSPACE_ACCESS_DENIED"},
				{"user", `UPDATE users SET status='disabled' WHERE user_id=?`, "WORKSPACE_ACCESS_DENIED"},
				{"workspace", `UPDATE workspaces SET status='deleting' WHERE workspace_id=?`, "WORKSPACE_ACCESS_DENIED"},
				{"project", `UPDATE projects SET deleted_at='2026-09-09T00:00:00Z' WHERE project_id=?`, "APPROVAL_NOT_FOUND"},
				{"foreign_workspace", "", "APPROVAL_NOT_FOUND"},
			} {
				name := kind + "/fresh/" + scenario.name
				if cached {
					name = kind + "/cached/" + scenario.name
				}
				t.Run(name, func(t *testing.T) {
					store, approval := approvalCommandFixture(t)
					principal := identity.DefaultLocalPrincipal()
					ctx := identity.WithPrincipal(context.Background(), principal)
					meta := CommandMeta{Scope: approval.ProjectID, CommandType: kind, IdempotencyKey: "approval-command", RequestHash: "original"}
					if cached {
						first, err := callApprovalCommand(t, ctx, store, approval, meta)
						if err != nil {
							t.Fatal(err)
						}
						retry, err := callApprovalCommand(t, ctx, store, approval, meta)
						if err != nil || !bytes.Equal(first, retry) {
							t.Fatalf("normal retry changed receipt: %s %s %v", first, retry, err)
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
					before := approvalCommandState(t, store, approval)
					_, err := callApprovalCommand(t, ctx, store, approval, meta)
					assertDomainCode(t, err, scenario.code)
					if after := approvalCommandState(t, store, approval); before != after {
						t.Fatalf("denied command wrote state: before=%s after=%s", before, after)
					}
				})
			}
		}
	}
}

func TestApprovalReceiptsCannotBeUsedForAnotherApprovalInTheSameRun(t *testing.T) {
	for _, kind := range []string{"resolve_approval", "request_approval_regeneration", "request_approval_revision"} {
		t.Run(kind, func(t *testing.T) {
			store, approval := approvalCommandFixture(t)
			ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
			meta := CommandMeta{Scope: approval.ProjectID, CommandType: kind, IdempotencyKey: "original-receipt", RequestHash: "same-internal-hash"}
			if _, err := callApprovalCommand(t, ctx, store, approval, meta); err != nil {
				t.Fatal(err)
			}
			// Public HTTP hashes already include the resource path; also bind internal cached receipts.
			_, err := store.db.Exec(`INSERT INTO approvals(approval_request_id, project_id, run_id, step_run_id, scope,
				status, version, title, reason, options_json, subject_kind, subject_ref_id, subject_version,
				subject_snapshot_hash, requested_at, resolved_at, resolution_json, actor_ref)
				SELECT 'another-approval', project_id, run_id, step_run_id, scope, 'approved', version, title, reason,
				options_json, subject_kind, subject_ref_id, subject_version, subject_snapshot_hash, requested_at,
				resolved_at, resolution_json, actor_ref FROM approvals WHERE approval_request_id=?`, approval.ApprovalRequestID)
			if err != nil {
				t.Fatal(err)
			}
			other := approval
			other.ApprovalRequestID = "another-approval"
			before := approvalCommandState(t, store, other)
			_, err = callApprovalCommand(t, ctx, store, other, meta)
			assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
			wrongScope := meta
			wrongScope.Scope = "another-project"
			_, err = callApprovalCommand(t, ctx, store, approval, wrongScope)
			assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
			if before != approvalCommandState(t, store, other) {
				t.Fatal("rejected receipt changed approval or wrote data")
			}
		})
	}
}

func TestApprovalRevisionWhitelistAndReceiptAfterSubjectExpires(t *testing.T) {
	store, approval := approvalCommandFixture(t)
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	meta := CommandMeta{Scope: approval.ProjectID, CommandType: "request_approval_revision", IdempotencyKey: "revision", RequestHash: "original"}
	for _, scope := range []string{approval.Scope, "quality_review", "final_selection"} {
		options := `["approve"]`
		if scope != approval.Scope {
			options = `["request_ai_revision"]`
		}
		if _, err := store.db.Exec(`UPDATE approvals SET scope=?, options_json=? WHERE approval_request_id=?`, scope, options, approval.ApprovalRequestID); err != nil {
			t.Fatal(err)
		}
		before := approvalCommandState(t, store, approval)
		_, err := callApprovalCommand(t, ctx, store, approval, meta)
		assertDomainCode(t, err, "APPROVAL_ACTION_NOT_ALLOWED")
		if before != approvalCommandState(t, store, approval) {
			t.Fatal("disallowed revision wrote data")
		}
	}
	options, _ := json.Marshal(approval.Options)
	if _, err := store.db.Exec(`UPDATE approvals SET scope=?, options_json=? WHERE approval_request_id=?`, approval.Scope, string(options), approval.ApprovalRequestID); err != nil {
		t.Fatal(err)
	}
	first, err := callApprovalCommand(t, ctx, store, approval, meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE approvals SET status='expired' WHERE approval_request_id=?`, approval.ApprovalRequestID); err != nil {
		t.Fatal(err)
	}
	before := approvalCommandState(t, store, approval)
	retry, err := callApprovalCommand(t, ctx, store, approval, meta)
	if err != nil || !bytes.Equal(first, retry) || before != approvalCommandState(t, store, approval) {
		t.Fatalf("accepted revision receipt was lost or replayed: %s %s %v", first, retry, err)
	}
}

func TestApprovalRevisionTargetsStayWithinProjectRunAndStep(t *testing.T) {
	store, approval := approvalCommandFixture(t)
	ctx := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	original, err := approvalRegenerationTargetsTx(ctx, tx, approval)
	if err != nil || len(original) != 1 {
		t.Fatalf("original target: %+v %v", original, err)
	}
	for _, field := range []string{"ProjectID", "RunID", "StepRunID"} {
		other := approval
		reflect.ValueOf(&other).Elem().FieldByName(field).SetString("another-target")
		targets, err := approvalRegenerationTargetsTx(ctx, tx, other)
		if err != nil || len(targets) != 0 {
			t.Errorf("%s crossed approval boundary: %+v %v", field, targets, err)
		}
	}
}
