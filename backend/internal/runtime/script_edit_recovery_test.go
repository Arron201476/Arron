package runtime

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
)

func assertScriptEditRecoverySnapshot(t *testing.T, store *Store, command CompleteScriptEditCommand, scopes []string) {
	t.Helper()
	ctx := context.Background()
	assertSnapshot := func(snapshot RunSnapshot) {
		t.Helper()
		pending := snapshot.PendingScriptEdit
		if pending == nil || !pending.CanComplete || pending.ProjectID != command.Scope || pending.RunID != command.RunID ||
			snapshot.Run.CurrentStepRunID == nil || pending.StepRunID != *snapshot.Run.CurrentStepRunID || !equalStrings(pending.PendingScopes, scopes) {
			t.Fatalf("missing or misbound script recovery: %+v", pending)
		}
		ids := append([]string(nil), pending.ExpectedScriptVersionIDs...)
		expected := append([]string(nil), command.ExpectedScriptVersionIDs...)
		sort.Strings(ids)
		sort.Strings(expected)
		if !equalStrings(ids, expected) {
			t.Fatalf("projected versions=%v want=%v", ids, expected)
		}
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &wire); err != nil || len(wire["pending_script_edit"]) == 0 {
			t.Fatalf("recovery missing from snapshot JSON: %s %v", encoded, err)
		}
	}
	snapshot, err := store.GetRunSnapshot(ctx, command.RunID)
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshot(snapshot)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	transactionSnapshot, err := store.getRunSnapshotTx(ctx, tx, command.RunID)
	rollbackErr := tx.Rollback()
	if err != nil || rollbackErr != nil {
		t.Fatalf("transaction snapshot: %v rollback: %v", err, rollbackErr)
	}
	assertSnapshot(transactionSnapshot)
	var taskID string
	if err := store.db.QueryRow(`SELECT task_item_id FROM task_items WHERE output_artifact_version_id=?`, command.ExpectedScriptVersionIDs[0]).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE task_items SET output_artifact_version_id=NULL WHERE task_item_id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	snapshot, err = store.GetRunSnapshot(ctx, command.RunID)
	if err != nil || snapshot.PendingScriptEdit == nil || snapshot.PendingScriptEdit.CanComplete || snapshot.PendingScriptEdit.DisabledReason == "" {
		t.Fatalf("inconsistent task exposed enabled recovery: %+v %v", snapshot.PendingScriptEdit, err)
	}
	_, err = store.CompleteScriptEdit(ctx, command)
	assertDomainCode(t, err, "ARTIFACT_VERSION_CONFLICT")
	if _, err := store.db.Exec(`UPDATE task_items SET output_artifact_version_id=? WHERE task_item_id=?`, command.ExpectedScriptVersionIDs[0], taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE projects SET active_write_run_id=NULL WHERE project_id=?`, command.Scope); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := store.db.Exec(`UPDATE projects SET active_write_run_id=? WHERE project_id=?`, command.RunID, command.Scope); err != nil {
			t.Fatal(err)
		}
	}()
	snapshot, err = store.GetRunSnapshot(ctx, command.RunID)
	if err != nil || snapshot.PendingScriptEdit != nil {
		t.Fatalf("inactive run exposed recovery: %+v %v", snapshot.PendingScriptEdit, err)
	}
	_, err = store.CompleteScriptEdit(ctx, command)
	assertDomainCode(t, err, "RUN_STATE_CONFLICT")
}
