package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"content-agent/backend/internal/capability"
)

func prepareApprovedPartialVideoBatch(t *testing.T, store *Store) (Approval, RunSnapshot, string) {
	t.Helper()
	ctx := context.Background()
	_, settled, first := preparePartialVideoBatch(t, store)
	waiting, err := store.ContinueWithPartialResults(ctx, ContinueWithPartialResultsCommand{
		StepRunID: *settled.Run.CurrentStepRunID, Confirmed: true,
	})
	if err != nil || waiting.CurrentApproval == nil {
		t.Fatalf("partial confirmation: %+v %v", waiting, err)
	}
	approval := *waiting.CurrentApproval
	resolved, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
		ApprovalRequestID: approval.ApprovalRequestID, Action: "approve",
		ExpectedApprovalVersion: approval.Version, SubjectSnapshotHash: approval.SubjectSnapshotHash,
	})
	if err != nil || currentStepID(resolved) != "aggregate_reference_scripts" || resolved.Run.Status != "paused" {
		t.Fatalf("partial approval: %+v %v", resolved, err)
	}
	var versionID string
	if err := store.db.QueryRowContext(ctx, `SELECT output_artifact_version_id FROM task_items WHERE task_item_id=?`, first.Task.TaskItemID).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	return approval, resolved, versionID
}

