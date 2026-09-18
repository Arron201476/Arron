package runtime

import "database/sql"

func migrateProjectFileBinaryVersions(db *sql.DB) error {
	var migrated bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info('project_file_versions') WHERE name='is_binary')`).Scan(&migrated); err != nil || migrated {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// No tables reference file-version rows. Preserve every legacy value and
	// its audit call while allowing one approved publication to contain a bundle.
	if _, err := tx.Exec(`ALTER TABLE project_file_versions RENAME TO project_file_versions_text_v61;` + projectFileSchema + `
		INSERT INTO project_file_versions(project_id,path_key,path,version,content,content_hash,size_bytes,deleted,agent_tool_call_id,created_at)
		SELECT project_id,path_key,path,version,content,content_hash,size_bytes,deleted,agent_tool_call_id,created_at FROM project_file_versions_text_v61;
		DROP TABLE project_file_versions_text_v61;`); err != nil {
		return err
	}
	return tx.Commit()
}
