package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const scriptCandidateColumns = `
	candidate_id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(project_id),
	source_run_id TEXT NOT NULL REFERENCES runs(run_id),
	source_capability_id TEXT NOT NULL,
	scripts_artifact_version_id TEXT NOT NULL REFERENCES artifact_versions(artifact_version_id),
	status TEXT NOT NULL,
	label TEXT NOT NULL,
	supersedes_candidate_id TEXT REFERENCES script_candidates(candidate_id),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE(scripts_artifact_version_id)
`

func migrateScriptCandidateVersions(db *sql.DB) (err error) {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	rows, err := conn.QueryContext(ctx, `
		SELECT i.name FROM pragma_index_list('script_candidates') i
		WHERE i."unique" = 1 AND i.partial = 0
			AND (SELECT COUNT(*) FROM pragma_index_info(i.name)) = 1
			AND EXISTS(SELECT 1 FROM pragma_index_info(i.name) WHERE name = 'source_run_id')`)
	if err != nil {
		return err
	}
	obsolete := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		obsolete[name] = true
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if len(obsolete) == 0 {
		return nil
	}
	// Rebuild on one connection without retargeting existing foreign keys.
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer func() {
		_, restoreErr := conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
		var enabled int
		if restoreErr == nil {
			restoreErr = conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled)
			if restoreErr == nil && enabled != 1 {
				restoreErr = fmt.Errorf("foreign key enforcement was not restored")
			}
		}
		err = errors.Join(err, restoreErr)
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err = tx.QueryContext(ctx, `SELECT name, sql FROM sqlite_master
		WHERE tbl_name='script_candidates' AND type IN ('index','trigger') AND sql IS NOT NULL`)
	if err != nil {
		return err
	}
	var definitions []string
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			rows.Close()
			return err
		}
		if !obsolete[name] {
			definitions = append(definitions, definition)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE script_candidates_v64 (`+scriptCandidateColumns+`);
		INSERT INTO script_candidates_v64(
			candidate_id, project_id, source_run_id, source_capability_id,
			scripts_artifact_version_id, status, label, supersedes_candidate_id, created_at, updated_at
		) SELECT candidate_id, project_id, source_run_id, source_capability_id,
			scripts_artifact_version_id, status, label, supersedes_candidate_id, created_at, updated_at
		FROM script_candidates;
		DROP TABLE script_candidates;
		ALTER TABLE script_candidates_v64 RENAME TO script_candidates;
	`); err != nil {
		return err
	}
	for _, definition := range definitions {
		if _, err := tx.ExecContext(ctx, definition); err != nil {
			return err
		}
	}
	var table, parent string
	var rowID sql.NullInt64
	var foreignKeyID int
	err = tx.QueryRowContext(ctx, `SELECT "table", rowid, parent, fkid FROM pragma_foreign_key_check
		WHERE "table" IN ('script_candidates','final_selections','final_selection_previews','exports') LIMIT 1`).Scan(
		&table, &rowID, &parent, &foreignKeyID)
	if err == nil {
		return fmt.Errorf("candidate migration foreign key violation in %s referencing %s", table, parent)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return tx.Commit()
}
