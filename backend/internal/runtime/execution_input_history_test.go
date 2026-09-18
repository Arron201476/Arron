package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"content-agent/backend/internal/identity"
)

func TestExecutionInputHistoryPaginationRebuildAndScope(t *testing.T) {
	database := filepath.Join(t.TempDir(), "history.db")
	store := openProjectFilesTestStore(t, database)
	defer func() { store.Close() }()
	project, claim := sdkResultRepairClaim(t, store)
	first := appendExecutionInputForTest(t, store, project, claim, "private-input-body")
	for i := 0; i < 52; i++ {
		id := fmt.Sprintf("history-%03d", i)
		_, err := store.db.Exec(`INSERT INTO execution_attempts(attempt_id,run_id,step_run_id,task_item_id,attempt_no,executor_id,provider_id,worker_id,request_fingerprint,input_snapshot_hash,status,token_hash,lease_until,usage_json,started_at)
			SELECT ?,run_id,step_run_id,task_item_id,?,executor_id,provider_id,worker_id,request_fingerprint,input_snapshot_hash,'failed',token_hash,lease_until,usage_json,started_at FROM execution_attempts WHERE attempt_id=?`, id, i+2, claim.Attempt.AttemptID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.db.Exec(`INSERT INTO execution_inputs(input_id,attempt_id,user_id,sequence,content,content_hash,status,created_at,included_at) SELECT ?,?,user_id,sequence,content,content_hash,'included',created_at,created_at FROM execution_inputs WHERE input_id=?`, "input-"+id, id, first.InputID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openProjectFilesTestStore(t, database)
	user := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	items, next, err := store.ListExecutionInputAttempts(user, project.ProjectID, claim.Attempt.RunID, "")
	if err != nil || len(items) != 50 || next == "" {
		t.Fatalf("first page: %d %q %v", len(items), next, err)
	}
	rest, end, err := store.ListExecutionInputAttempts(user, project.ProjectID, claim.Attempt.RunID, next)
	if err != nil || len(rest) != 3 || end != "" {
		t.Fatalf("second page: %d %q %v", len(rest), end, err)
	}
	seen := map[string]bool{}
	for _, item := range append(items, rest...) {
		if seen[item.AttemptID] || item.InputCount != 1 || item.TaskItemID != claim.Attempt.TaskItemID {
			t.Fatal("duplicate or incorrect history", item)
		}
		seen[item.AttemptID] = true
		if item.AttemptID == claim.Attempt.AttemptID {
			if !item.Current || item.IncludedCount != 0 {
				t.Fatal("current receipt misrepresented")
			}
		} else if item.Current || item.Status != "failed" || item.IncludedCount != 1 {
			t.Fatal("old receipt misrepresented")
		}
	}
	view, err := store.GetExecutionInputs(user, project.ProjectID, "history-000")
	if err != nil || view.CanAppend || view.Inputs[0].Status != "included" {
		t.Fatalf("old contents unavailable: %v", err)
	}
	encoded, _ := json.Marshal(items)
	for _, private := range []string{"token_hash", "content_hash", "private-input-body", "input_snapshot_hash", "worker_id"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("history leaked private execution details")
		}
	}
	_, _, err = store.ListExecutionInputAttempts(user, project.ProjectID, claim.Attempt.RunID, strings.Repeat("x", 257))
	assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
	foreign := identity.WithPrincipal(context.Background(), identity.Principal{Kind: identity.KindUser, UserID: "foreign", WorkspaceID: "other", Role: identity.RoleOwner})
	if _, _, err = store.ListExecutionInputAttempts(foreign, project.ProjectID, claim.Attempt.RunID, ""); err == nil {
		t.Fatal("foreign workspace may read history")
	}
	if _, _, err = store.ListExecutionInputAttempts(user, project.ProjectID, "other-run", ""); err == nil {
		t.Fatal("foreign run may read history")
	}
}
