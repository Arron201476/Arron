package runtime

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"novel2script-agent/backend/internal/agent"

	_ "modernc.org/sqlite"
)

const runtimeSchemaVersion = 1

const runtimeSchema = `
CREATE TABLE IF NOT EXISTS runtime_meta (key TEXT PRIMARY KEY, value INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS runtime_runs (run_id TEXT PRIMARY KEY, payload BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS runtime_events (event_id TEXT PRIMARY KEY, run_id TEXT NOT NULL, position INTEGER NOT NULL, payload BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS idx_runtime_events_run_position ON runtime_events(run_id, position);
CREATE TABLE IF NOT EXISTS runtime_artifacts (artifact_id TEXT PRIMARY KEY, run_id TEXT NOT NULL, position INTEGER NOT NULL, payload BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS idx_runtime_artifacts_run_position ON runtime_artifacts(run_id, position);
CREATE TABLE IF NOT EXISTS runtime_approvals (approval_id TEXT PRIMARY KEY, payload BLOB NOT NULL);`

func openRuntimeDatabase(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	var userVersion int
	if err = db.QueryRow("PRAGMA user_version").Scan(&userVersion); err != nil {
		db.Close()
		return nil, err
	}
	if userVersion > runtimeSchemaVersion {
		db.Close()
		return nil, fmt.Errorf("runtime database schema version %d is newer than supported version %d", userVersion, runtimeSchemaVersion)
	}
	pragmasAndSchema := "PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA synchronous=FULL; PRAGMA foreign_keys=ON;" + runtimeSchema + fmt.Sprintf("PRAGMA user_version=%d;", runtimeSchemaVersion)
	if _, err = db.Exec(pragmasAndSchema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func readRuntimeStateSQLite(path string) (runtimeState, bool, error) {
	db, err := openRuntimeDatabase(path)
	if err != nil {
		return runtimeState{}, false, err
	}
	defer db.Close()
	state := runtimeState{
		Runs: map[string]agent.Run{}, Events: map[string][]agent.RunEvent{},
		Artifacts: map[string][]agent.Artifact{}, Approvals: map[string]agent.ApprovalRequest{},
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM runtime_runs").Scan(&count); err != nil {
		return runtimeState{}, false, err
	}
	if count == 0 {
		return state, false, nil
	}
	if err := readJSONRows(db, "SELECT run_id, payload FROM runtime_runs", func(id string, payload []byte) error {
		var value agent.Run
		if err := json.Unmarshal(payload, &value); err != nil {
			return fmt.Errorf("run %s: %w", id, err)
		}
		state.Runs[id] = value
		return nil
	}); err != nil {
		return runtimeState{}, false, err
	}
	if err := readJSONRows(db, "SELECT event_id, payload FROM runtime_events ORDER BY run_id, position", func(_ string, payload []byte) error {
		var value agent.RunEvent
		if err := json.Unmarshal(payload, &value); err != nil {
			return err
		}
		state.Events[value.RunID] = append(state.Events[value.RunID], value)
		return nil
	}); err != nil {
		return runtimeState{}, false, err
	}
	if err := readJSONRows(db, "SELECT artifact_id, payload FROM runtime_artifacts ORDER BY run_id, position", func(_ string, payload []byte) error {
		var value agent.Artifact
		if err := json.Unmarshal(payload, &value); err != nil {
			return err
		}
		state.Artifacts[value.RunID] = append(state.Artifacts[value.RunID], value)
		return nil
	}); err != nil {
		return runtimeState{}, false, err
	}
	if err := readJSONRows(db, "SELECT approval_id, payload FROM runtime_approvals", func(id string, payload []byte) error {
		var value agent.ApprovalRequest
		if err := json.Unmarshal(payload, &value); err != nil {
			return err
		}
		state.Approvals[id] = value
		return nil
	}); err != nil {
		return runtimeState{}, false, err
	}
	_ = db.QueryRow("SELECT value FROM runtime_meta WHERE key='counter'").Scan(&state.Counter)
	return state, true, nil
}

func readJSONRows(db *sql.DB, query string, consume func(string, []byte) error) error {
	rows, err := db.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var payload []byte
		if err := rows.Scan(&id, &payload); err != nil {
			return err
		}
		if err := consume(id, payload); err != nil {
			return err
		}
	}
	return rows.Err()
}

func writeRuntimeStateSQLite(path string, state runtimeState) error {
	db, err := openRuntimeDatabase(path)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"runtime_runs", "runtime_events", "runtime_artifacts", "runtime_approvals"} {
		if _, err := tx.Exec("DELETE FROM " + table); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("INSERT INTO runtime_meta(key,value) VALUES('counter',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", state.Counter); err != nil {
		return err
	}
	for id, value := range state.Runs {
		if err := insertRuntimeJSON(tx, "INSERT INTO runtime_runs(run_id,payload) VALUES(?,?)", id, value); err != nil {
			return err
		}
	}
	for runID, values := range state.Events {
		for index, value := range values {
			payload, err := json.Marshal(value)
			if err != nil {
				return err
			}
			if _, err = tx.Exec("INSERT INTO runtime_events(event_id,run_id,position,payload) VALUES(?,?,?,?)", value.EventID, runID, index, payload); err != nil {
				return err
			}
		}
	}
	for runID, values := range state.Artifacts {
		for index, value := range values {
			payload, err := json.Marshal(value)
			if err != nil {
				return err
			}
			if _, err = tx.Exec("INSERT INTO runtime_artifacts(artifact_id,run_id,position,payload) VALUES(?,?,?,?)", value.ArtifactID, runID, index, payload); err != nil {
				return err
			}
		}
	}
	for id, value := range state.Approvals {
		if err := insertRuntimeJSON(tx, "INSERT INTO runtime_approvals(approval_id,payload) VALUES(?,?)", id, value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func insertRuntimeJSON(tx *sql.Tx, statement, id string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.Exec(statement, id, payload)
	return err
}
