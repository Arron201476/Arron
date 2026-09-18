package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func selectCandidateVersionForTest(t *testing.T, store *Store, candidate ScriptCandidate) FinalSelectionResult {
	t.Helper()
	ctx := context.Background()
	current, err := store.GetCurrentFinalSelection(ctx, candidate.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	var currentID *string
	if current != nil {
		currentID = &current.FinalSelectionID
	}
	preview, err := store.CreateFinalSelectionPreview(ctx, CreateFinalSelectionPreviewCommand{
		ProjectID: candidate.ProjectID, CandidateID: candidate.CandidateID,
		ExpectedArtifactVersionID: candidate.ScriptsArtifactVersionID, ExpectedCurrentSelectionID: currentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.ConfirmFinalSelection(ctx, ConfirmFinalSelectionCommand{
		ProjectID: candidate.ProjectID, CandidateID: candidate.CandidateID,
		ExpectedCurrentSelectionID: currentID, PreviewHash: preview.Preview.PreviewHash, Confirmed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func candidateVersionExportForTest(t *testing.T, store *Store, candidate ScriptCandidate) []byte {
	t.Helper()
	ctx := context.Background()
	export, err := store.CreateScriptExport(ctx, CreateScriptExportCommand{
		CandidateID: candidate.CandidateID, ArtifactVersionID: candidate.ScriptsArtifactVersionID, Format: "txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	content, err := store.OpenScriptExportContent(ctx, export.ExportID)
	if err != nil {
		t.Fatal(err)
	}
	defer content.File.Close()
	body, err := io.ReadAll(content.File)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func candidateUnitVersionsForTest(t *testing.T, store *Store, candidate ScriptCandidate) []ArtifactVersion {
	t.Helper()
	ctx := context.Background()
	aggregate, err := store.GetArtifactVersion(ctx, candidate.ScriptsArtifactVersionID)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		UnitRefs []struct {
			VersionID string `json:"artifact_version_id"`
		} `json:"unit_refs"`
	}
	if err := json.Unmarshal(aggregate.Payload, &payload); err != nil || len(payload.UnitRefs) == 0 {
		t.Fatalf("empty candidate unit references: %s %v", aggregate.Payload, err)
	}
	var units []ArtifactVersion
	for _, ref := range payload.UnitRefs {
		unit, err := store.GetArtifactVersion(ctx, ref.VersionID)
		if err != nil {
			t.Fatal(err)
		}
		units = append(units, unit)
	}
	return units
}

func assertCandidateHistoryForTest(t *testing.T, store *Store, candidate ScriptCandidate, body []byte, units []ArtifactVersion) {
	t.Helper()
	fresh, err := store.GetScriptCandidate(context.Background(), candidate.CandidateID)
	if err != nil || fresh.ScriptsArtifactVersionID != candidate.ScriptsArtifactVersionID || fresh.Status != "final" {
		t.Fatalf("old final candidate changed during edit: %+v %v", fresh, err)
	}
	after := candidateUnitVersionsForTest(t, store, candidate)
	if len(after) != len(units) {
		t.Fatal("old candidate lost unit references")
	}
	for index, unit := range units {
		saved := after[index]
		if saved.Status != "confirmed" && saved.Status != "superseded" {
			t.Fatalf("old candidate unit was downgraded: %+v", saved)
		}
		saved.Status = unit.Status
		if !reflect.DeepEqual(unit, saved) {
			t.Fatalf("old candidate unit content or confirmation changed: before=%+v after=%+v", unit, after[index])
		}
	}
	if !bytes.Equal(body, candidateVersionExportForTest(t, store, candidate)) {
		t.Fatal("editing changed old final export")
	}
}

func nextCandidateVersionForTest(t *testing.T, store *Store, previous ScriptCandidate) ScriptCandidate {
	t.Helper()
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	versionID := store.newID("av")
	if _, err := tx.Exec(`INSERT INTO artifact_versions(artifact_version_id,artifact_id,version,status,payload_json,
		schema_id,schema_version,created_by_kind,actor_ref,creation_reason,base_version_id,created_at,confirmed_at)
		SELECT ?,artifact_id,version+1,'confirmed',payload_json,schema_id,schema_version,
		created_by_kind,actor_ref,creation_reason,artifact_version_id,created_at,confirmed_at
		FROM artifact_versions WHERE artifact_version_id=?`, versionID, previous.ScriptsArtifactVersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE artifact_versions SET status='superseded' WHERE artifact_version_id=?;
		UPDATE artifacts SET current_version_id=? WHERE current_version_id=?`, previous.ScriptsArtifactVersionID, versionID, previous.ScriptsArtifactVersionID); err != nil {
		t.Fatal(err)
	}
	candidate, created, err := store.createScriptCandidateTx(ctx, tx, Run{
		RunID: previous.SourceRunID, ProjectID: previous.ProjectID, CapabilityID: previous.SourceCapabilityID,
	}, versionID, store.now())
	if err != nil || !created || candidate.CandidateID == previous.CandidateID || candidate.Status != "candidate" ||
		candidate.SupersedesCandidateID == nil || *candidate.SupersedesCandidateID != previous.CandidateID {
		t.Fatalf("aggregate did not create an independent candidate: %+v created=%v err=%v", candidate, created, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return candidate
}

func TestScriptCandidatesKeepPriorFinalAndVersionIdentityWithinOneRun(t *testing.T) {
	store, project, first := candidateCommandFixture(t)
	ctx := context.Background()
	selected := selectCandidateVersionForTest(t, store, first)
	before, err := store.GetScriptCandidate(ctx, first.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	body := candidateVersionExportForTest(t, store, first)
	second := nextCandidateVersionForTest(t, store, first)
	third := nextCandidateVersionForTest(t, store, second)
	after, err := store.GetScriptCandidate(ctx, first.CandidateID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("new aggregate rewrote old final: before=%+v after=%+v err=%v", before, after, err)
	}
	current, err := store.GetCurrentFinalSelection(ctx, project.ProjectID)
	if err != nil || current == nil || current.FinalSelectionID != selected.Selection.FinalSelectionID {
		t.Fatalf("new candidate changed active final: %+v %v", current, err)
	}
	if !bytes.Equal(body, candidateVersionExportForTest(t, store, first)) {
		t.Fatal("old final export changed")
	}
	for _, candidate := range []ScriptCandidate{first, second, third} {
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		replay, created, createErr := store.createScriptCandidateTx(ctx, tx, Run{
			RunID: candidate.SourceRunID, ProjectID: candidate.ProjectID, CapabilityID: candidate.SourceCapabilityID,
		}, candidate.ScriptsArtifactVersionID, store.now())
		rollbackErr := tx.Rollback()
		if createErr != nil || rollbackErr != nil || created || replay.CandidateID != candidate.CandidateID ||
			replay.ScriptsArtifactVersionID != candidate.ScriptsArtifactVersionID {
			t.Fatalf("exact version replay: %+v created=%v errors=%v/%v", replay, created, createErr, rollbackErr)
		}
	}
	selectCandidateVersionForTest(t, store, third)
	reselected := selectCandidateVersionForTest(t, store, first)
	if reselected.Candidate.ScriptsArtifactVersionID != first.ScriptsArtifactVersionID || reselected.Selection.SelectionNo != 3 ||
		!bytes.Equal(body, candidateVersionExportForTest(t, store, first)) {
		t.Fatal("reselecting historical final did not retain its original content")
	}
}

func legacyCandidateSchemaForTest(t *testing.T, db *sql.DB) {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=OFF;
		CREATE TABLE script_candidates_legacy (`+scriptCandidateColumns+`, UNIQUE(source_run_id));
		INSERT INTO script_candidates_legacy SELECT * FROM script_candidates;
		DROP TABLE script_candidates;
		ALTER TABLE script_candidates_legacy RENAME TO script_candidates;
		CREATE INDEX idx_script_candidates_project ON script_candidates(project_id,created_at DESC,candidate_id);
		CREATE UNIQUE INDEX one_final_candidate_per_project ON script_candidates(project_id) WHERE status='final';
		PRAGMA user_version=63;
		PRAGMA foreign_keys=ON;`); err != nil {
		t.Fatal(err)
	}
}

func TestV63CandidateMigrationPreservesFinalPreviewExportAndRollbackBackup(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "candidate-migration.db")
	registry := loadTestRegistry(t)
	store, err := Open(path, registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if store != nil {
			store.Close()
		}
	})
	project, err := store.CreateProject(ctx, "Candidate migration")
	if err != nil {
		t.Fatal(err)
	}
	first := createCandidateFixture(t, store, project, "migration-first")
	second := createCandidateFixture(t, store, project, "migration-second")
	seedExportScriptUnit(t, store, first)
	body := candidateVersionExportForTest(t, store, first)
	selectCandidateVersionForTest(t, store, first)
	selected := selectCandidateVersionForTest(t, store, second)
	if _, err := store.db.Exec(`UPDATE script_candidates SET supersedes_candidate_id=? WHERE candidate_id=?`, first.CandidateID, second.CandidateID); err != nil {
		t.Fatal(err)
	}
	preview, err := store.CreateFinalSelectionPreview(ctx, CreateFinalSelectionPreviewCommand{
		ProjectID: project.ProjectID, CandidateID: first.CandidateID, ExpectedCurrentSelectionID: &selected.Selection.FinalSelectionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.ListScriptCandidates(ctx, project.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	state := candidateCommandState(t, store)
	legacyCandidateSchemaForTest(t, store.db)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, registry)
	if err != nil {
		t.Fatal(err)
	}
	after, err := store.ListScriptCandidates(ctx, project.ProjectID)
	if err != nil || !reflect.DeepEqual(before, after) || candidateCommandState(t, store) != state {
		t.Fatalf("candidate migration changed rows or command state: %+v %+v %v", before, after, err)
	}
	var backup string
	if err := store.db.QueryRow(`SELECT backup_ref FROM migration_history WHERE from_version=63 AND to_version=? AND status='completed'`, schemaVersion).Scan(&backup); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(backup)
	if err != nil || info.Size() == 0 {
		t.Fatalf("migration backup missing: %v", err)
	}
	if !bytes.Equal(body, candidateVersionExportForTest(t, store, first)) {
		t.Fatal("migration changed historical export content")
	}
	confirmed, err := store.ConfirmFinalSelection(ctx, ConfirmFinalSelectionCommand{
		ProjectID: project.ProjectID, CandidateID: first.CandidateID, ExpectedCurrentSelectionID: &selected.Selection.FinalSelectionID,
		PreviewHash: preview.Preview.PreviewHash, Confirmed: true,
	})
	if err != nil || confirmed.Candidate.ScriptsArtifactVersionID != first.ScriptsArtifactVersionID {
		t.Fatalf("pre-migration preview lost its target: %+v %v", confirmed, err)
	}
	newCandidate := nextCandidateVersionForTest(t, store, first)
	if err := migrateScriptCandidateVersions(store.db); err != nil {
		t.Fatal("migration is not idempotent", err)
	}
	if _, err := store.db.Exec(`INSERT INTO script_candidates SELECT 'duplicate-candidate',project_id,source_run_id,source_capability_id,
		scripts_artifact_version_id,status,label,supersedes_candidate_id,created_at,updated_at FROM script_candidates WHERE candidate_id=?`, newCandidate.CandidateID); err == nil {
		t.Fatal("migration lost aggregate-version uniqueness")
	}
	if _, err := store.db.Exec(`UPDATE script_candidates SET status='final' WHERE candidate_id=?`, newCandidate.CandidateID); err == nil {
		t.Fatal("migration lost single-final uniqueness")
	}
	var enabled int
	if err := store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("foreign keys not restored: %d %v", enabled, err)
	}
	rows, err := store.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() || rows.Err() != nil {
		t.Fatal("migration left a broken foreign key", rows.Err())
	}
}

func TestCandidateMigrationRollsBackInvalidReferencesAndRestoresEnforcement(t *testing.T) {
	store, _, candidate := candidateCommandFixture(t)
	legacyCandidateSchemaForTest(t, store.db)
	if _, err := store.db.Exec(`PRAGMA foreign_keys=OFF; UPDATE script_candidates SET supersedes_candidate_id='missing-candidate'; PRAGMA foreign_keys=ON;`); err != nil {
		t.Fatal(err)
	}
	err := migrateScriptCandidateVersions(store.db)
	if err == nil || !strings.Contains(err.Error(), "foreign key violation") {
		t.Fatalf("invalid reference did not fail migration: %v", err)
	}
	var enabled, version, scratchCount, uniqueRunIndexes int
	if err := store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("failed migration left enforcement off: %d %v", enabled, err)
	}
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 63 {
		t.Fatalf("failed migration advanced schema version: %d %v", version, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='script_candidates_v64'`).Scan(&scratchCount); err != nil || scratchCount != 0 {
		t.Fatalf("failed migration leaked rebuilt table: %d %v", scratchCount, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_index_list('script_candidates') i
		WHERE i."unique"=1 AND (SELECT COUNT(*) FROM pragma_index_info(i.name))=1
		AND EXISTS(SELECT 1 FROM pragma_index_info(i.name) WHERE name='source_run_id')`).Scan(&uniqueRunIndexes); err != nil || uniqueRunIndexes != 1 {
		t.Fatalf("failed migration changed original schema: %d %v", uniqueRunIndexes, err)
	}
	after, err := store.GetScriptCandidate(context.Background(), candidate.CandidateID)
	if err != nil || after.ScriptsArtifactVersionID != candidate.ScriptsArtifactVersionID ||
		after.SupersedesCandidateID == nil || *after.SupersedesCandidateID != "missing-candidate" {
		t.Fatalf("failed migration rewrote original row: %+v %v", after, err)
	}
}
