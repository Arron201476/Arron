package runtime

import (
	"context"
	"testing"

	"content-agent/backend/internal/agentcontract"
)

func TestSemanticRevisionIncludesManagedCustomOutputsWithFrozenContracts(t *testing.T) {
	store, output, claim := managedEditFixture(t, "multi")
	if _, known := editableRevisionArtifacts[output.Artifact.ArtifactType]; known {
		t.Fatal("fixture must use a custom artifact type")
	}
	ctx := context.Background()
	request := agentcontract.MessageRequest{Content: "Revise the current output.", ClientContext: agentcontract.ClientContext{CurrentArtifactID: &output.Artifact.ArtifactID}}
	read := func() ([]TargetCandidate, error) {
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		return store.semanticTargetCandidatesTx(ctx, tx, output.Artifact.ProjectID, request)
	}
	before := artifactCommandState(t, store, output.Artifact.ArtifactID)
	candidates, err := read()
	if err != nil || len(candidates) != 1 || candidates[0].ArtifactID != output.Artifact.ArtifactID || candidates[0].ArtifactVersionID != output.ArtifactVersion.ArtifactVersionID {
		t.Fatalf("custom output candidates: %+v, %v", candidates, err)
	}
	if before != artifactCommandState(t, store, output.Artifact.ArtifactID) {
		t.Fatal("target discovery wrote state")
	}
	if _, err := store.db.Exec(`UPDATE context_packs SET context_hash='tampered' WHERE attempt_id=?`, claim.Attempt.AttemptID); err != nil {
		t.Fatal(err)
	}
	_, err = read()
	assertDomainCode(t, err, "CONTEXT_PACK_HASH_MISMATCH")
}

func TestSemanticRevisionDoesNotTreatLegacyDerivedOutputAsEditable(t *testing.T) {
	store, approval, _ := approvalRevisionFixture(t, "legacy")
	ctx := context.Background()
	var artifactID string
	if err := store.db.QueryRow(`SELECT artifact_id FROM artifacts WHERE step_run_id=? AND artifact_type='script_handoff'`, approval.StepRunID).Scan(&artifactID); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	candidates, err := store.semanticTargetCandidatesTx(ctx, tx, approval.ProjectID, agentcontract.MessageRequest{Content: "Revise the current output.", ClientContext: agentcontract.ClientContext{CurrentArtifactID: &artifactID}})
	if err != nil || len(candidates) != 0 {
		t.Fatalf("derived candidates: %+v, %v", candidates, err)
	}
}