func TestPartialConfirmationDrivesBatchAndAggregationTransitions(t *testing.T) {
	registry := loadTestRegistry(t)
	entry, _ := registry.Get("video_reference_creation")
	for index := range entry.Definition.Steps {
		step := &entry.Definition.Steps[index]
		switch step.ID {
		case "extract_video_scripts":
			step.Next = []capability.TransitionRef{
				{When: "all_batch_items_succeeded", To: "ingest_source"},
				{When: "incomplete_material_confirmed", To: "aggregate_reference_scripts"},
			}
		case "aggregate_reference_scripts":
			step.Next = []capability.TransitionRef{
				{When: "failure", To: "ingest_source"},
				{When: "incomplete_material_confirmed", To: "analyze_reference_scripts"},
			}
		}
	}
	store, err := Open(filepath.Join(t.TempDir(), "partial-transitions.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, resolved, _ := prepareApprovedPartialVideoBatch(t, store)
	result, err := store.ResumeRun(context.Background(), ResumeRunCommand{RunID: resolved.Run.RunID})
	if err != nil || currentStepID(result) != "analyze_reference_scripts" {
		t.Fatalf("aggregate partial branch: %+v %v", result, err)
	}
	var payload string
	if err := store.db.QueryRow(`SELECT v.payload_json FROM artifacts a JOIN artifact_versions v
		ON v.artifact_version_id=a.current_version_id WHERE a.run_id=? AND a.artifact_type='reference_scripts'`, resolved.Run.RunID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var reference struct {
		Completeness string `json:"completeness"`
		Failed       []int  `json:"failed_episode_nos"`
	}
	if err := json.Unmarshal([]byte(payload), &reference); err != nil || reference.Completeness != "incomplete" || !slices.Equal(reference.Failed, []int{2}) {
		t.Fatalf("aggregate metadata: %s %v", payload, err)
	}
}

func TestPartialConfirmationRequiresCurrentStepDecisionAndApprovedVersions(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "partial-facts.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	approval, resolved, versionID := prepareApprovedPartialVideoBatch(t, store)
	for _, scenario := range []string{
		"valid", "other-step-decision", "unapproved", "missing-decision", "wrong-source", "bad-hash",
		"wrong-failed-key", "wrong-count", "missing-audit", "duplicate-audit", "unconfirmed-version",
		"changed-current-version", "missing-output", "unsettled-task", "wrong-input-versions", "all-repaired", "repaired-stale-input",
	} {
		t.Run(scenario, func(t *testing.T) {
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := tx.ExecContext(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			var decisionID, payload string
			if err := tx.QueryRowContext(ctx, `SELECT decision_snapshot_id, payload_json FROM run_decision_snapshots
				WHERE run_id=? AND step_run_id=? AND decision_type='partial_batch_continuation' ORDER BY version DESC LIMIT 1`, approval.RunID, approval.StepRunID).Scan(&decisionID, &payload); err != nil {
				t.Fatal(err)
			}
			inputs := []string{versionID}
			switch scenario {
			case "other-step-decision":
				otherStep := *resolved.Run.CurrentStepRunID
				_, err := store.createRunDecisionSnapshotTx(ctx, tx, approval.ProjectID, approval.RunID, otherStep,
					"partial_batch_continuation", "step_run", otherStep,
					json.RawMessage(`{"mode":"continue_with_partial_results","succeeded_count":1,"failed_item_keys":["episode:99"]}`), store.now())
				if err != nil {
					t.Fatal(err)
				}
			case "unapproved":
				exec(`UPDATE approvals SET status='pending' WHERE approval_request_id=?`, approval.ApprovalRequestID)
			case "missing-decision":
				exec(`DELETE FROM run_decision_snapshots WHERE decision_snapshot_id=?`, decisionID)
			case "wrong-source":
				exec(`UPDATE run_decision_snapshots SET source_ref_id=? WHERE decision_snapshot_id=?`, *resolved.Run.CurrentStepRunID, decisionID)
			case "bad-hash":
				exec(`UPDATE run_decision_snapshots SET snapshot_hash='invalid' WHERE decision_snapshot_id=?`, decisionID)
			case "wrong-failed-key", "wrong-count":
				var decision map[string]any
				if err := json.Unmarshal([]byte(payload), &decision); err != nil {
					t.Fatal(err)
				}
				if scenario == "wrong-count" {
					decision["succeeded_count"] = 2
				} else {
					decision["failed_item_keys"] = []string{"episode:99"}
				}
				updated, err := json.Marshal(decision)
				if err != nil {
					t.Fatal(err)
				}
				exec(`UPDATE run_decision_snapshots SET payload_json=?, snapshot_hash=? WHERE decision_snapshot_id=?`, string(updated), sha256Hex(updated), decisionID)
			case "missing-audit":
				exec(`DELETE FROM events WHERE subject_id=? AND event_type='approval.requested'`, approval.ApprovalRequestID)
			case "duplicate-audit":
				_, err := store.appendEvent(ctx, tx, approval.ProjectID, &approval.RunID, &approval.StepRunID,
					"approval.requested", "approval", approval.ApprovalRequestID, map[string]any{"decision_snapshot_id": decisionID})
				if err != nil {
					t.Fatal(err)
				}
			case "unconfirmed-version":
				exec(`UPDATE artifact_versions SET status='pending_approval' WHERE artifact_version_id=?`, versionID)
			case "changed-current-version":
				exec(`UPDATE artifacts SET current_version_id='' WHERE current_version_id=?`, versionID)
			case "missing-output":
				exec(`UPDATE task_items SET output_artifact_version_id=NULL WHERE step_run_id=? AND status='succeeded'`, approval.StepRunID)
			case "unsettled-task":
				exec(`UPDATE task_items SET status='pending' WHERE step_run_id=? AND status='failed'`, approval.StepRunID)
			case "wrong-input-versions":
				inputs = []string{}
			case "all-repaired", "repaired-stale-input":
				// State fixture only: this is not an execution/regeneration E2E.
				exec(`UPDATE task_items SET status='succeeded', output_artifact_version_id='repaired-state-fixture' WHERE step_run_id=? AND status='failed'`, approval.StepRunID)
				if scenario == "all-repaired" {
					inputs = append(inputs, "repaired-state-fixture")
				}
			}
			failed, confirmed, err := partialBatchContinuationForStepTx(ctx, tx, approval.RunID, approval.StepRunID, inputs)
			switch scenario {
			case "valid", "other-step-decision":
				if err != nil || !confirmed || !slices.Equal(failed, []string{"episode:2"}) {
					t.Fatalf("current partial fact: %v %t %v", failed, confirmed, err)
				}
			case "all-repaired":
				if err != nil || confirmed || len(failed) != 0 {
					t.Fatalf("repaired batch inherited stale decision: %v %t %v", failed, confirmed, err)
				}
			default:
				assertDomainCode(t, err, "PARTIAL_CONTINUATION_CONFLICT")
			}
		})
	}
}

func TestEditingPartialResultsRebindsDecisionAndRequiresNewApproval(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "partial-edit.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, settled, first := preparePartialVideoBatch(t, store)
	waiting, err := store.ContinueWithPartialResults(ctx, ContinueWithPartialResultsCommand{
		StepRunID: *settled.Run.CurrentStepRunID, Confirmed: true,
	})
	if err != nil || waiting.CurrentApproval == nil {
		t.Fatalf("partial approval: %+v %v", waiting, err)
	}
	approval := *waiting.CurrentApproval
	for edit := 0; edit < 2; edit++ {
		var artifactID, versionID, payload string
		var version int
		if err := store.db.QueryRowContext(ctx, `SELECT a.artifact_id, v.artifact_version_id, v.version, v.payload_json
			FROM task_items t JOIN artifact_versions v ON v.artifact_version_id=t.output_artifact_version_id
			JOIN artifacts a ON a.artifact_id=v.artifact_id WHERE t.task_item_id=?`, first.Task.TaskItemID).Scan(&artifactID, &versionID, &version, &payload); err != nil {
			t.Fatal(err)
		}
		edited, err := store.CreateArtifactVersion(ctx, CreateVersionCommand{
			ArtifactID: artifactID, BaseVersionID: versionID, BaseVersion: version,
			ChangeMode: "whole_artifact", NewPayload: json.RawMessage(payload),
		})
		if err != nil || edited.Approval.Status != "pending" || edited.Approval.Title != approval.Title || edited.Approval.Reason != approval.Reason {
			t.Fatalf("edit must preserve partial confirmation: %+v %v", edited, err)
		}
		var replaced string
		if err := store.db.QueryRowContext(ctx, `SELECT json_extract(payload_json, '$.replaces_approval_request_id') FROM events
			WHERE subject_id=? AND event_type='approval.requested'`, edited.Approval.ApprovalRequestID).Scan(&replaced); err != nil || replaced != approval.ApprovalRequestID {
			t.Fatalf("replacement approval lineage: %q %v", replaced, err)
		}
		_, err = store.ResolveApproval(ctx, ResolveApprovalCommand{
			ApprovalRequestID: approval.ApprovalRequestID, Action: "approve",
			ExpectedApprovalVersion: approval.Version, SubjectSnapshotHash: approval.SubjectSnapshotHash,
		})
		assertDomainCode(t, err, "APPROVAL_ALREADY_RESOLVED")
		approval = edited.Approval
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = partialBatchContinuationForStepTx(ctx, tx, approval.RunID, approval.StepRunID, nil)
	tx.Rollback()
	assertDomainCode(t, err, "PARTIAL_CONTINUATION_CONFLICT")
	resolved, err := store.ResolveApproval(ctx, ResolveApprovalCommand{
		ApprovalRequestID: approval.ApprovalRequestID, Action: "approve",
		ExpectedApprovalVersion: approval.Version, SubjectSnapshotHash: approval.SubjectSnapshotHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.ResumeRun(ctx, ResumeRunCommand{RunID: resolved.Run.RunID})
	if err != nil || currentStepID(result) != "analyze_reference_scripts" {
		t.Fatalf("continue after explicit revised partial approval: %+v %v", result, err)
	}
}

func TestIncompleteSourceFactRequiresSealedUserConfirmedInput(t *testing.T) {
	ctx := context.Background()
	registry := loadTestRegistry(t)
	entry, _ := registry.Get("video_reference_creation")
	for index := range entry.Definition.Steps {
		if step := &entry.Definition.Steps[index]; step.ID == "ingest_source" {
			step.Next = []capability.TransitionRef{{When: "incomplete_material_confirmed", To: "extract_video_scripts"}}
		}
	}
	store, err := Open(filepath.Join(t.TempDir(), "source-fact.db"), registry)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "Incomplete source fact")
	if err != nil {
		t.Fatal(err)
	}
	message, err := store.CreateUserMessage(ctx, project.PrimaryConversationID, "Extract the supplied episodes")
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.CreateAssetSet(ctx, CreateAssetSetCommand{ProjectID: project.ProjectID, Purpose: "video_reference_source", DisplayName: "Source episodes"})
	if err != nil {
		t.Fatal(err)
	}
	first := createVideoAsset(t, store, project.ProjectID, "episode_1.mp4")
	third := createVideoAsset(t, store, project.ProjectID, "episode_3.mp4")
	set, err = store.CreateAssetSetVersion(ctx, CreateAssetSetVersionCommand{
		AssetSetID: set.AssetSet.AssetSetID, ExpectedCurrentVersion: set.AssetSet.CurrentVersion,
		Changes: []AssetSetChange{{Operation: "add_asset", AssetID: first.Asset.AssetID}, {Operation: "add_asset", AssetID: third.Asset.AssetID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	set, err = store.SealAssetSet(ctx, SealAssetSetCommand{
		AssetSetID: set.AssetSet.AssetSetID, ExpectedCurrentVersion: set.AssetSet.CurrentVersion, UserConfirmedUploadComplete: true,
		ContinuationPolicy: json.RawMessage(`{"mode":"continue_incomplete","confirmed_by":"user"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]any{
		"project_id": project.ProjectID, "source_type": "video_reference", "asset_set_id": set.AssetSet.AssetSetID,
		"asset_set_version_id": set.Version.AssetSetVersionID, "collection_state": "sealed", "user_request_message_id": message.MessageID, "user_notes": []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.StartRun(ctx, bindStartRunProposal(t, store, StartRunCommand{
		ProjectID: project.ProjectID, ConversationID: project.PrimaryConversationID, CapabilityID: "video_reference_creation", CapabilityVersion: "1.2.0",
		RunKind: "generation", Input: input, Confirmed: true,
		Config: json.RawMessage(`{"config_ref":"extraction","payload":{"fidelity_level":"high","timecode_precision":"second","uncertain_content_policy":"mark"}}`),
	}))
	if err != nil {
		t.Fatal(err)
	}
	running := approveSnapshotAndResume(t, store, started)
	if currentStepID(running) != "extract_video_scripts" {
		t.Fatalf("missing-material branch: %+v", running)
	}
	for _, scenario := range []string{"valid", "not-user-confirmed", "not-sealed", "wrong-project", "complete-source"} {
		t.Run(scenario, func(t *testing.T) {
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := tx.ExecContext(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "not-user-confirmed":
				exec(`UPDATE asset_set_versions SET continuation_policy_json='{"mode":"continue_incomplete","confirmed_by":"model"}' WHERE asset_set_version_id=?`, set.Version.AssetSetVersionID)
			case "not-sealed":
				exec(`UPDATE asset_set_versions SET status='draft' WHERE asset_set_version_id=?`, set.Version.AssetSetVersionID)
			case "wrong-project":
				exec(`UPDATE run_input_snapshot_versions SET payload_json=json_set(payload_json, '$.project_id', 'other-project') WHERE run_input_snapshot_version_id=?`, running.Run.CurrentInputSnapshotVersionID)
			case "complete-source":
				exec(`UPDATE asset_set_versions SET completeness_json=json_set(completeness_json, '$.missing_episode_numbers', json('[]')) WHERE asset_set_version_id=?`, set.Version.AssetSetVersionID)
			}
			confirmed, err := sourceIncompleteMaterialConfirmedTx(ctx, tx, running.Run.RunID)
			if scenario == "valid" || scenario == "complete-source" {
				if err != nil || confirmed != (scenario == "valid") {
					t.Fatalf("source fact: %t %v", confirmed, err)
				}
			} else {
				assertDomainCode(t, err, "DEPENDENCY_INCOMPLETE")
			}
		})
	}
}
