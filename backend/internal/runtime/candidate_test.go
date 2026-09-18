package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestFinalSelectionPreviewConfirmAndReplace(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "content_agent.db"), loadTestRegistry(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	project, err := store.CreateProject(ctx, "Final Selection")
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	firstCandidate := createCandidateFixture(t, store, project, "first")
	secondCandidate := createCandidateFixture(t, store, project, "second")
	thirdCandidate := createCandidateFixture(t, store, project, "third")

	firstPreviewCommand := CreateFinalSelectionPreviewCommand{
		CommandMeta: CommandMeta{
			Scope:          project.ProjectID,
			CommandType:    "create_final_selection_preview",
			IdempotencyKey: "candidate-preview-first",
			RequestHash:    "candidate-preview-first-hash",
		},
		ProjectID:   project.ProjectID,
		CandidateID: firstCandidate.CandidateID,
	}
	firstPreview, err := store.CreateFinalSelectionPreview(ctx, firstPreviewCommand)
	if err != nil {
		t.Fatalf("CreateFinalSelectionPreview(first) error = %v", err)
	}
	repeatedPreview, err := store.CreateFinalSelectionPreview(ctx, firstPreviewCommand)
	if err != nil ||
		repeatedPreview.Preview.FinalSelectionPreviewID != firstPreview.Preview.FinalSelectionPreviewID ||
		repeatedPreview.Preview.PreviewHash != firstPreview.Preview.PreviewHash {
		t.Fatalf("repeated preview = %+v, error = %v", repeatedPreview, err)
	}
	if firstPreview.Approval.Scope != "final_selection" ||
		firstPreview.Approval.SubjectRefID != firstCandidate.CandidateID ||
		firstPreview.Preview.CurrentSelectionID != nil ||
		firstPreview.Preview.ProposedSelectionNo != 1 {
		t.Fatalf("first preview = %+v", firstPreview)
	}
	_, err = store.ResolveApproval(ctx, ResolveApprovalCommand{
		ApprovalRequestID:       firstPreview.Approval.ApprovalRequestID,
		Action:                  "select_final",
		ExpectedApprovalVersion: firstPreview.Approval.Version,
		SubjectSnapshotHash:     firstPreview.Approval.SubjectSnapshotHash,
	})
	assertDomainCode(t, err, "APPROVAL_COMMAND_MISMATCH")
	_, err = store.ConfirmFinalSelection(ctx, ConfirmFinalSelectionCommand{
		ProjectID:   project.ProjectID,
		CandidateID: firstCandidate.CandidateID,
		PreviewHash: firstPreview.Preview.PreviewHash,
	})
	assertDomainCode(t, err, "REQUIRED_CONFIRMATION_MISSING")

	firstConfirmCommand := ConfirmFinalSelectionCommand{
		CommandMeta: CommandMeta{
			Scope:          project.ProjectID,
			CommandType:    "confirm_final_selection",
			IdempotencyKey: "candidate-confirm-first",
			RequestHash:    "candidate-confirm-first-hash",
		},
		ProjectID:   project.ProjectID,
		CandidateID: firstCandidate.CandidateID,
		PreviewHash: firstPreview.Preview.PreviewHash,
		Confirmed:   true,
	}
	firstSelection, err := store.ConfirmFinalSelection(ctx, firstConfirmCommand)
	if err != nil {
		t.Fatalf("ConfirmFinalSelection(first) error = %v", err)
	}
	repeatedSelection, err := store.ConfirmFinalSelection(ctx, firstConfirmCommand)
	if err != nil ||
		repeatedSelection.Selection.FinalSelectionID != firstSelection.Selection.FinalSelectionID {
		t.Fatalf("repeated final selection = %+v, error = %v", repeatedSelection, err)
	}
	current, err := store.GetCurrentFinalSelection(ctx, project.ProjectID)
	if err != nil || current == nil ||
		current.FinalSelectionID != firstSelection.Selection.FinalSelectionID ||
		current.CandidateID != firstCandidate.CandidateID ||
		current.SelectionNo != 1 {
		t.Fatalf("current first selection = %+v, error = %v", current, err)
	}

	_, err = store.CreateFinalSelectionPreview(ctx, CreateFinalSelectionPreviewCommand{
		ProjectID:   project.ProjectID,
		CandidateID: secondCandidate.CandidateID,
	})
	assertDomainCode(t, err, "FINAL_SELECTION_CONFLICT")
	firstSelectionID := firstSelection.Selection.FinalSelectionID
	secondPreview, err := store.CreateFinalSelectionPreview(ctx, CreateFinalSelectionPreviewCommand{
		CommandMeta: CommandMeta{
			Scope:          project.ProjectID,
			CommandType:    "create_final_selection_preview",
			IdempotencyKey: "candidate-preview-second",
			RequestHash:    "candidate-preview-second-hash",
		},
		ProjectID:                  project.ProjectID,
		CandidateID:                secondCandidate.CandidateID,
		ExpectedCurrentSelectionID: &firstSelectionID,
	})
	if err != nil {
		t.Fatalf("CreateFinalSelectionPreview(second) error = %v", err)
	}
	thirdPreview, err := store.CreateFinalSelectionPreview(ctx, CreateFinalSelectionPreviewCommand{
		CommandMeta: CommandMeta{
			Scope:          project.ProjectID,
			CommandType:    "create_final_selection_preview",
			IdempotencyKey: "candidate-preview-third",
			RequestHash:    "candidate-preview-third-hash",
		},
		ProjectID:                  project.ProjectID,
		CandidateID:                thirdCandidate.CandidateID,
		ExpectedCurrentSelectionID: &firstSelectionID,
	})
	if err != nil {
		t.Fatalf("CreateFinalSelectionPreview(third) error = %v", err)
	}
	_, err = store.ConfirmFinalSelection(ctx, ConfirmFinalSelectionCommand{
		ProjectID:                  project.ProjectID,
		CandidateID:                secondCandidate.CandidateID,
		PreviewHash:                secondPreview.Preview.PreviewHash,
		ExpectedCurrentSelectionID: &firstSelectionID,
		Confirmed:                  true,
	})
	assertDomainCode(t, err, "FINAL_SELECTION_PREVIEW_EXPIRED")
	if thirdPreview.Preview.PreviewHash == secondPreview.Preview.PreviewHash {
		t.Fatal("different candidate previews unexpectedly share a hash")
	}

	replacementPreview, err := store.CreateFinalSelectionPreview(ctx, CreateFinalSelectionPreviewCommand{
		CommandMeta: CommandMeta{
			Scope:          project.ProjectID,
			CommandType:    "create_final_selection_preview",
			IdempotencyKey: "candidate-preview-replacement",
			RequestHash:    "candidate-preview-replacement-hash",
		},
		ProjectID:                  project.ProjectID,
		CandidateID:                secondCandidate.CandidateID,
		ExpectedCurrentSelectionID: &firstSelectionID,
	})
	if err != nil {
		t.Fatalf("CreateFinalSelectionPreview(replacement) error = %v", err)
	}
	replacement, err := store.ConfirmFinalSelection(ctx, ConfirmFinalSelectionCommand{
		CommandMeta: CommandMeta{
			Scope:          project.ProjectID,
			CommandType:    "confirm_final_selection",
			IdempotencyKey: "candidate-confirm-replacement",
			RequestHash:    "candidate-confirm-replacement-hash",
		},
		ProjectID:                  project.ProjectID,
		CandidateID:                secondCandidate.CandidateID,
		PreviewHash:                replacementPreview.Preview.PreviewHash,
		ExpectedCurrentSelectionID: &firstSelectionID,
		Confirmed:                  true,
	})
	if err != nil {
		t.Fatalf("ConfirmFinalSelection(replacement) error = %v", err)
	}
	if replacement.Selection.SelectionNo != 2 ||
		replacement.Selection.ReplacedSelectionID == nil ||
		*replacement.Selection.ReplacedSelectionID != firstSelectionID {
		t.Fatalf("replacement selection = %+v", replacement.Selection)
	}
	current, err = store.GetCurrentFinalSelection(ctx, project.ProjectID)
	if err != nil || current == nil ||
		current.FinalSelectionID != replacement.Selection.FinalSelectionID ||
		current.CandidateID != secondCandidate.CandidateID {
		t.Fatalf("current replacement = %+v, error = %v", current, err)
	}
	projectAfter, err := store.GetProject(ctx, project.ProjectID)
	if err != nil ||
		projectAfter.CurrentFinalSelectionID == nil ||
		*projectAfter.CurrentFinalSelectionID != replacement.Selection.FinalSelectionID ||
		projectAfter.CurrentFocusArtifactVersionID == nil ||
		*projectAfter.CurrentFocusArtifactVersionID != secondCandidate.ScriptsArtifactVersionID {
		t.Fatalf("project final selection projection = %+v, error = %v", projectAfter, err)
	}
	firstAfter, err := store.GetScriptCandidate(ctx, firstCandidate.CandidateID)
	if err != nil || firstAfter.Status != "historical_final" {
		t.Fatalf("first historical candidate = %+v, error = %v", firstAfter, err)
	}
	secondAfter, err := store.GetScriptCandidate(ctx, secondCandidate.CandidateID)
	if err != nil || secondAfter.Status != "final" {
		t.Fatalf("second final candidate = %+v, error = %v", secondAfter, err)
	}

	if _, err := store.db.ExecContext(ctx, `
		UPDATE artifact_versions SET status = 'invalidated'
		WHERE artifact_version_id = ?`,
		thirdCandidate.ScriptsArtifactVersionID,
	); err != nil {
		t.Fatalf("invalidate third candidate artifact version: %v", err)
	}
	replacementSelectionID := replacement.Selection.FinalSelectionID
	_, err = store.CreateFinalSelectionPreview(ctx, CreateFinalSelectionPreviewCommand{
		ProjectID:                  project.ProjectID,
		CandidateID:                thirdCandidate.CandidateID,
		ExpectedCurrentSelectionID: &replacementSelectionID,
	})
	assertDomainCode(t, err, "SCRIPT_CANDIDATE_INVALID")

	if _, err := store.db.ExecContext(ctx, `
		UPDATE artifact_versions SET status = 'superseded'
		WHERE artifact_version_id = ?`,
		firstCandidate.ScriptsArtifactVersionID,
	); err != nil {
		t.Fatalf("supersede first candidate artifact version: %v", err)
	}
	historicalPreview, err := store.CreateFinalSelectionPreview(
		ctx,
		CreateFinalSelectionPreviewCommand{
			CommandMeta: CommandMeta{
				Scope:          project.ProjectID,
				CommandType:    "create_final_selection_preview",
				IdempotencyKey: "candidate-preview-historical",
				RequestHash:    "candidate-preview-historical-hash",
			},
			ProjectID:                  project.ProjectID,
			CandidateID:                firstCandidate.CandidateID,
			ExpectedCurrentSelectionID: &replacementSelectionID,
		},
	)
	if err != nil {
		t.Fatalf("CreateFinalSelectionPreview(historical) error = %v", err)
	}
	historicalSelection, err := store.ConfirmFinalSelection(
		ctx,
		ConfirmFinalSelectionCommand{
			CommandMeta: CommandMeta{
				Scope:          project.ProjectID,
				CommandType:    "confirm_final_selection",
				IdempotencyKey: "candidate-confirm-historical",
				RequestHash:    "candidate-confirm-historical-hash",
			},
			ProjectID:                  project.ProjectID,
			CandidateID:                firstCandidate.CandidateID,
			PreviewHash:                historicalPreview.Preview.PreviewHash,
			ExpectedCurrentSelectionID: &replacementSelectionID,
			Confirmed:                  true,
		},
	)
	if err != nil {
		t.Fatalf("ConfirmFinalSelection(historical) error = %v", err)
	}
	if historicalSelection.Selection.SelectionNo != 3 ||
		historicalSelection.Selection.ReplacedSelectionID == nil ||
		*historicalSelection.Selection.ReplacedSelectionID != replacementSelectionID {
		t.Fatalf("historical selection = %+v", historicalSelection.Selection)
	}
	firstAfter, err = store.GetScriptCandidate(ctx, firstCandidate.CandidateID)
	if err != nil || firstAfter.Status != "final" {
		t.Fatalf("reselected historical candidate = %+v, error = %v", firstAfter, err)
	}
	secondAfter, err = store.GetScriptCandidate(ctx, secondCandidate.CandidateID)
	if err != nil || secondAfter.Status != "historical_final" {
		t.Fatalf("replaced second candidate = %+v, error = %v", secondAfter, err)
	}
	var activeSelections, replacedSelections, finalCandidates int
	if err := store.db.QueryRowContext(ctx, `
		SELECT
			SUM(CASE WHEN status = 'active' THEN 1 ELSE 0 END),
			SUM(CASE WHEN status = 'replaced' THEN 1 ELSE 0 END)
		FROM final_selections WHERE project_id = ?`,
		project.ProjectID,
	).Scan(&activeSelections, &replacedSelections); err != nil {
		t.Fatalf("count final selections: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM script_candidates
		WHERE project_id = ? AND status = 'final'`,
		project.ProjectID,
	).Scan(&finalCandidates); err != nil {
		t.Fatalf("count final candidates: %v", err)
	}
	if activeSelections != 1 || replacedSelections != 2 || finalCandidates != 1 {
		t.Fatalf(
			"active selections=%d replaced selections=%d final candidates=%d",
			activeSelections,
			replacedSelections,
			finalCandidates,
		)
	}
}

func createCandidateFixture(
	t *testing.T,
	store *Store,
	project Project,
	suffix string,
) ScriptCandidate {
	t.Helper()
	ctx := context.Background()
	now := store.now()
	runID := "run_candidate_" + suffix
	stepRunID := "step_candidate_" + suffix
	artifactID := "art_candidate_" + suffix
	versionID := "av_candidate_" + suffix
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx(candidate fixture): %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO runs(
			run_id, project_id, conversation_id, capability_id, capability_version,
			run_kind, write_intent, status, current_step_run_id,
			current_input_snapshot_version_id, input_snapshot_status, config_snapshot_json,
			run_event_seq, started_at, ended_at, created_at, updated_at
		) VALUES(?, ?, ?, 'novel_to_script', '1.4.0', 'generation', 1, 'completed', ?,
			?, 'sealed', '{}', 0, ?, ?, ?, ?)`,
		runID,
		project.ProjectID,
		project.PrimaryConversationID,
		stepRunID,
		"risv_candidate_"+suffix,
		formatTime(now),
		formatTime(now),
		formatTime(now),
		formatTime(now),
	); err != nil {
		t.Fatalf("insert candidate run: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO step_runs(
			step_run_id, run_id, step_id, status, attempt_count, approval_policy,
			input_version_snapshot_json, task_cursor_json, started_at, ended_at
		) VALUES(?, ?, 'aggregate_scripts', 'completed', 1, 'none', '[]', '{}', ?, ?)`,
		stepRunID,
		runID,
		formatTime(now),
		formatTime(now),
	); err != nil {
		t.Fatalf("insert candidate step: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifacts(
			artifact_id, project_id, run_id, step_run_id, capability_id, artifact_type,
			scope_key, current_version_id, created_at, updated_at
		) VALUES(?, ?, ?, ?, 'novel_to_script', 'scripts', 'singleton', ?, ?, ?)`,
		artifactID,
		project.ProjectID,
		runID,
		stepRunID,
		versionID,
		formatTime(now),
		formatTime(now),
	); err != nil {
		t.Fatalf("insert candidate artifact: %v", err)
	}
	payload, err := json.Marshal(map[string]any{
		"episode_count":           1,
		"unit_refs":               []any{},
		"completeness":            "complete",
		"global_continuity_state": map[string]any{},
		"quality_flags":           []string{},
	})
	if err != nil {
		t.Fatalf("marshal candidate payload: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO artifact_versions(
			artifact_version_id, artifact_id, version, status, payload_json,
			schema_id, schema_version, created_by_kind, actor_ref, creation_reason,
			created_at, confirmed_at
		) VALUES(?, ?, 1, 'confirmed', ?, 'scripts', '1.0.0', 'runtime',
			'candidate_fixture', 'aggregation', ?, ?)`,
		versionID,
		artifactID,
		string(payload),
		formatTime(now),
		formatTime(now),
	); err != nil {
		t.Fatalf("insert candidate version: %v", err)
	}
	candidate, _, err := store.createScriptCandidateTx(ctx, tx, Run{
		RunID:             runID,
		ProjectID:         project.ProjectID,
		CapabilityID:      "novel_to_script",
		CapabilityVersion: "1.4.0",
		Status:            "completed",
	}, versionID, now)
	if err != nil {
		t.Fatalf("createScriptCandidateTx() error = %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit candidate fixture: %v", err)
	}
	return candidate
}
