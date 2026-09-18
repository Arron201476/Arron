package runtime

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestProjectFileV61MigrationPreservesLegacyDraftHashAndRawBytes(t *testing.T) {
	database := filepath.Join(t.TempDir(), "migration.db")
	store := openProjectFilesTestStore(t, database)
	ctx, project, before := createSkillDraftForTest(t, store)
	_, original, err := store.ExportProjectSkillDraft(ctx, project.ProjectID, before.RootPath, before.SnapshotHash)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.db.Exec(`
		ALTER TABLE project_file_versions RENAME TO binary_files_fixture;
		CREATE TABLE project_file_versions (
			project_id TEXT NOT NULL REFERENCES projects(project_id), path_key TEXT NOT NULL, path TEXT NOT NULL,
			version INTEGER NOT NULL CHECK(version>0), content TEXT NOT NULL, content_hash TEXT NOT NULL,
			size_bytes INTEGER NOT NULL CHECK(size_bytes>=0), deleted INTEGER NOT NULL CHECK(deleted IN (0,1)),
			agent_tool_call_id TEXT NOT NULL UNIQUE REFERENCES agent_tool_calls(agent_tool_call_id), created_at TEXT NOT NULL,
			PRIMARY KEY(project_id,path_key,version));
		INSERT INTO project_file_versions SELECT project_id,path_key,path,version,content,content_hash,size_bytes,deleted,agent_tool_call_id,created_at FROM binary_files_fixture;
		DROP TABLE binary_files_fixture;
		PRAGMA user_version=61;`)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openProjectFilesTestStore(t, database)
	defer store.Close()
	after, err := store.PreviewProjectSkillDraft(ctx, project.ProjectID, before.RootPath)
	if err != nil || after.SnapshotHash != before.SnapshotHash {
		t.Fatalf("migration invalidated unchanged Skill approval: %+v %v", after, err)
	}
	_, archive, err := store.ExportProjectSkillDraft(ctx, project.ProjectID, before.RootPath, before.SnapshotHash)
	if err != nil || !bytes.Equal(archive, original) {
		t.Fatal("migration changed the legacy package", err)
	}
	file, body, err := store.ReadProjectFileBytes(ctx, project.ProjectID, before.RootPath+"/SKILL.md", 1)
	if err != nil || file.Binary || string(body) != authoredSkillText {
		t.Fatalf("migration changed text metadata or bytes: %+v %v", file, err)
	}
	if err := migrateProjectFileBinaryVersions(store.db); err != nil {
		t.Fatal("migration is not idempotent", err)
	}
}
