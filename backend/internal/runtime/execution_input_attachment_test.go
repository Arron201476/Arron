package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"content-agent/backend/internal/agentcontract"
	"content-agent/backend/internal/identity"
)

func TestExecutionInputAttachmentsThreeModesPreserveRevisionAndOwnership(t *testing.T) {
	user := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	for _, mode := range []string{"conversation", "background_task", "stateful_workflow"} {
		t.Run(mode, func(t *testing.T) {
			var store *Store
			var project, ownerID, table, column string
			var appendInput func(string, []agentcontract.AttachmentRef) (string, error)
			switch mode {
			case "conversation":
				s, p, turn, _ := pauseTurnFixture(t)
				store, project, ownerID, table, column = s, p.ProjectID, turn.AgentTurnID, "agent_turn_inputs", "agent_turn_id"
				appendInput = func(content string, refs []agentcontract.AttachmentRef) (string, error) {
					input, err := store.AppendAgentTurnInput(user, AppendAgentTurnInputCommand{AgentTurnID: ownerID, Content: content, AttachmentRefs: refs})
					return input.InputID, err
				}
			case "background_task":
				s, task, _ := pausedBackgroundStore(t)
				store, project, ownerID, table, column = s, task.ProjectID, task.AgentTaskID, "agent_task_inputs", "agent_task_id"
				appendInput = func(content string, refs []agentcontract.AttachmentRef) (string, error) {
					input, err := store.AppendAgentTaskInput(user, AppendAgentTaskInputCommand{AgentTaskID: ownerID, Content: content, AttachmentRefs: refs})
					return input.InputID, err
				}
			case "stateful_workflow":
				store = openProjectFilesTestStore(t, t.TempDir()+"/attachments.db")
				p, claim := sdkResultRepairClaim(t, store)
				project, ownerID, table, column = p.ProjectID, claim.Attempt.AttemptID, "execution_inputs", "attempt_id"
				appendInput = func(content string, refs []agentcontract.AttachmentRef) (string, error) {
					input, err := store.AppendExecutionInput(user, AppendExecutionInputCommand{ProjectID: project, AttemptID: ownerID, Content: content, AttachmentRefs: refs})
					return input.InputID, err
				}
			}
			defer store.Close()
			asset := createTextAsset(t, store, project, "source.txt", "Immutable attached source")
			refs := []agentcontract.AttachmentRef{{AssetID: asset.Asset.AssetID, AssetSnapshotID: asset.Snapshot.AssetSnapshotID, DisplayName: "untrusted-name"}}
			originalID, err := appendInput("", refs)
			if err != nil {
				t.Fatal("attachment-only input failed", err)
			}
			var raw string
			if err := store.db.QueryRow(`SELECT attachments_json FROM `+table+` WHERE input_id=?`, originalID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			original, err := decodeInputAttachments(raw)
			if err != nil || len(original) != 1 || original[0].Name != "source.txt" || original[0].Checksum != asset.Asset.Checksum {
				t.Fatal("client metadata became trusted", original, err)
			}
			change, err := store.ChangeExecutionInput(user, inputChangeCommand(project, mode, ownerID, originalID, "revise", "Updated instruction", "with-attachment"))
			if err != nil {
				t.Fatal(err)
			}
			if err := store.db.QueryRow(`SELECT attachments_json FROM `+table+` WHERE input_id=?`, change.ReplacementInputID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			revised, err := decodeInputAttachments(raw)
			if err != nil || !slices.Equal(original, revised) {
				t.Fatal("revision replaced attachment identity", err)
			}
			if mode == "stateful_workflow" {
				view, err := store.GetExecutionInputs(user, project, ownerID)
				if err != nil || len(view.Inputs) != 2 || view.Inputs[1].ContentHash != executionInputContentHash("Updated instruction", original) {
					t.Fatal("stateful content digest omitted attachments", err)
				}
			}
			_, err = appendInput("duplicate", append(refs, refs...))
			assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
			_, err = appendInput("too many", []agentcontract.AttachmentRef{refs[0], refs[0], refs[0], refs[0], refs[0]})
			assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
			other, err := store.CreateProject(user, "Other project")
			if err != nil {
				t.Fatal(err)
			}
			foreign := createTextAsset(t, store, other.ProjectID, "foreign.txt", "Not authorized material")
			_, err = appendInput("foreign", []agentcontract.AttachmentRef{{AssetID: foreign.Asset.AssetID, AssetSnapshotID: foreign.Snapshot.AssetSnapshotID}})
			assertDomainCode(t, err, "RESOURCE_PROJECT_MISMATCH")
			if _, err := store.db.Exec(`UPDATE assets SET status='deleted' WHERE asset_id=?`, asset.Asset.AssetID); err != nil {
				t.Fatal(err)
			}
			_, err = appendInput("deleted", refs)
			assertDomainCode(t, err, "ASSET_SOURCE_UNAVAILABLE")
			if _, err := store.ChangeExecutionInput(user, inputChangeCommand(project, mode, ownerID, change.ReplacementInputID, "withdraw", "", "withdraw-deleted-ref")); err != nil {
				t.Fatal("withdrawal unnecessarily required the deleted file", err)
			}
			var count int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE `+column+`=?`, ownerID).Scan(&count); err != nil || count != 2 {
				t.Fatal("invalid attachment left a partial input", count, err)
			}
		})
	}
}

func TestExecutionInputAttachmentHashGoldenAndLegacy(t *testing.T) {
	item := agentcontract.ExecutionInputAttachment{AssetID: "a", AssetSnapshotID: "s", Kind: "text", Name: "note.txt", MIMEType: "text/plain", Checksum: sha256Hex([]byte("file")), SizeBytes: 4, TextHash: sha256Hex([]byte("parsed"))}
	want := sha256Hex([]byte("request\x00a\x00s\x00text\x00note.txt\x00text/plain\x00" + item.Checksum + "\x004\x00" + item.TextHash))
	if executionInputContentHash("request", []agentcontract.ExecutionInputAttachment{item}) != want || executionInputContentHash("request", nil) != sha256Hex([]byte("request")) {
		t.Fatal("cross-language or legacy input digest changed")
	}
	for _, raw := range []string{"null", "{}", "[{\"asset_id\":\"bad\"}]"} {
		if _, err := decodeInputAttachments(raw); err == nil {
			t.Fatal("invalid manifest accepted", raw)
		}
	}
	encoded, _ := json.Marshal([]agentcontract.ExecutionInputAttachment{item})
	if _, err := decodeInputAttachments(string(encoded)); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionInputAttachmentsMigration51PreservesNativeState(t *testing.T) {
	database := filepath.Join(t.TempDir(), "migration.db")
	store := openProjectFilesTestStore(t, database)
	defer func() { store.Close() }()
	project, claim := sdkResultRepairClaim(t, store)
	old := appendExecutionInputForTest(t, store, project, claim, "old-text")
	if _, err := store.PauseExecutionForApproval(context.Background(), inputCheckpointForTest(t, claim, nil)); err != nil {
		t.Fatal(err)
	}
	var nativeBefore string
	if err := store.db.QueryRow(`SELECT state_json FROM execution_run_states WHERE attempt_id=?`, claim.Attempt.AttemptID).Scan(&nativeBefore); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"agent_turn_inputs", "agent_task_inputs", "execution_inputs"} {
		if _, err := store.db.Exec(`ALTER TABLE ` + table + ` DROP COLUMN attachments_json`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`PRAGMA user_version=51`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openProjectFilesTestStore(t, database)
	var nativeAfter, backup string
	if err := store.db.QueryRow(`SELECT state_json FROM execution_run_states WHERE attempt_id=?`, claim.Attempt.AttemptID).Scan(&nativeAfter); err != nil || nativeAfter != nativeBefore {
		t.Fatal("native input was changed by migration", err)
	}
	if err := store.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version=51 AND to_version=? AND status='completed'`, schemaVersion).Scan(&backup); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatal(err)
	}
	user := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	view, err := store.GetExecutionInputs(user, project.ProjectID, claim.Attempt.AttemptID)
	if err != nil || len(view.Inputs) != 1 || view.Inputs[0].ContentHash != old.ContentHash || len(view.Inputs[0].Attachments) != 0 {
		t.Fatal("legacy text input changed", err)
	}
	asset := createTextAsset(t, store, project.ProjectID, "after.txt", "After migration")
	added, err := store.AppendExecutionInput(user, AppendExecutionInputCommand{ProjectID: project.ProjectID, AttemptID: claim.Attempt.AttemptID, AttachmentRefs: []agentcontract.AttachmentRef{{AssetID: asset.Asset.AssetID, AssetSnapshotID: asset.Snapshot.AssetSnapshotID}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openProjectFilesTestStore(t, database)
	next, err := store.ClaimExecutionTask(context.Background(), statefulResumeCommand(claim))
	if err != nil || next == nil || len(next.AdditionalInputs) != 2 || next.AdditionalInputs[1].InputID != added.InputID || !slices.Equal(next.AdditionalInputs[1].Attachments, added.Attachments) {
		t.Fatal("reopened native claim lost attachment identity", err)
	}
}

func TestExecutionInputAttachmentsQuotaIdempotencyAndFrozenParse(t *testing.T) {
	store, project, turn, _ := pauseTurnFixture(t)
	defer store.Close()
	user := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	asset := createTextAsset(t, store, project.ProjectID, "large.txt", strings.Repeat("x", 4<<20))
	command := AppendAgentTurnInputCommand{AgentTurnID: turn.AgentTurnID, AttachmentRefs: []agentcontract.AttachmentRef{{AssetID: asset.Asset.AssetID, AssetSnapshotID: asset.Snapshot.AssetSnapshotID}}, CommandMeta: CommandMeta{IdempotencyKey: "attached", CommandType: "append_agent_turn_input", RequestHash: "original"}}
	first, err := store.AppendAgentTurnInput(user, command)
	if err != nil || len(first.Attachments) != 1 || first.Attachments[0].TextHash != asset.Asset.Checksum {
		t.Fatal("parsed text version not frozen", err)
	}
	change := inputChangeCommand(project.ProjectID, "conversation", turn.AgentTurnID, first.InputID, "revise", "Revised", "quota-revise")
	revised, err := store.ChangeExecutionInput(user, change)
	if err != nil {
		t.Fatal("exact 8 MiB rejected", err)
	}
	small := createTextAsset(t, store, project.ProjectID, "small.txt", "x")
	_, err = store.AppendAgentTurnInput(user, AppendAgentTurnInputCommand{AgentTurnID: turn.AgentTurnID, AttachmentRefs: []agentcontract.AttachmentRef{{AssetID: small.Asset.AssetID, AssetSnapshotID: small.Snapshot.AssetSnapshotID}}})
	assertDomainCode(t, err, "REQUEST_VALIDATION_FAILED")
	if _, err := store.ChangeExecutionInput(user, inputChangeCommand(project.ProjectID, "conversation", turn.AgentTurnID, revised.ReplacementInputID, "withdraw", "", "quota-withdraw")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE asset_blobs SET status='deleted' WHERE blob_id=?`, asset.Asset.OriginalBlobID); err != nil {
		t.Fatal(err)
	}
	retried, err := store.AppendAgentTurnInput(user, command)
	if err != nil || retried.InputID != first.InputID || retried.Status != "superseded" || !slices.Equal(retried.Attachments, first.Attachments) {
		t.Fatal("lost reply retry changed its frozen receipt", err)
	}
	command.RequestHash = "changed"
	_, err = store.AppendAgentTurnInput(user, command)
	assertDomainCode(t, err, "IDEMPOTENCY_KEY_REUSED")
	command.CommandMeta = CommandMeta{}
	_, err = store.AppendAgentTurnInput(user, command)
	assertDomainCode(t, err, "ASSET_SOURCE_UNAVAILABLE")
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM agent_turn_inputs WHERE agent_turn_id=?`, turn.AgentTurnID).Scan(&count); err != nil || count != 2 {
		t.Fatal("invalid quota request left history", err)
	}
}

func TestExecutionInputAttachmentsRejectReparsedRevisionAndInvalidSize(t *testing.T) {
	store, project, turn, _ := pauseTurnFixture(t)
	defer store.Close()
	user := identity.WithPrincipal(context.Background(), identity.DefaultLocalPrincipal())
	asset := createTextAsset(t, store, project.ProjectID, "source.txt", "Original text")
	command := AppendAgentTurnInputCommand{AgentTurnID: turn.AgentTurnID, AttachmentRefs: []agentcontract.AttachmentRef{{AssetID: asset.Asset.AssetID, AssetSnapshotID: asset.Snapshot.AssetSnapshotID}}}
	first, err := store.AppendAgentTurnInput(user, command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE asset_parse_results SET content_text=?,content_hash=? WHERE asset_id=?`, "Reparsed text", sha256Hex([]byte("Reparsed text")), asset.Asset.AssetID); err != nil {
		t.Fatal(err)
	}
	_, err = store.ChangeExecutionInput(user, inputChangeCommand(project.ProjectID, "conversation", turn.AgentTurnID, first.InputID, "revise", "Keep files", "reparse"))
	assertDomainCode(t, err, "EXECUTION_INPUT_CONFLICT")
	for _, size := range []int64{0, -1, maxInputAttachmentBytes + 1} {
		item := first.Attachments[0]
		item.SizeBytes = size
		if validInputAttachment(item) {
			t.Fatal("invalid file size accepted", size)
		}
	}
	if _, err := store.db.Exec(`UPDATE asset_parse_results SET status='failed' WHERE asset_id=?`, asset.Asset.AssetID); err != nil {
		t.Fatal(err)
	}
	_, err = store.AppendAgentTurnInput(user, command)
	assertDomainCode(t, err, "ASSET_SOURCE_UNAVAILABLE")
}
